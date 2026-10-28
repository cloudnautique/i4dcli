package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"i4dcli/internal/policy"
)

// Exercise actual linker-baked binaries so runtime flags cannot accidentally
// replace the build's permission ceiling.
func TestEmbeddedProfileBinaries(t *testing.T) {
	t.Setenv("I4D_PROFILE", "full")
	t.Setenv("I4D_READONLY", "false")
	t.Setenv("I4D_NO_INPUT", "true")
	t.Setenv("I4D_ENABLE_COMMANDS_EXACT", "")
	t.Setenv("ICLOUD_USERNAME", "")
	t.Setenv("ICLOUD_APP_PASSWORD", "")
	for _, profile := range []string{"readonly", "editor", "full"} {
		t.Run(profile, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "i4d")
			build := exec.Command("go", "build", "-ldflags", "-X i4dcli/internal/policy.embeddedProfile="+profile, "-o", binary, ".")
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build: %v %s", err, output)
			}
			cmd := exec.Command(binary, "--profile", "full", "--readonly=false", "policy", "--json")
			output, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var snapshot policy.Snapshot
			if err := json.Unmarshal(output, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.EmbeddedProfile != profile {
				t.Fatalf("wrong embedded profile: %+v", snapshot)
			}
			for _, d := range snapshot.Commands {
				if d.Command == "events.delete" && d.Allowed != (profile == "full") {
					t.Fatalf("delete decision %+v", d)
				}
			}
			if profile == "full" {
				return
			}
			verb := "delete"
			if profile == "readonly" {
				verb = "update"
			}
			output, err = exec.Command(binary, "--profile", "full", "--readonly=false", "--enable-commands-exact", "events."+verb, "events", verb).CombinedOutput()
			if err == nil || !strings.Contains(string(output), "blocked by embedded profile") {
				t.Fatalf("bypass attempt: %v %s", err, output)
			}
		})
	}
}
