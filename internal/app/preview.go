package app

import (
	"i4dcli/internal/calendar"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
	"strings"
)

type Mutation struct {
	Action   string           `json:"action"`
	Calendar string           `json:"calendar"`
	Path     string           `json:"path,omitempty"`
	Changes  calendar.Changes `json:"changes"`
}

func (m Mutation) Validate(p policy.Policy) (Mutation, error) {
	c, err := policy.Resolve("events." + m.Action)
	if err != nil {
		return m, fault.Usage(err)
	}
	if c.Capability != policy.Create && c.Capability != policy.Edit && c.Capability != policy.Delete {
		return m, fault.New("usage", "not an event mutation: %s", m.Action)
	}
	if err := p.Check(c.Name); err != nil {
		return m, err
	}
	m.Action = strings.TrimPrefix(c.Name, "events.")
	if strings.TrimSpace(m.Calendar) == "" {
		return m, fault.New("usage", "calendar is required")
	}
	if c.Capability != policy.Create && m.Path == "" {
		return m, fault.New("usage", "event path is required")
	}
	if err := m.Changes.ValidateScope(); err != nil {
		return m, fault.Usage(err)
	}
	if c.Capability != policy.Delete {
		if err := m.Changes.Validate(c.Capability == policy.Create); err != nil {
			return m, fault.Usage(err)
		}
	}
	return m, nil
}

type Plan struct {
	DryRun        bool     `json:"dry_run"`
	Validation    string   `json:"validation"`
	RemoteChecked bool     `json:"remote_checked"`
	Request       Mutation `json:"request"`
	Note          string   `json:"note"`
}

func Preview(p policy.Policy, m Mutation) (Plan, error) {
	m, err := m.Validate(p)
	if err != nil {
		return Plan{}, err
	}
	return Plan{DryRun: true, Validation: "local", RemoteChecked: false, Request: m, Note: "No network requests made. Calendar existence, path membership, recurrence, permissions on the server, and ETags have not been checked."}, nil
}
