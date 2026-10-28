// Package app provides operations shared by the CLI and future MCP handlers.
package app

import (
	"context"

	"net/http"
	"strings"
	"time"

	"i4dcli/internal/calendar"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
)

type Service struct {
	client      *http.Client
	endpoint    string
	permissions policy.Policy
}

func New(client *http.Client, endpoint string, permissions policy.Policy) *Service {
	copy := *client
	copy.Transport = fault.Transport{Base: copy.Transport}
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
		return nil, fault.New("usage", "provide at least one calendar")
	}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return nil, fault.New("usage", "calendar cannot be empty")
		}
	}
	if !from.Before(to) {
		return nil, fault.New("usage", "end of range must be later than start")
	}
	selected, err := s.selected(ctx, names)
	if err != nil {
		return nil, err
	}
	return calendar.Events(ctx, s.client, s.endpoint, selected, from, to, loc, report)
}
func (s *Service) Mutate(ctx context.Context, name, object, action string, changes calendar.Changes) (string, error) {
	m, err := (Mutation{Action: action, Calendar: name, Path: object, Changes: changes}).Validate(s.permissions)
	if err != nil {
		return "", err
	}
	selected, err := s.selected(ctx, []string{name})
	if err != nil {
		return "", err
	}
	return calendar.Mutate(ctx, s.client, s.endpoint, selected[0], m.Action, object, changes)
}

func (s *Service) Get(ctx context.Context, name, object string, loc *time.Location) (calendar.Object, error) {
	if err := s.permissions.Check("events.get"); err != nil {
		return calendar.Object{}, err
	}
	if strings.TrimSpace(name) == "" || object == "" {
		return calendar.Object{}, fault.New("usage", "calendar and event path are required")
	}
	selected, err := s.selected(ctx, []string{name})
	if err != nil {
		return calendar.Object{}, err
	}
	return calendar.Get(ctx, s.client, s.endpoint, selected[0], object, loc)
}
