package mcpserver

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"i4dcli/internal/app"
	"i4dcli/internal/policy"
)

func TestConfiguredTimezoneAndToolOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "REPORT" {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>/calendars/work/event.ics</d:href><d:propstat><d:prop><c:calendar-data>`)
			_ = xml.EscapeText(w, []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VEVENT\r\nUID:night\r\nDTSTART:20261028T020000Z\r\nDTEND:20261028T030000Z\r\nSUMMARY:Night\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
			fmt.Fprint(w, `</c:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)

			return
		}
		if r.Method == "GET" {
			w.Header().Set("ETag", `"v1"`)
			fmt.Fprint(w, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VEVENT\r\nUID:test\r\nDTSTAMP:20261028T120000Z\r\nDTSTART:20261028T120000Z\r\nDTEND:20261028T130000Z\r\nSUMMARY:Lunch\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
			return
		}
		if r.Method != "PROPFIND" {
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(400)
			return
		}
		href, props := "/", `<d:current-user-principal><d:href>/principal/</d:href></d:current-user-principal>`
		switch r.URL.Path {
		case "/principal/":
			href, props = r.URL.Path, `<c:calendar-home-set><d:href>/calendars/</d:href></c:calendar-home-set>`
		case "/calendars/":
			href, props = "/calendars/work/", `<d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Work</d:displayname>`
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>%s</d:href><d:propstat><d:prop>%s</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, href, props)
	}))
	defer srv.Close()
	p, _ := policy.New(policy.Options{})
	o := options()
	o.DefaultTimezone = "America/New_York"
	c := connect(t, p, o, func(context.Context) (*app.Service, func(), error) {
		return app.New(srv.Client(), srv.URL, p), func() {}, nil
	})
	for _, zone := range []string{"", "UTC"} {
		args := map[string]any{"calendar": "Work", "path": "/calendars/work/event.ics"}
		want := "2026-10-28T08:00:00-04:00"
		if zone != "" {
			args["timezone"] = zone
			want = "2026-10-28T12:00:00Z"
		}
		got := call(t, c, "events_get", args)
		if got.IsError || !strings.Contains(resultText(got), want) {
			t.Fatalf("%s", resultText(got))
		}
		wantCount := `"returned":0`
		args = map[string]any{"calendars": []string{"Work"}, "from": "2026-10-28", "to": "2026-10-29"}
		if zone != "" {
			args["timezone"] = zone
			wantCount = `"returned":1`
		}
		got = call(t, c, "events_list", args)
		if got.IsError || !strings.Contains(resultText(got), wantCount) {
			t.Fatal(resultText(got))
		}
	}
}
