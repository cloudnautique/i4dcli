package app

import (
	"fmt"
	"time"
)

// Range resolves date shortcuts using calendar arithmetic in the selected zone.
// Days counts local calendar days starting today, including DST transitions.
type Range struct {
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	Today    bool   `json:"today,omitempty"`
	Tomorrow bool   `json:"tomorrow,omitempty"`
	Days     int    `json:"days,omitempty"`
}

func (r Range) Resolve(now time.Time) (time.Time, time.Time, *time.Location, error) {
	zone := r.Timezone
	if zone == "" {
		zone = "Local"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid timezone: %w", err)
	}
	shortcuts := 0
	if r.Today {
		shortcuts++
	}
	if r.Tomorrow {
		shortcuts++
	}
	if r.Days != 0 {
		shortcuts++
	}
	if r.Days < 0 || r.Days > 3660 {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("--days must be between 1 and 3660")
	}
	if shortcuts > 1 || (shortcuts > 0 && (r.From != "" || r.To != "")) {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("choose one of --today, --tomorrow, --days, or --from/--to")
	}
	var from, to time.Time
	if shortcuts == 1 {
		local := now.In(loc)
		from = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		if r.Tomorrow {
			from = from.AddDate(0, 0, 1)
		}
		days := r.Days
		if days == 0 {
			days = 1
		}
		to = from.AddDate(0, 0, days)
	} else {
		from, err = parseBoundary(r.From, loc)
		if err != nil {
			return from, to, loc, fmt.Errorf("--from: %w", err)
		}
		to, err = parseBoundary(r.To, loc)
		if err != nil {
			return from, to, loc, fmt.Errorf("--to: %w", err)
		}
	}
	if !from.Before(to) {
		return from, to, loc, fmt.Errorf("--to must be later than --from")
	}
	return from, to, loc, nil
}
func parseBoundary(s string, loc *time.Location) (time.Time, error) {
	if t, err := time.ParseInLocation(time.DateOnly, s, loc); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or RFC3339 timestamp")
}
