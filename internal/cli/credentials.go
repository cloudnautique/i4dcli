package cli

import (
	"fmt"
	"golang.org/x/term"
	"i4dcli/internal/fault"
	"io"
	"os"
	"strings"
)

func credentials(username string, noInput bool, stderr io.Writer) (string, error) {
	if strings.TrimSpace(username) == "" {
		return "", fault.New("config", "set ICLOUD_USERNAME or pass --username")
	}
	password := os.Getenv("ICLOUD_APP_PASSWORD")
	if password == "" {
		if noInput {
			return "", fault.New("config", "ICLOUD_APP_PASSWORD is required with --no-input; password prompting is disabled")
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return "", fault.New("config", "set ICLOUD_APP_PASSWORD or run in a terminal for a hidden password prompt")
		}
		fmt.Fprint(stderr, "Apple app-specific password: ")
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		password = string(value)
	}
	if strings.TrimSpace(password) == "" {
		return "", fault.New("config", "app-specific password is empty")
	}
	return password, nil
}
