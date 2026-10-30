package calendar

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
	"i4dcli/internal/fault"
)

// Changes contains only the fields to replace. Dates imply all-day events;
// timestamps must include an offset. Start and End must be changed together.
type Changes struct {
	Summary      *string `json:"summary,omitempty"`
	Location     *string `json:"location,omitempty"`
	Description  *string `json:"description,omitempty"`
	Start        string  `json:"start,omitempty"`
	End          string  `json:"end,omitempty"`
	RRule        *string `json:"rrule,omitempty"`
	Scope        string  `json:"scope,omitempty"`
	RecurrenceID string  `json:"recurrence_id,omitempty"`
	Timezone     string  `json:"timezone,omitempty"`
}

func (c Changes) Validate(create bool) error {
	if err := c.ValidateScope(); err != nil {
		return err
	}
	if create && c.Scope == "occurrence" {
		return fmt.Errorf("new events cannot have occurrence scope")
	}
	if c.RRule != nil {
		if err := validateRule(*c.RRule); err != nil {
			return err
		}
	}
	if create && c.Timezone != "" && c.Timezone != "UTC" && c.RRule != nil && *c.RRule != "" {
		rule, _ := rrule.StrToROption(*c.RRule)
		if rule != nil && rule.Count == 0 && rule.Until.IsZero() {
			return fmt.Errorf("new timezone recurrence requires finite COUNT or UNTIL")
		}
	}
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			return fmt.Errorf("timezone must be an IANA timezone")
		}
		if c.Start == "" {
			return fmt.Errorf("timezone requires start and end")
		}
	}
	if create && (c.Summary == nil || strings.TrimSpace(*c.Summary) == "") {
		return fmt.Errorf("--summary is required and cannot be blank")
	}
	if c.Summary != nil && strings.TrimSpace(*c.Summary) == "" {
		return fmt.Errorf("summary cannot be blank")
	}
	if create || c.Start != "" || c.End != "" {
		_, _, _, err := c.times()
		if err != nil {
			return err
		}
	}
	if !create && c.Summary == nil && c.Location == nil && c.Description == nil && c.Start == "" && c.End == "" && c.RRule == nil {
		return fmt.Errorf("provide at least one field to edit")
	}
	return nil
}

func (c Changes) times() (time.Time, time.Time, bool, error) {
	start, err := time.Parse(time.DateOnly, c.Start)
	allDay := err == nil
	layout := time.DateOnly
	if !allDay {
		layout = time.RFC3339
		start, err = time.Parse(layout, c.Start)
	}
	if err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("--start: expected YYYY-MM-DD or RFC3339 timestamp")
	}
	end, err := time.Parse(layout, c.End)
	if err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("--end is required with --start and must use the same date or timestamp format")
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, false, fmt.Errorf("--end must be later than --start (all-day end is exclusive)")
	}
	return start, end, allDay, nil
}

func (c Changes) apply(e ical.Event) {
	for key, value := range map[string]*string{"SUMMARY": c.Summary, "LOCATION": c.Location, "DESCRIPTION": c.Description} {
		if value != nil {
			e.Props.SetText(key, *value)
		}
	}
	if c.Start != "" {
		start, end, allDay, _ := c.times() // validated before any request
		e.Props.Del("DURATION")
		if allDay {
			e.Props.SetDate("DTSTART", start)
			e.Props.SetDate("DTEND", end)
		} else {
			zone := c.Timezone
			if zone == "" && e.Props.Get("DTSTART") != nil {
				zone = e.Props.Get("DTSTART").Params.Get("TZID")
			}
			loc := time.UTC
			if zone != "" {
				if parsed, err := time.LoadLocation(zone); err == nil {
					loc = parsed
				}
			}
			endLoc := loc
			if c.Timezone == "" {
				if prop := e.Props.Get("DTEND"); prop != nil && prop.Params.Get("TZID") != "" {
					if parsed, err := time.LoadLocation(prop.Params.Get("TZID")); err == nil {
						endLoc = parsed
					}
				}
			}
			e.Props.SetDateTime("DTSTART", start.In(loc))
			e.Props.SetDateTime("DTEND", end.In(endLoc))
		}
	}
	if c.RRule != nil {
		if *c.RRule == "" {
			e.Props.Del("RRULE")
		} else {
			e.Props.Set(&ical.Prop{Name: "RRULE", Value: strings.TrimPrefix(*c.RRule, "RRULE:")})
		}
	}
	now := time.Now().UTC()
	e.Props.SetDateTime("DTSTAMP", now)
	e.Props.SetDateTime("LAST-MODIFIED", now)
}

// objectURL confines writes to a direct member of the selected calendar.
func objectURL(endpoint string, c Calendar, object string) (*url.URL, error) {
	base, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	collection, err := base.Parse(c.Path)
	if err != nil {
		return nil, err
	}
	collection.Path = strings.TrimSuffix(collection.Path, "/") + "/"
	collection.RawPath = ""
	target, err := collection.Parse(object)
	if err != nil {
		return nil, err
	}
	if target.Scheme != collection.Scheme || target.Host != collection.Host || target.User != nil || target.RawQuery != "" || target.Fragment != "" || strings.HasSuffix(target.Path, "/") || path.Dir(target.Path) != strings.TrimSuffix(collection.Path, "/") || path.Clean(target.Path) != target.Path {
		return nil, fmt.Errorf("--path must be an event directly inside the selected calendar")
	}
	return target, nil
}

func mutationRequest(ctx context.Context, client *http.Client, method string, target *url.URL, body []byte, etag string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	}
	if etag == "*" {
		req.Header.Set("If-None-Match", "*")
	} else if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fault.HTTP(resp.StatusCode)
	}
	return resp, nil
}

// Mutate changes ordinary calendar resources. Scheduling objects require the
// separately authorized Schedule entry point because PUT/DELETE can send mail.
func Mutate(ctx context.Context, client *http.Client, endpoint string, c Calendar, action, object string, changes Changes) (string, error) {
	return mutate(ctx, client, endpoint, c, action, object, changes, nil)
}

func mutate(ctx context.Context, client *http.Client, endpoint string, c Calendar, action, object string, changes Changes, scheduling *scheduleContext) (string, error) {
	if action != "create" && action != "edit" && action != "delete" {
		return "", fmt.Errorf("unknown event action %q", action)
	}
	if err := changes.ValidateScope(); err != nil {
		return "", err
	}
	if action != "delete" {
		if action == "create" || scheduling == nil || hasChangeFields(changes) {
			if err := changes.Validate(action == "create"); err != nil {
				return "", err
			}
		}
	}
	var data *ical.Calendar
	var event ical.Event
	var events []ical.Event
	etag := "*"
	scheduleTag := ""
	if action == "create" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return "", err
		}
		uid := fmt.Sprintf("%x", id)
		object = strings.TrimSuffix(c.Path, "/") + "/" + uid + ".ics"
		data = ical.NewCalendar()
		data.Props.SetText("VERSION", "2.0")
		data.Props.SetText("PRODID", "-//i4dcli//Calendar//EN")
		event = *ical.NewEvent()
		event.Props.SetText("UID", uid)
		event.Props.SetDateTime("CREATED", time.Now().UTC())
		data.Children = append(data.Children, event.Component)
	} else if object == "" {
		return "", fmt.Errorf("--path is required")
	}
	target, err := objectURL(endpoint, c, object)
	if err != nil {
		return "", fault.Usage(err)
	}
	if action != "create" {
		resp, err := mutationRequest(ctx, client, http.MethodGet, target, nil, "")
		if err != nil {
			return "", err
		}
		etag = resp.Header.Get("ETag")
		scheduleTag = resp.Header.Get("Schedule-Tag")
		raw, err := io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		if len(raw) > 10<<20 {
			return "", fmt.Errorf("calendar object exceeds 10 MiB")
		}
		data, err = ical.NewDecoder(bytes.NewReader(raw)).Decode()
		if err != nil {
			return "", fmt.Errorf("read event: %w", err)
		}
		if len(etag) < 2 || !strings.HasPrefix(etag, "\"") || !strings.HasSuffix(etag, "\"") {
			return "", fmt.Errorf("server did not supply a strong ETag; cannot safely %s", action)
		}
		event, events, err = masterEvent(data)
		if err != nil {
			return "", err
		}
		if scheduling == nil {
			for _, e := range events {
				if e.Props.Get("ORGANIZER") != nil || e.Props.Get("ATTENDEE") != nil {
					return "", fault.New("permission_denied", "meeting resources require explicit invite, respond, or cancel authorization")
				}
			}
		}
		isRecurring := recurring(event) || len(events) > 1
		if isRecurring && changes.Scope == "" {
			return "", fmt.Errorf("recurring resources require explicit series or occurrence scope")
		}
		if changes.Scope == "occurrence" {
			if !isRecurring {
				return "", fmt.Errorf("occurrence scope requires a recurring event")
			}
			event, err = selectOccurrence(ctx, data, event, events, changes.RecurrenceID)
			if err != nil {
				return "", err
			}
		}
	}
	if scheduling != nil {
		if err := scheduling.prepare(data, event, action, changes); err != nil {
			return "", err
		}
	}
	method := http.MethodDelete
	var body bytes.Buffer
	if action != "delete" || changes.Scope == "occurrence" {
		if action == "delete" && changes.Scope == "occurrence" {
			if scheduling != nil {
				event.Props.SetText("STATUS", "CANCELLED")
			} else {
				master, _, err := masterEvent(data)
				if err != nil {
					return "", err
				}
				targetTime, err := occurrenceTime(master, changes.RecurrenceID)
				if err != nil {
					return "", err
				}
				master.Props.Add(dateLike("EXDATE", master.Props.Get("DTSTART"), targetTime))
				children := data.Children[:0]
				for _, child := range data.Children {
					if child != event.Component {
						children = append(children, child)
					}
				}
				data.Children = children
				event = master
			}
		} else {
			if changes.Start != "" && changes.Timezone == "" {
				for _, key := range []string{"DTSTART", "DTEND"} {
					if prop := event.Props.Get(key); prop != nil && prop.Params.Get("TZID") != "" {
						if _, err := time.LoadLocation(prop.Params.Get("TZID")); err != nil {
							return "", fmt.Errorf("cannot preserve unknown event timezone")
						}
					}
				}
			}
			changes.apply(event)
		}
		if action != "create" && (scheduling == nil || scheduling.request.Action != "respond") {
			if err := incrementSequence(event); err != nil {
				return "", err
			}
		}

		if changes.RRule != nil || changes.Start != "" {
			if err := validateUntil(event); err != nil {
				return "", err
			}
		}
		if changes.Start != "" || changes.RRule != nil {
			if err := ensureTimezone(data, event); err != nil {
				return "", err
			}
		}
		if err := ical.NewEncoder(&body).Encode(data); err != nil {
			return "", err
		}
		method = http.MethodPut
	}
	requestClient := client
	if scheduling != nil && scheduleTag != "" {
		copy := *client
		copy.Transport = scheduleTagTransport{Base: client.Transport, Tag: scheduleTag}
		requestClient = &copy
	}
	resp, err := mutationRequest(ctx, requestClient, method, target, body.Bytes(), etag)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	return target.EscapedPath(), nil
}
func hasChangeFields(c Changes) bool {
	return c.Summary != nil || c.Location != nil || c.Description != nil || c.Start != "" || c.End != "" || c.RRule != nil || c.Timezone != ""
}
func incrementSequence(event ical.Event) error {
	seq := 0
	if p := event.Props.Get("SEQUENCE"); p != nil {
		var err error
		seq, err = p.Int()
		if err != nil || seq < 0 || seq == int(^uint(0)>>1) {
			return fmt.Errorf("invalid event SEQUENCE")
		}
	}
	event.Props.Set(&ical.Prop{Name: "SEQUENCE", Value: strconv.Itoa(seq + 1)})
	event.Props.SetDateTime("DTSTAMP", time.Now().UTC())
	event.Props.SetDateTime("LAST-MODIFIED", time.Now().UTC())
	return nil
}
