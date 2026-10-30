// Package contacts provides bounded iCloud CardDAV discovery and contact CRUD.
package contacts

import (
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-vcard"
	"i4dcli/internal/fault"
)

type AddressBook struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}
type Contact struct {
	Path         string     `json:"path"`
	ETag         string     `json:"etag,omitempty"`
	UID          string     `json:"uid"`
	Name         string     `json:"name"`
	GivenName    string     `json:"given_name,omitempty"`
	FamilyName   string     `json:"family_name,omitempty"`
	Emails       []string   `json:"emails"`
	Phones       []string   `json:"phones"`
	Organization string     `json:"organization,omitempty"`
	Note         string     `json:"note,omitempty"`
	Birthday     string     `json:"birthday,omitempty"`
	URLs         []string   `json:"urls,omitempty"`
	Properties   vcard.Card `json:"properties,omitempty"`
}
type Query struct {
	AddressBook string `json:"addressbook"`
	Search      string `json:"search,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}
type Page struct {
	Contacts  []Contact `json:"contacts"`
	Returned  int       `json:"returned"`
	Truncated bool      `json:"truncated"`
}
type Changes struct {
	Name         *string   `json:"name,omitempty"`
	GivenName    *string   `json:"given_name,omitempty"`
	FamilyName   *string   `json:"family_name,omitempty"`
	Emails       *[]string `json:"emails,omitempty"`
	Phones       *[]string `json:"phones,omitempty"`
	Organization *string   `json:"organization,omitempty"`
	Note         *string   `json:"note,omitempty"`
	Birthday     *string   `json:"birthday,omitempty"`
	URLs         *[]string `json:"urls,omitempty"`
}
type Mutation struct {
	Action      string  `json:"action"`
	AddressBook string  `json:"addressbook"`
	Path        string  `json:"path,omitempty"`
	Changes     Changes `json:"changes"`
}
type Result struct {
	Action string `json:"action"`
	Path   string `json:"path"`
}

func validText(s string, max int) bool { return len(s) <= max && !strings.ContainsAny(s, "\r\x00") }
func (q Query) Validate() error {
	if strings.TrimSpace(q.AddressBook) == "" || !validText(q.AddressBook, 4096) {
		return fault.New("usage", "provide an address book name or path")
	}
	if q.Limit < 0 || q.Limit > 1000 {
		return fault.New("usage", "contact limit must be 1–1000")
	}
	if !validText(q.Search, 1024) {
		return fault.New("usage", "contact search exceeds 1024 bytes or contains invalid controls")
	}
	return nil
}
func (c Changes) Validate(create bool) error {
	if create && (c.Name == nil || strings.TrimSpace(*c.Name) == "") {
		return fault.New("usage", "contact name is required")
	}
	if c.Name != nil && strings.TrimSpace(*c.Name) == "" {
		return fault.New("usage", "contact name cannot be blank")
	}
	count := 0
	for _, s := range []*string{c.Name, c.GivenName, c.FamilyName, c.Organization, c.Note, c.Birthday} {
		if s != nil {
			count++
			if !validText(*s, 10000) {
				return fault.New("usage", "contact text exceeds 10000 bytes or contains invalid controls")
			}
		}
	}
	for _, s := range []*string{c.GivenName, c.FamilyName} {
		if s != nil && strings.ContainsAny(*s, ";\n") {
			return fault.New("usage", "structured names cannot contain semicolons or newlines")
		}
	}
	for _, list := range []*[]string{c.Emails, c.Phones, c.URLs} {
		if list != nil {
			count++
			if len(*list) > 50 {
				return fault.New("usage", "at most 50 values per contact field")
			}
			for _, s := range *list {
				if strings.TrimSpace(s) == "" || !validText(s, 4096) {
					return fault.New("usage", "contact list values must be nonempty and at most 4096 bytes")
				}
			}
		}
	}
	if c.Emails != nil {
		for _, value := range *c.Emails {
			a, err := mail.ParseAddress(value)
			if err != nil || a.Address != value {
				return fault.New("usage", "contact email must be an address without a display name")
			}
		}
	}
	if c.Birthday != nil && *c.Birthday != "" {
		if _, err := time.Parse(time.DateOnly, *c.Birthday); err != nil {
			return fault.New("usage", "birthday must be YYYY-MM-DD or empty to clear")
		}
	}
	if !create && count == 0 {
		return fault.New("usage", "provide at least one contact field to edit")
	}
	return nil
}
func (m Mutation) Validate() error {
	if err := (Query{AddressBook: m.AddressBook}).Validate(); err != nil {
		return err
	}
	if m.Action != "create" && m.Action != "edit" && m.Action != "delete" {
		return fault.New("usage", "unknown contact mutation")
	}
	if m.Action != "create" && (m.Path == "" || !validText(m.Path, 8192)) {
		return fault.New("usage", "contact path is required")
	}
	if m.Action == "create" && m.Path != "" {
		return fault.New("usage", "create does not accept an existing contact path")
	}
	if m.Action != "delete" {
		return m.Changes.Validate(m.Action == "create")
	}
	return nil
}
func Preview(m Mutation) (map[string]any, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return map[string]any{"dry_run": true, "validation": "local", "remote_checked": false, "request": m}, nil
}
