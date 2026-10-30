package calendar

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

func (c Changes) ValidateScope() error {
	if c.Scope != "" && c.Scope != "series" && c.Scope != "occurrence" {
		return fmt.Errorf("scope must be series or occurrence")
	}
	if c.Scope == "occurrence" {
		if c.RecurrenceID == "" {
			return fmt.Errorf("occurrence scope requires recurrence-id")
		}
		if _, err := parseRecurrenceInput(c.RecurrenceID); err != nil {
			return err
		}
		if c.RRule != nil {
			return fmt.Errorf("rrule cannot be changed for a single occurrence")
		}
	} else if c.RecurrenceID != "" {
		return fmt.Errorf("recurrence-id requires occurrence scope")
	}
	return nil
}
func parseRecurrenceInput(value string) (time.Time, error) {
	for _, layout := range []string{time.DateOnly, time.RFC3339, "20060102", "20060102T150405Z", "20060102T150405"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("recurrence-id must be the original occurrence date or RFC3339 timestamp")
}
func validateRule(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 2048 || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("invalid recurrence rule")
	}
	rule, err := rrule.StrToROption(value)
	if err != nil {
		return fmt.Errorf("invalid recurrence rule: %w", err)
	}
	if rule.Freq > rrule.DAILY {
		return fmt.Errorf("recurrence writes support daily, weekly, monthly, or yearly frequency")
	}
	if _, err := rrule.NewRRule(*rule); err != nil {
		return fmt.Errorf("invalid recurrence rule: %w", err)
	}
	if strings.Contains(value, "COUNT=") && strings.Contains(value, "UNTIL=") {
		return fmt.Errorf("recurrence rule cannot combine COUNT and UNTIL")
	}
	return nil
}
func recurring(event ical.Event) bool {
	for _, key := range []string{"RRULE", "RDATE", "EXRULE", "EXDATE", "RECURRENCE-ID"} {
		if event.Props.Get(key) != nil {
			return true
		}
	}
	return false
}
func cloneComponent(c *ical.Component) *ical.Component {
	copy := ical.NewComponent(c.Name)
	for name, props := range c.Props {
		for _, p := range props {
			np := ical.NewProp(name)
			np.Value = p.Value
			for key, values := range p.Params {
				np.Params[key] = append([]string(nil), values...)
			}
			copy.Props.Add(np)
		}
	}
	for _, child := range c.Children {
		copy.Children = append(copy.Children, cloneComponent(child))
	}
	return copy
}
func masterEvent(cal *ical.Calendar) (ical.Event, []ical.Event, error) {
	var master ical.Event
	events := cal.Events()
	uid := ""
	for _, child := range cal.Children {
		if child.Name != "VEVENT" && child.Name != "VTIMEZONE" {
			return master, nil, fmt.Errorf("object contains non-event components")
		}
	}
	for _, event := range events {
		id, err := event.Props.Text("UID")
		if err != nil || id == "" {
			return master, nil, fmt.Errorf("event has no valid UID")
		}
		if uid != "" && id != uid {
			return master, nil, fmt.Errorf("resource contains multiple event UIDs")
		}
		uid = id
		if event.Props.Get("RECURRENCE-ID") == nil {
			if master.Component != nil {
				return master, nil, fmt.Errorf("resource contains multiple recurrence masters")
			}
			master = event
		}
	}
	if master.Component == nil {
		return master, nil, fmt.Errorf("resource has no recurrence master")
	}
	return master, events, nil
}
func dateLike(name string, original *ical.Prop, t time.Time) *ical.Prop {
	p := ical.NewProp(name)
	for key, values := range original.Params {
		p.Params[key] = append([]string(nil), values...)
	}
	p.Params.Del("RANGE")
	if zone := original.Params.Get("TZID"); zone != "" {
		if loc, err := time.LoadLocation(zone); err == nil {
			t = t.In(loc)
		}
	}
	if original.ValueType() == ical.ValueDate {
		p.Value = t.Format("20060102")
	} else if strings.HasSuffix(original.Value, "Z") {
		p.Value = t.UTC().Format("20060102T150405Z")
	} else {
		p.Value = t.Format("20060102T150405")
	}
	return p
}
func occurrenceTime(master ical.Event, input string) (time.Time, error) {
	requested, err := parseRecurrenceInput(input)
	if err != nil {
		return time.Time{}, err
	}
	prop := master.Props.Get("DTSTART")
	if prop == nil {
		return time.Time{}, fmt.Errorf("recurrence master lacks DTSTART")
	}
	start, err := prop.DateTime(time.UTC)
	if err != nil {
		return time.Time{}, err
	}
	if prop.ValueType() == ical.ValueDate {
		if len(input) != 10 && len(input) != 8 {
			return time.Time{}, fmt.Errorf("all-day recurrence-id requires a date")
		}
		requested = time.Date(requested.Year(), requested.Month(), requested.Day(), 0, 0, 0, 0, start.Location())
	} else if len(input) == 10 || len(input) == 8 {
		return time.Time{}, fmt.Errorf("timed recurrence-id requires a timestamp")
	}
	if len(input) == 15 {
		requested, err = time.ParseInLocation("20060102T150405", input, start.Location())
		if err != nil {
			return time.Time{}, err
		}
	}
	return requested.In(start.Location()), nil
}
func dateListMatches(props []ical.Prop, target time.Time) (bool, error) {
	for _, p := range props {
		if p.ValueType() == ical.ValuePeriod {
			return false, fmt.Errorf("RDATE periods are unsupported for occurrence mutation")
		}
		for _, value := range strings.Split(p.Value, ",") {
			part := p
			part.Value = value
			t, err := part.DateTime(target.Location())
			if err != nil {
				return false, err
			}
			if t.Equal(target) {
				return true, nil
			}
		}
	}
	return false, nil
}
func occurrenceMember(ctx context.Context, master ical.Event, target time.Time) (bool, error) {
	if master.Props.Get("EXRULE") != nil {
		return false, fmt.Errorf("EXRULE is unsupported for occurrence mutation")
	}
	excluded, err := dateListMatches(master.Props.Values("EXDATE"), target)
	if err != nil || excluded {
		return false, err
	}
	start, err := master.DateTimeStart(time.UTC)
	if err != nil {
		return false, err
	}
	if target.Equal(start) {
		return true, nil
	}
	extra, err := dateListMatches(master.Props.Values("RDATE"), target)
	if err != nil || extra {
		return extra, err
	}
	ruleProp := master.Props.Get("RRULE")
	if ruleProp == nil {
		return false, nil
	}
	if err := validateRule(ruleProp.Value); err != nil {
		return false, err
	}
	option, err := rrule.StrToROptionInLocation(ruleProp.Value, start.Location())
	if err != nil {
		return false, err
	}
	option.Dtstart = start
	rule, err := rrule.NewRRule(*option)
	if err != nil {
		return false, err
	}
	next := rule.Iterator()
	for n := 0; n < 10000; n++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		date, ok := next()
		if !ok || date.After(target) {
			return false, nil
		}
		if date.Equal(target) {
			return true, nil
		}
	}
	return false, fmt.Errorf("recurrence membership exceeds 10000 occurrences; narrow the series before editing")
}

// selectOccurrence validates the original instance identity even if its detached
// exception has moved to a different start time.
func selectOccurrence(ctx context.Context, cal *ical.Calendar, master ical.Event, events []ical.Event, input string) (ical.Event, error) {
	target, err := occurrenceTime(master, input)
	if err != nil {
		return ical.Event{}, err
	}
	var existing *ical.Event
	for i := range events {
		p := events[i].Props.Get("RECURRENCE-ID")
		if p == nil {
			continue
		}
		if p.Params.Get("RANGE") != "" {
			return ical.Event{}, fmt.Errorf("range recurrence exceptions are unsupported for occurrence mutation")
		}
		rid, err := p.DateTime(target.Location())
		if err != nil {
			return ical.Event{}, err
		}
		if rid.Equal(target) {
			if existing != nil {
				return ical.Event{}, fmt.Errorf("duplicate recurrence exceptions")
			}
			existing = &events[i]
		}
	}
	// Existing detached exceptions are authoritative series members, including
	// instances excluded from the master by EXDATE.
	if existing != nil {
		return *existing, nil
	}
	member, err := occurrenceMember(ctx, master, target)
	if err != nil {
		return ical.Event{}, err
	}
	if !member {
		return ical.Event{}, fmt.Errorf("recurrence-id is not an occurrence in this series")
	}
	event := ical.Event{Component: cloneComponent(master.Component)}
	for _, key := range []string{"RRULE", "RDATE", "EXRULE", "EXDATE"} {
		event.Props.Del(key)
	}
	event.Props.Set(dateLike("RECURRENCE-ID", master.Props.Get("DTSTART"), target))
	start, err := master.DateTimeStart(time.UTC)
	if err != nil {
		return ical.Event{}, err
	}
	end, err := master.DateTimeEnd(time.UTC)
	if err != nil {
		return ical.Event{}, err
	}
	if master.Props.Get("DTSTART").ValueType() == ical.ValueDate && master.Props.Get("DTEND") == nil && master.Props.Get("DURATION") == nil {
		end = start.AddDate(0, 0, 1)
	}
	event.Props.Set(dateLike("DTSTART", master.Props.Get("DTSTART"), target))
	if prop := master.Props.Get("DTEND"); prop != nil {
		event.Props.Set(dateLike("DTEND", prop, target.Add(end.Sub(start))))
	}
	cal.Children = append(cal.Children, event.Component)
	return event, nil
}
