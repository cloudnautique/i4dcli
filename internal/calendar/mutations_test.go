package calendar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

const editable = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VEVENT\r\nUID:existing\r\nDTSTAMP:20261028T120000Z\r\nDTSTART:20261029T120000Z\r\nDURATION:PT1H\r\nSUMMARY:Original\r\nLOCATION:Home\r\nDESCRIPTION:Keep me\r\nX-CUSTOM:preserved\r\nSEQUENCE:2\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:Reminder\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func str(s string) *string { return &s }

func TestMutations(t *testing.T) {
	for _, action := range []string{"create", "edit", "delete"} {
		t.Run(action, func(t *testing.T) {
			writes, reads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					reads++
					w.Header().Set("ETag", `"v1"`)
					fmt.Fprint(w, editable)
					return
				}
				writes++
				if action == "create" {
					if r.Header.Get("If-None-Match") != "*" {
						t.Error("missing create condition")
					}
				} else if r.Header.Get("If-Match") != `"v1"` {
					t.Error("missing concurrency condition")
				}
				if action == "delete" {
					if r.Method != "DELETE" {
						t.Errorf("method %s", r.Method)
					}
				} else {
					if r.Method != "PUT" {
						t.Errorf("method %s", r.Method)
					}
					data, err := ical.NewDecoder(r.Body).Decode()
					if err != nil {
						t.Fatal(err)
					}
					e := data.Events()[0]
					title, _ := e.Props.Text("SUMMARY")
					if title != "New, title;\nsecond line" {
						t.Errorf("title %q", title)
					}
					if e.Props.Get("DTSTART").ValueType() != ical.ValueDate || e.Props.Get("DTEND").Value != "20261031" {
						t.Error("incorrect all-day range")
					}
					if e.Props.Get("DURATION") != nil {
						t.Error("duration not removed")
					}
					if action == "edit" {
						if e.Props.Get("UID").Value != "existing" || e.Props.Get("X-CUSTOM").Value != "preserved" || e.Props.Get("SEQUENCE").Value != "3" || len(e.Children) != 1 {
							t.Error("lost existing data")
						}
						if v, _ := e.Props.Text("LOCATION"); v != "" {
							t.Error("location not cleared")
						}
						if v, _ := e.Props.Text("DESCRIPTION"); v != "Keep me" {
							t.Error("description not preserved")
						}
					}
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			changes := Changes{Summary: str("New, title;\nsecond line"), Location: str(""), Start: "2026-10-30", End: "2026-10-31"}
			p, err := Mutate(context.Background(), server.Client(), server.URL, Calendar{Path: "/cal/"}, action, "/cal/event.ics", changes)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(p, "/cal/") || writes != 1 {
				t.Fatalf("path %s, writes %d", p, writes)
			}
			if action != "create" && reads != 1 {
				t.Error("did not fetch original")
			}
		})
	}
}

func TestMutationSafeguards(t *testing.T) {
	for _, tc := range []struct {
		name, data, etag string
		conflict         bool
	}{
		{"missing etag", editable, "", false},
		{"weak etag", editable, `W/"v1"`, false},
		{"recurring", strings.Replace(editable, "SUMMARY:", "RRULE:FREQ=DAILY\r\nSUMMARY:", 1), `"v1"`, false},
		{"exception", strings.Replace(editable, "SUMMARY:", "RECURRENCE-ID:20261029T120000Z\r\nSUMMARY:", 1), `"v1"`, false},
		{"conflict", editable, `"v1"`, true},
	} {
		for _, action := range []string{"edit", "delete"} {
			t.Run(tc.name+action, func(t *testing.T) {
				writes := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "GET" {
						w.Header().Set("ETag", tc.etag)
						fmt.Fprint(w, tc.data)
						return
					}
					writes++
					w.WriteHeader(http.StatusPreconditionFailed)
				}))
				defer server.Close()
				_, err := Mutate(context.Background(), server.Client(), server.URL, Calendar{Path: "/cal/"}, action, "/cal/event.ics", Changes{Summary: str("Changed")})
				if err == nil {
					t.Fatal("accepted unsafe mutation")
				}
				if !tc.conflict && writes != 0 {
					t.Error("unsafe write")
				}
				if tc.conflict && !strings.Contains(err.Error(), "changed on the server") {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestMutationValidation(t *testing.T) {
	for _, c := range []Changes{
		{}, {Summary: str(" ")}, {Start: "2026-10-29"}, {End: "2026-10-30"},
		{Start: "2026-10-29", End: "2026-10-29"},
		{Start: "2026-10-29", End: "2026-10-30T12:00:00Z"},
	} {
		if err := c.Validate(false); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	for _, object := range []string{"/other/event.ics", "/cal/", "/cal/sub/event.ics", "/cal/%2e%2e/event.ics", "https://other.example/cal/event.ics", "/cal/event.ics?x=1"} {
		if _, err := objectURL("https://example.test/", Calendar{Path: "/cal/"}, object); err == nil {
			t.Errorf("accepted %s", object)
		}
	}
	c := Changes{Start: "2026-10-29T12:00:00-04:00", End: "2026-10-29T13:00:00-04:00"}
	e := *ical.NewEvent()
	c.apply(e)
	if e.Props.Get("DTSTART").Value != "20261029T160000Z" {
		t.Error("offset lost")
	}
}
