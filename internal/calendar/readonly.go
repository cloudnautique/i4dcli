package calendar

import (
	"i4dcli/internal/fault"
	"net/http"
)

// ReadOnlyTransport rejects writes before dispatch, including redirected requests.
// CalDAV discovery and queries use PROPFIND and REPORT rather than just GET.
type ReadOnlyTransport struct{ Base http.RoundTripper }

func (t ReadOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND", "REPORT":
	default:
		return nil, fault.New("permission_denied", "request %s blocked by read-only policy", req.Method)
	}
	for _, name := range []string{"X-HTTP-Method-Override", "X-Method-Override", "X-HTTP-Method"} {
		if req.Header.Get(name) != "" {
			return nil, fault.New("permission_denied", "method override blocked by read-only policy")
		}
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
func (t ReadOnlyTransport) CloseIdleConnections() {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if closer, ok := base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
