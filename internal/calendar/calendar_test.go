package calendar

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDiscovery(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "PROPFIND" {
					t.Errorf("method = %s", r.Method)
				}
				var props, href string
				switch r.URL.Path {
				case "/":
					href = "/"
					props = `<d:current-user-principal><d:href>/account/principal/</d:href></d:current-user-principal>`
				case "/account/principal/":
					href = r.URL.Path
					props = `<c:calendar-home-set><d:href>/account/calendars/</d:href></c:calendar-home-set>`
				case "/account/calendars/":
					if r.Header.Get("Depth") != "1" {
						t.Error("missing Depth: 1")
					}
					href = "/account/calendars/work/"
					props = `<d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Work &amp; Home</d:displayname><c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>`
					if empty {
						props = `<d:resourcetype><d:collection/></d:resourcetype>`
					}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(207)
				fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>%s</d:href><d:propstat><d:prop>%s</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, href, props)
			}))
			defer server.Close()
			result, err := List(context.Background(), server.Client(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 3 {
				t.Fatalf("calls=%d", calls)
			}
			if empty {
				if result == nil || len(result) != 0 {
					t.Fatalf("want nonnil empty slice: %#v", result)
				}
				return
			}
			if len(result) != 1 || result[0].Name != "Work & Home" || result[0].Path != "/account/calendars/work/" {
				t.Fatalf("result=%#v", result)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCredentialsAndRedirect(t *testing.T) {
	client := NewClient("test-user", "test-password")
	count := 0
	client.Transport = authTransport{roundTripFunc(func(r *http.Request) (*http.Response, error) {
		count++
		user, pass, ok := r.BasicAuth()
		if !ok || user != "test-user" || pass != "test-password" {
			t.Error("missing credentials")
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method != "PROPFIND" || string(body) != "discovery" || r.Header.Get("Depth") != "0" {
			t.Error("redirect changed request")
		}
		if count == 1 {
			return &http.Response{StatusCode: 301, Header: http.Header{"Location": []string{"https://p42-caldav.icloud.com/principal/"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}), "test-user", "test-password"}
	req, _ := http.NewRequest("PROPFIND", Endpoint, strings.NewReader("discovery"))
	req.Header.Set("Depth", "0")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if count != 2 {
		t.Fatalf("count=%d", count)
	}
}

func TestEndpointRestriction(t *testing.T) {
	for _, raw := range []string{"http://caldav.icloud.com/", "https://caldav.icloud.com.evil.test/", "https://evil.test/", "https://p42-caldav.icloud.com:8443/", "https://user@caldav.icloud.com/"} {
		u, _ := url.Parse(raw)
		if trusted(u) {
			t.Errorf("trusted %s", raw)
		}
		transport := authTransport{roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("credentials could escape"); return nil, nil }), "user", "secret"}
		req, _ := http.NewRequest("PROPFIND", raw, nil)
		if _, err := transport.RoundTrip(req); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestMalformedAndTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(207); fmt.Fprint(w, "not xml") }))
	defer server.Close()
	if _, err := List(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("accepted malformed XML")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err := List(ctx, server.Client(), server.URL); err == nil {
		t.Fatal("ignored deadline")
	}
}
