package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"i4dcli/internal/app"
	"i4dcli/internal/calendar"
	"i4dcli/internal/policy"
)

func Run(ctx context.Context, args []string, out, stderr io.Writer) error {
	normalized, global, err := parseGlobal(args)
	if err != nil {
		return err
	}
	permissions, err := policy.New(policy.Options{Profile: global.profile, ReadOnly: global.readonly, NoInput: global.noInput, EnabledExact: global.exact})
	if err != nil {
		return err
	}
	args = normalized
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		return printHelp(out, permissions)
	}
	if args[0] == "help" {
		args = append(args[1:], "--help")
	}
	if len(args) == 1 && args[0] == "--help" {
		return printHelp(out, permissions)
	}
	if args[0] == "policy" {
		if len(args) > 1 {
			if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
				return printHelp(out, permissions)
			}
			return fmt.Errorf("unexpected policy argument %q", args[1])
		}
		return printPolicy(out, permissions, global.asJSON)
	}
	if (args[0] == "events" || args[0] == "calendars") && (len(args) == 1 || (len(args) == 2 && (args[1] == "--help" || args[1] == "-h"))) {
		return printHelp(out, permissions, args[0])
	}
	if len(args) < 2 {
		return errors.New("expected a command and subcommand (see --help)")
	}
	command, err := policy.Resolve(args[0] + "." + args[1])
	if err != nil {
		return err
	}
	if err := permissions.Check(command.Name); err != nil {
		return err
	}
	parts := strings.Split(command.Name, ".")
	args[0], args[1] = parts[0], parts[1]
	flags := flag.NewFlagSet("i4d "+args[0]+" "+args[1], flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: i4d %s\n", command.Usage)
		flags.PrintDefaults()
		printGlobalHelp(stderr)
	}
	username, asJSON, timeout := &global.username, &global.asJSON, &global.timeout
	var names calendarNames
	var fromArg, toArg, zone string
	var verbose bool
	var changes calendar.Changes
	var object, summary, location, description string
	mutation := args[0] == "events" && args[1] != "list"
	if args[0] == "events" {
		if !mutation {
			flags.BoolVar(&verbose, "verbose", false, "show resolved range and per-calendar result counts on stderr")

			flags.StringVar(&fromArg, "from", "", "inclusive start: YYYY-MM-DD or RFC3339 (required)")
			flags.StringVar(&toArg, "to", "", "exclusive end: YYYY-MM-DD or RFC3339 (required)")
			flags.StringVar(&zone, "timezone", "Local", "IANA timezone for dates, floating events, and display")
		}
		flags.Var(&names, "calendar", "calendar full name (case-insensitive) or exact path; repeat for listing")
		if mutation {
			if args[1] != "create" {
				flags.StringVar(&object, "path", "", "event path from events list --json (required)")
			}
			if args[1] != "delete" {
				flags.StringVar(&summary, "summary", "", "event title")
				flags.StringVar(&location, "location", "", "event location; empty clears it")
				flags.StringVar(&description, "description", "", "event description; empty clears it")
				flags.StringVar(&changes.Start, "start", "", "start: YYYY-MM-DD or RFC3339; requires --end")
				flags.StringVar(&changes.End, "end", "", "exclusive end, same format as --start")
			}
		}
	}
	if err := flags.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	var from, to time.Time
	var loc *time.Location
	if mutation {
		flags.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "summary":
				changes.Summary = &summary
			case "location":
				changes.Location = &location
			case "description":
				changes.Description = &description
			}
		})
		if len(names) != 1 {
			return errors.New("provide exactly one --calendar")
		}
		if args[1] != "create" && object == "" {
			return errors.New("--path is required")
		}
		if args[1] != "delete" {
			if err := changes.Validate(args[1] == "create"); err != nil {
				return err
			}
		}
	}
	if args[0] == "events" && !mutation {
		var err error
		loc, err = time.LoadLocation(zone)
		if err != nil {
			return fmt.Errorf("invalid timezone: %w", err)
		}
		from, err = parseBoundary(fromArg, loc)
		if err != nil {
			return fmt.Errorf("--from: %w", err)
		}
		to, err = parseBoundary(toArg, loc)
		if err != nil {
			return fmt.Errorf("--to: %w", err)
		}
		if !from.Before(to) {
			return errors.New("--to must be later than --from")
		}
		if len(names) == 0 {
			return errors.New("provide at least one --calendar")
		}
	}
	password, err := credentials(*username, permissions.NoInput(), stderr)
	if err != nil {
		return err
	}
	*username = strings.TrimSpace(*username)
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client := calendar.NewClient(*username, password)
	defer client.CloseIdleConnections()
	service := app.New(client, calendar.Endpoint, permissions)
	if args[0] == "events" {
		if mutation {
			path, err := service.Mutate(ctx, names[0], object, args[1], changes)
			if err != nil {
				return err
			}
			if *asJSON {
				return json.NewEncoder(out).Encode(map[string]string{"action": args[1], "path": path})
			}
			_, err = fmt.Fprintf(out, "Event %s succeeded: %s\n", args[1], clean(path))
			return err
		}
		var report func(calendar.QueryStats)
		if verbose {
			fmt.Fprintf(stderr, "Range: [%s, %s), timezone: %s\n", from.Format(time.RFC3339), to.Format(time.RFC3339), loc)
			report = func(s calendar.QueryStats) {
				fmt.Fprintf(stderr, "Selected calendar: %q (%s)\n", s.Calendar, clean(s.CalendarPath))
				fmt.Fprintf(stderr, "Calendar %q: %d objects returned, %d event instances, %d matching range\n", s.Calendar, s.Objects, s.Instances, s.Matched)
			}
		}
		events, err := service.Events(ctx, names, from, to, loc, report)
		if err != nil {
			return err
		}
		return printEvents(out, events, *asJSON)
	}
	calendars, err := service.Calendars(ctx)
	if err != nil {
		return err
	}
	return printCalendars(out, calendars, *asJSON)
}

type calendarNames []string

func (n *calendarNames) String() string { return strings.Join(*n, ", ") }
func (n *calendarNames) Set(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("calendar cannot be empty")
	}
	*n = append(*n, s)
	return nil
}
func parseBoundary(s string, loc *time.Location) (time.Time, error) {
	if t, err := time.ParseInLocation(time.DateOnly, s, loc); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("expected YYYY-MM-DD or RFC3339 timestamp")
}
