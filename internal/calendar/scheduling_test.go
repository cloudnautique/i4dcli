package calendar

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

func scheduleFixture(t *testing.T, source string, request Scheduling, object string, addresses []string, supported bool) (*ical.Calendar, string, error) {
	t.Helper()
	var saved *ical.Calendar
	method := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "OPTIONS":
			if supported {
				w.Header().Set("DAV", "1, calendar-access, calendar-auto-schedule")
			}
			w.WriteHeader(200)
		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			if r.URL.Path == "/" {
				fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/</d:href><d:propstat><d:prop><d:current-user-principal><d:href>/principal</d:href></d:current-user-principal></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
			} else {
				fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>/principal</d:href><d:propstat><d:prop><c:calendar-user-address-set>`)
				for _, address := range addresses {
					fmt.Fprintf(w, "<d:href>mailto:%s</d:href>", address)
				}
				fmt.Fprint(w, `</c:calendar-user-address-set><c:schedule-outbox-URL><d:href>/outbox/</d:href></c:schedule-outbox-URL></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
			}
		case "GET":
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Schedule-Tag", `"s1"`)
			fmt.Fprint(w, source)
		default:
			method = r.Method
			if object != "" && (r.Header.Get("If-Match") != `"v1"` || r.Header.Get("If-Schedule-Tag-Match") != `"s1"`) {
				t.Error("missing ETag/schedule-tag concurrency guards")
			}
			if r.Method == "PUT" {
				var err error
				saved, err = ical.NewDecoder(r.Body).Decode()
				if err != nil {
					t.Error(err)
				}
			}
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	_, err := Schedule(t.Context(), server.Client(), server.URL, Calendar{Path: "/cal/"}, object, request)
	return saved, method, err
}
func TestServerSchedulingCreateAndRespond(t *testing.T) {
	req := Scheduling{Action: "invite", Attendees: []string{"guest@example.com"}, Changes: Changes{Summary: str("Meeting"), Start: "2026-11-01T12:00:00Z", End: "2026-11-01T13:00:00Z"}}
	cal, method, err := scheduleFixture(t, "", req, "", []string{"owner@example.com"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if method != "PUT" || cal.Events()[0].Props.Get("ORGANIZER").Value != "mailto:owner@example.com" {
		t.Fatal("missing organizer")
	}
	source := strings.Replace(editable, "SUMMARY:", "ORGANIZER:mailto:owner@example.com\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:guest@example.com\r\nATTENDEE;PARTSTAT=ACCEPTED:mailto:other@example.com\r\nSUMMARY:", 1)
	cal, _, err = scheduleFixture(t, source, Scheduling{Action: "respond", PartStat: "ACCEPTED"}, "/cal/event.ics", []string{"guest@example.com"}, true)
	if err != nil {
		t.Fatal(err)
	}
	attendees := cal.Events()[0].Props.Values("ATTENDEE")
	if attendees[0].Params.Get("PARTSTAT") != "ACCEPTED" || attendees[1].Params.Get("PARTSTAT") != "ACCEPTED" || cal.Events()[0].Props.Get("SEQUENCE").Value != "2" {
		t.Fatal("response changed unrelated state")
	}
	_, method, err = scheduleFixture(t, source, Scheduling{Action: "cancel"}, "/cal/event.ics", []string{"owner@example.com"}, true)
	if err != nil || method != "DELETE" {
		t.Fatalf("cancel %s %v", method, err)
	}
}
func TestSchedulingCapabilityAndIdentityChecks(t *testing.T) {
	base := Scheduling{Action: "invite", Attendees: []string{"guest@example.com"}, Changes: Changes{Summary: str("Meeting"), Start: "2026-11-01", End: "2026-11-02"}}
	for _, tc := range []struct {
		name      string
		supported bool
		owned     []string
		actor     string
	}{
		{"unsupported", false, []string{"owner@example.com"}, ""}, {"spoof", true, []string{"owner@example.com"}, "spoof@example.com"}, {"ambiguous", true, []string{"owner@example.com", "alias@example.com"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			req.Actor = tc.actor
			_, method, err := scheduleFixture(t, "", req, "", tc.owned, tc.supported)
			if err == nil || method != "" {
				t.Fatalf("unsafe scheduling write %s %v", method, err)
			}
		})
	}
	base.Organizer = "alias@example.com"
	_, method, err := scheduleFixture(t, "", base, "", []string{"owner@example.com", "alias@example.com"}, true)
	if err != nil || method != "PUT" {
		t.Fatalf("owned alias %s %v", method, err)
	}
}
