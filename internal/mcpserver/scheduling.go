package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mark3labs/mcp-go/mcp"
	"i4dcli/internal/app"
	"i4dcli/internal/calendar"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
	"strings"
)

type schedulingInput struct {
	Organizer    string   `json:"organizer"`
	Attendee     string   `json:"attendee"`
	Calendar     string   `json:"calendar"`
	Path         string   `json:"path"`
	Summary      *string  `json:"summary"`
	Location     *string  `json:"location"`
	Description  *string  `json:"description"`
	Start        string   `json:"start"`
	End          string   `json:"end"`
	RRule        *string  `json:"rrule"`
	Scope        string   `json:"scope"`
	RecurrenceID string   `json:"recurrence_id"`
	Timezone     string   `json:"timezone"`
	Attendees    []string `json:"attendees"`
	PartStat     string   `json:"partstat"`
}

func executeScheduling(ctx context.Context, p policy.Policy, o Options, s spec, req mcp.CallToolRequest, factory Factory) (any, error) {
	if err := p.Check(s.command); err != nil {
		return nil, err
	}
	if !o.AllowSend || !o.AllowWrite {
		return nil, fault.New("permission_denied", "calendar scheduling requires --allow-write and --allow-send")
	}
	if s.command == "events.cancel" && !o.AllowDelete {
		return nil, fault.New("permission_denied", "meeting cancellation also requires --allow-delete")
	}
	raw, err := json.Marshal(req.GetArguments())
	if err != nil {
		return nil, fault.Usage(err)
	}
	var in schedulingInput
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&in); err != nil {
		return nil, fault.Usage(err)
	}
	if in.Timezone == "" && in.Start != "" {
		in.Timezone = o.DefaultTimezone
	}
	r := calendar.Scheduling{Organizer: in.Organizer, Attendee: in.Attendee, Action: strings.TrimPrefix(s.command, "events."), Attendees: in.Attendees, PartStat: in.PartStat, Changes: calendar.Changes{Summary: in.Summary, Location: in.Location, Description: in.Description, Start: in.Start, End: in.End, RRule: in.RRule, Scope: in.Scope, RecurrenceID: in.RecurrenceID, Timezone: in.Timezone}}
	if r, err = app.ValidateScheduling(p, in.Calendar, in.Path, r); err != nil {
		return nil, err
	}
	if o.DryRun {
		return map[string]any{"dry_run": true, "validation": "local", "remote_checked": false, "calendar": in.Calendar, "path": in.Path, "request": r}, nil
	}
	if !o.Force {
		return nil, fault.New("confirmation_required", "server operator must supply --force to authorize invitation delivery")
	}
	service, close, err := factory(ctx)
	if err != nil {
		return nil, err
	}
	defer close()
	path, err := service.Scheduling(ctx, in.Calendar, in.Path, r)
	if err != nil {
		return nil, err
	}
	return map[string]any{"action": s.command, "path": path, "scheduling": "server_managed", "delivery_confirmed": false}, nil
}
func schedulingTool(s spec) mcp.Tool {
	opts := []mcp.ToolOption{mcp.WithDescription(s.description), mcp.WithSchemaAdditionalProperties(false), mcp.WithReadOnlyHintAnnotation(false), mcp.WithDestructiveHintAnnotation(s.command == "events.cancel"), mcp.WithIdempotentHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(true), mcp.WithString("calendar", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(4096)), mcp.WithString("scope", mcp.Enum("series", "occurrence")), mcp.WithString("recurrence_id", mcp.MaxLength(128))}
	pathOpts := []mcp.PropertyOption{mcp.MinLength(1), mcp.MaxLength(4096)}
	if s.command != "events.invite" {
		pathOpts = append(pathOpts, mcp.Required())
	}
	opts = append(opts, mcp.WithString("path", pathOpts...))
	if s.command == "events.invite" || s.command == "events.cancel" {
		opts = append(opts, mcp.WithString("organizer", mcp.MaxLength(320), mcp.Description("Optional principal-owned calendar address; verified by the server")))
	}
	if s.command == "events.invite" {
		for _, field := range []string{"summary", "location", "description", "start", "end", "rrule", "timezone"} {
			opts = append(opts, mcp.WithString(field, mcp.MaxLength(8192)))
		}
		opts = append(opts, mcp.WithArray("attendees", mcp.MinItems(1), mcp.MaxItems(100), mcp.WithStringItems(mcp.MinLength(1), mcp.MaxLength(320))))
	}
	if s.command == "events.respond" {
		opts = append(opts, mcp.WithString("attendee", mcp.MaxLength(320), mcp.Description("Optional principal-owned attendee address; verified by the server")), mcp.WithString("partstat", mcp.Required(), mcp.Enum("ACCEPTED", "DECLINED", "TENTATIVE")))
	}
	return mcp.NewTool(s.name, opts...)
}
