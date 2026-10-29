package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

type globals struct {
	username             string
	account              string
	usernameSet          bool
	asJSON               bool
	plain, dryRun, force bool
	timeout              time.Duration
	readonly, noInput    bool
	profile, exact       string
}

// Extract global flags wherever they occur, without interpreting command flag
// values (a summary of "--readonly" is text, not a permission flag).
func parseGlobal(args []string) ([]string, globals, error) {
	g := globals{}
	fs := globalFlags(&g)
	globalArgs, rest := []string{}, []string{}
	requestedJSON := false
	localValues := map[string]bool{"calendar": true, "from": true, "to": true, "timezone": true, "path": true, "summary": true, "location": true, "description": true, "start": true, "end": true, "days": true, "allow-tool": true, "max-results": true, "max-output-bytes": true}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			rest = append(rest, arg)
			continue
		}
		name, value, equals := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if f := fs.Lookup(name); f != nil {
			if name == "json" {
				enabled := true
				if equals {
					enabled, _ = strconv.ParseBool(value)
				}
				requestedJSON = enabled
				g.asJSON = enabled
			}
			globalArgs = append(globalArgs, arg)
			boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
			if !equals && !(ok && boolean.IsBoolFlag()) {
				if i+1 >= len(args) {
					return rest, g, fmt.Errorf("flag needs an argument: --%s", name)
				}
				i++
				globalArgs = append(globalArgs, args[i])
			}
		} else {
			rest = append(rest, arg)
			if localValues[name] && !equals && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
		}
	}
	if err := fs.Parse(globalArgs); err != nil {
		g.asJSON = requestedJSON
		return rest, g, err
	}

	// Environment supplies defaults only; explicit flags take precedence.
	supplied := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	g.usernameSet = supplied["username"] || os.Getenv("ICLOUD_USERNAME") != ""
	for _, item := range []struct {
		flag, env string
		target    *bool
	}{
		{"readonly", "I4D_READONLY", &g.readonly}, {"no-input", "I4D_NO_INPUT", &g.noInput},
	} {
		if value := os.Getenv(item.env); value != "" && !supplied[item.flag] {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return rest, g, fmt.Errorf("%s must be a boolean", item.env)
			}
			*item.target = parsed
		}
	}
	if value := os.Getenv("I4D_PROFILE"); value != "" && !supplied["profile"] {
		g.profile = value
	}
	if value := os.Getenv("I4D_ENABLE_COMMANDS_EXACT"); value != "" && !supplied["enable-commands-exact"] {
		g.exact = value
	}
	if g.asJSON && g.plain {
		return rest, g, fmt.Errorf("--json and --plain cannot be combined")
	}
	if g.timeout <= 0 {
		return rest, g, fmt.Errorf("timeout must be positive")
	}
	return rest, g, nil
}

func globalFlags(g *globals) *flag.FlagSet {
	fs := flag.NewFlagSet("i4d", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&g.account, "account", os.Getenv("I4D_ACCOUNT"), "named account from XDG config (or I4D_ACCOUNT)")
	fs.StringVar(&g.username, "username", os.Getenv("ICLOUD_USERNAME"), "Apple Account email")
	fs.BoolVar(&g.asJSON, "json", false, "structured JSON output")
	fs.BoolVar(&g.plain, "plain", false, "stable TSV output")
	fs.BoolVar(&g.dryRun, "dry-run", false, "validate and preview a mutation without network requests")
	fs.BoolVar(&g.force, "force", false, "skip delete confirmation; never overrides permissions")
	fs.DurationVar(&g.timeout, "timeout", 30*time.Second, "operation or MCP tool timeout")
	fs.BoolVar(&g.readonly, "readonly", false, "block mutations")
	fs.BoolVar(&g.noInput, "no-input", false, "never prompt")
	fs.StringVar(&g.profile, "profile", "full", "readonly, editor, or full")
	fs.StringVar(&g.exact, "enable-commands-exact", "", "exact permitted command names, comma-separated")
	return fs
}
