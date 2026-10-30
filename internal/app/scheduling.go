package app

import (
	"context"
	"i4dcli/internal/calendar"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
	"strings"
)

// ValidateScheduling enforces permissions and local scheduling constraints before
// credentials or network access. Principal identity and server scheduling support
// are verified separately by the calendar adapter.
func ValidateScheduling(p policy.Policy, name, object string, r calendar.Scheduling) (calendar.Scheduling, error) {
	switch r.Action {
	case "invite", "respond", "cancel":
	default:
		return r, fault.New("usage", "unknown calendar scheduling action")
	}
	if err := p.Check("events." + r.Action); err != nil {
		return r, err
	}
	if strings.TrimSpace(name) == "" {
		return r, fault.New("usage", "calendar is required")
	}
	if r.Action != "invite" && object == "" {
		return r, fault.New("usage", "event path is required")
	}
	if err := r.Validate(); err != nil {
		return r, fault.Usage(err)
	}
	if err := r.Changes.ValidateScope(); err != nil {
		return r, fault.Usage(err)
	}
	if r.Action == "invite" {
		ch := r.Changes
		if object == "" {
			if len(r.Attendees) == 0 {
				return r, fault.New("usage", "new invitation requires attendees")
			}
			if err := ch.Validate(true); err != nil {
				return r, fault.Usage(err)
			}
		} else if ch.Summary != nil || ch.Location != nil || ch.Description != nil || ch.Start != "" || ch.End != "" || ch.RRule != nil {
			if err := ch.Validate(false); err != nil {
				return r, fault.Usage(err)
			}
		}
	}
	return r, nil
}
func (s *Service) Schedule(ctx context.Context, name, object string, r calendar.Scheduling) (string, error) {
	r, err := ValidateScheduling(s.permissions, name, object, r)
	if err != nil {
		return "", err
	}
	selected, err := s.selected(ctx, []string{name})
	if err != nil {
		return "", err
	}
	return calendar.Schedule(ctx, s.client, s.endpoint, selected[0], object, r)
}

// Scheduling is the shared CLI/MCP scheduling operation.
func (s *Service) Scheduling(ctx context.Context, name, object string, r calendar.Scheduling) (string, error) {
	return s.Schedule(ctx, name, object, r)
}
