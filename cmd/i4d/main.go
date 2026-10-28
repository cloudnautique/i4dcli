package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"golang.org/x/term"
	"i4dcli/internal/calendar"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		fmt.Fprintln(out, "Usage: i4d calendars list [options]\n       i4d events list --calendar NAME [--calendar NAME ...] --from DATE --to DATE [options]\n       i4d events create --calendar NAME --summary TEXT --start DATE --end DATE [options]\n       i4d events edit --calendar NAME --path PATH [--summary TEXT] [--start DATE --end DATE] [options]\n       i4d events delete --calendar NAME --path PATH [options]\n\nCredentials: ICLOUD_USERNAME and ICLOUD_APP_PASSWORD.\nIf the password is unset, an interactive terminal prompt is used.")
		return nil
	}
	if len(args) < 2 || (args[0] != "calendars" && args[0] != "events") || (args[1] != "list" && (args[0] != "events" || (args[1] != "create" && args[1] != "edit" && args[1] != "delete"))) {
		return errors.New("expected: i4d calendars list or i4d events list/create/edit/delete (see --help)")
	}
	flags := flag.NewFlagSet("i4d "+args[0]+" "+args[1], flag.ContinueOnError)
	flags.SetOutput(stderr)
	username := flags.String("username", os.Getenv("ICLOUD_USERNAME"), "Apple Account email address")
	asJSON := flags.Bool("json", false, "output JSON")
	timeout := flags.Duration("timeout", 30*time.Second, "timeout for the entire operation")
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
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
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
	*username = strings.TrimSpace(*username)
	if *username == "" {
		return errors.New("set ICLOUD_USERNAME or pass --username")
	}
	password := os.Getenv("ICLOUD_APP_PASSWORD")
	if password == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("set ICLOUD_APP_PASSWORD or run in a terminal for a hidden password prompt")
		}
		fmt.Fprint(stderr, "Apple app-specific password: ")
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		password = string(value)
	}
	if strings.TrimSpace(password) == "" {
		return errors.New("app-specific password is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client := calendar.NewClient(*username, password)
	defer client.CloseIdleConnections()
	calendars, err := calendar.List(ctx, client, calendar.Endpoint)
	if err != nil {
		return err
	}
	if args[0] == "events" {
		selected, err := calendar.Select(calendars, names)
		if err != nil {
			return err
		}
		if mutation {
			path, err := calendar.Mutate(ctx, client, calendar.Endpoint, selected[0], args[1], object, changes)
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
			for _, c := range selected {
				fmt.Fprintf(stderr, "Selected calendar: %q (%s)\n", c.Name, c.Path)
			}
			report = func(s calendar.QueryStats) {
				fmt.Fprintf(stderr, "Calendar %q: %d objects returned, %d event instances, %d matching range\n", s.Calendar, s.Objects, s.Instances, s.Matched)
			}
		}
		events, err := calendar.Events(ctx, client, calendar.Endpoint, selected, from, to, loc, report)
		if err != nil {
			return err
		}
		if *asJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(events)
		}
		if len(events) == 0 {
			_, err := fmt.Fprintln(out, "No events found.")
			return err
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "START\tEND\tCALENDAR\tSUMMARY\tSTATUS")
		for _, e := range events {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Start, e.End, clean(e.Calendar), clean(e.Summary), clean(e.Status))
		}
		return w.Flush()
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(calendars)
	}
	if len(calendars) == 0 {
		_, err := fmt.Fprintln(out, "No calendars found.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCOMPONENTS\tPATH")
	for _, c := range calendars {
		fmt.Fprintf(w, "%s\t%s\t%s\n", clean(c.Name), clean(strings.Join(c.Components, ",")), clean(c.Path))
	}
	return w.Flush()
}

// Prevent remote display names from injecting terminal control sequences.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
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
