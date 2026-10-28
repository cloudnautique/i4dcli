package cli

import (
	"strings"
	"testing"
)

func TestTerminalSanitization(t *testing.T) {
	if got := clean("Name\n\x1b[31m\t"); strings.ContainsAny(got, "\n\x1b\t") {
		t.Fatalf("unsafe: %q", got)
	}
}
