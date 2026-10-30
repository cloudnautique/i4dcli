package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"i4dcli/internal/fault"
)

func TestRecurringCalendarPreviews(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = trapTransport{t}
	defer func() { http.DefaultTransport = old }()
	t.Setenv("ICLOUD_USERNAME", "")
	t.Setenv("ICLOUD_APP_PASSWORD", "")
	for _, args := range [][]string{
		{"events", "create", "--calendar", "Work", "--summary", "Daily", "--start", "2026-10-30T10:00:00Z", "--end", "2026-10-30T11:00:00Z", "--rrule", "FREQ=DAILY;COUNT=3"},
		{"events", "edit", "--calendar", "Work", "--path", "/cal/e.ics", "--scope", "occurrence", "--recurrence-id", "2026-10-31T10:00:00Z", "--summary", "Moved"},
		{"events", "delete", "--calendar", "Work", "--path", "/cal/e.ics", "--scope", "series"},
	} {
		var out bytes.Buffer
		if err := Run(context.Background(), append(args, "--dry-run", "--json"), &out, io.Discard); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["dry_run"] != true {
			t.Fatal(out.String(), err)
		}
	}
	for _, args := range [][]string{
		{"events", "delete", "--calendar", "Work", "--path", "/cal/e.ics", "--scope", "future"},
		{"events", "delete", "--calendar", "Work", "--path", "/cal/e.ics", "--scope", "occurrence"},
		{"events", "create", "--calendar", "Work", "--summary", "Bad rule", "--start", "2026-10-30", "--end", "2026-10-31", "--rrule", "not-a-rule"},
	} {
		if err := Run(context.Background(), append(args, "--dry-run"), io.Discard, io.Discard); fault.ExitCode(err) != 2 {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
func TestSchedulingPreviewsAndAuthorization(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = trapTransport{t}
	defer func() { http.DefaultTransport = old }()
	t.Setenv("ICLOUD_USERNAME", "")
	t.Setenv("ICLOUD_APP_PASSWORD", "")
	invite := []string{"events", "invite", "--calendar", "Work", "--summary", "Meeting", "--start", "2026-10-30T10:00:00Z", "--end", "2026-10-30T11:00:00Z", "--organizer", "me@example.com", "--attendee", "you@example.com"}
	for _, args := range [][]string{invite, {"events", "respond", "--calendar", "Work", "--path", "/cal/e.ics", "--attendee", "me@example.com", "--partstat", "ACCEPTED"}, {"events", "cancel", "--calendar", "Work", "--path", "/cal/e.ics", "--organizer", "me@example.com"}} {
		var out bytes.Buffer
		if err := Run(context.Background(), append(args, "--dry-run", "--json"), &out, io.Discard); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), `"remote_checked":false`) {
			t.Fatal(out.String())
		}
	}
	for _, flag := range []string{"--readonly", "--profile=editor"} {
		if err := Run(context.Background(), append(invite, flag, "--dry-run"), io.Discard, io.Discard); fault.ExitCode(err) != 6 {
			t.Fatalf("%s: %v", flag, err)
		}
	}
	if err := Run(context.Background(), append(invite, "--no-input"), io.Discard, io.Discard); fault.ExitCode(err) != 11 {
		t.Fatal(err)
	}
}

func TestCalendarMutationTimezoneDefaults(t *testing.T) {
	installConfig(t, exampleConfig)
	var out bytes.Buffer
	args := []string{"events", "edit", "--path", "/cal/e.ics", "--summary", "Renamed", "--dry-run", "--json"}
	if err := Run(t.Context(), args, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"timezone"`) {
		t.Fatalf("summary-only edit would replace timezone: %s", out.String())
	}
	if err := Run(t.Context(), append(args, "--timezone", "Europe/London"), io.Discard, io.Discard); fault.ExitCode(err) != 2 {
		t.Fatalf("timezone without boundaries: %v", err)
	}
	out.Reset()
	if err := Run(t.Context(), []string{"events", "create", "--summary", "New", "--start", "2026-10-30T10:00:00Z", "--end", "2026-10-30T11:00:00Z", "--dry-run", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"timezone":"UTC"`) {
		t.Fatal(out.String())
	}
}
