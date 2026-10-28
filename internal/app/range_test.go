package app

import (
	"testing"
	"time"
)

func TestRangeShortcutsAcrossDST(t *testing.T) {
	for _, tc := range []struct {
		now      string
		span     Range
		from, to string
		hours    int
	}{
		{"2026-03-08T17:00:00Z", Range{Today: true, Timezone: "America/New_York"}, "2026-03-08T00:00:00-05:00", "2026-03-09T00:00:00-04:00", 23},
		{"2026-11-01T17:00:00Z", Range{Today: true, Timezone: "America/New_York"}, "2026-11-01T00:00:00-04:00", "2026-11-02T00:00:00-05:00", 25},
		{"2026-03-08T01:00:00Z", Range{Tomorrow: true, Timezone: "America/New_York"}, "2026-03-08T00:00:00-05:00", "2026-03-09T00:00:00-04:00", 23},
		{"2026-03-08T17:00:00Z", Range{Days: 2, Timezone: "America/New_York"}, "2026-03-08T00:00:00-05:00", "2026-03-10T00:00:00-04:00", 47},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		from, to, _, err := tc.span.Resolve(now)
		if err != nil {
			t.Fatal(err)
		}
		if from.Format(time.RFC3339) != tc.from || to.Format(time.RFC3339) != tc.to || int(to.Sub(from).Hours()) != tc.hours {
			t.Errorf("range %s to %s", from, to)
		}
	}
}
func TestRangeConflicts(t *testing.T) {
	for _, r := range []Range{{}, {Today: true, Tomorrow: true}, {Today: true, Days: 2}, {Days: -1}, {Days: 3661}, {Today: true, From: "2026-10-28"}, {Today: true, Timezone: "bad"}, {From: "2026-10-29", To: "2026-10-28"}} {
		if _, _, _, err := r.Resolve(time.Now()); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}
