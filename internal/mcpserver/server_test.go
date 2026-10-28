package mcpserver

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"i4dcli/internal/app"
	"i4dcli/internal/policy"
)

func options() Options {
	return Options{Timeout: time.Second, MaxResults: 1000, MaxOutputBytes: 1 << 20}
}
func connect(t *testing.T, p policy.Policy, o Options, f Factory) *client.Client {
	t.Helper()
	srv, err := New(p, o, f)
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err = c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	initialize(t, c)
	return c
}
func initialize(t *testing.T, c *client.Client) {
	t.Helper()
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1"}
	if _, err := c.Initialize(t.Context(), req); err != nil {
		t.Fatal(err)
	}
}
func call(t *testing.T, c *client.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	result, err := c.CallTool(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func resultText(r *mcp.CallToolResult) string {
	data, _ := json.Marshal(r.StructuredContent)
	return string(data)
}
func TestDefaultToolSurfaceAndSchemaValidation(t *testing.T) {
	p, _ := policy.New(policy.Options{})
	c := connect(t, p, options(), func(context.Context) (*app.Service, func(), error) {
		t.Fatal("invalid request authenticated")
		return nil, nil, nil
	})
	tools, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("tools %+v", tools.Tools)
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "delete") || strings.Contains(tool.Name, "create") {
			t.Fatal(tool.Name)
		}
	}
	for _, args := range []map[string]any{
		{"calendar": "Work", "path": "/cal/e.ics", "force": true},
		{"calendar": 12, "path": "/cal/e.ics"},
		{"calendar": "Work"},
	} {
		req := mcp.CallToolRequest{}
		req.Params.Name = "events_get"
		req.Params.Arguments = args
		result, err := c.CallTool(t.Context(), req)
		if err == nil && !result.IsError {
			t.Fatalf("accepted invalid args %+v", args)
		}
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = "events_delete"
	req.Params.Arguments = map[string]any{"calendar": "Work", "path": "/cal/e.ics"}
	result, err := c.CallTool(t.Context(), req)
	if err == nil && !result.IsError {
		t.Fatal("hidden write executed")
	}
}
func TestPolicyIntersectionAndSelection(t *testing.T) {
	for _, tc := range []struct {
		profile string
		want    int
	}{{"readonly", 3}, {"editor", 5}, {"full", 6}} {
		p, _ := policy.New(policy.Options{Profile: tc.profile})
		o := options()
		o.AllowWrite = true
		o.AllowDelete = true
		tools, err := Tools(p, o)
		if err != nil || len(tools) != tc.want {
			t.Fatalf("%s: %d %v", tc.profile, len(tools), err)
		}
	}
	p, _ := policy.New(policy.Options{EnabledExact: "mcp,events.update"})
	o := options()
	o.AllowWrite = true
	tools, err := Tools(p, o)
	if err != nil || len(tools) != 1 || tools[0].Name != "events_edit" {
		t.Fatalf("exact grant: %v %v", tools, err)
	}
	for _, selection := range [][]string{{"events.typo"}, {"events.*"}, {", ,"}} {
		o.AllowTools = selection
		if _, err = Tools(p, o); err == nil {
			t.Errorf("accepted %v", selection)
		}
	}
}
func TestPreviewAndDeleteAuthorization(t *testing.T) {
	p, _ := policy.New(policy.Options{})
	factory := func(context.Context) (*app.Service, func(), error) {
		t.Fatal("unexpected authentication")
		return nil, nil, nil
	}
	o := options()
	o.AllowWrite = true
	o.AllowDelete = true
	o.DryRun = true
	c := connect(t, p, o, factory)
	result := call(t, c, "events_create", map[string]any{"calendar": "Work", "summary": "Lunch", "start": "2026-10-28", "end": "2026-10-29"})
	if result.IsError || !strings.Contains(resultText(result), `"remote_checked":false`) {
		t.Fatal(resultText(result))
	}
	result = call(t, c, "events_edit", map[string]any{"calendar": "Work", "path": "/cal/e.ics", "start": "2026-10-28"})
	if !result.IsError || !strings.Contains(resultText(result), `"class":"usage"`) {
		t.Fatal(resultText(result))
	}
	o.DryRun = false
	c2 := connect(t, p, o, factory)
	result = call(t, c2, "events_delete", map[string]any{"calendar": "Work", "path": "/cal/e.ics"})
	if !result.IsError || !strings.Contains(resultText(result), `"class":"confirmation_required"`) {
		t.Fatal(resultText(result))
	}
}

// Exercise actual CRUD through an MCP client and a local CalDAV server.
func TestMCPEventLifecycle(t *testing.T) {
	var mu sync.Mutex
	objects := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PROPFIND":
			href := "/"
			props := `<d:current-user-principal><d:href>/principal/</d:href></d:current-user-principal>`
			if r.URL.Path == "/principal/" {
				href = r.URL.Path
				props = `<c:calendar-home-set><d:href>/calendars/</d:href></c:calendar-home-set>`
			}
			if r.URL.Path == "/calendars/" {
				href = "/calendars/work/"
				props = `<d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Work</d:displayname>`
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>%s</d:href><d:propstat><d:prop>%s</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, href, props)
		case "REPORT":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">`)
			for path, data := range objects {
				fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop><c:calendar-data>`, path)
				_ = xml.EscapeText(w, []byte(data))
				fmt.Fprint(w, `</c:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
			}
			fmt.Fprint(w, `</d:multistatus>`)
		case "PUT":
			if _, exists := objects[r.URL.Path]; exists {
				if r.Header.Get("If-Match") != `"v1"` {
					t.Error("missing ETag guard")
				}
			} else if r.Header.Get("If-None-Match") != "*" {
				t.Error("missing create guard")
			}
			data, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = string(data)
			w.WriteHeader(http.StatusCreated)
		case "GET":
			data, ok := objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			fmt.Fprint(w, data)
		case "DELETE":
			if r.Header.Get("If-Match") != `"v1"` {
				t.Error("missing delete ETag")
			}
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	p, _ := policy.New(policy.Options{})
	o := options()
	o.AllowWrite = true
	o.AllowDelete = true
	o.Force = true
	c := connect(t, p, o, func(context.Context) (*app.Service, func(), error) {
		return app.New(server.Client(), server.URL, p), func() {}, nil
	})
	created := call(t, c, "events_create", map[string]any{"calendar": "Work", "summary": "Lunch", "start": "2026-10-28", "end": "2026-10-29"})
	if created.IsError {
		t.Fatal(resultText(created))
	}
	var envelope struct {
		Result struct {
			Path string `json:"path"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resultText(created)), &envelope); err != nil {
		t.Fatal(err)
	}
	path := envelope.Result.Path
	edited := call(t, c, "events_edit", map[string]any{"calendar": "Work", "path": path, "summary": "Updated lunch"})
	if edited.IsError {
		t.Fatal(resultText(edited))
	}
	got := call(t, c, "events_get", map[string]any{"calendar": "Work", "path": path})
	if got.IsError || !strings.Contains(resultText(got), "Updated lunch") {
		t.Fatal(resultText(got))
	}
	second := call(t, c, "events_create", map[string]any{"calendar": "Work", "summary": "Second", "start": "2026-10-28", "end": "2026-10-29"})
	if second.IsError {
		t.Fatal(resultText(second))
	}
	listed := call(t, c, "events_list", map[string]any{"calendars": []string{"Work"}, "from": "2026-10-28", "to": "2026-10-30", "limit": 1})
	if listed.IsError || !strings.Contains(resultText(listed), `"truncated":true`) || !strings.Contains(resultText(listed), `"returned":1`) {
		t.Fatal(resultText(listed))
	}
	enlarged := call(t, c, "events_edit", map[string]any{"calendar": "Work", "path": path, "description": strings.Repeat("x", 2000)})
	if enlarged.IsError {
		t.Fatal(resultText(enlarged))
	}
	limited := o
	limited.MaxOutputBytes = 1024
	limitedClient := connect(t, p, limited, func(context.Context) (*app.Service, func(), error) {
		return app.New(server.Client(), server.URL, p), func() {}, nil
	})
	oversized := call(t, limitedClient, "events_get", map[string]any{"calendar": "Work", "path": path})
	if !oversized.IsError || !strings.Contains(resultText(oversized), "output limit") {
		t.Fatal(resultText(oversized))
	}
	deleted := call(t, c, "events_delete", map[string]any{"calendar": "Work", "path": path})
	if deleted.IsError {
		t.Fatal(resultText(deleted))
	}
	missing := call(t, c, "events_get", map[string]any{"calendar": "Work", "path": path})
	if !missing.IsError || !strings.Contains(resultText(missing), `"class":"not_found"`) {
		t.Fatal(resultText(missing))
	}
}
func TestToolTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	p, _ := policy.New(policy.Options{})
	o := options()
	o.Timeout = 25 * time.Millisecond
	c := connect(t, p, o, func(context.Context) (*app.Service, func(), error) {
		return app.New(server.Client(), server.URL, p), func() {}, nil
	})
	result := call(t, c, "calendars_list", map[string]any{})
	if !result.IsError || !strings.Contains(resultText(result), `"class":"retryable"`) {
		t.Fatal(resultText(result))
	}
}
