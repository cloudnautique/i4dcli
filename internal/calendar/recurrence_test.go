package calendar

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

const seriesData = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VTIMEZONE\r\nTZID:America/New_York\r\nX-PRESERVE:original\r\nBEGIN:STANDARD\r\nDTSTART:20251102T020000\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\nBEGIN:VEVENT\r\nUID:series\r\nDTSTAMP:20261001T120000Z\r\nDTSTART;TZID=America/New_York:20261030T090000\r\nDTEND;TZID=America/New_York:20261030T100000\r\nRRULE:FREQ=DAILY;COUNT=6\r\nEXDATE;TZID=America/New_York:20261101T090000\r\nSUMMARY:Original\r\nSEQUENCE:1\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nUID:series\r\nDTSTAMP:20261001T120000Z\r\nRECURRENCE-ID;TZID=America/New_York:20261031T090000\r\nDTSTART;TZID=America/New_York:20261031T110000\r\nDTEND;TZID=America/New_York:20261031T120000\r\nSUMMARY:Exception\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func mutateFixture(t *testing.T, source, action string, changes Changes) (*ical.Calendar, string, error) {
	t.Helper()
	var written *ical.Calendar
	method := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("ETag", `"version"`)
			fmt.Fprint(w, source)
			return
		}
		method = r.Method
		if action != "create" && r.Header.Get("If-Match") != `"version"` {
			t.Error("missing strong ETag")
		}
		if r.Method == "PUT" {
			var err error
			written, err = ical.NewDecoder(r.Body).Decode()
			if err != nil {
				t.Error(err)
			}
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	_, err := Mutate(t.Context(), server.Client(), server.URL, Calendar{Path: "/cal/"}, action, "/cal/event.ics", changes)
	return written, method, err
}
func TestRecurrenceSeriesPreservesDetachedException(t *testing.T) {
	cal, method, err := mutateFixture(t, seriesData, "edit", Changes{Scope: "series", Summary: str("Updated")})
	if err != nil {
		t.Fatal(err)
	}
	if method != "PUT" || len(cal.Events()) != 2 {
		t.Fatalf("unexpected result %s %#v", method, cal)
	}
	if cal.Events()[1].Props.Get("SUMMARY").Value != "Exception" || cal.Events()[0].Props.Get("RRULE").Value != "FREQ=DAILY;COUNT=6" || cal.Events()[0].Props.Get("DTSTART").Params.Get("TZID") != "America/New_York" {
		t.Fatal("lost exceptions/rule/timezone")
	}
	if cal.Children[0].Props.Get("X-PRESERVE").Value != "original" {
		t.Fatal("timezone replaced")
	}
}
func TestOccurrenceEditDSTAndDelete(t *testing.T) {
	cal, _, err := mutateFixture(t, seriesData, "edit", Changes{Scope: "occurrence", RecurrenceID: "2026-11-02T09:00:00-05:00", Summary: str("One")})
	if err != nil {
		t.Fatal(err)
	}
	e := cal.Events()[2]
	if e.Props.Get("RECURRENCE-ID").Value != "20261102T090000" || e.Props.Get("DTSTART").Value != "20261102T090000" || e.Props.Get("DTSTART").Params.Get("TZID") != "America/New_York" || e.Props.Get("RRULE") != nil {
		t.Fatalf("bad override %#v", e.Props)
	}
	cal, method, err := mutateFixture(t, seriesData, "delete", Changes{Scope: "occurrence", RecurrenceID: "2026-10-31T09:00:00-04:00"})
	if err != nil {
		t.Fatal(err)
	}
	if method != "PUT" || len(cal.Events()) != 1 || len(cal.Events()[0].Props.Values("EXDATE")) != 2 {
		t.Fatal("occurrence deletion lost series or failed exclusion")
	}
	_, method, err = mutateFixture(t, seriesData, "delete", Changes{Scope: "series"})
	if err != nil || method != "DELETE" {
		t.Fatalf("series delete %s %v", method, err)
	}
}
func TestRecurrenceMembershipFailsClosed(t *testing.T) {
	for _, rid := range []string{"2026-11-01T09:00:00-05:00", "2026-12-01T09:00:00-05:00", "2026-11-02T10:00:00-05:00"} {
		_, method, err := mutateFixture(t, seriesData, "edit", Changes{Scope: "occurrence", RecurrenceID: rid, Summary: str("no")})
		if err == nil || method != "" {
			t.Fatalf("invalid occurrence accepted %s %v", rid, err)
		}
	}
}
func TestRecurringCreateAndTimezoneCoverage(t *testing.T) {
	c := Changes{Summary: str("DST series"), Start: "2026-10-30T09:00:00-04:00", End: "2026-10-30T10:00:00-04:00", Timezone: "America/New_York", RRule: str("FREQ=DAILY;COUNT=6")}
	cal, _, err := mutateFixture(t, "", "create", c)
	if err != nil {
		t.Fatal(err)
	}
	if cal.Events()[0].Props.Get("DTSTART").Params.Get("TZID") != "America/New_York" {
		t.Fatal("missing TZID")
	}
	found := false
	for _, component := range cal.Children {
		if component.Name == "VTIMEZONE" {
			found = true
			if len(component.Children) < 2 {
				t.Fatal("missing DST transitions")
			}
		}
	}
	if !found {
		t.Fatal("missing timezone component")
	}
	c.RRule = str("FREQ=DAILY")
	if c.Validate(true) == nil {
		t.Fatal("unbounded newTZID accepted")
	}
}
func TestOrdinaryMeetingWritesBlocked(t *testing.T) {
	source := strings.Replace(editable, "SUMMARY:", "ORGANIZER:mailto:owner@example.com\r\nATTENDEE:mailto:guest@example.com\r\nSUMMARY:", 1)
	for _, action := range []string{"edit", "delete"} {
		_, method, err := mutateFixture(t, source, action, Changes{Summary: str("blocked")})
		if err == nil || method != "" {
			t.Fatalf("ordinary scheduling write allowed %s %v", action, err)
		}
	}
}
func TestRecurrenceRDateListMembership(t *testing.T) {
	source := strings.Replace(seriesData, "RRULE:FREQ=DAILY;COUNT=6", "RDATE;TZID=America/New_York:20261105T090000,20261106T090000", 1)
	_, _, err := mutateFixture(t, source, "edit", Changes{Scope: "occurrence", RecurrenceID: "2026-11-06T09:00:00-05:00", Summary: str("rdate")})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedTimezoneRegeneratedWhenRuleExtends(t *testing.T) {
	cal, _, err := mutateFixture(t, "", "create", Changes{Summary: str("Finite"), Start: "2026-10-30T09:00:00-04:00", End: "2026-10-30T10:00:00-04:00", Timezone: "America/New_York", RRule: str("FREQ=DAILY;COUNT=2")})
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	if err := ical.NewEncoder(&source).Encode(cal); err != nil {
		t.Fatal(err)
	}
	updated, _, err := mutateFixture(t, source.String(), "edit", Changes{Scope: "series", RRule: str("FREQ=DAILY;UNTIL=20300101T140000Z")})
	if err != nil {
		t.Fatal(err)
	}
	zones := 0
	for _, component := range updated.Children {
		if component.Name == "VTIMEZONE" {
			zones++
			through, _ := component.Props.Text("X-I4D-TZ-THROUGH")
			if through != "2031-12-31" {
				t.Fatalf("timezone not extended: %s", through)
			}
		}
	}
	if zones != 1 {
		t.Fatalf("duplicate timezone components %d", zones)
	}
	_, method, err := mutateFixture(t, source.String(), "edit", Changes{Scope: "series", RRule: str("FREQ=DAILY")})
	if err == nil || method != "" {
		t.Fatal("unbounded extension of generated timezone accepted")
	}
}
func TestRawTZIDRecurrenceIDCanBeUsedFromInspection(t *testing.T) {
	_, _, err := mutateFixture(t, seriesData, "edit", Changes{Scope: "occurrence", RecurrenceID: "20261102T090000", Summary: str("Raw identifier")})
	if err != nil {
		t.Fatal(err)
	}
}
