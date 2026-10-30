package contacts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/emersion/go-webdav/carddav"
	"i4dcli/internal/fault"
)

const Endpoint = "https://contacts.icloud.com/"
const maxResponse = 16 << 20

type Client struct {
	http     *http.Client
	endpoint string
	readonly bool
	mu       sync.Mutex
	origin   string
}

func trusted(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "contacts.icloud.com" {
		return true
	}
	shard, ok := strings.CutSuffix(host, "-contacts.icloud.com")
	if !ok || len(shard) < 2 || shard[0] != 'p' {
		return false
	}
	for _, r := range shard[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

type transport struct {
	base               http.RoundTripper
	username, password string
	readonly           bool
}

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !trusted(r.URL) {
		return nil, fault.New("permission_denied", "refusing untrusted CardDAV endpoint")
	}
	if t.readonly && r.Method != "GET" && r.Method != "PROPFIND" && r.Method != "REPORT" && r.Method != "OPTIONS" {
		return nil, fault.New("permission_denied", "CardDAV writes blocked by readonly transport")
	}
	copy := r.Clone(r.Context())
	copy.SetBasicAuth(t.username, t.password)
	return t.base.RoundTrip(copy)
}
func (t transport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
func NewClient(username, password string, readonly bool) *Client {
	client := &http.Client{Transport: transport{http.DefaultTransport, username, password, readonly}}
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many CardDAV redirects")
		}
		if !trusted(r.URL) {
			return fault.New("permission_denied", "refusing untrusted CardDAV redirect")
		}
		original := via[0]
		if original.Method == "PUT" || original.Method == "DELETE" {
			return fault.New("conflict", "CardDAV write redirected; rediscover the address book before retrying")
		}
		r.Method = original.Method
		r.Header = original.Header.Clone()
		if original.GetBody != nil {
			body, err := original.GetBody()
			if err != nil {
				return err
			}
			r.Body = body
			r.GetBody = original.GetBody
			r.ContentLength = original.ContentLength
		}
		return nil
	}
	return newClient(client, Endpoint, readonly)
}
func newClient(client *http.Client, endpoint string, readonly bool) *Client {
	copy := *client
	c := &Client{http: &copy, endpoint: endpoint, readonly: readonly}
	base := copy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copy.Transport = boundedTransport{base: base, client: c}
	return c
}
func (c *Client) Close() error { c.http.CloseIdleConnections(); return nil }

type boundedTransport struct {
	base   http.RoundTripper
	client *Client
}

func (t boundedTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		response.Body.Close()
		return nil, fault.HTTP(response.StatusCode)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		t.client.origin = r.URL.Scheme + "://" + r.URL.Host + "/"
	}
	response.Body = &boundedBody{ReadCloser: response.Body, remaining: maxResponse}
	return response, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			_ = b.Close()
			return 0, fault.New("error", "CardDAV response exceeds 16 MiB")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}
func (c *Client) addressBooks(ctx context.Context) ([]AddressBook, error) {
	dav, err := carddav.NewClient(c.http, c.endpoint)
	if err != nil {
		return nil, err
	}
	principal, err := dav.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if principal == "" {
		return nil, fault.New("error", "CardDAV principal is empty")
	}
	// Keep the shard discovered via a verified redirect for conditional writes.
	if c.origin != "" {
		c.endpoint = c.origin
		dav, err = carddav.NewClient(c.http, c.endpoint)
		if err != nil {
			return nil, err
		}
	}
	home, err := dav.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return nil, err
	}
	if home == "" {
		return nil, fault.New("error", "CardDAV home is empty")
	}
	books, err := dav.FindAddressBooks(ctx, home)
	if err != nil {
		return nil, err
	}
	if len(books) > 1000 {
		return nil, fault.New("error", "address book count exceeds 1000")
	}
	if c.origin != "" {
		c.endpoint = c.origin
	}
	result := []AddressBook{}
	for _, b := range books {
		result = append(result, AddressBook{b.Name, b.Path, b.Description})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Path < result[j].Path
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}
func (c *Client) AddressBooks(ctx context.Context) ([]AddressBook, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.addressBooks(ctx)
}
func (c *Client) selected(ctx context.Context, name string) (AddressBook, error) {
	books, err := c.addressBooks(ctx)
	if err != nil {
		return AddressBook{}, err
	}
	for _, b := range books {
		if b.Path == name {
			return b, nil
		}
	}
	matches := []AddressBook{}
	for _, b := range books {
		if strings.EqualFold(b.Name, name) {
			matches = append(matches, b)
		}
	}
	if len(matches) == 0 {
		return AddressBook{}, fault.New("not_found", "address book not found")
	}
	if len(matches) > 1 {
		return AddressBook{}, fault.New("usage", "address book name is ambiguous; use its path")
	}
	return matches[0], nil
}
