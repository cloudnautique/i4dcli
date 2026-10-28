// Package calendar discovers iCloud calendars over CalDAV.
package calendar

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/emersion/go-webdav/caldav"
	"i4dcli/internal/fault"
)

const Endpoint = "https://caldav.icloud.com/"

type Calendar struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Description string   `json:"description,omitempty"`
	Components  []string `json:"components"`
}

// List discovers the principal and calendar home rather than guessing account paths.
func List(ctx context.Context, client *http.Client, endpoint string) ([]Calendar, error) {
	dav, err := caldav.NewClient(client, endpoint)
	if err != nil {
		return nil, err
	}
	principal, err := dav.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover principal: %w", err)
	}
	if principal == "" {
		return nil, fmt.Errorf("server returned an empty principal")
	}
	home, err := dav.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("discover calendar home: %w", err)
	}
	if home == "" {
		return nil, fmt.Errorf("server returned an empty calendar home")
	}
	calendars, err := dav.FindCalendars(ctx, home)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}
	result := make([]Calendar, 0, len(calendars))
	for _, c := range calendars {
		result = append(result, Calendar{c.Name, c.Path, c.Description, c.SupportedComponentSet})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Path < result[j].Path
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// Only Apple's CalDAV service and its numbered shards may receive credentials.
func trusted(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "caldav.icloud.com" {
		return true
	}
	shard, ok := strings.CutSuffix(host, "-caldav.icloud.com")
	if !ok || len(shard) < 2 || shard[0] != 'p' {
		return false
	}
	for _, c := range shard[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

type authTransport struct {
	base               http.RoundTripper
	username, password string
}

func (t authTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !trusted(req.URL) {
		return nil, fmt.Errorf("refusing credentials to an untrusted CalDAV endpoint")
	}
	r := req.Clone(req.Context())
	r.SetBasicAuth(t.username, t.password)
	resp, err := t.base.RoundTrip(r)
	if err == nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
		resp.Body.Close()
		return nil, fmt.Errorf("iCloud rejected access; check your Apple Account and app-specific password: %w", fault.HTTP(resp.StatusCode))
	}
	return resp, err
}

func NewClient(username, password string) *http.Client {
	return &http.Client{
		Transport: authTransport{http.DefaultTransport, username, password},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many CalDAV redirects")
			}
			if !trusted(req.URL) {
				return fmt.Errorf("refusing redirect to an untrusted CalDAV endpoint")
			}
			// CalDAV discovery must stay PROPFIND even on 301/302/303.
			original := via[0]
			req.Method = original.Method
			req.Header = original.Header.Clone()
			if original.GetBody != nil {
				body, err := original.GetBody()
				if err != nil {
					return err
				}
				req.Body = body
				req.GetBody = original.GetBody
				req.ContentLength = original.ContentLength
			}
			return nil
		},
	}
}
