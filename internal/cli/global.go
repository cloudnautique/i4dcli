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
	username          string
	asJSON            bool
	timeout           time.Duration
	readonly, noInput bool
	profile, exact    string
}

// Extract global flags wherever they occur, without interpreting command flag
// values (a summary of "--readonly" is text, not a permission flag).
func parseGlobal(args []string) ([]string, globals, error) {
	g := globals{}
	fs := flag.NewFlagSet("i4d", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&g.username, "username", os.Getenv("ICLOUD_USERNAME"), "")
	fs.BoolVar(&g.asJSON, "json", false, "")
	fs.DurationVar(&g.timeout, "timeout", 30*time.Second, "")
	fs.BoolVar(&g.readonly, "readonly", false, "")
	fs.BoolVar(&g.noInput, "no-input", false, "")
	fs.StringVar(&g.profile, "profile", "full", "")
	fs.StringVar(&g.exact, "enable-commands-exact", "", "")
	globalArgs, rest := []string{}, []string{}
	localValues := map[string]bool{"calendar": true, "from": true, "to": true, "timezone": true, "path": true, "summary": true, "location": true, "description": true, "start": true, "end": true}
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
		name, _, equals := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if f := fs.Lookup(name); f != nil {
			globalArgs = append(globalArgs, arg)
			boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
			if !equals && !(ok && boolean.IsBoolFlag()) {
				if i+1 >= len(args) {
					return nil, g, fmt.Errorf("flag needs an argument: --%s", name)
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
		return nil, g, err
	}
	// Environment supplies defaults only; explicit flags take precedence.
	supplied := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	for _, item := range []struct {
		flag, env string
		target    *bool
	}{
		{"readonly", "I4D_READONLY", &g.readonly}, {"no-input", "I4D_NO_INPUT", &g.noInput},
	} {
		if value := os.Getenv(item.env); value != "" && !supplied[item.flag] {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, g, fmt.Errorf("%s must be a boolean", item.env)
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
	if g.timeout <= 0 {
		return nil, g, fmt.Errorf("timeout must be positive")
	}
	return rest, g, nil
}
