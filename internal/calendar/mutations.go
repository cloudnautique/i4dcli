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
)

// Changes contains only the fields to replace. Dates imply all-day events;
// timestamps must include an offset. Start and End must be changed together.
type Changes struct {
	Summary, Location, Description *string
	Start, End                     string
}

func (c Changes) Validate(create bool) error {
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
	if !create && c.Summary == nil && c.Location == nil && c.Description == nil && c.Start == "" && c.End == "" {
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
			e.Props.SetDateTime("DTSTART", start.UTC())
			e.Props.SetDateTime("DTEND", end.UTC())
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
		if resp.StatusCode == http.StatusPreconditionFailed {
			return nil, fmt.Errorf("event changed on the server or already exists; list events again before retrying")
		}
		return nil, fmt.Errorf("%s event: HTTP %d", method, resp.StatusCode)
	}
	return resp, nil
}

// Mutate creates, edits or deletes a standalone event. Edits retain unmodified
// properties and alarms; edit/delete require a strong server ETag.
func Mutate(ctx context.Context, client *http.Client, endpoint string, c Calendar, action, object string, changes Changes) (string, error) {
	if action != "create" && action != "edit" && action != "delete" {
		return "", fmt.Errorf("unknown event action %q", action)
	}
	if action != "delete" {
		if err := changes.Validate(action == "create"); err != nil {
			return "", err
		}
	}
	var data *ical.Calendar
	var event ical.Event
	etag := "*"
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
		return "", err
	}
	if action != "create" {
		resp, err := mutationRequest(ctx, client, http.MethodGet, target, nil, "")
		if err != nil {
			return "", err
		}
		etag = resp.Header.Get("ETag")
		data, err = ical.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode()
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("read event: %w", err)
		}
		if len(etag) < 2 || !strings.HasPrefix(etag, "\"") || !strings.HasSuffix(etag, "\"") {
			return "", fmt.Errorf("server did not supply a strong ETag; cannot safely %s", action)
		}
		events := data.Events()
		if len(events) != 1 {
			return "", fmt.Errorf("only standalone events can be edited or deleted")
		}
		for _, child := range data.Children {
			if child.Name != "VEVENT" && child.Name != "VTIMEZONE" {
				return "", fmt.Errorf("object contains non-event components; cannot safely %s", action)
			}
		}
		event = events[0]
		if uid, err := event.Props.Text("UID"); err != nil || uid == "" {
			return "", fmt.Errorf("event has no valid UID")
		}
		for _, key := range []string{"RRULE", "RDATE", "EXRULE", "EXDATE", "RECURRENCE-ID"} {
			if event.Props.Get(key) != nil {
				return "", fmt.Errorf("recurring events cannot yet be edited or deleted")
			}
		}
	}
	method := http.MethodDelete
	var body bytes.Buffer
	if action != "delete" {
		changes.apply(event)
		if action == "edit" {
			seq := 0
			if p := event.Props.Get("SEQUENCE"); p != nil {
				seq, err = p.Int()
				if err != nil || seq < 0 || seq == int(^uint(0)>>1) {
					return "", fmt.Errorf("invalid event SEQUENCE")
				}
			}
			event.Props.Set(&ical.Prop{Name: "SEQUENCE", Value: strconv.Itoa(seq + 1)})
		}
		if err := ical.NewEncoder(&body).Encode(data); err != nil {
			return "", err
		}
		method = http.MethodPut
	}
	resp, err := mutationRequest(ctx, client, method, target, body.Bytes(), etag)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	return target.EscapedPath(), nil
}
