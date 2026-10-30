package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"i4dcli/internal/app"
	"i4dcli/internal/calendar"
	"i4dcli/internal/config"
	"i4dcli/internal/policy"
)

type eventOptions struct {
	names                                  calendarNames
	span                                   app.Range
	verbose                                bool
	object, summary, location, description string
	rrule, organizer, partstat             string
	attendees                              calendarNames
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
		if c.Name == "events.get" || c.Name == "events.edit" || c.Name == "events.delete" || c.Capability == policy.Send {
			fs.StringVar(&o.object, "path", "", "event path from listing (required)")
		}
		if c.Capability == policy.Create || c.Capability == policy.Edit || c.Name == "events.invite" {
			fs.StringVar(&o.summary, "summary", "", "event title")
			fs.StringVar(&o.location, "location", "", "location; empty clears it")
			fs.StringVar(&o.description, "description", "", "description; empty clears it")
			fs.StringVar(&o.changes.Start, "start", "", "start: YYYY-MM-DD or RFC3339; requires --end")
			fs.StringVar(&o.changes.End, "end", "", "exclusive end; same format as --start")
		}

		if c.Capability == policy.Create || c.Capability == policy.Edit || c.Capability == policy.Delete || c.Capability == policy.Send {
			fs.StringVar(&o.changes.Scope, "scope", "", "recurrence mutation scope: series or occurrence")
			fs.StringVar(&o.changes.RecurrenceID, "recurrence-id", "", "original occurrence start: date or RFC3339 timestamp")
		}
		if c.Capability == policy.Create || c.Capability == policy.Edit || c.Name == "events.invite" {
			fs.StringVar(&o.rrule, "rrule", "", "recurrence rule; empty clears recurrence on series edit")
			fs.StringVar(&o.changes.Timezone, "timezone", "", "IANA timezone for event start/end; requires both boundaries")
		}
		if c.Capability == policy.Send {
			fs.StringVar(&o.organizer, "organizer", "", "organizer email; omitted discovers authenticated principal identity")
			fs.Var(&o.attendees, "attendee", "attendee email; repeat for invite, one for response")
			fs.StringVar(&o.partstat, "partstat", "", "response status: ACCEPTED, DECLINED, or TENTATIVE")
		}
	}
	return fs, o
}
func (o *eventOptions) supplied(fs *flag.FlagSet) error {
	var err error
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "rrule":
			o.changes.RRule = &o.rrule
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

func applyCalendarDefaults(fs *flag.FlagSet, o *eventOptions, account config.Account) {
	applyEventDefaults(fs, o, account)
	suppliedTimezone := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "timezone" {
			suppliedTimezone = true
		}
	})
	if !suppliedTimezone && account.Timezone != "" && o.changes.Start != "" {
		o.changes.Timezone = account.Timezone
	}
}
