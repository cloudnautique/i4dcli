package mcpserver

import (
	"context"
	"github.com/mark3labs/mcp-go/mcp"
	"i4dcli/internal/app"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
	"testing"
)

func TestSchedulingPreviewAndSendGate(t *testing.T) {
	p, _ := policy.New(policy.Options{})
	o := options()
	o.AllowWrite = true
	o.AllowSend = true
	o.DryRun = true
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"calendar": "Work", "summary": "Meeting", "start": "2026-11-01T12:00:00Z", "end": "2026-11-01T13:00:00Z", "attendees": []string{"you@example.com"}}
	factory := func(context.Context) (*app.Service, func(), error) {
		t.Fatal("preview reached calendar factory")
		return nil, nil, nil
	}
	if _, err := executeScheduling(t.Context(), p, o, spec{command: "events.invite"}, req, factory); err != nil {
		t.Fatal(err)
	}
	o.DryRun = false
	if _, err := executeScheduling(t.Context(), p, o, spec{command: "events.invite"}, req, factory); fault.ExitCode(err) != 11 {
		t.Fatal(err)
	}
	o.DryRun = true
	o.AllowSend = false
	if _, err := executeScheduling(t.Context(), p, o, spec{command: "events.invite"}, req, factory); fault.ExitCode(err) != 6 {
		t.Fatal(err)
	}
	for _, name := range []string{"readonly", "editor"} {
		p, _ := policy.New(policy.Options{Profile: name})
		if p.Check("events.invite") == nil {
			t.Fatal(name, "allowed invitation")
		}
	}
}

func TestSummaryOnlyRecurringPreviewKeepsStoredTimezone(t *testing.T) {
	p, _ := policy.New(policy.Options{})
	o := options()
	o.AllowWrite = true
	o.DryRun = true
	o.DefaultTimezone = "America/New_York"
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"calendar": "Work", "path": "/cal/e.ics", "scope": "series", "summary": "Updated"}
	factory := func(context.Context) (*app.Service, func(), error) {
		t.Fatal("preview opened connection")
		return nil, nil, nil
	}
	if _, err := execute(t.Context(), p, o, spec{command: "events.edit"}, req, factory); err != nil {
		t.Fatal(err)
	}
}
