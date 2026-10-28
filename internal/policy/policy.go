// Package policy defines the shared command registry and invocation permissions.
package policy

import (
	"fmt"
	"strings"
)

type Capability string

const (
	Read    Capability = "read"
	Create  Capability = "create"
	Edit    Capability = "edit"
	Delete  Capability = "delete"
	Inspect Capability = "inspect"
)

type Command struct {
	Name       string     `json:"name"`
	Capability Capability `json:"capability"`
	Usage      string     `json:"usage"`
	Aliases    []string   `json:"aliases,omitempty"`
}

func Commands() []Command {
	return []Command{
		{"calendars.list", Read, "calendars list [options]", nil},
		{"events.list", Read, "events list --calendar NAME --from DATE --to DATE [options]", nil},
		{"events.create", Create, "events create --calendar NAME --summary TEXT --start DATE --end DATE [options]", nil},
		{"events.edit", Edit, "events edit --calendar NAME --path PATH [fields] [options]", []string{"events.update"}},
		{"events.delete", Delete, "events delete --calendar NAME --path PATH [options]", nil},
		{"policy", Inspect, "policy [--json]", nil},
	}
}

func Resolve(name string) (Command, error) {
	for _, c := range Commands() {
		if c.Name == name {
			return c, nil
		}
		for _, alias := range c.Aliases {
			if alias == name {
				return c, nil
			}
		}
	}
	return Command{}, fmt.Errorf("unknown command %q (see --help)", name)
}

// Set at build time with -ldflags -X. Flags and environment cannot replace it.
var embeddedProfile = "full"

type Options struct {
	Profile      string
	ReadOnly     bool
	NoInput      bool
	EnabledExact string
}

type Policy struct {
	embedded string
	profile  string
	readOnly bool
	noInput  bool
	exact    map[string]bool
}

func New(o Options) (Policy, error) { return newPolicy(embeddedProfile, o) }
func newPolicy(embedded string, o Options) (Policy, error) {
	if o.Profile == "" {
		o.Profile = "full"
	}
	for _, name := range []string{embedded, o.Profile} {
		if name != "readonly" && name != "editor" && name != "full" {
			return Policy{}, fmt.Errorf("unknown permission profile %q; expected readonly, editor, or full", name)
		}
	}
	p := Policy{embedded: embedded, profile: o.Profile, readOnly: o.ReadOnly, noInput: o.NoInput}
	if o.EnabledExact != "" {
		p.exact = map[string]bool{}
		for _, name := range strings.Split(o.EnabledExact, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			c, err := Resolve(name)
			if err != nil {
				return Policy{}, fmt.Errorf("--enable-commands-exact: %w", err)
			}
			p.exact[c.Name] = true
		}
		if len(p.exact) == 0 {
			return Policy{}, fmt.Errorf("--enable-commands-exact must contain at least one command")
		}
	}
	return p, nil
}

func profileAllows(profile string, capability Capability) bool {
	switch profile {
	case "full":
		return true
	case "editor":
		return capability == Read || capability == Create || capability == Edit || capability == Inspect
	case "readonly":
		return capability == Read || capability == Inspect
	default:
		return false
	}
}

func (p Policy) Check(name string) error {
	c, err := Resolve(name)
	if err != nil {
		return err
	}
	if !profileAllows(p.embedded, c.Capability) {
		return fmt.Errorf("command %q blocked by embedded profile %q", c.Name, p.embedded)
	}
	if !profileAllows(p.profile, c.Capability) {
		return fmt.Errorf("command %q blocked by --profile %s", c.Name, p.profile)
	}
	if p.readOnly && !profileAllows("readonly", c.Capability) {
		return fmt.Errorf("command %q blocked by --readonly", c.Name)
	}
	// Policy inspection never grants access to account data and remains available.
	if c.Capability != Inspect && p.exact != nil && !p.exact[c.Name] {
		return fmt.Errorf("command %q blocked by --enable-commands-exact", c.Name)
	}
	return nil
}

func (p Policy) ReadOnly() bool {
	return p.readOnly || p.embedded == "readonly" || p.profile == "readonly"
}
func (p Policy) NoInput() bool { return p.noInput }

type Decision struct {
	Command string `json:"command"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}
type Snapshot struct {
	EmbeddedProfile string     `json:"embedded_profile"`
	Profile         string     `json:"profile"`
	ReadOnly        bool       `json:"readonly"`
	NoInput         bool       `json:"no_input"`
	Commands        []Decision `json:"commands"`
}

func (p Policy) Snapshot() Snapshot {
	s := Snapshot{EmbeddedProfile: p.embedded, Profile: p.profile, ReadOnly: p.ReadOnly(), NoInput: p.NoInput(), Commands: []Decision{}}
	for _, c := range Commands() {
		d := Decision{Command: c.Name, Allowed: true}
		if err := p.Check(c.Name); err != nil {
			d.Allowed = false
			d.Reason = err.Error()
		}
		s.Commands = append(s.Commands, d)
	}
	return s
}
