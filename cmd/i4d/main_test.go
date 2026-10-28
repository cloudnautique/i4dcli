package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCLIValidation(t *testing.T) {
	t.Setenv("ICLOUD_USERNAME", "")
	for _, args := range [][]string{{"calendars", "list"}, {"bad"}, {"calendars", "list", "--timeout", "0s"}, {"calendars", "list", "extra"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out, &out); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, &out, &out); err != nil || !strings.Contains(out.String(), "calendars list") {
		t.Fatalf("help: %s %v", out.String(), err)
	}
}
func TestTerminalSanitization(t *testing.T) {
	if got := clean("Name\n\x1b[31m\t"); strings.ContainsAny(got, "\n\x1b\t") {
		t.Fatalf("unsafe: %q", got)
	}
}

func TestEventArguments(t *testing.T) {
	for _, args := range [][]string{
		{"events", "list", "--from", "2026-10-28", "--to", "2026-10-28", "--calendar", "Bill"},
		{"events", "list", "--from", "invalid", "--to", "2026-10-29", "--calendar", "Bill"},
		{"events", "list", "--from", "2026-10-28", "--to", "2026-10-29"},
		{"events", "list", "--from", "2026-10-28", "--to", "2026-10-29", "--timezone", "invalid"},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out, &out); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestMutationArguments(t *testing.T) {
	t.Setenv("ICLOUD_USERNAME", "")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"events", "create", "--calendar", "Bill"}, "--summary"},
		{[]string{"events", "create", "--calendar", "Bill", "--summary", "Title"}, "--start"},
		{[]string{"events", "edit", "--calendar", "Bill"}, "--path"},
		{[]string{"events", "edit", "--calendar", "Bill", "--path", "/cal/e.ics"}, "field to edit"},
		{[]string{"events", "delete", "--calendar", "Bill", "--calendar", "Home", "--path", "/cal/e.ics"}, "exactly one"},
		{[]string{"events", "delete", "--calendar", "Bill", "--path", "/cal/e.ics"}, "ICLOUD_USERNAME"},
		{[]string{"events", "edit", "--calendar", "Bill", "--path", "/cal/e.ics", "--location", ""}, "ICLOUD_USERNAME"},
	} {
		var out bytes.Buffer
		err := run(context.Background(), tc.args, &out, &out)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %s", tc.args, err, tc.want)
		}
	}
	for _, action := range []string{"create", "edit", "delete"} {
		var out bytes.Buffer
		if err := run(context.Background(), []string{"events", action, "--help"}, &out, &out); err != nil || !strings.Contains(out.String(), "-calendar") {
			t.Errorf("help %s: %v", action, err)
		}
	}
}
