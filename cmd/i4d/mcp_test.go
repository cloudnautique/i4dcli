package main

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestMCPStdio(t *testing.T) {
	t.Setenv("ICLOUD_USERNAME", "")
	t.Setenv("ICLOUD_APP_PASSWORD", "")
	binary := filepath.Join(t.TempDir(), "i4d")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c, err := client.NewStdioMCPClient(binary, nil, "--dry-run", "mcp", "--allow-write")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "i4d-test", Version: "1"}
	if _, err = c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil || len(tools.Tools) != 20 {
		t.Fatalf("tools %v %v", tools, err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = "events_create"
	req.Params.Arguments = map[string]any{"calendar": "Work", "summary": "Lunch", "start": "2026-10-28", "end": "2026-10-29"}
	result, err := c.CallTool(ctx, req)
	if err != nil || result.IsError {
		t.Fatalf("preview %v %v", result, err)
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--json", "--readonly", "events", "delete"}, 6},
		{[]string{"--json", "--no-input", "calendars", "list"}, 10},
		{[]string{"--json", "events", "list", "--calendar", "Work", "--days", "0"}, 2},
	} {
		output, err := exec.Command(binary, tc.args...).Output()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != tc.code {
			t.Fatalf("exit: %v", err)
		}
		var envelope struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(output, &envelope); err != nil || envelope.Error.Code != tc.code {
			t.Fatalf("error JSON: %s %v", output, err)
		}
	}
}
