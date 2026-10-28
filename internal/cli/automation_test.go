package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"i4dcli/internal/app"
	"i4dcli/internal/calendar"
	"i4dcli/internal/fault"
)

func TestDryRunDoesNotAuthenticateOrConnect(t *testing.T) {
	t.Setenv("ICLOUD_USERNAME", "")
	t.Setenv("ICLOUD_APP_PASSWORD", "")
	old := http.DefaultTransport
	http.DefaultTransport = trapTransport{t}
	defer func() { http.DefaultTransport = old }()
	for _, action := range []string{"create", "edit", "update", "delete"} {
		args := []string{"--json", "--dry-run", "events", action, "--calendar", "Work"}
		if action == "create" {
			args = append(args, "--summary", "Lunch", "--start", "2026-10-28", "--end", "2026-10-29")
		} else {
			args = append(args, "--path", "/cal/e.ics")
		}
		if action == "edit" || action == "update" {
			args = append(args, "--location", "")
		}
		var out, stderr bytes.Buffer
		if err := Run(context.Background(), args, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		var plan app.Plan
		if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
			t.Fatal(err)
		}
		if !plan.DryRun || plan.RemoteChecked || plan.Validation != "local" || stderr.Len() != 0 {
			t.Fatalf("preview %s stderr %s", out.String(), stderr.String())
		}
		if action == "update" && (plan.Request.Action != "edit" || plan.Request.Changes.Location == nil) {
			t.Fatal("alias or clear lost")
		}
	}
}
func TestJSONErrorsAndConfirmation(t *testing.T) {
	t.Setenv("ICLOUD_USERNAME", "")
	t.Setenv("ICLOUD_APP_PASSWORD", "")
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--json", "events", "list", "--today", "--tomorrow", "--calendar", "Work"}, 2},
		{[]string{"--json", "events", "list", "--days", "0", "--calendar", "Work"}, 2},
		{[]string{"--json", "--readonly", "--force", "events", "delete"}, 6},
		{[]string{"--json", "--no-input", "events", "delete", "--calendar", "Work", "--path", "/cal/e.ics"}, 11},
		{[]string{"--json", "--force", "events", "delete", "--calendar", "Work", "--path", "/cal/e.ics"}, 10},
		{[]string{"--json", "--plain", "calendars", "list"}, 2},
		{[]string{"--json", "--timeout=bad", "calendars", "list"}, 2},
		{[]string{"--json", "--timeout"}, 2},
		{[]string{"--timeout=bad", "--json", "calendars", "list"}, 2},
		{[]string{"--json", "--dry-run", "calendars", "list"}, 2},
	} {
		var out, stderr bytes.Buffer
		err := Run(context.Background(), tc.args, &out, &stderr)
		if err == nil || fault.ExitCode(err) != tc.code {
			t.Errorf("%v: %v", tc.args, err)
			continue
		}
		var envelope struct {
			Error fault.Error `json:"error"`
		}
		if err = json.Unmarshal(out.Bytes(), &envelope); err != nil || envelope.Error.Code != tc.code {
			t.Errorf("envelope %s: %v", out.String(), err)
		}
	}
}
func TestConfirmation(t *testing.T) {
	for _, answer := range []string{"no\n", "yes\n", ""} {
		var out bytes.Buffer
		err := readDeleteConfirmation(strings.NewReader(answer), &out, app.Mutation{Calendar: "Work", Path: "/cal/e.ics"})
		if (err == nil) != (answer == "yes\n") {
			t.Errorf("%q: %v", answer, err)
		}
	}
}
func TestSchemaReflectsFlagsAndPermissions(t *testing.T) {
	var out bytes.Buffer
	err := Run(context.Background(), []string{"--readonly", "schema", "--json"}, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		SchemaVersion int `json:"schema_version"`
		Commands      []struct {
			Name  string     `json:"name"`
			Flags []flagInfo `json:"flags"`
		} `json:"commands"`
	}
	if err = json.Unmarshal(out.Bytes(), &schema); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range schema.Commands {
		if c.Name == "events.create" || c.Name == "events.delete" {
			t.Fatal("blocked command exposed")
		}
		if c.Name == "events.list" {
			for _, f := range c.Flags {
				if f.Name == "today" {
					found = true
				}
			}
		}
	}
	if schema.SchemaVersion != 1 || !found {
		t.Fatal(out.String())
	}
}
func TestPlainAndHumanPaths(t *testing.T) {
	event := calendar.Event{Start: "2026-10-28", End: "2026-10-29", Summary: "A\tB\nC", Path: "/cal/e.ics"}
	var out bytes.Buffer
	if err := printEvents(&out, []calendar.Event{event}, false, true); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || len(strings.Split(lines[1], "\t")) != 6 || !strings.Contains(lines[1], "/cal/e.ics") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := printEvents(&out, []calendar.Event{event}, false, false); err != nil || !strings.Contains(out.String(), "/cal/e.ics") {
		t.Fatal(out.String())
	}
}
func TestMCPListAndProtocolErrors(t *testing.T) {
	t.Setenv("ICLOUD_USERNAME", "")
	t.Setenv("ICLOUD_APP_PASSWORD", "")
	var out, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"mcp", "--list-tools"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "events_create") || !strings.Contains(out.String(), "events_get") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := Run(context.Background(), []string{"--json", "mcp"}, &out, &stderr); err == nil || out.Len() != 0 {
		t.Fatalf("MCP stream polluted: %v %s", err, out.String())
	}
}

func TestJSONFlagValueIsNotOutputMode(t *testing.T) {
	var out, stderr bytes.Buffer
	err := Run(context.Background(), []string{"--username", "--json", "--timeout=bad", "calendars", "list"}, &out, &stderr)
	if err == nil || out.Len() != 0 {
		t.Fatalf("flag value interpreted as output mode: %v %s", err, out.String())
	}
}
