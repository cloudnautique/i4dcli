package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"i4dcli/internal/app"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
)

const exampleConfig = `{"version":1,"default_account":"home","accounts":{"home":{"username":"home@icloud.com","calendars":["Home"],"timezone":"UTC"},"work":{"username":"work@icloud.com","calendars":["Work","Team"],"timezone":"America/New_York"}}}`

func installConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("I4D_ACCOUNT", "")
	t.Setenv("ICLOUD_USERNAME", "")
	t.Setenv("ICLOUD_APP_PASSWORD", "")
	path := filepath.Join(dir, "i4d", "config.json")
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAccountPrecedenceAndIdentityIsolation(t *testing.T) {
	installConfig(t, exampleConfig)
	for _, tc := range []struct {
		name, envAccount, envUsername string
		flags                         []string
		username, calendar, timezone  string
	}{
		{name: "default", username: "home@icloud.com", calendar: "Home", timezone: "UTC"},
		{name: "environment selection", envAccount: "work", username: "work@icloud.com", calendar: "Work", timezone: "America/New_York"},
		{name: "flag selection", envAccount: "work", flags: []string{"--account", "home"}, username: "home@icloud.com", calendar: "Home", timezone: "UTC"},
		{name: "same identity", envUsername: "HOME@icloud.com", username: "HOME@icloud.com", calendar: "Home", timezone: "UTC"},
		{name: "different environment identity", envUsername: "other@icloud.com", username: "other@icloud.com"},
		{name: "different flag identity", envUsername: "home@icloud.com", flags: []string{"--username", "other@icloud.com"}, username: "other@icloud.com"},
		{name: "explicit blank username", flags: []string{"--username="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("I4D_ACCOUNT", tc.envAccount)
			t.Setenv("ICLOUD_USERNAME", tc.envUsername)
			_, g, err := parseGlobal(tc.flags)
			if err != nil {
				t.Fatal(err)
			}
			a, err := configuredAccount(&g)
			if err != nil {
				t.Fatal(err)
			}
			cal := ""
			if len(a.Calendars) > 0 {
				cal = a.Calendars[0]
			}
			if g.username != tc.username || cal != tc.calendar || a.Timezone != tc.timezone {
				t.Fatalf("username %q defaults %+v", g.username, a)
			}
		})
	}
	_, g, _ := parseGlobal([]string{"--account", "missing"})
	if _, err := configuredAccount(&g); fault.ExitCode(err) != 10 {
		t.Fatalf("unknown account: %v", err)
	}
}

func TestCalendarAndTimezoneOverrideDefaults(t *testing.T) {
	installConfig(t, exampleConfig)
	for _, flags := range [][]string{nil, {"--calendar", "Explicit", "--calendar", "Second", "--timezone", "Europe/London"}} {
		_, g, _ := parseGlobal(nil)
		a, err := configuredAccount(&g)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := policy.Resolve("events.list")
		fs, o := commandFlags(c, io.Discard)
		if err := fs.Parse(flags); err != nil {
			t.Fatal(err)
		}
		applyEventDefaults(fs, o, a)
		wantNames, wantZone := "Home", "UTC"
		if len(flags) > 0 {
			wantNames, wantZone = "Explicit,Second", "Europe/London"
		}
		if strings.Join(o.names, ",") != wantNames || o.span.Timezone != wantZone {
			t.Fatalf("%+v", o)
		}
	}
}

func TestConfiguredMutationPreviewAndPolicy(t *testing.T) {
	installConfig(t, exampleConfig)
	old := http.DefaultTransport
	http.DefaultTransport = trapTransport{t}
	defer func() { http.DefaultTransport = old }()
	args := []string{"events", "create", "--dry-run", "--json", "--summary", "Lunch", "--start", "2026-10-29", "--end", "2026-10-30"}
	var out bytes.Buffer
	if err := Run(context.Background(), args, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var plan app.Plan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Request.Calendar != "Home" {
		t.Fatalf("%s", out.String())
	}
	out.Reset()
	if err := Run(context.Background(), append(args, "--account", "work"), &out, io.Discard); fault.ExitCode(err) != 2 {
		t.Fatalf("multiple defaults must not pick a mutation target: %v", err)
	}
	out.Reset()
	if err := Run(context.Background(), append(args, "--readonly"), &out, io.Discard); fault.ExitCode(err) != 6 {
		t.Fatalf("readonly bypassed: %v", err)
	}
}

func TestConfigInspectionAndErrorsStayLocal(t *testing.T) {
	path := installConfig(t, `{"password":"private-password"}`)
	old := http.DefaultTransport
	http.DefaultTransport = trapTransport{t}
	defer func() { http.DefaultTransport = old }()
	for _, args := range [][]string{{"--help"}, {"help", "config"}, {"help", "events", "create"}, {"policy", "--json"}, {"schema"}, {"config", "path"}, {"mcp", "--list-tools"}} {
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, io.Discard); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"config", "show", "--json"}, {"calendars", "list", "--json"}, {"mcp"}} {
		var out bytes.Buffer
		err := Run(context.Background(), args, &out, io.Discard)
		if fault.ExitCode(err) != 10 || strings.Contains(out.String(), "private-password") || strings.Contains(err.Error(), "private-password") {
			t.Fatalf("%v: %v output=%s", args, err, out.String())
		}
		if args[0] == "mcp" && out.Len() != 0 {
			t.Fatal("config error polluted MCP stdout")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"config", "show", "--readonly", "--enable-commands-exact", "events.list"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"exists": false`) {
		t.Fatal(out.String())
	}
}
