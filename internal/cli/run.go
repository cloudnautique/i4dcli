package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"i4dcli/internal/app"
	"i4dcli/internal/calendar"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
)

type countingWriter struct {
	io.Writer
	written int
}

func (w *countingWriter) Write(b []byte) (int, error) {
	n, err := w.Writer.Write(b)
	w.written += n
	return n, err
}

func Run(ctx context.Context, args []string, out, stderr io.Writer) (err error) {
	normalized, g, parseErr := parseGlobal(args)
	output := &countingWriter{Writer: out}
	protocol := len(normalized) > 0 && normalized[0] == "mcp"
	defer func() {
		// Never append an error envelope to partial output or an MCP protocol stream.
		if err != nil && g.asJSON && !protocol && output.written == 0 {
			_ = json.NewEncoder(output).Encode(map[string]any{"error": fault.Describe(err)})
		}
	}()
	if parseErr != nil {
		return fault.Usage(parseErr)
	}
	return run(ctx, normalized, g, output, stderr)
}

func run(ctx context.Context, args []string, g globals, out, stderr io.Writer) error {
	p, err := policy.New(policy.Options{Profile: g.profile, ReadOnly: g.readonly, NoInput: g.noInput, EnabledExact: g.exact})
	if err != nil {
		return fault.Usage(err)
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		return printHelp(out, p)
	}
	if args[0] == "help" {
		args = append(args[1:], "--help")
	}
	if len(args) == 1 && args[0] == "--help" {
		return printHelp(out, p)
	}
	if args[0] == "mcp" {
		return runMCP(ctx, args[1:], g, p, out, stderr)
	}
	if args[0] == "policy" || args[0] == "schema" {
		if len(args) > 1 {
			if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
				return printHelp(out, p)
			}
			return fault.New("usage", "unexpected %s argument %q", args[0], args[1])
		}
		if g.dryRun || g.force {
			return fault.New("usage", "--dry-run and --force only apply to mutation commands")
		}
		if args[0] == "schema" {
			if g.plain {
				return fault.New("usage", "schema always emits JSON; --plain is not supported")
			}
			return printSchema(out, p)
		}
		return printPolicy(out, p, g.asJSON, g.plain)
	}
	if (args[0] == "events" || args[0] == "calendars") && (len(args) == 1 || (len(args) == 2 && (args[1] == "--help" || args[1] == "-h"))) {
		return printHelp(out, p, args[0])
	}
	if len(args) < 2 {
		return fault.New("usage", "expected a command and subcommand (see --help)")
	}
	c, err := policy.Resolve(args[0] + "." + args[1])
	if err != nil {
		return fault.Usage(err)
	}
	if err = p.Check(c.Name); err != nil {
		return err
	}
	fs, o := commandFlags(c, stderr)
	if err = fs.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fault.Usage(err)
	}
	if fs.NArg() != 0 {
		return fault.New("usage", "unexpected positional arguments")
	}
	if err = o.supplied(fs); err != nil {
		return fault.Usage(err)
	}
	mutation := c.Capability == policy.Create || c.Capability == policy.Edit || c.Capability == policy.Delete
	if g.dryRun && !mutation {
		return fault.New("usage", "--dry-run only applies to mutations")
	}
	if g.force && c.Capability != policy.Delete {
		return fault.New("usage", "--force only applies to deletion")
	}
	var from, to time.Time
	var loc *time.Location
	if c.Name == "events.list" {
		if len(o.names) == 0 {
			return fault.New("usage", "provide at least one --calendar")
		}
		from, to, loc, err = o.span.Resolve(time.Now())
		if err != nil {
			return fault.Usage(err)
		}
	}
	if mutation || c.Name == "events.get" {
		if len(o.names) != 1 {
			return fault.New("usage", "provide exactly one --calendar")
		}
		if c.Name != "events.create" && o.object == "" {
			return fault.New("usage", "--path is required")
		}
	}
	if c.Name == "events.get" {
		loc, err = time.LoadLocation(o.span.Timezone)
		if err != nil {
			return fault.New("usage", "invalid timezone: %s", err)
		}
	}
	var request app.Mutation
	if mutation {
		request = app.Mutation{Action: strings.TrimPrefix(c.Name, "events."), Calendar: o.names[0], Path: o.object, Changes: o.changes}
		request, err = request.Validate(p)
		if err != nil {
			return err
		}
		if g.dryRun {
			plan, err := app.Preview(p, request)
			if err != nil {
				return err
			}
			return printPlan(out, plan, g)
		}
		if c.Capability == policy.Delete {
			if err = confirmDelete(os.Stdin, stderr, request, g.noInput, g.force); err != nil {
				return err
			}
		}
	}
	password, err := credentials(g.username, p.NoInput(), stderr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	client := calendar.NewClient(strings.TrimSpace(g.username), password)
	defer client.CloseIdleConnections()
	service := app.New(client, calendar.Endpoint, p)
	switch c.Name {
	case "calendars.list":
		calendars, err := service.Calendars(ctx)
		if err != nil {
			return err
		}
		return printCalendars(out, calendars, g.asJSON, g.plain)
	case "events.list":
		var report func(calendar.QueryStats)
		if o.verbose {
			fmt.Fprintf(stderr, "Range: [%s, %s), timezone: %s\n", from.Format(time.RFC3339), to.Format(time.RFC3339), loc)
			report = func(s calendar.QueryStats) {
				fmt.Fprintf(stderr, "Selected calendar: %q (%s)\nCalendar %q: %d objects returned, %d event instances, %d matching range\n", s.Calendar, clean(s.CalendarPath), s.Calendar, s.Objects, s.Instances, s.Matched)
			}
		}
		events, err := service.Events(ctx, o.names, from, to, loc, report)
		if err != nil {
			return err
		}
		return printEvents(out, events, g.asJSON, g.plain)
	case "events.get":
		object, err := service.Get(ctx, o.names[0], o.object, loc)
		if err != nil {
			return err
		}
		return printObject(out, object, g)
	default:
		path, err := service.Mutate(ctx, request.Calendar, request.Path, request.Action, request.Changes)
		if err != nil {
			return err
		}
		if g.asJSON {
			return json.NewEncoder(out).Encode(map[string]string{"action": request.Action, "path": path})
		}
		if g.plain {
			_, err = fmt.Fprintf(out, "ACTION\tPATH\n%s\t%s\n", request.Action, clean(path))
			return err
		}
		_, err = fmt.Fprintf(out, "Event %s succeeded: %s\n", request.Action, clean(path))
		return err
	}
}
