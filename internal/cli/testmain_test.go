package cli

import (
	"fmt"
	"os"
	"testing"
)

// Tests and spawned CLI processes must never inherit real account settings.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "i4d-test-config-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, name := range []string{"I4D_ACCOUNT", "ICLOUD_USERNAME", "ICLOUD_APP_PASSWORD", "I4D_PROFILE", "I4D_READONLY", "I4D_NO_INPUT", "I4D_ENABLE_COMMANDS_EXACT"} {
		if err := os.Unsetenv(name); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
