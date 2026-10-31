package calendar

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	netmail "net/mail"
	"net/url"
	"reflect"
	"strings"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"i4dcli/internal/fault"
)

// Scheduling is an explicitly authorized server-scheduling operation. Empty
// identities resolve only from principal-owned calendar addresses, never login.
type Scheduling struct {
	Action    string   `json:"action"`
	Actor     string   `json:"actor,omitempty"`
	Organizer string   `json:"organizer,omitempty"`
	Attendee  string   `json:"attendee,omitempty"`
	PartStat  string   `json:"partstat,omitempty"`
	Attendees []string `json:"attendees,omitempty"`
	Changes   Changes  `json:"changes"`
}
type SchedulingRequest = Scheduling

func (s Scheduling) Validate() error {
	if s.Action != "invite" && s.Action != "respond" && s.Action != "cancel" {
		return fmt.Errorf("scheduling action must be invite, respond, or cancel")
	}
	if err := s.Changes.ValidateScope(); err != nil {
		return err
	}
	for _, value := range []string{s.Actor, s.Organizer, s.Attendee} {
		if value != "" {
			if _, err := calendarAddress(value); err != nil {
				return err
			}
		}
	}
	if len(s.Attendees) > 100 {
		return fmt.Errorf("at most 100 attendees are supported")
	}
	seen := map[string]bool{}
	for _, value := range s.Attendees {
		address, err := calendarAddress(value)
		if err != nil {
			return err
		}
		if seen[address] {
			return fmt.Errorf("duplicate attendee")
		}
		seen[address] = true
	}
	if s.Action == "respond" {
		switch strings.ToUpper(s.PartStat) {
		case "ACCEPTED", "DECLINED", "TENTATIVE":
		default:
			return fmt.Errorf("partstat must be ACCEPTED, DECLINED, or TENTATIVE")
		}
		if s.Organizer != "" || s.Attendees != nil || hasChangeFields(s.Changes) {
			return fmt.Errorf("respond can only change the owned attendee participation status")
		}
	} else if s.PartStat != "" || s.Attendee != "" {
		return fmt.Errorf("attendee and partstat only apply to respond")
	}
	if s.Action == "cancel" && (s.Attendees != nil || hasChangeFields(s.Changes)) {
		return fmt.Errorf("cancel accepts only identity and recurrence scope")
	}
	if s.Action == "invite" && hasChangeFields(s.Changes) {
		if err := s.Changes.Validate(false); err != nil {
			return err
		}
	}
	return nil
}
func calendarAddress(value string) (string, error) {
	if len(value) > 320 || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("calendar identity must be an email address")
	}
	if len(value) >= 7 && strings.EqualFold(value[:7], "mailto:") {
		value = value[7:]
	}
	a, err := netmail.ParseAddress(value)
	if err != nil || a.Name != "" || a.Address != value {
		return "", fmt.Errorf("calendar identity must be a bare email address")
	}
	return "mailto:" + strings.ToLower(a.Address), nil
}

type scheduleContext struct {
	request Scheduling
	owned   map[string]bool
	changed []ical.Event
}

func Schedule(ctx context.Context, client *http.Client, endpoint string, c Calendar, object string, s Scheduling) (string, error) {
	if err := s.Validate(); err != nil {
		return "", fault.Usage(err)
	}
	action := "edit"
	if s.Action == "invite" && object == "" {
		action = "create"
		if err := s.Changes.Validate(true); err != nil {
			return "", fault.Usage(err)
		}
		if len(s.Attendees) == 0 {
			return "", fault.New("usage", "new invitation requires attendees")
		}
	}
	if s.Action != "invite" && object == "" {
		return "", fault.New("usage", "path is required for respond or cancel")
	}
	if s.Action == "cancel" {
		action = "delete"
	}
	owned, err := discoverScheduling(ctx, client, endpoint, c)
	if err != nil {
		return "", err
	}
	return mutate(ctx, client, endpoint, c, action, object, s.Changes, &scheduleContext{request: s, owned: owned})
}

func (s *scheduleContext) actor(explicit, existing string) (string, error) {
	if explicit == "" {
		explicit = s.request.Actor
	}
	if explicit != "" {
		address, err := calendarAddress(explicit)
		if err != nil {
			return "", err
		}
		if !s.owned[address] {
			return "", fault.New("permission_denied", "scheduling identity is not owned by the authenticated principal")
		}
		return address, nil
	}
	if existing != "" {
		if address, err := calendarAddress(existing); err == nil && s.owned[address] {
			return address, nil
		}
	}
	if len(s.owned) != 1 {
		return "", fault.New("usage", "principal has multiple calendar addresses; select an explicit scheduling identity")
	}
	for address := range s.owned {
		return address, nil
	}
	return "", fault.New("permission_denied", "principal has no usable calendar address")
}
func (s *scheduleContext) prepare(data *ical.Calendar, event ical.Event, action string, changes Changes) error {
	targets := []ical.Event{event}
	if changes.Scope != "occurrence" {
		targets = data.Events()
	}
	if s.request.Action == "respond" {
		actor, err := s.actor(s.request.Attendee, "")
		if err != nil {
			return err
		}
		for _, target := range targets {
			before := cloneComponent(target.Component)
			if target.Props.Get("ORGANIZER") == nil {
				return fmt.Errorf("resource is not a meeting invitation")
			}
			matched := false
			props := target.Props["ATTENDEE"]
			for i := range props {
				address, err := calendarAddress(props[i].Value)
				if err != nil {
					return fmt.Errorf("invalid invitation attendee")
				}
				if address == actor {
					if matched {
						return fmt.Errorf("duplicate owned attendee")
					}
					matched = true
					if agent := props[i].Params.Get("SCHEDULE-AGENT"); agent != "" && agent != "SERVER" {
						return fmt.Errorf("attendee disables server scheduling")
					}
					if props[i].Params == nil {
						props[i].Params = ical.Params{}
					}
					props[i].Params.Set("PARTSTAT", strings.ToUpper(s.request.PartStat))
					props[i].Params.Set("RSVP", "FALSE")
				}
			}
			if !matched {
				return fault.New("permission_denied", "owned identity is not an attendee on this invitation")
			}
			target.Props["ATTENDEE"] = props
			s.recordChange(before, target)
		}
		return nil
	}
	existing := ""
	if prop := event.Props.Get("ORGANIZER"); prop != nil {
		existing = prop.Value
	}
	actor, err := s.actor(s.request.Organizer, existing)
	if err != nil {
		return err
	}
	for _, target := range targets {
		before := cloneComponent(target.Component)
		organizer := target.Props.Get("ORGANIZER")
		if organizer != nil {
			address, err := calendarAddress(organizer.Value)
			if err != nil || address != actor {
				return fault.New("permission_denied", "only the invitation organizer may update or cancel it")
			}
			if agent := organizer.Params.Get("SCHEDULE-AGENT"); agent != "" && agent != "SERVER" {
				return fmt.Errorf("organizer disables server scheduling")
			}
		}
		if organizer == nil && s.request.Action == "cancel" {
			return fmt.Errorf("resource is not an organizer meeting")
		}
		p := ical.NewProp("ORGANIZER")
		if organizer != nil {
			for key, values := range organizer.Params {
				p.Params[key] = append([]string(nil), values...)
			}
		}
		p.Value = actor
		p.Params.Set("SCHEDULE-AGENT", "SERVER")
		target.Props.Set(p)
		if s.request.Attendees != nil {
			if len(s.request.Attendees) == 0 {
				return fmt.Errorf("empty attendee replacement is not supported; cancel the meeting instead")
			}
			old := map[string]ical.Prop{}
			for _, a := range target.Props.Values("ATTENDEE") {
				if address, err := calendarAddress(a.Value); err == nil {
					old[address] = a
				}
			}
			target.Props.Del("ATTENDEE")
			for _, value := range s.request.Attendees {
				address, _ := calendarAddress(value)
				p := ical.NewProp("ATTENDEE")
				if previous, ok := old[address]; ok {
					*p = previous
					p.Params = ical.Params{}
					for key, values := range previous.Params {
						p.Params[key] = append([]string(nil), values...)
					}
				} else {
					p.Params.Set("PARTSTAT", "NEEDS-ACTION")
					p.Params.Set("RSVP", "TRUE")
					p.Params.Set("ROLE", "REQ-PARTICIPANT")
				}
				p.Value = address
				p.Params.Set("SCHEDULE-AGENT", "SERVER")
				target.Props.Add(p)
			}
		}
		if s.request.Action == "invite" && len(target.Props.Values("ATTENDEE")) == 0 {
			return fmt.Errorf("invitation requires attendees")
		}
		s.recordChange(before, target)
	}
	return nil
}

// Record semantic edits before timestamps change so unchanged exceptions keep
// their own revisions. Every changed exception is independently scheduled.
func (s *scheduleContext) recordChange(before *ical.Component, after ical.Event) {
	if !reflect.DeepEqual(before, after.Component) {
		s.changed = append(s.changed, after)
	}
}
func (s *scheduleContext) updateRevisions(selected ical.Event, create bool) error {
	seen := map[*ical.Component]bool{}
	for _, event := range append([]ical.Event{selected}, s.changed...) {
		if seen[event.Component] {
			continue
		}
		seen[event.Component] = true
		(Changes{}).apply(event)
		if !create && s.request.Action != "respond" {
			if err := incrementSequence(event); err != nil {
				return err
			}
		}
	}
	return nil
}

type scheduleTagTransport struct {
	Base http.RoundTripper
	Tag  string
}

func (t scheduleTagTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.Header = req.Header.Clone()
	if req.Method == http.MethodPut || req.Method == http.MethodDelete {
		copy.Header.Set("If-Schedule-Tag-Match", t.Tag)
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(copy)
}
func discoverScheduling(ctx context.Context, client *http.Client, endpoint string, c Calendar) (map[string]bool, error) {
	base, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	collection, err := base.Parse(c.Path)
	if err != nil {
		return nil, err
	}
	if collection.Scheme != base.Scheme || collection.Host != base.Host {
		return nil, fmt.Errorf("scheduling calendar must belong to the discovered endpoint")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodOptions, collection.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	supported := false
	for _, value := range resp.Header.Values("DAV") {
		for _, feature := range strings.Split(value, ",") {
			if strings.TrimSpace(feature) == "calendar-auto-schedule" {
				supported = true
			}
		}
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fault.HTTP(resp.StatusCode)
	}
	if !supported {
		return nil, fault.New("usage", "server does not advertise calendar-auto-schedule; no invitation was sent")
	}
	dav, err := caldav.NewClient(client, endpoint)
	if err != nil {
		return nil, err
	}
	principal, err := dav.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if principal == "" {
		return nil, fmt.Errorf("server returned no current principal")
	}
	target, err := base.Parse(principal)
	if err != nil {
		return nil, err
	}
	if target.Scheme != base.Scheme || target.Host != base.Host {
		return nil, fmt.Errorf("scheduling principal must belong to the discovered endpoint")
	}
	body := `<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:calendar-user-address-set/><c:schedule-outbox-URL/></d:prop></d:propfind>`
	request, err = http.NewRequestWithContext(ctx, "PROPFIND", target.String(), bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Depth", "0")
	request.Header.Set("Content-Type", "application/xml; charset=utf-8")
	resp, err = client.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fault.HTTP(resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("scheduling discovery response exceeds 1 MiB")
	}
	var multistatus struct {
		Responses []struct {
			Props []struct {
				Status string `xml:"DAV: status"`
				Prop   struct {
					Addresses struct {
						Hrefs []string `xml:"DAV: href"`
					} `xml:"urn:ietf:params:xml:ns:caldav calendar-user-address-set"`
					Outbox struct {
						Href string `xml:"DAV: href"`
					} `xml:"urn:ietf:params:xml:ns:caldav schedule-outbox-URL"`
				} `xml:"DAV: prop"`
			} `xml:"DAV: propstat"`
		} `xml:"DAV: response"`
	}
	if err := xml.Unmarshal(raw, &multistatus); err != nil {
		return nil, fmt.Errorf("invalid scheduling discovery response")
	}
	owned := map[string]bool{}
	outbox := ""
	for _, response := range multistatus.Responses {
		for _, prop := range response.Props {
			parts := strings.Fields(prop.Status)
			if len(parts) < 2 || parts[1] != "200" {
				continue
			}
			for _, value := range prop.Prop.Addresses.Hrefs {
				if address, err := calendarAddress(value); err == nil {
					owned[address] = true
				}
			}
			if prop.Prop.Outbox.Href != "" {
				outbox = prop.Prop.Outbox.Href
			}
		}
	}
	if len(owned) == 0 || outbox == "" {
		return nil, fault.New("usage", "server did not expose scheduling addresses and outbox; no invitation was sent")
	}
	return owned, nil
}
