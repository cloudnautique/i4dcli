package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"

	"i4dcli/internal/calendar"
	"i4dcli/internal/policy"
)

func printEvents(out io.Writer, events []calendar.Event, asJSON bool) error {
	if asJSON {
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
func printCalendars(out io.Writer, calendars []calendar.Calendar, asJSON bool) error {

	if asJSON {
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

func printGlobalHelp(out io.Writer) {
	fmt.Fprintln(out, `
Global options (before or after the command):
  --username EMAIL              Apple Account email (or ICLOUD_USERNAME)
  --json                        JSON output
  --timeout 30s                 Timeout for the entire operation
  --readonly                    Block all mutations
  --no-input                    Never prompt for credentials
  --profile NAME                readonly, editor, or full (default full)
  --enable-commands-exact LIST   Comma-separated command names, e.g. events.list

Credentials: ICLOUD_USERNAME and ICLOUD_APP_PASSWORD.
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
func printPolicy(out io.Writer, permissions policy.Policy, asJSON bool) error {
	snapshot := permissions.Snapshot()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(snapshot)
	}
	fmt.Fprintf(out, "Embedded profile: %s\nRuntime profile: %s\nRead-only: %t\nNo input: %t\n", snapshot.EmbeddedProfile, snapshot.Profile, snapshot.ReadOnly, snapshot.NoInput)
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "COMMAND\tALLOWED\tREASON")
	for _, c := range snapshot.Commands {
		fmt.Fprintf(w, "%s\t%t\t%s\n", c.Command, c.Allowed, c.Reason)
	}
	return w.Flush()
}
