package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"i4dcli/internal/calendar"
	"i4dcli/internal/policy"
)

func TestServicePolicy(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Errorf("blocked request: %s", r.Method)
		w.WriteHeader(500)
	}))
	defer server.Close()
	p, _ := policy.New(policy.Options{ReadOnly: true})
	service := New(server.Client(), server.URL, p)
	for _, action := range []string{"create", "edit", "update", "delete"} {
		if _, err := service.Mutate(context.Background(), "Work", "/work/e.ics", action, calendar.Changes{}); err == nil {
			t.Errorf("accepted %s", action)
		}
	}
	if calls != 0 {
		t.Fatal("network activity")
	}
}
func TestDiscoveryWithExactGrants(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == "PUT" {
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("missing create condition")
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		if r.Method == "REPORT" {
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:"/>`)
			return
		}
		if r.Method != "PROPFIND" {
			t.Errorf("method %s", r.Method)
		}
		var props, href string
		switch r.URL.Path {
		case "/":
			href = "/"
			props = `<d:current-user-principal><d:href>/principal/</d:href></d:current-user-principal>`
		case "/principal/":
			href = r.URL.Path
			props = `<c:calendar-home-set><d:href>/calendars/</d:href></c:calendar-home-set>`
		case "/calendars/":
			href = "/calendars/work/"
			props = `<d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Work</d:displayname>`
		default:
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>%s</d:href><d:propstat><d:prop>%s</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, href, props)
	}))
	defer server.Close()
	p, _ := policy.New(policy.Options{ReadOnly: true, EnabledExact: "events.list"})
	service := New(server.Client(), server.URL, p)
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	events, err := service.Events(context.Background(), []string{"Work"}, from, from.AddDate(0, 0, 1), time.UTC, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 || calls != 4 {
		t.Fatalf("events %v calls %d", events, calls)
	}
	if _, err := service.Calendars(context.Background()); err == nil {
		t.Fatal("direct discovery command granted")
	}
	if calls != 4 {
		t.Fatal("blocked discovery dispatched")
	}
	p, _ = policy.New(policy.Options{EnabledExact: "events.create"})
	service = New(server.Client(), server.URL, p)
	title := "Lunch"
	if _, err := service.Mutate(context.Background(), "Work", "", "create", calendar.Changes{Summary: &title, Start: "2026-10-28", End: "2026-10-29"}); err != nil {
		t.Fatal(err)
	}
	if calls != 8 {
		t.Fatalf("create calls: %d", calls)
	}
}
