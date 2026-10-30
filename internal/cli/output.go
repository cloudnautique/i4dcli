package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"

	"i4dcli/internal/app"
	"i4dcli/internal/calendar"
	"i4dcli/internal/policy"
)

func printEvents(out io.Writer, events []calendar.Event, asJSON bool, plain bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(events)
	}
	if len(events) == 0 && !plain {
		_, err := fmt.Fprintln(out, "No events found.")
		return err
	}
	w, flush := tableWriter(out, plain)
	fmt.Fprintln(w, "START\tEND\tCALENDAR\tSUMMARY\tSTATUS\tPATH")
	for _, e := range events {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Start, e.End, clean(e.Calendar), clean(e.Summary), clean(e.Status), clean(e.Path))
	}
	return flush()
}
func printCalendars(out io.Writer, calendars []calendar.Calendar, asJSON bool, plain bool) error {

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(calendars)
	}
	if len(calendars) == 0 && !plain {
		_, err := fmt.Fprintln(out, "No calendars found.")
		return err
	}
	w, flush := tableWriter(out, plain)
	fmt.Fprintln(w, "NAME\tCOMPONENTS\tPATH")
	for _, c := range calendars {
		fmt.Fprintf(w, "%s\t%s\t%s\n", clean(c.Name), clean(strings.Join(c.Components, ",")), clean(c.Path))
	}
	return flush()
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

func printGlobalHelp(out io.Writer) {
	fmt.Fprintln(out, `
Global options (before or after the command):
  --account NAME                Named account from XDG config (or I4D_ACCOUNT)
  --mail-address EMAIL          iCloud Mail address (or ICLOUD_MAIL_ADDRESS)
  --username EMAIL              Apple Account email (or ICLOUD_USERNAME)
  --json                        JSON output (including structured errors)
  --plain                       Stable TSV output
  --dry-run                     Local-only mutation preview; no network
  --force                       Authorize delete/send confirmation; respects permissions
  --timeout 30s                 Timeout for the entire operation
  --readonly                    Block all mutations
  --no-input                    Never prompt for credentials
  --profile NAME                readonly, editor, or full (default full)
  --enable-commands-exact LIST   Comma-separated command names, e.g. events.list

Credentials: ICLOUD_USERNAME for calendars; ICLOUD_MAIL_ADDRESS for mail.
Both use ICLOUD_APP_PASSWORD (or the hidden interactive prompt).
Use i4d policy --json to inspect effective permissions.`)
}
func printHelp(out io.Writer, permissions policy.Policy, group ...string) error {
	fmt.Fprintln(out, "Usage:")
	for _, c := range policy.Commands() {
		if permissions.Check(c.Name) == nil && (len(group) == 0 || strings.HasPrefix(c.Name, group[0]+".")) {
			fmt.Fprintf(out, "  i4d %s\n", c.Usage)
		}
	}
	fmt.Fprintln(out, "\nUse i4d help COMMAND SUBCOMMAND for options.")
	if permissions.Check("events.edit") == nil {
		fmt.Fprintln(out, "events update is an alias for events edit.")
	}
	printGlobalHelp(out)
	return nil
}
func printPolicy(out io.Writer, permissions policy.Policy, asJSON bool, plain bool) error {
	snapshot := permissions.Snapshot()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(snapshot)
	}
	if !plain {
		fmt.Fprintf(out, "Embedded profile: %s\nRuntime profile: %s\nRead-only: %t\nNo input: %t\n", snapshot.EmbeddedProfile, snapshot.Profile, snapshot.ReadOnly, snapshot.NoInput)
	}
	w, flush := tableWriter(out, plain)
	fmt.Fprintln(w, "COMMAND\tALLOWED\tREASON")
	for _, c := range snapshot.Commands {
		fmt.Fprintf(w, "%s\t%t\t%s\n", c.Command, c.Allowed, c.Reason)
	}
	return flush()
}

type checkedWriter struct {
	io.Writer
	err error
}

func (w *checkedWriter) Write(b []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.Writer.Write(b)
	w.err = err
	return n, err
}
func tableWriter(out io.Writer, plain bool) (io.Writer, func() error) {
	checked := &checkedWriter{Writer: out}
	if plain {
		return checked, func() error { return checked.err }
	}
	w := tabwriter.NewWriter(checked, 0, 4, 2, ' ', 0)
	return w, func() error {
		if err := w.Flush(); err != nil {
			return err
		}
		return checked.err
	}
}
func printPlan(out io.Writer, plan app.Plan, g globals) error {
	if g.asJSON {
		return json.NewEncoder(out).Encode(plan)
	}
	if g.plain {
		data, err := json.Marshal(plan.Request)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "DRY_RUN\tVALIDATION\tREMOTE_CHECKED\tREQUEST_JSON\ntrue\tlocal\tfalse\t%s\n", data)
		return err
	}
	fmt.Fprintf(out, "Dry run: would %s an event in %s\n%s\n", plan.Request.Action, clean(plan.Request.Calendar), plan.Note)
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(plan.Request)
}
func printObject(out io.Writer, object calendar.Object, g globals) error {
	if g.asJSON {
		return json.NewEncoder(out).Encode(object)
	}
	w, flush := tableWriter(out, g.plain)
	fmt.Fprintln(w, "FIELD\tVALUE")
	fmt.Fprintf(w, "path\t%s\netag\t%s\n", clean(object.Path), clean(object.ETag))
	for i, e := range object.Events {
		recurrence, _ := json.Marshal(e.Recurrence)
		for _, f := range []struct{ name, value string }{{"uid", e.UID}, {"summary", e.Summary}, {"start", e.Start}, {"end", e.End}, {"location", e.Location}, {"description", e.Description}, {"status", e.Status}, {"recurrence_id", e.RecurrenceID}, {"recurrence", string(recurrence)}} {
			fmt.Fprintf(w, "events.%d.%s\t%s\n", i, f.name, clean(f.value))
		}
	}
	return flush()
}
