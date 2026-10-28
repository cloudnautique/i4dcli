package fault

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{
		{HTTP(http.StatusUnauthorized), 4}, {HTTP(http.StatusForbidden), 6}, {HTTP(http.StatusNotFound), 5}, {HTTP(http.StatusPreconditionFailed), 9}, {HTTP(http.StatusTooManyRequests), 7}, {HTTP(http.StatusServiceUnavailable), 8}, {context.Canceled, 130}, {context.DeadlineExceeded, 8}, {fmt.Errorf("generic"), 1}, {Usage(fmt.Errorf("bad flags")), 2},
	} {
		if got := ExitCode(fmt.Errorf("wrapped: %w", tc.err)); got != tc.code {
			t.Errorf("%v: %d", tc.err, got)
		}
	}
}
