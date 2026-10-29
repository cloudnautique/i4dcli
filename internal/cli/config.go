package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"i4dcli/internal/config"
	"i4dcli/internal/fault"
)

func configuredAccount(g *globals) (config.Account, error) {
	path, err := config.Path()
	if err != nil {
		return config.Account{}, fault.New("config", "%s", err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		return config.Account{}, fault.New("config", "%s: %s", path, err)
	}
	account, err := cfg.Select(g.account)
	if err != nil {
		return config.Account{}, fault.New("config", "%s", err)
	}
	// A calendar username override must not inherit another calendar identity's
	// destinations. Mail has an independently configured identity.
	if g.usernameSet {
		if !strings.EqualFold(strings.TrimSpace(g.username), strings.TrimSpace(account.Username)) {
			return config.Account{Mail: account.Mail}, nil
		}
	} else {
		g.username = account.Username
	}
	return account, nil
}

func applyEventDefaults(fs *flag.FlagSet, o *eventOptions, account config.Account) {
	supplied := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	if !supplied["calendar"] {
		o.names = append(calendarNames(nil), account.Calendars...)
	}
	if !supplied["timezone"] && account.Timezone != "" {
		o.span.Timezone = account.Timezone
	}
}

func runConfig(args []string, g globals, out io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) || (len(args) == 2 && (args[1] == "--help" || args[1] == "-h")) {
		_, err := fmt.Fprintln(out, "Usage: i4d config path [--json]\n       i4d config show [--json]\n\nInspect the XDG config path or validated non-secret account settings.\nEdit config.json directly; passwords and permission profiles are not stored here.")
		return err
	}
	if len(args) != 1 || (args[0] != "path" && args[0] != "show") {
		return fault.New("usage", "expected config path or config show")
	}
	if g.force || g.dryRun {
		return fault.New("usage", "--dry-run and --force only apply to mutation commands")
	}
	if g.plain {
		return fault.New("usage", "config does not support --plain")
	}
	path, err := config.Path()
	if err != nil {
		return fault.New("config", "%s", err)
	}
	if args[0] == "path" {
		if g.asJSON {
			return json.NewEncoder(out).Encode(map[string]string{"path": path})
		}
		_, err = fmt.Fprintln(out, path)
		return err
	}
	cfg, exists, err := config.Load(path)
	if err != nil {
		return fault.New("config", "%s: %s", path, err)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Path   string      `json:"path"`
		Exists bool        `json:"exists"`
		Config config.File `json:"config"`
	}{path, exists, cfg})
}
