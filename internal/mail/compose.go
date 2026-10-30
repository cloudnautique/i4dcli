package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"net/textproto"
	"regexp"
	"strings"
	"time"

	"i4dcli/internal/fault"
)

const MaxOutgoingBytes = 10 << 20

type OutgoingAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	Data        []byte `json:"data"`
}
type Compose struct {
	From        string               `json:"from"`
	To          []string             `json:"to,omitempty"`
	Cc          []string             `json:"cc,omitempty"`
	Bcc         []string             `json:"bcc,omitempty"`
	Subject     string               `json:"subject"`
	Text        string               `json:"text,omitempty"`
	HTML        string               `json:"html,omitempty"`
	InReplyTo   string               `json:"in_reply_to,omitempty"`
	References  []string             `json:"references,omitempty"`
	Attachments []OutgoingAttachment `json:"attachments,omitempty"`
}
type PreparedMessage struct {
	Data       []byte   `json:"-"`
	From       string   `json:"from"`
	Recipients []string `json:"recipients"`
	MessageID  string   `json:"message_id"`
}

var messageIDPattern = regexp.MustCompile(`^<[^<>\s@]+@[^<>\s@]+>$`)

func safeHeader(s string) bool { return !strings.ContainsAny(s, "\r\n\x00") }
func address(s string) (*netmail.Address, error) {
	if !safeHeader(s) || len(s) > 1024 {
		return nil, fault.New("usage", "invalid email address")
	}
	a, err := netmail.ParseAddress(s)
	if err != nil {
		return nil, fault.New("usage", "invalid email address")
	}
	if !strings.Contains(a.Address, "@") {
		return nil, fault.New("usage", "email address must contain a domain")
	}
	for _, r := range a.Address {
		if r > 127 || r < 33 {
			return nil, fault.New("usage", "SMTPUTF8 addresses are not supported")
		}
	}
	if len(a.String()) > 900 {
		return nil, fault.New("usage", "email address header is too long")
	}
	return a, nil
}
func (c Compose) Validate() error      { return c.validate(false) }
func (c Compose) ValidateDraft() error { return c.validate(true) }
func (c Compose) validate(draft bool) error {
	if _, err := address(c.From); err != nil {
		return err
	}
	n := len(c.To) + len(c.Cc) + len(c.Bcc)
	if (!draft && n < 1) || n > 100 {
		return fault.New("usage", "provide between 1 and 100 recipients")
	}
	for _, group := range [][]string{c.To, c.Cc, c.Bcc} {
		for _, s := range group {
			if _, err := address(s); err != nil {
				return err
			}
		}
	}
	if !safeHeader(c.Subject) || len(c.Subject) > 4096 {
		return fault.New("usage", "subject must be at most 4096 bytes without newlines")
	}
	for _, word := range strings.Fields(c.Subject) {
		if len(word) > 900 {
			return fault.New("usage", "subject contains an overlong word")
		}
	}
	if !draft && c.Text == "" && c.HTML == "" {
		return fault.New("usage", "message body is required")
	}
	if c.InReplyTo != "" && (len(c.InReplyTo) > 900 || !messageIDPattern.MatchString(c.InReplyTo)) {
		return fault.New("usage", "invalid reply message ID")
	}
	if len(c.References) > 100 {
		return fault.New("usage", "too many message references")
	}
	for _, id := range c.References {
		if len(id) > 900 || !messageIDPattern.MatchString(id) {
			return fault.New("usage", "invalid reference message ID")
		}
	}
	if len(c.Attachments) > 50 {
		return fault.New("usage", "at most 50 attachments are supported")
	}
	size := len(c.Text) + len(c.HTML)
	for _, a := range c.Attachments {
		if a.Filename == "" || len(a.Filename) > 255 || !safeHeader(a.Filename) || strings.ContainsAny(a.Filename, "/\\") {
			return fault.New("usage", "attachment requires a safe filename")
		}
		if a.ContentType != "" {
			t, _, err := mime.ParseMediaType(a.ContentType)
			if err != nil || !safeHeader(a.ContentType) || strings.HasPrefix(t, "multipart/") {
				return fault.New("usage", "invalid attachment content type")
			}
		}
		size += len(a.Data)
	}
	if size > MaxOutgoingBytes {
		return fault.New("usage", "outgoing message exceeds 10 MiB content limit")
	}
	return nil
}
func BuildMessage(c Compose) (PreparedMessage, error) { return buildMessage(c, false) }
func buildMessage(c Compose, draft bool) (PreparedMessage, error) {
	var result PreparedMessage
	if err := c.validate(draft); err != nil {
		return result, err
	}
	from, _ := address(c.From)
	result.From = from.Address
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from.String())
	seen := map[string]bool{}
	for _, g := range []struct {
		name   string
		values []string
	}{{"To", c.To}, {"Cc", c.Cc}, {"", c.Bcc}} {
		values := []string{}
		for _, s := range g.values {
			a, _ := address(s)
			values = append(values, a.String())
			key := a.Address
			if !seen[key] {
				result.Recipients = append(result.Recipients, key)
				seen[key] = true
			}
		}
		if g.name != "" && len(values) > 0 {
			fmt.Fprintf(&b, "%s: %s\r\n", g.name, strings.Join(values, ",\r\n "))
		}
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return result, err
	}
	result.MessageID = "<" + hex.EncodeToString(random) + "@" + strings.SplitN(from.Address, "@", 2)[1] + ">"
	fmt.Fprintf(&b, "Date: %s\r\nMessage-ID: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n", time.Now().Format(time.RFC1123Z), result.MessageID, foldHeader(mime.QEncoding.Encode("utf-8", c.Subject)))
	if c.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", c.InReplyTo)
	}
	if len(c.References) > 0 {
		fmt.Fprintf(&b, "References: %s\r\n", foldHeader(strings.Join(c.References, " ")))
	}
	if len(c.Attachments) > 0 {
		mixed := multipart.NewWriter(&b)
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mixed.Boundary())
		h := textproto.MIMEHeader{}
		if c.HTML != "" && c.Text != "" {
			var alt bytes.Buffer
			aw := multipart.NewWriter(&alt)
			h.Set("Content-Type", "multipart/alternative; boundary="+aw.Boundary())
			if err := writeTexts(aw, c); err != nil {
				return result, err
			}
			if err := aw.Close(); err != nil {
				return result, err
			}
			w, _ := mixed.CreatePart(h)
			_, _ = w.Write(alt.Bytes())
		} else {
			typ, body := "text/plain", c.Text
			if c.HTML != "" {
				typ, body = "text/html", c.HTML
			}
			if err := writeText(mixed, typ, body); err != nil {
				return result, err
			}
		}
		for _, a := range c.Attachments {
			h = textproto.MIMEHeader{}
			typ := a.ContentType
			if typ == "" {
				typ = "application/octet-stream"
			}
			h.Set("Content-Type", typ)
			h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename}))
			h.Set("Content-Transfer-Encoding", "base64")
			w, err := mixed.CreatePart(h)
			if err != nil {
				return result, err
			}
			encoded := base64.StdEncoding.EncodeToString(a.Data)
			for len(encoded) > 0 {
				n := min(76, len(encoded))
				fmt.Fprint(w, encoded[:n]+"\r\n")
				encoded = encoded[n:]
			}
		}
		if err := mixed.Close(); err != nil {
			return result, err
		}
	} else if c.HTML != "" && c.Text != "" {
		m := multipart.NewWriter(&b)
		fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", m.Boundary())
		if err := writeTexts(m, c); err != nil {
			return result, err
		}
		_ = m.Close()
	} else {
		typ, body := "text/plain", c.Text
		if c.HTML != "" {
			typ, body = "text/html", c.HTML
		}
		fmt.Fprintf(&b, "Content-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", typ)
		q := quotedprintable.NewWriter(&b)
		_, _ = q.Write([]byte(body))
		_ = q.Close()
	}
	if b.Len() > 16<<20 {
		return result, fault.New("usage", "encoded outgoing message exceeds 16 MiB")
	}
	result.Data = b.Bytes()
	return result, nil
}
func writeTexts(m *multipart.Writer, c Compose) error {
	if err := writeText(m, "text/plain", c.Text); err != nil {
		return err
	}
	return writeText(m, "text/html", c.HTML)
}
func writeText(m *multipart.Writer, typ, body string) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", typ+"; charset=utf-8")
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	w, err := m.CreatePart(h)
	if err != nil {
		return err
	}
	q := quotedprintable.NewWriter(w)
	if _, err = q.Write([]byte(body)); err != nil {
		return err
	}
	return q.Close()
}

func Reply(original Message, from, text string, all bool) (Compose, error) {
	c := Compose{From: from, Text: text, Subject: original.Subject}
	if !strings.HasPrefix(strings.ToLower(c.Subject), "re:") {
		c.Subject = "Re: " + c.Subject
	}
	c.To = append([]string(nil), original.ReplyTo...)
	if len(c.To) == 0 {
		c.To = append(c.To, original.From...)
	}
	if all {
		c.Cc = append(c.Cc, original.To...)
		c.Cc = append(c.Cc, original.Cc...)
	}
	sender, err := address(from)
	if err != nil {
		return c, err
	}
	seen := map[string]bool{strings.ToLower(sender.Address): true}
	clean := func(values []string) []string {
		out := []string{}
		for _, s := range values {
			a, err := address(s)
			if err != nil {
				continue
			}
			key := strings.ToLower(a.Address)
			if !seen[key] {
				seen[key] = true
				out = append(out, s)
			}
		}
		return out
	}
	c.To = clean(c.To)
	c.Cc = clean(c.Cc)
	if original.MessageID != "" {
		c.InReplyTo = original.MessageID
		c.References = append(append([]string(nil), original.References...), original.MessageID)
	}
	if original.Text != "" {
		c.Text += "\n\n" + strings.Join(prefixLines(original.Text, "> "), "\n")
	}
	return c, c.Validate()
}
func prefixLines(s, prefix string) []string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return lines
}
func Forward(original Message, from, text string, to []string) (Compose, error) {
	c := Compose{From: from, To: to, Text: text + "\n\n---------- Forwarded message ----------\nFrom: " + strings.Join(original.From, ", ") + "\nSubject: " + original.Subject + "\n\n" + original.Text, Subject: original.Subject}
	if original.HTML != "" {
		c.HTML = "<p>" + html.EscapeString(text) + "</p><hr><p>From: " + html.EscapeString(strings.Join(original.From, ", ")) + "<br>Subject: " + html.EscapeString(original.Subject) + "</p>" + original.HTML
	}
	if !strings.HasPrefix(strings.ToLower(c.Subject), "fwd:") {
		c.Subject = "Fwd: " + c.Subject
	}
	return c, c.Validate()
}

// BuildDraft preserves Bcc for later draft submission; SMTP messages omit it.
func BuildDraft(c Compose) ([]byte, error) {
	p, err := buildMessage(c, true)
	if err != nil {
		return nil, err
	}
	if len(c.Bcc) == 0 {
		return p.Data, nil
	}
	values := []string{}
	for _, s := range c.Bcc {
		a, _ := address(s)
		values = append(values, a.String())
	}
	return append([]byte("Bcc: "+strings.Join(values, ",\r\n ")+"\r\n"), p.Data...), nil
}

// PreparedFromRaw converts an IMAP draft to SMTP data while retaining Bcc only
// in the envelope. Sender authorization remains the caller's responsibility.
func PreparedFromRaw(raw []byte) (PreparedMessage, error) {
	var p PreparedMessage
	if len(raw) == 0 || len(raw) > 16<<20 {
		return p, fault.New("usage", "draft must be between 1 byte and 16 MiB")
	}
	m, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return p, fault.New("usage", "invalid draft message")
	}
	if len(m.Header["From"]) != 1 {
		return p, fault.New("usage", "draft requires exactly one From header")
	}
	from, err := address(m.Header.Get("From"))
	if err != nil {
		return p, err
	}
	p.From = from.Address
	seen := map[string]bool{}
	for _, key := range []string{"To", "Cc", "Bcc"} {
		for _, value := range m.Header[key] {
			if !safeHeader(value) {
				return p, fault.New("usage", "invalid draft recipients")
			}
			a, err := netmail.ParseAddressList(value)
			if err != nil {
				return p, fault.New("usage", "invalid draft recipients")
			}
			for _, v := range a {
				valid, err := address(v.String())
				if err != nil {
					return p, err
				}
				if !seen[valid.Address] {
					p.Recipients = append(p.Recipients, valid.Address)
					seen[valid.Address] = true
				}
			}
		}
	}
	if len(p.Recipients) == 0 || len(p.Recipients) > 100 {
		return p, fault.New("usage", "draft requires between 1 and 100 recipients")
	}
	var b bytes.Buffer
	for key, values := range m.Header {
		if strings.EqualFold(key, "Bcc") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(key), "resent-") {
			return p, fault.New("usage", "resent draft headers are not supported")
		}
		if !safeHeader(key) {
			return p, fault.New("usage", "invalid draft header")
		}
		for _, v := range values {
			if !safeHeader(v) {
				return p, fault.New("usage", "invalid draft header")
			}
			fmt.Fprintf(&b, "%s: %s\r\n", key, v)
		}
	}
	b.WriteString("\r\n")
	_, err = b.ReadFrom(m.Body)
	if err != nil {
		return p, err
	}
	p.Data = b.Bytes()
	p.MessageID = m.Header.Get("Message-ID")
	if p.MessageID != "" && !messageIDPattern.MatchString(p.MessageID) {
		return p, fault.New("usage", "invalid draft Message-ID")
	}
	return p, nil
}

func PrepareDraft(raw []byte, from string) (PreparedMessage, error) {
	p, err := PreparedFromRaw(raw)
	if err != nil {
		return p, err
	}
	a, err := address(from)
	if err != nil {
		return p, err
	}
	if !strings.EqualFold(a.Address, p.From) {
		return PreparedMessage{}, fault.New("permission_denied", "draft sender differs from configured mail address")
	}
	return p, nil
}

func foldHeader(s string) string {
	var b strings.Builder
	column := 0
	for i, word := range strings.Split(s, " ") {
		if i > 0 {
			if column+1+len(word) > 76 {
				b.WriteString("\r\n ")
				column = 1
			} else {
				b.WriteByte(' ')
				column++
			}
		}
		b.WriteString(word)
		column += len(word)
	}
	return b.String()
}
