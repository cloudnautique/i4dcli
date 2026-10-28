package calendar

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func TestSelect(t *testing.T) {
	all := []Calendar{{Name: "Bill", Path: "/bill", Components: []string{"VEVENT"}}, {Name: "Bill and Kelley", Path: "/shared", Components: []string{"VEVENT"}}, {Name: "Tasks", Path: "/tasks", Components: []string{"VTODO"}}}
	got, err := Select(all, []string{"bill", "BILL AND KELLEY", "/bill"})
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	for _, names := range [][]string{nil, {"missing"}, {"Tasks"}} {
		if _, err := Select(all, names); err == nil {
			t.Fatalf("accepted %v", names)
		}
	}
	all = append(all, Calendar{Name: "Bill", Path: "/other"})
	if _, err := Select(all, []string{"bill"}); err == nil {
		t.Fatal("ambiguous name accepted")
	}
	if _, err := Select(all, []string{"/bill"}); err != nil {
		t.Fatal(err)
	}
}

func TestEventsReport(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if r.Method != "REPORT" || !strings.Contains(string(body), "expand") || !strings.Contains(string(body), "time-range") || !strings.Contains(string(body), "VEVENT") {
			t.Errorf("invalid query: %s %s", r.Method, body)
		}
		// Check the returned-data projection, not just the VEVENT filter.
		var query struct {
			Prop struct {
				Data struct {
					Comp struct {
						Name     string `xml:"name,attr"`
						Children []struct {
							Name  string `xml:"name,attr"`
							Props []struct {
								Name string `xml:"name,attr"`
							} `xml:"urn:ietf:params:xml:ns:caldav prop"`
						} `xml:"urn:ietf:params:xml:ns:caldav comp"`
					} `xml:"urn:ietf:params:xml:ns:caldav comp"`
				} `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
			} `xml:"DAV: prop"`
		}
		if err := xml.Unmarshal(body, &query); err != nil {
			t.Fatal(err)
		}
		requestsEvents := false
		for _, child := range query.Prop.Data.Comp.Children {
			if child.Name == "VEVENT" {
				requestsEvents = true
				names := map[string]bool{}
				for _, p := range child.Props {
					names[p.Name] = true
				}
				for _, name := range []string{"UID", "DTSTAMP", "DTSTART", "DTEND", "DURATION", "RECURRENCE-ID", "RRULE", "RDATE", "EXRULE", "EXDATE", "SUMMARY", "LOCATION", "STATUS"} {
					if !names[name] {
						t.Errorf("missing explicit event property %s", name)
					}
				}
			}
		}
		if query.Prop.Data.Comp.Name != "VCALENDAR" || !requestsEvents {
			t.Errorf("missing explicit VEVENT data projection: %s", body)
		}
		events := `BEGIN:VEVENT
UID:recurring
RECURRENCE-ID:20261028T120000Z
DTSTART:20261028T120000Z
DTEND:20261028T130000Z
SUMMARY:First occurrence
END:VEVENT
BEGIN:VEVENT
UID:recurring
RECURRENCE-ID:20261029T120000Z
DTSTART:20261029T140000Z
DURATION:PT1H
SUMMARY:Moved occurrence
END:VEVENT
BEGIN:VEVENT
UID:outside
DTSTART:20261030T000000Z
SUMMARY:Exclusive boundary
END:VEVENT
BEGIN:VEVENT
UID:overlap
DTSTART:20261027T230000Z
DTEND:20261028T010000Z
SUMMARY:Overnight
END:VEVENT
BEGIN:VEVENT
UID:allday
DTSTART;VALUE=DATE:20261029
SUMMARY:All day
END:VEVENT
`
		if r.URL.Path == "/shared" {
			events = `BEGIN:VEVENT
UID:shared
DTSTART:20261028T110000Z
SUMMARY:Shared event
END:VEVENT
`
		}
		data := "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//Test//EN\n" + events + "END:VCALENDAR\n"
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>%s/event.ics</d:href><d:propstat><d:prop><c:calendar-data>`, r.URL.Path)
		xml.EscapeText(w, []byte(strings.ReplaceAll(data, "\n", "\r\n")))
		fmt.Fprint(w, `</c:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
	}))
	defer server.Close()
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	var stats []QueryStats
	got, err := Events(context.Background(), server.Client(), server.URL, []Calendar{{Name: "Bill", Path: "/bill"}, {Name: "Shared", Path: "/shared"}}, from, from.AddDate(0, 0, 2), time.UTC, func(s QueryStats) { stats = append(stats, s) })
	if len(stats) != 2 || stats[0].Objects != 1 || stats[0].Instances != 5 || stats[0].Matched != 4 {
		t.Fatalf("unexpected diagnostics: %+v", stats)
	}
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(got) != 5 {
		t.Fatalf("calls=%d events=%+v", calls, got)
	}
	if got[0].UID != "overlap" || got[1].Calendar != "Shared" || got[4].RecurrenceID != "20261029T120000Z" {
		t.Fatalf("bad ordering or recurrence: %+v", got)
	}
	if !got[3].AllDay || got[3].Start != "2026-10-29" || got[3].End != "2026-10-30" {
		t.Fatalf("bad all-day: %+v", got[3])
	}
}

func TestEventTimezonesAndUnexpandedRecurrence(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	e := ical.NewEvent()
	e.Props.SetDate("DTSTART", time.Date(2026, 11, 1, 0, 0, 0, 0, loc))
	parsed, end, err := parseEvent(*e, Calendar{}, "test", loc)
	if err != nil || end.Sub(parsed.start) != 25*time.Hour {
		t.Fatalf("DST all-day: %+v %v %v", parsed, end, err)
	}
	e.Props.SetText("RRULE", "FREQ=DAILY")
	if _, _, err := parseEvent(*e, Calendar{}, "test", loc); err == nil {
		t.Fatal("accepted unexpanded recurrence")
	}
	e.Props.Del("RRULE")
	e.Props.Del("DTSTART")
	if _, _, err := parseEvent(*e, Calendar{}, "test", loc); err == nil {
		t.Fatal("accepted missing DTSTART")
	}
}

func TestEventsFailureIsNotPartialSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	now := time.Now()
	got, err := Events(context.Background(), server.Client(), server.URL, []Calendar{{Name: "Bill", Path: "/bill"}}, now, now.Add(time.Hour), time.UTC)
	if err == nil || got != nil {
		t.Fatalf("got %v %v", got, err)
	}
}

// Reproduce calendar objects that contain only VCALENDAR metadata. These must
// not be reported as a successfully empty agenda.
func TestEmptyCalendarEnvelopesAreNotAnEmptyAgenda(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>/shared/event.ics</d:href><d:propstat><d:prop><c:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
END:VCALENDAR
</c:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
	}))
	defer server.Close()
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	var stats QueryStats
	events, err := Events(context.Background(), server.Client(), server.URL, []Calendar{{Name: "Shared", Path: "/shared"}}, from, from.AddDate(0, 0, 7), time.UTC, func(s QueryStats) { stats = s })
	if err == nil || !strings.Contains(err.Error(), "no VEVENT components") || events != nil {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if stats.Objects != 1 || stats.Instances != 0 {
		t.Fatalf("stats=%+v", stats)
	}
}
