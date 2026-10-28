package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"i4dcli/internal/app"
	"i4dcli/internal/calendar"
	"i4dcli/internal/policy"
)

type eventOptions struct {
	names                                  calendarNames
	span                                   app.Range
	verbose                                bool
	object, summary, location, description string
	changes                                calendar.Changes
}

func commandFlags(c policy.Command, output io.Writer) (*flag.FlagSet, *eventOptions) {
	fs := flag.NewFlagSet("i4d "+strings.ReplaceAll(c.Name, ".", " "), flag.ContinueOnError)
	fs.SetOutput(output)
	fs.Usage = func() { fmt.Fprintf(output, "Usage: i4d %s\n", c.Usage); fs.PrintDefaults(); printGlobalHelp(output) }
	o := &eventOptions{}
	if strings.HasPrefix(c.Name, "events.") {
		fs.Var(&o.names, "calendar", "calendar full name or path; repeat for listing")
		if c.Name == "events.list" || c.Name == "events.get" {
			fs.StringVar(&o.span.Timezone, "timezone", "Local", "IANA timezone for dates and display")
		}
		if c.Name == "events.list" {
			fs.BoolVar(&o.verbose, "verbose", false, "show range and per-calendar counts on stderr")
			fs.StringVar(&o.span.From, "from", "", "inclusive start: YYYY-MM-DD or RFC3339")
			fs.StringVar(&o.span.To, "to", "", "exclusive end: YYYY-MM-DD or RFC3339")
			fs.BoolVar(&o.span.Today, "today", false, "today in the selected timezone")
			fs.BoolVar(&o.span.Tomorrow, "tomorrow", false, "tomorrow in the selected timezone")
			fs.IntVar(&o.span.Days, "days", 0, "number of local calendar days starting today (1–3660)")
		}
		if c.Name == "events.get" || c.Name == "events.edit" || c.Name == "events.delete" {
			fs.StringVar(&o.object, "path", "", "event path from listing (required)")
		}
		if c.Capability == policy.Create || c.Capability == policy.Edit {
			fs.StringVar(&o.summary, "summary", "", "event title")
			fs.StringVar(&o.location, "location", "", "location; empty clears it")
			fs.StringVar(&o.description, "description", "", "description; empty clears it")
			fs.StringVar(&o.changes.Start, "start", "", "start: YYYY-MM-DD or RFC3339; requires --end")
			fs.StringVar(&o.changes.End, "end", "", "exclusive end; same format as --start")
		}
	}
	return fs, o
}
func (o *eventOptions) supplied(fs *flag.FlagSet) error {
	var err error
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "summary":
			o.changes.Summary = &o.summary
		case "location":
			o.changes.Location = &o.location
		case "description":
			o.changes.Description = &o.description
		case "days":
			if o.span.Days <= 0 {
				err = fmt.Errorf("--days must be between 1 and 3660")
			}
		case "start", "end":
			if f.Value.String() == "" {
				err = fmt.Errorf("--%s cannot be empty", f.Name)
			}
		case "from", "to":
			if f.Value.String() == "" {
				err = fmt.Errorf("--%s cannot be empty", f.Name)
			}
		}
	})
	return err
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
