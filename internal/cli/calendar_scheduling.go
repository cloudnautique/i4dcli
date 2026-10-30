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

	"i4dcli/internal/app"
	"i4dcli/internal/calendar"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
)

func runCalendarScheduling(ctx context.Context, args []string, g globals, p policy.Policy, out, stderr io.Writer) error {
	c, err := policy.Resolve("events." + args[0])
	if err != nil {
		return fault.Usage(err)
	}
	if err = p.Check(c.Name); err != nil {
		return err
	}
	fs, o := commandFlags(c, stderr)
	if err = fs.Parse(args[1:]); err != nil {
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
	account, err := configuredAccount(&g)
	if err != nil {
		return err
	}
	applyCalendarDefaults(fs, o, account)
	if len(o.names) != 1 {
		return fault.New("usage", "provide exactly one --calendar")
	}
	r := calendar.Scheduling{Action: strings.TrimPrefix(c.Name, "events."), Organizer: o.organizer, PartStat: strings.ToUpper(o.partstat), Changes: o.changes}
	if r.Action == "respond" {
		if len(o.attendees) > 1 {
			return fault.New("usage", "respond accepts at most one --attendee")
		}
		if len(o.attendees) == 1 {
			r.Attendee = o.attendees[0]
		}
	} else {
		r.Attendees = append([]string(nil), o.attendees...)
	}
	r, err = app.ValidateScheduling(p, o.names[0], o.object, r)
	if err != nil {
		return err
	}
	if g.dryRun {
		return printMailValue(out, map[string]any{"action": c.Name, "dry_run": true, "validation": "local", "remote_checked": false, "calendar": o.names[0], "path": o.object, "request": r, "note": "Principal identity, recipient delivery, recurrence membership, server scheduling support and ETags have not been checked."}, g)
	}
	if err = confirmMail(os.Stdin, stderr, "Send calendar "+r.Action+" for "+o.names[0], p.NoInput(), g.force); err != nil {
		return err
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
	path, err := service.Schedule(ctx, o.names[0], o.object, r)
	if err != nil {
		return err
	}
	if g.asJSON {
		return json.NewEncoder(out).Encode(map[string]string{"action": r.Action, "path": path})
	}
	if g.plain {
		_, err = fmt.Fprintf(out, "ACTION\tPATH\n%s\t%s\n", r.Action, clean(path))
		return err
	}
	_, err = fmt.Fprintf(out, "Calendar %s succeeded: %s\n", r.Action, clean(path))
	return err
}
