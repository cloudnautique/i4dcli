package calendar

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"i4dcli/internal/fault"
)

type EventDetail struct {
	Event
	Description string              `json:"description,omitempty"`
	Recurrence  map[string][]string `json:"recurrence,omitempty"`
}
type Object struct {
	Path   string        `json:"path"`
	ETag   string        `json:"etag,omitempty"`
	Events []EventDetail `json:"events"`
}

// Get inspects a complete resource, including a recurrence master and exceptions.
// It does not expand recurrence or imply that the resource is editable.
func Get(ctx context.Context, client *http.Client, endpoint string, c Calendar, object string, loc *time.Location) (Object, error) {
	target, err := objectURL(endpoint, c, object)
	if err != nil {
		return Object{}, fault.Usage(err)
	}
	resp, err := mutationRequest(ctx, client, http.MethodGet, target, nil, "")
	if err != nil {
		return Object{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
	if err != nil {
		return Object{}, err
	}
	if len(data) > 10<<20 {
		return Object{}, fmt.Errorf("calendar object exceeds 10 MiB")
	}
	cal, err := ical.NewDecoder(strings.NewReader(string(data))).Decode()
	if err != nil {
		return Object{}, fmt.Errorf("read event: %w", err)
	}
	result := Object{Path: target.EscapedPath(), ETag: resp.Header.Get("ETag"), Events: []EventDetail{}}
	for _, component := range cal.Events() {
		event, _, err := parseEventFields(component, c, result.Path, loc)
		if err != nil {
			return Object{}, err
		}
		description, err := component.Props.Text("DESCRIPTION")
		if err != nil {
			return Object{}, err
		}
		detail := EventDetail{Event: event, Description: description, Recurrence: map[string][]string{}}
		for _, key := range []string{"RRULE", "RDATE", "EXRULE", "EXDATE"} {
			for _, p := range component.Props.Values(key) {
				detail.Recurrence[key] = append(detail.Recurrence[key], p.Value)
			}
		}
		result.Events = append(result.Events, detail)
	}
	if len(result.Events) == 0 {
		return Object{}, fmt.Errorf("object contains no events")
	}
	return result, nil
}
