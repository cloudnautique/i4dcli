package mail

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"i4dcli/internal/fault"
)

const host = "imap.mail.me.com"
const maxWireBytes = 32 << 20

// Client requires explicit write authorization for mutations. Instances serialize operations and have a
// bounded lifetime ingress budget; callers normally use one session per request.
type Client struct {
	imap       *imapclient.Client
	conn       net.Conn
	account    string
	allowWrite bool
	mu         sync.Mutex
}

// Dial connects only to Apple's IMAP endpoint using verified TLS. AccountID is a
// non-secret stable identity for the selected account, bound into IDs/cursors.
func Dial(ctx context.Context, o Options) (*Client, error) {
	if o.Username == "" || o.Password == "" {
		return nil, fault.New("config", "mail username and app-specific password are required")
	}
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 30 * time.Second}, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "993"))
	if err != nil {
		return nil, err
	}
	return newClient(ctx, conn, o)
}

// The injected connection is package-private: production callers cannot redirect
// credentials through configuration or tool inputs.
func newClient(ctx context.Context, conn net.Conn, o Options) (*Client, error) {
	key := sha256.Sum256([]byte(host + "\x00" + o.AccountID + "\x00" + o.Username))
	bounded := &budgetConn{Conn: conn, remaining: maxWireBytes}
	c := &Client{imap: imapclient.New(bounded, nil), conn: bounded, account: hex.EncodeToString(key[:]), allowWrite: o.AllowWrite}
	ctx, end := c.operation(ctx)
	defer end()
	if err := c.imap.Login(o.Username, o.Password).Wait(); err != nil {
		c.Close()
		return nil, mailError(ctx, err, "authenticate")
	}
	return c, nil
}

func (c *Client) Close() error { return c.imap.Close() }
func (c *Client) operation(ctx context.Context) (context.Context, func()) {
	c.mu.Lock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
	return ctx, func() { stop(); cancel(); c.mu.Unlock() }
}

type budgetConn struct {
	net.Conn
	remaining int64
}

func (c *budgetConn) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		_ = c.Conn.Close()
		return 0, fault.New("error", "IMAP response exceeds session byte limit")
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.Conn.Read(p)
	c.remaining -= int64(n)
	return n, err
}

func mailError(ctx context.Context, err error, action string) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var status *imap.Error
	if errors.As(err, &status) {
		class := "error"
		switch status.Code {
		case imap.ResponseCodeAuthenticationFailed, imap.ResponseCodeExpired:
			class = "auth_required"
		case imap.ResponseCodeAuthorizationFailed, imap.ResponseCodeNoPerm:
			class = "permission_denied"
		case imap.ResponseCodeNonExistent:
			class = "not_found"
		case imap.ResponseCodeUnavailable, imap.ResponseCodeInUse:
			class = "retryable"
		}
		if action == "authenticate" && status.Type == imap.StatusResponseTypeNo {
			class = "auth_required"
		}
		return fault.New(class, "mail %s failed (%s %s)", action, status.Type, status.Code)
	}
	if errors.Is(err, io.EOF) {
		return fault.New("retryable", "mail connection closed during %s", action)
	}
	return err
}
