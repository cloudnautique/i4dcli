package contacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
	"i4dcli/internal/fault"
)

const maxCard = 1 << 20

func contact(card vcard.Card, object, etag string, detail bool) Contact {
	c := Contact{Path: object, ETag: etag, UID: card.Value("UID"), Name: card.Value("FN"), Emails: card.Values("EMAIL"), Phones: card.Values("TEL"), Organization: card.Value("ORG"), Note: card.Value("NOTE"), Birthday: card.Value("BDAY"), URLs: card.Values("URL")}
	if c.Emails == nil {
		c.Emails = []string{}
	}
	if c.Phones == nil {
		c.Phones = []string{}
	}
	if name := card.Name(); name != nil {
		c.GivenName = name.GivenName
		c.FamilyName = name.FamilyName
	}
	if detail {
		c.Properties = card
	}
	return c
}
func (c *Client) List(ctx context.Context, q Query) (Page, error) {
	if err := q.Validate(); err != nil {
		return Page{}, err
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	book, err := c.selected(ctx, q.AddressBook)
	if err != nil {
		return Page{}, err
	}
	dav, err := carddav.NewClient(c.http, c.endpoint)
	if err != nil {
		return Page{}, err
	}
	query := &carddav.AddressBookQuery{Limit: q.Limit + 1, DataRequest: carddav.AddressDataRequest{Props: []string{"VERSION", "UID", "FN", "N", "EMAIL", "TEL", "ORG", "BDAY", "URL"}}}
	if q.Search != "" {
		for _, key := range []string{"FN", "EMAIL", "TEL", "ORG"} {
			query.PropFilters = append(query.PropFilters, carddav.PropFilter{Name: key, TextMatches: []carddav.TextMatch{{Text: q.Search}}})
		}
	}
	objects, err := dav.QueryAddressBook(ctx, book.Path, query)
	if err != nil {
		return Page{}, err
	}
	result := Page{Contacts: []Contact{}, Truncated: len(objects) > q.Limit}
	for _, obj := range objects {
		if _, err := c.objectURL(book, obj.Path); err != nil {
			return Page{}, err
		}
		result.Contacts = append(result.Contacts, contact(obj.Card, obj.Path, obj.ETag, false))
	}
	sort.Slice(result.Contacts, func(i, j int) bool {
		if result.Contacts[i].Name == result.Contacts[j].Name {
			return result.Contacts[i].Path < result.Contacts[j].Path
		}
		return result.Contacts[i].Name < result.Contacts[j].Name
	})
	if len(result.Contacts) > q.Limit {
		result.Contacts = result.Contacts[:q.Limit]
	}
	result.Returned = len(result.Contacts)
	return result, nil
}
func (c *Client) objectURL(book AddressBook, object string) (*url.URL, error) {
	base, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, err
	}
	collection, err := base.Parse(book.Path)
	if err != nil {
		return nil, err
	}
	if collection.Scheme != base.Scheme || collection.Host != base.Host || collection.User != nil || collection.RawQuery != "" || collection.Fragment != "" {
		return nil, fault.New("error", "untrusted address book path")
	}
	collection.Path = strings.TrimSuffix(collection.Path, "/") + "/"
	collection.RawPath = ""
	target, err := collection.Parse(object)
	if err != nil {
		return nil, err
	}
	if target.Scheme != collection.Scheme || target.Host != collection.Host || target.User != nil || target.RawQuery != "" || target.Fragment != "" || strings.HasSuffix(target.Path, "/") || path.Dir(target.Path) != strings.TrimSuffix(collection.Path, "/") || path.Clean(target.Path) != target.Path {
		return nil, fault.New("usage", "contact path must be directly inside the selected address book")
	}
	return target, nil
}
func (c *Client) read(ctx context.Context, book AddressBook, object string) (vcard.Card, string, *url.URL, []byte, error) {
	target, err := c.objectURL(book, object)
	if err != nil {
		return nil, "", nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, "", nil, nil, err
	}
	req.Header.Set("Accept", "text/vcard")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, "", nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, "", nil, nil, fault.New("error", "unexpected contact read HTTP status")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCard+1))
	if err != nil {
		return nil, "", nil, nil, err
	}
	if len(raw) > maxCard {
		return nil, "", nil, nil, fault.New("error", "contact exceeds 1 MiB")
	}
	decoder := vcard.NewDecoder(bytes.NewReader(raw))
	card, err := decoder.Decode()
	if err != nil {
		return nil, "", nil, nil, fault.New("error", "invalid contact vCard")
	}
	if _, err := decoder.Decode(); err != io.EOF {
		return nil, "", nil, nil, fault.New("error", "contact must contain exactly one vCard")
	}
	if card.Value("VERSION") != "3.0" && card.Value("VERSION") != "4.0" {
		return nil, "", nil, nil, fault.New("error", "unsupported vCard version")
	}
	return card, response.Header.Get("ETag"), target, raw, nil
}
func (c *Client) Get(ctx context.Context, bookName, object string) (Contact, error) {
	if err := (Query{AddressBook: bookName}).Validate(); err != nil {
		return Contact{}, err
	}
	if object == "" || !validText(object, 8192) {
		return Contact{}, fault.New("usage", "contact path required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	book, err := c.selected(ctx, bookName)
	if err != nil {
		return Contact{}, err
	}
	card, etag, target, _, err := c.read(ctx, book, object)
	if err != nil {
		return Contact{}, err
	}
	return contact(card, target.EscapedPath(), etag, true), nil
}
func strongETag(s string) bool {
	return len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' && !strings.ContainsAny(s[1:len(s)-1], "\"\r\n")
}
func (c Changes) apply(card vcard.Card) {
	for key, value := range map[string]*string{"FN": c.Name, "ORG": c.Organization, "NOTE": c.Note, "BDAY": c.Birthday} {
		if value != nil {
			if *value == "" {
				delete(card, key)
			} else {
				card.SetValue(key, *value)
			}
		}
	}
	if c.GivenName != nil || c.FamilyName != nil {
		n := card.Name()
		if n == nil {
			n = &vcard.Name{}
		}
		if c.GivenName != nil {
			n.GivenName = *c.GivenName
		}
		if c.FamilyName != nil {
			n.FamilyName = *c.FamilyName
		}
		// Name embeds the original Field, including parameters and group.
		// Rebuild this edited property while keeping omitted name components.
		n.Field = nil
		card.SetName(n)
	}
	for key, values := range map[string]*[]string{"EMAIL": c.Emails, "TEL": c.Phones, "URL": c.URLs} {
		if values != nil {
			delete(card, key)
			for _, s := range *values {
				card.AddValue(key, s)
			}
		}
	}
	card.SetRevision(time.Now().UTC())
}
func (c *Client) Mutate(ctx context.Context, m Mutation) (Result, error) {
	if c.readonly {
		return Result{}, fault.New("permission_denied", "contacts client is readonly")
	}
	if err := m.Validate(); err != nil {
		return Result{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	book, err := c.selected(ctx, m.AddressBook)
	if err != nil {
		return Result{}, err
	}
	card := vcard.Card{}
	etag := "*"
	var target *url.URL
	var original []byte
	if m.Action == "create" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return Result{}, err
		}
		uid := fmt.Sprintf("%x", id)
		card.SetValue("VERSION", "3.0")
		card.SetValue("UID", uid)
		card.SetValue("FN", *m.Changes.Name)
		card.SetName(&vcard.Name{})
		target, err = c.objectURL(book, strings.TrimSuffix(book.Path, "/")+"/"+uid+".vcf")
	} else {
		card, etag, target, original, err = c.read(ctx, book, m.Path)
		if err == nil && !strongETag(etag) {
			err = fault.New("conflict", "contact edit/delete requires a strong ETag")
		}
	}
	if err != nil {
		return Result{}, err
	}
	method := http.MethodDelete
	var data bytes.Buffer
	if m.Action != "delete" {
		method = http.MethodPut
		m.Changes.apply(card)
		if err := encodeContact(&data, card, original, m.Changes); err != nil {
			return Result{}, err
		}
		if data.Len() > maxCard {
			return Result{}, fault.New("usage", "contact exceeds 1 MiB")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(data.Bytes()))
	if err != nil {
		return Result{}, err
	}
	if m.Action == "create" {
		req.Header.Set("If-None-Match", "*")
	} else {
		req.Header.Set("If-Match", etag)
	}
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "text/vcard; charset=utf-8")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{}, fault.New("error", "unexpected CardDAV write HTTP status")
	}
	return Result{Action: m.Action, Path: target.EscapedPath()}, nil
}
