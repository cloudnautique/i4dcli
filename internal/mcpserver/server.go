// Package mcpserver exposes typed calendar tools using the shared application layer.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"i4dcli/internal/app"
	"i4dcli/internal/calendar"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
)

type Options struct {
	DefaultTimezone                        string
	AllowTools                             []string
	AllowWrite, AllowDelete, Force, DryRun bool
	Timeout                                time.Duration
	MaxResults, MaxOutputBytes             int
}
type Factory func(context.Context) (*app.Service, func(), error)
type spec struct{ name, command, description string }

var specs = []spec{
	{"calendars_list", "calendars.list", "List available calendars."},
	{"events_list", "events.list", "List events in a date range. A truncated result requires narrowing the range; no events beyond the limit are returned."},
	{"events_get", "events.get", "Inspect an event resource, including recurrence rules and exceptions. Inspection does not imply it is editable."},
	{"events_create", "events.create", "Create a standalone event. All-day end dates are exclusive."},
	{"events_edit", "events.edit", "Edit a standalone event. Omitted fields are preserved; empty location or description clears it. Start/end must be supplied together."},
	{"events_delete", "events.delete", "Delete a standalone event. Requires operator authorization; the tool cannot grant itself permission."},
}

func enabled(p policy.Policy, o Options) ([]spec, error) {
	if err := p.Check("mcp"); err != nil {
		return nil, err
	}
	if o.Timeout <= 0 || o.MaxResults < 1 || o.MaxResults > 10000 || o.MaxOutputBytes < 1024 || o.MaxOutputBytes > 10<<20 {
		return nil, fault.New("usage", "MCP limits: positive timeout, max-results 1–10000, max-output-bytes 1024–10485760")
	}
	if o.AllowDelete && !o.AllowWrite {
		return nil, fault.New("usage", "--allow-delete requires --allow-write")
	}
	selected := map[string]bool{}
	for _, value := range o.AllowTools {
		for _, name := range strings.Split(value, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			known := false
			for _, s := range specs {
				if s.name == name {
					known = true
					break
				}
			}
			if !known {
				return nil, fault.New("usage", "unknown MCP tool %q; use exact tool names", name)
			}
			selected[name] = true
		}
	}
	if len(o.AllowTools) > 0 && len(selected) == 0 {
		return nil, fault.New("usage", "--allow-tool must name at least one tool")
	}
	result := []spec{}
	for _, s := range specs {
		if len(selected) > 0 && !selected[s.name] {
			continue
		}
		c, _ := policy.Resolve(s.command)
		if c.Capability != policy.Read && !o.AllowWrite {
			continue
		}
		if c.Capability == policy.Delete && !o.AllowDelete {
			continue
		}
		if p.Check(s.command) != nil {
			continue
		}
		result = append(result, s)
	}
	if len(result) == 0 {
		return nil, fault.New("permission_denied", "no MCP tools enabled by the selected permissions")
	}
	return result, nil
}

func Tools(p policy.Policy, o Options) ([]mcp.Tool, error) {
	allowed, err := enabled(p, o)
	if err != nil {
		return nil, err
	}
	result := []mcp.Tool{}
	for _, s := range allowed {
		result = append(result, tool(s))
	}
	return result, nil
}
func New(p policy.Policy, o Options, factory Factory) (*server.MCPServer, error) {
	allowed, err := enabled(p, o)
	if err != nil {
		return nil, err
	}
	srv := server.NewMCPServer("i4d", "0.1.0", server.WithToolCapabilities(false), server.WithInputSchemaValidation())
	for _, s := range allowed {
		srv.AddTool(tool(s), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			ctx, cancel := context.WithTimeout(ctx, o.Timeout)
			defer cancel()
			value, err := execute(ctx, p, o, s, req, factory)
			if err != nil {
				return errorResult(err), nil
			}
			envelope := map[string]any{"result": value}
			encoded, err := json.Marshal(envelope)
			if err != nil {
				return errorResult(err), nil
			}
			if len(encoded) > o.MaxOutputBytes {
				command, _ := policy.Resolve(s.command)
				if command.Capability != policy.Read && !o.DryRun {
					envelope = map[string]any{"result": map[string]any{"completed": true, "action": s.command, "output_omitted": true, "note": "Operation succeeded. The result exceeded the output limit; inspect events to recover its path. Do not retry the mutation."}}
					encoded, _ = json.Marshal(envelope)
				} else {
					return errorResult(fault.New("error", "tool result exceeds output limit; narrow the query or increase the server's --max-output-bytes")), nil
				}
			}
			// Structured output plus a JSON text fallback works with older MCP clients.
			return mcp.NewToolResultStructured(envelope, string(encoded)), nil
		})
	}
	return srv, nil
}
func errorResult(err error) *mcp.CallToolResult {
	detail := fault.Describe(err)
	if len(detail.Message) > 512 {
		detail.Message = strings.ToValidUTF8(detail.Message[:512], "�") + "…"
	}
	envelope := map[string]any{"error": detail}
	data, _ := json.Marshal(envelope)
	result := mcp.NewToolResultStructured(envelope, string(data))
	result.IsError = true
	return result
}

type input struct {
	Calendar    string   `json:"calendar"`
	Calendars   []string `json:"calendars"`
	Path        string   `json:"path"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	Timezone    string   `json:"timezone"`
	Today       bool     `json:"today"`
	Tomorrow    bool     `json:"tomorrow"`
	Days        int      `json:"days"`
	Limit       int      `json:"limit"`
	Summary     *string  `json:"summary"`
	Location    *string  `json:"location"`
	Description *string  `json:"description"`
	Start       string   `json:"start"`
	End         string   `json:"end"`
}

func execute(ctx context.Context, p policy.Policy, o Options, s spec, req mcp.CallToolRequest, factory Factory) (any, error) {
	if err := p.Check(s.command); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req.GetArguments())
	if err != nil {
		return nil, fault.Usage(err)
	}
	var in input
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&in); err != nil {
		return nil, fault.Usage(err)
	}
	if in.Timezone == "" {
		in.Timezone = o.DefaultTimezone
	}
	var from, to time.Time
	var loc *time.Location
	var mutation app.Mutation
	switch s.command {
	case "events.list":
		from, to, loc, err = (app.Range{From: in.From, To: in.To, Timezone: in.Timezone, Today: in.Today, Tomorrow: in.Tomorrow, Days: in.Days}).Resolve(time.Now())
		if err != nil {
			return nil, fault.Usage(err)
		}
		if len(in.Calendars) == 0 {
			return nil, fault.New("usage", "at least one calendar is required")
		}
	case "events.get":
		zone := in.Timezone
		if zone == "" {
			zone = "Local"
		}
		loc, err = time.LoadLocation(zone)
		if err != nil {
			return nil, fault.Usage(err)
		}
	case "events.create", "events.edit", "events.delete":
		mutation, err = (app.Mutation{Action: strings.TrimPrefix(s.command, "events."), Calendar: in.Calendar, Path: in.Path, Changes: calendar.Changes{Summary: in.Summary, Location: in.Location, Description: in.Description, Start: in.Start, End: in.End}}).Validate(p)
		if err != nil {
			return nil, err
		}
		if o.DryRun {
			return app.Preview(p, mutation)
		}
		if s.command == "events.delete" && !o.Force {
			return nil, fault.New("confirmation_required", "server operator must supply --force to authorize deletion")
		}
	}
	service, close, err := factory(ctx)
	if err != nil {
		return nil, err
	}
	defer close()
	limit := o.MaxResults
	if in.Limit > 0 && in.Limit < limit {
		limit = in.Limit
	}
	switch s.command {
	case "calendars.list":
		values, err := service.Calendars(ctx)
		if err != nil {
			return nil, err
		}
		count := len(values)
		if len(values) > limit {
			values = values[:limit]
		}
		return map[string]any{"calendars": values, "returned": len(values), "matched": count, "truncated": count > len(values)}, nil
	case "events.list":
		values, err := service.Events(ctx, in.Calendars, from, to, loc, nil)
		if err != nil {
			return nil, err
		}
		count := len(values)
		if len(values) > limit {
			values = values[:limit]
		}
		return map[string]any{"events": values, "returned": len(values), "matched": count, "truncated": count > len(values)}, nil
	case "events.get":
		return service.Get(ctx, in.Calendar, in.Path, loc)
	default:
		path, err := service.Mutate(ctx, mutation.Calendar, mutation.Path, mutation.Action, mutation.Changes)
		if err != nil {
			return nil, err
		}
		return map[string]string{"action": mutation.Action, "path": path}, nil
	}
}

func tool(s spec) mcp.Tool {
	c, _ := policy.Resolve(s.command)
	opts := []mcp.ToolOption{mcp.WithDescription(s.description), mcp.WithSchemaAdditionalProperties(false), mcp.WithReadOnlyHintAnnotation(c.Capability == policy.Read), mcp.WithDestructiveHintAnnotation(c.Capability == policy.Delete || c.Capability == policy.Edit), mcp.WithIdempotentHintAnnotation(c.Capability == policy.Read), mcp.WithOpenWorldHintAnnotation(true)}
	if s.command == "events.list" {
		opts = append(opts, mcp.WithArray("calendars", mcp.Required(), mcp.MinItems(1), mcp.MaxItems(50), mcp.WithStringItems(mcp.MinLength(1), mcp.MaxLength(4096))),
			mcp.WithString("from", mcp.MinLength(1), mcp.MaxLength(64)), mcp.WithString("to", mcp.MinLength(1), mcp.MaxLength(64)),
			mcp.WithBoolean("today"), mcp.WithBoolean("tomorrow"), mcp.WithInteger("days", mcp.Min(1), mcp.Max(3660)))
	}
	if s.command == "events.list" || s.command == "calendars.list" {
		opts = append(opts, mcp.WithInteger("limit", mcp.Min(1), mcp.Max(10000)))
	}
	if s.command == "events.list" || s.command == "events.get" {
		opts = append(opts, mcp.WithString("timezone", mcp.MinLength(1), mcp.MaxLength(128), mcp.Description("IANA timezone; defaults to the configured account timezone or server local timezone")))
	}
	if s.command != "events.list" && s.command != "calendars.list" {
		opts = append(opts, mcp.WithString("calendar", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(4096)))
	}
	if s.command == "events.get" || s.command == "events.edit" || s.command == "events.delete" {
		opts = append(opts, mcp.WithString("path", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(4096)))
	}
	if c.Capability == policy.Create || c.Capability == policy.Edit {
		summary := []mcp.PropertyOption{mcp.MinLength(1), mcp.MaxLength(10000)}
		dates := []mcp.PropertyOption{mcp.MinLength(1), mcp.MaxLength(64)}
		if c.Capability == policy.Create {
			summary = append(summary, mcp.Required())
			dates = append(dates, mcp.Required())
		}
		opts = append(opts, mcp.WithString("summary", summary...), mcp.WithString("start", dates...), mcp.WithString("end", dates...), mcp.WithString("location", mcp.MaxLength(10000)), mcp.WithString("description", mcp.MaxLength(100000)))
	}
	return mcp.NewTool(s.name, opts...)
}
