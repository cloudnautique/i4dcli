// Package app provides operations shared by the CLI and future MCP handlers.
package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"i4dcli/internal/calendar"
	"i4dcli/internal/policy"
)

type Service struct {
	client      *http.Client
	endpoint    string
	permissions policy.Policy
}

func New(client *http.Client, endpoint string, permissions policy.Policy) *Service {
	copy := *client
	if permissions.ReadOnly() {
		copy.Transport = calendar.ReadOnlyTransport{Base: copy.Transport}
	}
	return &Service{client: &copy, endpoint: endpoint, permissions: permissions}
}
func (s *Service) Calendars(ctx context.Context) ([]calendar.Calendar, error) {
	if err := s.permissions.Check("calendars.list"); err != nil {
		return nil, err
	}
	return calendar.List(ctx, s.client, s.endpoint)
}
func (s *Service) selected(ctx context.Context, names []string) ([]calendar.Calendar, error) {
	// Discovery is an implementation step of the authorized operation, not a
	// separate user command requiring an additional command grant.
	all, err := calendar.List(ctx, s.client, s.endpoint)
	if err != nil {
		return nil, err
	}
	return calendar.Select(all, names)
}
func (s *Service) Events(ctx context.Context, names []string, from, to time.Time, loc *time.Location, report func(calendar.QueryStats)) ([]calendar.Event, error) {
	if err := s.permissions.Check("events.list"); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("provide at least one calendar")
	}
	if !from.Before(to) {
		return nil, fmt.Errorf("end of range must be later than start")
	}
	selected, err := s.selected(ctx, names)
	if err != nil {
		return nil, err
	}
	return calendar.Events(ctx, s.client, s.endpoint, selected, from, to, loc, report)
}
func (s *Service) Mutate(ctx context.Context, name, object, action string, changes calendar.Changes) (string, error) {
	c, err := policy.Resolve("events." + action)
	if err != nil {
		return "", err
	}
	if c.Capability != policy.Create && c.Capability != policy.Edit && c.Capability != policy.Delete {
		return "", fmt.Errorf("not an event mutation: %s", action)
	}
	if err := s.permissions.Check(c.Name); err != nil {
		return "", err
	}
	if c.Capability != policy.Delete {
		if err := changes.Validate(c.Capability == policy.Create); err != nil {
			return "", err
		}
	}
	if name == "" {
		return "", fmt.Errorf("calendar is required")
	}
	if c.Capability != policy.Create && object == "" {
		return "", fmt.Errorf("event path is required")
	}
	selected, err := s.selected(ctx, []string{name})
	if err != nil {
		return "", err
	}
	return calendar.Mutate(ctx, s.client, s.endpoint, selected[0], c.Name[len("events."):], object, changes)
}
