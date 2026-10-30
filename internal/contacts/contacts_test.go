package contacts

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"i4dcli/internal/fault"
)

type fixture struct {
	mu       sync.Mutex
	objects  map[string]string
	etag     string
	conflict bool
	writes   int
	requests []string
}

func newFixture(t *testing.T) (*Client, *fixture) {
	t.Helper()
	f := &fixture{objects: map[string]string{}, etag: `"v1"`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		switch r.Method {
		case "PROPFIND":
			href, props := "/", `<d:current-user-principal><d:href>/principal/</d:href></d:current-user-principal>`
			switch r.URL.Path {
			case "/principal/":
				href, props = r.URL.Path, `<c:addressbook-home-set><d:href>/books/</d:href></c:addressbook-home-set>`
			case "/books/":
				href, props = "/books/personal/", `<d:resourcetype><d:collection/><c:addressbook/></d:resourcetype><d:displayname>Personal</d:displayname>`
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav"><d:response><d:href>%s</d:href><d:propstat><d:prop>%s</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, href, props)
		case "REPORT":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "nresults") {
				t.Error("missing server query bound")
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">`)
			for path, data := range f.objects {
				fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop><c:address-data>`, path)
				_ = xml.EscapeText(w, []byte(data))
				fmt.Fprint(w, `</c:address-data><d:getetag>&quot;v1&quot;</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
			}
			fmt.Fprint(w, `</d:multistatus>`)
		case "GET":
			data, ok := f.objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/vcard")
			w.Header().Set("ETag", f.etag)
			fmt.Fprint(w, data)
		case "PUT":
			f.writes++
			if f.conflict {
				w.WriteHeader(412)
				return
			}
			if _, exists := f.objects[r.URL.Path]; exists {
				if r.Header.Get("If-Match") != f.etag {
					t.Errorf("missing edit precondition %q", r.Header.Get("If-Match"))
				}
			} else if r.Header.Get("If-None-Match") != "*" {
				t.Error("missing create precondition")
			}
			data, _ := io.ReadAll(r.Body)
			f.objects[r.URL.Path] = string(data)
			w.WriteHeader(201)
		case "DELETE":
			f.writes++
			if r.Header.Get("If-Match") != f.etag {
				t.Error("missing delete precondition")
			}
			delete(f.objects, r.URL.Path)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.Client(), srv.URL, false), f
}
func str(s string) *string { return &s }
func TestLifecyclePreservesPropertiesAndGuardsWrites(t *testing.T) {
	c, f := newFixture(t)
	ctx := context.Background()
	books, err := c.AddressBooks(ctx)
	if err != nil || len(books) != 1 || books[0].Name != "Personal" {
		t.Fatalf("%+v %v", books, err)
	}
	created, err := c.Mutate(ctx, Mutation{Action: "create", AddressBook: "personal", Changes: Changes{Name: str("Ada"), Emails: &[]string{"ada@example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.objects[created.Path] = strings.Replace(f.objects[created.Path], "END:VCARD", "item1.EMAIL;TYPE=WORK:work@example.com\r\nitem1.X-ABLabel:Custom\r\nX-CUSTOM:untouched\r\nPHOTO;ENCODING=b:YWJj\r\nEND:VCARD", 1)
	f.mu.Unlock()
	got, err := c.Get(ctx, "Personal", created.Path)
	if err != nil || got.Name != "Ada" || got.Properties.Value("X-CUSTOM") != "untouched" {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = c.Mutate(ctx, Mutation{Action: "edit", AddressBook: "Personal", Path: created.Path, Changes: Changes{Name: str("Ada Lovelace"), Note: str("Notes\nwith lines")}})
	if err != nil {
		t.Fatal(err)
	}
	got, err = c.Get(ctx, "Personal", created.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ada Lovelace" || len(got.Emails) != 2 || got.Properties.Value("X-CUSTOM") != "untouched" || got.Properties.Value("PHOTO") != "YWJj" || got.Properties.Get("X-ABLABEL") == nil {
		t.Fatalf("fields lost: %+v", got)
	}
	_, err = c.Mutate(ctx, Mutation{Action: "edit", AddressBook: "Personal", Path: created.Path, Changes: Changes{Emails: &[]string{}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err = c.Get(ctx, "Personal", created.Path)
	if err != nil || len(got.Emails) != 0 {
		t.Fatalf("clear failed %+v %v", got, err)
	}
	f.mu.Lock()
	f.conflict = true
	f.mu.Unlock()
	if _, err = c.Mutate(ctx, Mutation{Action: "edit", AddressBook: "Personal", Path: created.Path, Changes: Changes{Name: str("Collision")}}); fault.ExitCode(err) != 9 {
		t.Fatalf("conflict: %v", err)
	}
	f.mu.Lock()
	f.conflict = false
	f.mu.Unlock()
	if _, err = c.Mutate(ctx, Mutation{Action: "delete", AddressBook: "Personal", Path: created.Path}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Get(ctx, "Personal", created.Path); fault.ExitCode(err) != 5 {
		t.Fatalf("missing: %v", err)
	}
}
func TestQueryBoundAndSort(t *testing.T) {
	c, _ := newFixture(t)
	ctx := context.Background()
	for _, name := range []string{"Zed", "Ada", "Bob"} {
		if _, err := c.Mutate(ctx, Mutation{Action: "create", AddressBook: "Personal", Changes: Changes{Name: str(name)}}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.List(ctx, Query{AddressBook: "Personal", Limit: 2})
	if err != nil || !page.Truncated || page.Returned != 2 || page.Contacts[0].Name != "Ada" {
		t.Fatalf("%+v %v", page, err)
	}
}
func TestRejectUnsafePathsAndWeakETags(t *testing.T) {
	c, f := newFixture(t)
	ctx := context.Background()
	created, err := c.Mutate(ctx, Mutation{Action: "create", AddressBook: "Personal", Changes: Changes{Name: str("Ada")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range []string{"../outside.vcf", "/books/elsewhere/a.vcf", "https://example.com/steal", "/books/personal/%2e%2e/outside.vcf", created.Path + "?x=1", created.Path + "#fragment"} {
		if _, err := c.Mutate(ctx, Mutation{Action: "delete", AddressBook: "Personal", Path: object}); err == nil {
			t.Fatalf("accepted %s", object)
		}
	}
	for _, etag := range []string{"", `W/"v1"`, "unquoted"} {
		f.mu.Lock()
		f.etag = etag
		before := f.writes
		f.mu.Unlock()
		_, err := c.Mutate(ctx, Mutation{Action: "delete", AddressBook: "Personal", Path: created.Path})
		if fault.ExitCode(err) != 9 {
			t.Fatalf("%q: %v", etag, err)
		}
		f.mu.Lock()
		if f.writes != before {
			t.Error("weak ETag wrote")
		}
		f.mu.Unlock()
	}
	c.readonly = true
	f.mu.Lock()
	before := len(f.requests)
	f.mu.Unlock()
	if _, err := c.Mutate(ctx, Mutation{Action: "delete", AddressBook: "Personal", Path: created.Path}); fault.ExitCode(err) != 6 {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != before {
		t.Fatal("readonly write touched network")
	}
}
func TestTrustedEndpoints(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{{Endpoint, true}, {"https://p42-contacts.icloud.com/a", true}, {"http://contacts.icloud.com", false}, {"https://contacts.icloud.com.evil.test", false}, {"https://alice@contacts.icloud.com", false}, {"https://contacts.icloud.com:444", false}, {"https://px-contacts.icloud.com", false}} {
		u, _ := url.Parse(tc.url)
		if trusted(u) != tc.want {
			t.Errorf("%s", tc.url)
		}
	}
}
func TestLocalValidationAndPreview(t *testing.T) {
	for _, change := range []Changes{{}, {Name: str("")}, {Name: str("bad\rname")}, {Name: str("Ada"), Emails: &[]string{"bad"}}, {Name: str("Ada"), Birthday: str("tomorrow")}} {
		if _, err := Preview(Mutation{Action: "create", AddressBook: "Personal", Changes: change}); err == nil {
			t.Fatalf("accepted %+v", change)
		}
	}
	plan, err := Preview(Mutation{Action: "create", AddressBook: "Personal", Changes: Changes{Name: str("Ada")}})
	if err != nil || plan["remote_checked"] != false {
		t.Fatalf("%v %v", plan, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCredentialRedirectBoundaryAndWriteRedirect(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	for _, tc := range []struct {
		method, target string
		wantCalls      int
		wantError      bool
	}{
		{"PROPFIND", "https://p42-contacts.icloud.com/principal", 2, false},
		{"PROPFIND", "https://example.com/steal", 1, true},
		{"PUT", "https://p42-contacts.icloud.com/other", 1, true},
	} {
		calls := 0
		http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			user, pass, ok := r.BasicAuth()
			if !ok || user != "test-user" || pass != "test-password" {
				t.Error("missing authorized credentials")
			}
			if r.Method != tc.method {
				t.Error("redirect changed method")
			}
			header := http.Header{}
			if calls == 1 {
				header.Set("Location", tc.target)
				return &http.Response{StatusCode: 302, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			}
			return &http.Response{StatusCode: 207, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})
		c := NewClient("test-user", "test-password", false)
		req, _ := http.NewRequest(tc.method, Endpoint, strings.NewReader("request"))
		response, err := c.http.Do(req)
		if response != nil {
			response.Body.Close()
		}
		if (err != nil) != tc.wantError || calls != tc.wantCalls {
			t.Fatalf("%+v: calls=%d err=%v", tc, calls, err)
		}
	}
}
func TestCardAndWireBounds(t *testing.T) {
	c, f := newFixture(t)
	ctx := context.Background()
	f.objects["/books/personal/big.vcf"] = strings.Repeat("x", maxCard+1)
	if _, err := c.Get(ctx, "Personal", "/books/personal/big.vcf"); err == nil {
		t.Fatal("oversized card accepted")
	}
	body := &boundedBody{ReadCloser: io.NopCloser(strings.NewReader("12345")), remaining: 4}
	if _, err := io.ReadAll(body); err == nil {
		t.Fatal("oversized response accepted")
	}
}
