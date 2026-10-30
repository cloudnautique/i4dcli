package calendar

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// ensureTimezone exports only a bounded, fully covered set of tzdata transitions.
// Existing timezone components are preserved byte-for-property; unbounded new
// timezone recurrence must not silently drift or omit future transitions.
func ensureTimezone(data *ical.Calendar, event ical.Event) error {
	prop := event.Props.Get("DTSTART")
	if prop == nil {
		return nil
	}
	tzid := prop.Params.Get("TZID")
	if tzid == "" {
		return nil
	}
	replaceIndex := -1
	for index, component := range data.Children {
		if component.Name == "VTIMEZONE" {
			id, _ := component.Props.Text("TZID")
			if id == tzid {
				if marker, _ := component.Props.Text("X-I4D-TZ-EXPORT"); marker != "1" {
					return nil
				}
				replaceIndex = index
			}
		}
	}
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return fmt.Errorf("unknown event TZID")
	}
	start, end, err := timezoneRange(event, loc)
	if err != nil {
		return err
	}
	for _, other := range data.Events() {
		p := other.Props.Get("DTSTART")
		if p == nil || p.Params.Get("TZID") != tzid {
			continue
		}
		first, last, err := timezoneRange(other, loc)
		if err != nil {
			return err
		}
		if first.Before(start) {
			start = first
		}
		if last.After(end) {
			end = last
		}
	}
	if start.Year() < 2 || end.Year() > 9998 {
		return fmt.Errorf("timezone export supports years 2 through 9998")
	}
	lower := time.Date(start.Year()-1, 1, 1, 0, 0, 0, 0, loc)
	upper := time.Date(end.Year()+1, 12, 31, 23, 59, 59, 0, loc)
	zone := ical.NewComponent("VTIMEZONE")
	zone.Props.SetText("TZID", tzid)
	zone.Props.SetText("X-I4D-TZ-EXPORT", "1")
	zone.Props.SetText("X-I4D-TZ-THROUGH", upper.Format("2006-01-02"))
	type observance struct {
		kind, name string
		from, to   int
		dates      []string
	}
	groups := map[string]*observance{}
	add := func(at time.Time, from, to int, name string, dst bool) {
		kind := "STANDARD"
		if dst {
			kind = "DAYLIGHT"
		}
		key := fmt.Sprintf("%s/%s/%d/%d", kind, name, from, to)
		group := groups[key]
		if group == nil {
			group = &observance{kind: kind, name: name, from: from, to: to}
			groups[key] = group
		}
		group.dates = append(group.dates, at.UTC().Add(time.Duration(from)*time.Second).Format("20060102T150405"))
	}
	name, offset := lower.Zone()
	add(lower, offset, offset, name, lower.IsDST())
	current := lower
	for n := 0; n < 20000; n++ {
		_, bound := current.ZoneBounds()
		if bound.IsZero() || bound.After(upper) {
			break
		}
		if !bound.After(current) {
			return fmt.Errorf("invalid timezone transition bounds")
		}
		next := bound.In(loc)
		nextName, nextOffset := next.Zone()
		add(bound, offset, nextOffset, nextName, next.IsDST())
		offset = nextOffset
		current = next
		if n == 19999 {
			return fmt.Errorf("timezone export exceeds transition limit")
		}
	}
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		component := ical.NewComponent(group.kind)
		component.Props.Set(&ical.Prop{Name: "DTSTART", Value: group.dates[0]})
		component.Props.Set(&ical.Prop{Name: "TZOFFSETFROM", Value: utcOffset(group.from)})
		component.Props.Set(&ical.Prop{Name: "TZOFFSETTO", Value: utcOffset(group.to)})
		component.Props.SetText("TZNAME", group.name)
		if len(group.dates) > 1 {
			component.Props.Set(&ical.Prop{Name: "RDATE", Value: strings.Join(group.dates[1:], ",")})
		}
		zone.Children = append(zone.Children, component)
	}
	if replaceIndex >= 0 {
		data.Children[replaceIndex] = zone
	} else {
		data.Children = append(data.Children, zone)
	}
	return nil
}
func utcOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	if seconds%60 != 0 {
		return fmt.Sprintf("%s%02d%02d%02d", sign, seconds/3600, (seconds%3600)/60, seconds%60)
	}
	return fmt.Sprintf("%s%02d%02d", sign, seconds/3600, (seconds%3600)/60)
}
func validateUntil(event ical.Event) error {
	rule := event.Props.Get("RRULE")
	start := event.Props.Get("DTSTART")
	if rule == nil || start == nil {
		return nil
	}
	for _, part := range strings.Split(rule.Value, ";") {
		if strings.HasPrefix(part, "UNTIL=") {
			value := strings.TrimPrefix(part, "UNTIL=")
			if start.ValueType() == ical.ValueDate {
				if len(value) != 8 {
					return fmt.Errorf("all-day recurrence UNTIL must be YYYYMMDD")
				}
			} else if start.Params.Get("TZID") != "" || strings.HasSuffix(start.Value, "Z") {
				if len(value) != 16 || !strings.HasSuffix(value, "Z") {
					return fmt.Errorf("timed recurrence UNTIL must be UTC YYYYMMDDTHHMMSSZ")
				}
			}
		}
	}
	return nil
}
func timezoneRange(event ical.Event, loc *time.Location) (time.Time, time.Time, error) {
	start, err := event.DateTimeStart(loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := event.DateTimeEnd(loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("event ends before start")
	}
	duration := end.Sub(start)
	if p := event.Props.Get("RRULE"); p != nil {
		option, err := rrule.StrToROptionInLocation(p.Value, loc)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		option.Dtstart = start
		if option.Count == 0 && option.Until.IsZero() {
			return time.Time{}, time.Time{}, fmt.Errorf("new timezone recurrence requires finite COUNT or UNTIL so VTIMEZONE covers the whole series")
		}
		if !option.Until.IsZero() {
			if option.Until.After(end) {
				end = option.Until.Add(duration)
			}
		} else {
			if option.Count > 10000 {
				return time.Time{}, time.Time{}, fmt.Errorf("new timezone recurrence COUNT exceeds 10000")
			}
			rule, err := rrule.NewRRule(*option)
			if err != nil {
				return time.Time{}, time.Time{}, err
			}
			next := rule.Iterator()
			for n := 0; n < option.Count; n++ {
				t, ok := next()
				if !ok {
					break
				}
				if t.Add(duration).After(end) {
					end = t.Add(duration)
				}
			}
		}
	}
	for _, p := range event.Props.Values("RDATE") {
		for _, value := range strings.Split(p.Value, ",") {
			copy := p
			copy.Value = value
			t, err := copy.DateTime(loc)
			if err != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("unsupported RDATE for timezone export")
			}
			if t.Add(duration).After(end) {
				end = t.Add(duration)
			}
		}
	}
	return start, end, nil
}
