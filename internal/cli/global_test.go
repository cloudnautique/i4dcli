package cli

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestGlobalFlagPlacement(t *testing.T) {
	for _, args := range [][]string{
		{"--readonly", "events", "update"},
		{"events", "--readonly", "update"},
		{"events", "update", "--readonly"},
		{"events", "delete", "--profile", "editor"},
		{"--enable-commands-exact", "events.list", "events", "create"},
	} {
		var out bytes.Buffer
		err := Run(context.Background(), args, &out, &out)
		if err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Errorf("%v: %v", args, err)
		}
		if out.Len() != 0 {
			t.Errorf("blocked command printed %s", out.String())
		}
	}
}
func TestGlobalExtractionPreservesValues(t *testing.T) {
	args, g, err := parseGlobal([]string{"events", "edit", "--summary", "--readonly", "--location=--json", "--json", "--", "--no-input"})
	if err != nil {
		t.Fatal(err)
	}
	if g.readonly || g.noInput || !g.asJSON {
		t.Fatalf("flags consumed as data: %+v", g)
	}
	if got := strings.Join(args, "|"); got != "events|edit|--summary|--readonly|--location=--json|--|--no-input" {
		t.Fatal(got)
	}
}
func TestEnvironmentDefaults(t *testing.T) {
	t.Setenv("I4D_READONLY", "true")
	t.Setenv("I4D_NO_INPUT", "1")
	t.Setenv("I4D_PROFILE", "editor")
	_, g, err := parseGlobal([]string{"--readonly=false", "policy"})
	if err != nil || g.readonly || !g.noInput || g.profile != "editor" {
		t.Fatalf("%+v %v", g, err)
	}
	t.Setenv("I4D_READONLY", "typo")
	if _, _, err := parseGlobal([]string{"policy"}); err == nil {
		t.Fatal("invalid env accepted")
	}
}
func TestNoInputAndPolicyInspection(t *testing.T) {
	t.Setenv("ICLOUD_USERNAME", "example@example.com")
	t.Setenv("ICLOUD_APP_PASSWORD", "")
	var out bytes.Buffer
	err := Run(context.Background(), []string{"--no-input", "calendars", "list"}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "--no-input") || out.Len() != 0 {
		t.Fatalf("%v %s", err, out.String())
	}
	err = Run(context.Background(), []string{"--readonly", "policy", "--json"}, &out, &out)
	if err != nil || !strings.Contains(out.String(), `"readonly": true`) || !strings.Contains(out.String(), `"allowed": false`) {
		t.Fatalf("%v %s", err, out.String())
	}
}

type trapTransport struct{ t *testing.T }

func (r trapTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Fatal("blocked command reached network")
	return nil, nil
}
func TestRestrictionsBeforeCredentialsOrNetwork(t *testing.T) {
	t.Setenv("ICLOUD_USERNAME", "test@example.com")
	t.Setenv("ICLOUD_APP_PASSWORD", "test-password")
	old := http.DefaultTransport
	http.DefaultTransport = trapTransport{t}
	defer func() { http.DefaultTransport = old }()
	for _, verb := range []string{"create", "edit", "update", "delete"} {
		var out bytes.Buffer
		err := Run(context.Background(), []string{"events", verb, "--readonly"}, &out, &out)
		if err == nil || !strings.Contains(err.Error(), "--readonly") {
			t.Fatalf("%s: %v", verb, err)
		}
	}
}
func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"events", "--help"}, {"help", "calendars"}, {"help"}, {"help", "events", "list"}, {"events", "list", "--help"}, {"--readonly", "--help"}} {
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, &out); err != nil || !strings.Contains(out.String(), "--readonly") {
			t.Errorf("%v: %v %s", args, err, out.String())
		}
		if len(args) == 2 && args[0] == "--readonly" && strings.Contains(out.String(), "i4d events delete") {
			t.Fatal("help exposes blocked command")
		}
	}
}
