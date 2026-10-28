package calendar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"i4dcli/internal/fault"
)

func TestGetEventResource(t *testing.T) {
	for _, recurring := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.Path != "/cal/e.ics" {
				t.Errorf("request %s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("ETag", `"v1"`)
			data := editable
			if recurring {
				data = strings.Replace(data, "SUMMARY:", "RRULE:FREQ=DAILY\r\nSUMMARY:", 1)
			}
			fmt.Fprint(w, data)
		}))
		object, err := Get(context.Background(), server.Client(), server.URL, Calendar{Path: "/cal/", Name: "Work"}, "/cal/e.ics", time.UTC)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if object.ETag != `"v1"` || len(object.Events) != 1 || object.Events[0].Description != "Keep me" || object.Events[0].End != "2026-10-29T13:00:00Z" {
			t.Fatalf("object %+v", object)
		}
		if recurring && object.Events[0].Recurrence["RRULE"][0] != "FREQ=DAILY" {
			t.Fatal("recurrence lost")
		}
	}
}
func TestGetMissingAndOutOfCalendar(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, err := Get(context.Background(), server.Client(), server.URL, Calendar{Path: "/cal/"}, "/cal/missing.ics", time.UTC)
	if fault.ExitCode(err) != 5 {
		t.Fatal(err)
	}
	_, err = Get(context.Background(), server.Client(), server.URL, Calendar{Path: "/cal/"}, "/other/e.ics", time.UTC)
	if fault.ExitCode(err) != 2 {
		t.Fatal(err)
	}
}
