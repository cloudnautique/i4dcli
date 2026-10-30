// Package fault defines stable errors shared by CLI and MCP clients.
package fault

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

var Codes = map[string]int{"error": 1, "usage": 2, "auth_required": 4, "not_found": 5, "permission_denied": 6, "rate_limited": 7, "retryable": 8, "conflict": 9, "config": 10, "confirmation_required": 11, "delivery_unknown": 12, "cancelled": 130}

type Error struct {
	Code    int    `json:"code"`
	Class   string `json:"class"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }
func New(class, format string, args ...any) error {
	return &Error{Code: Codes[class], Class: class, Message: fmt.Sprintf(format, args...)}
}
func Describe(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		copy := *e
		copy.Message = err.Error()
		return &copy
	}
	class := "error"
	var network net.Error
	if errors.Is(err, context.Canceled) {
		class = "cancelled"
	} else if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) {
		class = "retryable"
	}
	return &Error{Code: Codes[class], Class: class, Message: err.Error()}
}
func Usage(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	return New("usage", "%s", err)
}
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	return Describe(err).Code
}
func HTTP(status int) error {
	class := "error"
	switch status {
	case http.StatusUnauthorized:
		class = "auth_required"
	case http.StatusForbidden:
		class = "permission_denied"
	case http.StatusNotFound:
		class = "not_found"
	case http.StatusConflict, http.StatusPreconditionFailed:
		class = "conflict"
	case http.StatusTooManyRequests:
		class = "rate_limited"
	default:
		if status >= 500 {
			class = "retryable"
		}
	}
	if class == "conflict" {
		return New(class, "event changed on the server or already exists; list events again before retrying (HTTP %d)", status)
	}
	return New(class, "calendar request failed: HTTP %d %s", status, http.StatusText(status))
}

// Transport classifies HTTP failures before a protocol library loses their codes.
type Transport struct{ Base http.RoundTripper }

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, HTTP(resp.StatusCode)
	}
	return resp, nil
}
func (t Transport) CloseIdleConnections() {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if c, ok := base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
