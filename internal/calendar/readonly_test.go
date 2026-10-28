package calendar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadOnlyTransport(t *testing.T) {
	calls := 0
	transport := ReadOnlyTransport{Base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	for _, method := range []string{"GET", "HEAD", "OPTIONS", "PROPFIND", "REPORT"} {
		req, _ := http.NewRequest(method, "https://caldav.icloud.com/", nil)
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if calls != 5 {
		t.Fatalf("read calls %d", calls)
	}
	for _, method := range []string{"PUT", "DELETE", "POST", "PATCH", "PROPPATCH", "MKCALENDAR", "MOVE", "COPY", "UNKNOWN"} {
		req, _ := http.NewRequest(method, "https://caldav.icloud.com/", nil)
		if _, err := transport.RoundTrip(req); err == nil {
			t.Errorf("accepted %s", method)
		}
	}
	req, _ := http.NewRequest("GET", "https://caldav.icloud.com/", nil)
	req.Header.Set("X-HTTP-Method-Override", "DELETE")
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("override allowed")
	}
	if calls != 5 {
		t.Fatal("write reached network")
	}
}
func TestReadOnlyRechecksRedirects(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Location", "/target")
		w.WriteHeader(302)
	}))
	defer server.Close()
	client := server.Client()
	client.Transport = ReadOnlyTransport{Base: client.Transport}
	client.CheckRedirect = func(r *http.Request, _ []*http.Request) error { r.Method = "DELETE"; return nil }
	if _, err := client.Get(server.URL); err == nil {
		t.Fatal("redirected write allowed")
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
}
