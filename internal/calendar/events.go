package calendar

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"i4dcli/internal/fault"
)

type Event struct {
	Calendar     string `json:"calendar"`
	CalendarPath string `json:"calendar_path"`
	Path         string `json:"path"`
	UID          string `json:"uid"`
	RecurrenceID string `json:"recurrence_id,omitempty"`
	Summary      string `json:"summary"`
	Start        string `json:"start"`
	End          string `json:"end"`
	AllDay       bool   `json:"all_day"`
	Location     string `json:"location,omitempty"`
	Status       string `json:"status,omitempty"`
	start        time.Time
}

// Select matches case-insensitive full names or exact paths, rejecting ambiguity.
func Select(all []Calendar, names []string) ([]Calendar, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("provide at least one --calendar name or path")
	}
	selected := []Calendar{}
	seen := map[string]bool{}
	for _, name := range names {
		matches := []Calendar{}
		for _, c := range all {
			if c.Path == name || strings.EqualFold(c.Name, name) {
				matches = append(matches, c)
			}
		}
		if len(matches) == 0 {
			return nil, fault.New("not_found", "calendar %q not found; run calendars list", name)
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("calendar %q is ambiguous; select its path instead", name)
		}
		c := matches[0]
		supported := len(c.Components) == 0
		for _, comp := range c.Components {
			if comp == "VEVENT" {
				supported = true
			}
		}
		if !supported {
			return nil, fmt.Errorf("calendar %q does not support events", name)
		}
		if !seen[c.Path] {
			selected = append(selected, c)
			seen[c.Path] = true
		}
	}
	return selected, nil
}

type QueryStats struct {
	CalendarPath string
	Calendar     string
	Objects      int
	Instances    int
	Matched      int
}

// Events requests server-expanded recurrence instances and returns overlapping events.
// from is inclusive and to exclusive. loc controls display and floating/date values.
func Events(ctx context.Context, client *http.Client, endpoint string, calendars []Calendar, from, to time.Time, loc *time.Location, diagnostics ...func(QueryStats)) ([]Event, error) {
	if !from.Before(to) {
		return nil, fmt.Errorf("--to must be later than --from")
	}
	dav, err := caldav.NewClient(client, endpoint)
	if err != nil {
		return nil, err
	}
	result := []Event{}
	// Widen retrieval for DATE/floating values whose server timezone may differ.
	start, end := from.Add(-24*time.Hour), to.Add(24*time.Hour)
	query := &caldav.CalendarQuery{
		// Explicit properties avoid depending on the server's allprop/allcomp
		// projection behavior. Keep recurrence fields so an unexpanded response
		// cannot silently masquerade as individual occurrences.
		CompRequest: caldav.CalendarCompRequest{
			Name: "VCALENDAR", Props: []string{"VERSION", "PRODID", "CALSCALE"},
			Comps: []caldav.CalendarCompRequest{
				{Name: "VEVENT", Props: []string{
					"UID", "DTSTAMP", "DTSTART", "DTEND", "DURATION",
					"RECURRENCE-ID", "RRULE", "RDATE", "EXRULE", "EXDATE",
					"SUMMARY", "LOCATION", "STATUS",
				}},
				{Name: "VTIMEZONE", Props: []string{"TZID"}, Comps: []caldav.CalendarCompRequest{
					{Name: "STANDARD", Props: []string{"DTSTART", "TZOFFSETFROM", "TZOFFSETTO", "RRULE", "RDATE", "TZNAME"}},
					{Name: "DAYLIGHT", Props: []string{"DTSTART", "TZOFFSETFROM", "TZOFFSETTO", "RRULE", "RDATE", "TZNAME"}},
				}},
			},
			Expand: &caldav.CalendarExpandRequest{Start: start, End: end},
		},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT", Start: start, End: end}}},
	}
	for _, c := range calendars {
		objects, err := dav.QueryCalendar(ctx, c.Path, query)
		if err != nil {
			return nil, fmt.Errorf("query calendar %q: %w", c.Name, err)
		}
		stats := QueryStats{Calendar: c.Name, CalendarPath: c.Path, Objects: len(objects)}
		for _, obj := range objects {
			if obj.Data == nil {
				return nil, fmt.Errorf("missing calendar data in %s", obj.Path)
			}
			for _, component := range obj.Data.Events() {
				stats.Instances++
				e, finish, err := parseEvent(component, c, obj.Path, loc)
				if err != nil {
					return nil, fmt.Errorf("calendar %q object %s: %w", c.Name, obj.Path, err)
				}
				overlaps := e.start.Before(to) && finish.After(from)
				if finish.Equal(e.start) {
					overlaps = !e.start.Before(from) && e.start.Before(to)
				}
				if overlaps {
					stats.Matched++
					result = append(result, e)
				}
			}
		}
		for _, report := range diagnostics {
			if report != nil {
				report(stats)
			}
		}
		if stats.Objects > 0 && stats.Instances == 0 {
			return nil, fmt.Errorf("calendar %q: server returned %d calendar objects but no VEVENT components; event retrieval is incomplete", c.Name, stats.Objects)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if !a.start.Equal(b.start) {
			return a.start.Before(b.start)
		}
		if a.CalendarPath != b.CalendarPath {
			return a.CalendarPath < b.CalendarPath
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.RecurrenceID < b.RecurrenceID
	})
	return result, nil
}

func parseEvent(component ical.Event, c Calendar, path string, loc *time.Location) (Event, time.Time, error) {
	e := Event{Calendar: c.Name, CalendarPath: c.Path, Path: path}
	for _, key := range []string{"RRULE", "RDATE", "EXRULE", "EXDATE"} {
		if component.Props.Get(key) != nil {
			return e, time.Time{}, fmt.Errorf("server did not expand recurrence (%s remains)", key)
		}
	}
	return parseEventFields(component, c, path, loc)
}

func parseEventFields(component ical.Event, c Calendar, path string, loc *time.Location) (Event, time.Time, error) {
	e := Event{Calendar: c.Name, CalendarPath: c.Path, Path: path}
	prop := component.Props.Get("DTSTART")
	if prop == nil {
		keys := make([]string, 0, len(component.Props))
		for key := range component.Props {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return e, time.Time{}, fmt.Errorf("event has no DTSTART (returned property names: %s)", strings.Join(keys, ", "))
	}
	start, err := component.DateTimeStart(loc)
	if err != nil {
		return e, time.Time{}, err
	}
	end, err := component.DateTimeEnd(loc)
	if err != nil {
		return e, time.Time{}, err
	}
	e.AllDay = prop.ValueType() == ical.ValueDate
	if e.AllDay && component.Props.Get("DTEND") == nil && component.Props.Get("DURATION") == nil {
		end = start.AddDate(0, 0, 1)
	}
	if end.Before(start) {
		return e, time.Time{}, fmt.Errorf("event ends before it starts")
	}
	e.start = start
	if loc == nil {
		loc = time.UTC
	}
	e.Start = start.In(loc).Format(time.RFC3339)
	e.End = end.In(loc).Format(time.RFC3339)
	if e.AllDay {
		e.Start = start.Format(time.DateOnly)
		e.End = end.Format(time.DateOnly)
	}
	for key, dst := range map[string]*string{"UID": &e.UID, "SUMMARY": &e.Summary, "LOCATION": &e.Location, "STATUS": &e.Status} {
		*dst, err = component.Props.Text(key)
		if err != nil {
			return e, time.Time{}, err
		}
	}
	if p := component.Props.Get("RECURRENCE-ID"); p != nil {
		e.RecurrenceID = p.Value
	}
	return e, end, nil
}
