package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/smtp"
	"net/textproto"
	"time"

	"i4dcli/internal/fault"
)

const smtpHost = "smtp.mail.me.com"

type SMTPOptions struct{ Username, Password string }
type SendResult struct {
	Accepted  bool   `json:"accepted"`
	MessageID string `json:"message_id"`
	SentCopy  string `json:"sent_copy"`
	Note      string `json:"note"`
}

// Send submits exactly once. Acceptance is SMTP acceptance, not inbox delivery.
// No Sent copy is appended because iCloud's submission-side behavior must first
// be verified against an account; callers must not resend to recover a Sent copy.
func Send(ctx context.Context, o SMTPOptions, p PreparedMessage) (SendResult, error) {
	var empty SendResult
	if err := validatePrepared(p); err != nil {
		return empty, err
	}
	if o.Username == "" || o.Password == "" {
		return empty, fault.New("config", "SMTP username and app-specific password are required")
	}
	d := net.Dialer{Timeout: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(smtpHost, "587"))
	if err != nil {
		return empty, err
	}
	return sendConnection(ctx, conn, smtpHost, o, p, &tls.Config{ServerName: smtpHost, MinVersion: tls.VersionTLS12})
}
func validatePrepared(p PreparedMessage) error {
	if _, err := address(p.From); err != nil {
		return err
	}
	if len(p.Recipients) < 1 || len(p.Recipients) > 100 || len(p.Data) == 0 || len(p.Data) > 16<<20 {
		return fault.New("usage", "invalid outgoing message size or recipient count")
	}
	for _, r := range p.Recipients {
		if _, err := address(r); err != nil {
			return err
		}
	}
	return nil
}
func sendConnection(ctx context.Context, conn net.Conn, host string, o SMTPOptions, p PreparedMessage, tlsConfig *tls.Config) (SendResult, error) {
	empty := SendResult{}
	defer conn.Close()
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return empty, smtpFailure(err, "connect")
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return empty, fault.New("permission_denied", "SMTP server does not offer required STARTTLS")
	}
	if err = c.StartTLS(tlsConfig); err != nil {
		return empty, smtpFailure(err, "establish TLS")
	}
	if err = c.Auth(smtp.PlainAuth("", o.Username, o.Password, host)); err != nil {
		return empty, smtpFailure(err, "authenticate")
	}
	if err = c.Mail(p.From); err != nil {
		return empty, smtpFailure(err, "set sender")
	}
	for _, recipient := range p.Recipients {
		if err = c.Rcpt(recipient); err != nil {
			_ = c.Reset()
			return empty, smtpFailure(err, "accept recipients; nothing submitted")
		}
	}
	w, err := c.Data()
	if err != nil {
		return empty, smtpFailure(err, "begin message")
	}
	if _, err = w.Write(p.Data); err != nil {
		return empty, fault.New("delivery_unknown", "connection failed during SMTP message transfer; inspect mail before retrying")
	}
	if err = w.Close(); err != nil {
		var status *textproto.Error
		if errors.As(err, &status) {
			return empty, smtpFailure(err, "accept message")
		}
		return empty, fault.New("delivery_unknown", "SMTP acceptance response was lost; message may have been sent; do not automatically retry")
	}
	// QUIT failure cannot turn an acknowledged submission into a failed send.
	_ = c.Quit()
	return SendResult{Accepted: true, MessageID: p.MessageID, SentCopy: "not_managed", Note: "SMTP accepted the message; delivery is not confirmed. Sent-folder behavior is managed by the provider; no extra copy was appended."}, nil
}
func smtpFailure(err error, stage string) error {
	var status *textproto.Error
	if errors.As(err, &status) {
		class := "error"
		if stage == "authenticate" {
			class = "auth_required"
		} else if status.Code >= 400 && status.Code < 500 {
			class = "retryable"
		}
		return fault.New(class, "SMTP %s failed (status %d)", stage, status.Code)
	}
	return err
}
