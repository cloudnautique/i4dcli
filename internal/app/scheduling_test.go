package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"i4dcli/internal/calendar"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
)

func TestSchedulingPolicyBeforeDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("denied scheduling attempted discovery: %s", r.Method)
		w.WriteHeader(500)
	}))
	defer server.Close()
	for _, options := range []policy.Options{{ReadOnly: true}, {Profile: "editor"}, {EnabledExact: "events.get"}} {
		p, err := policy.New(options)
		if err != nil {
			t.Fatal(err)
		}
		service := New(server.Client(), server.URL, p)
		for _, action := range []string{"invite", "respond", "cancel"} {
			if _, err := service.Scheduling(t.Context(), "Work", "/work/event.ics", calendar.Scheduling{Action: action}); fault.ExitCode(err) != 6 {
				t.Fatalf("%s: %v", action, err)
			}
		}
	}
}
