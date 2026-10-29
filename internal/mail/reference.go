package mail

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode"

	"i4dcli/internal/fault"
)

type reference struct {
	Version  int    `json:"v"`
	Account  string `json:"a"`
	Mailbox  string `json:"m"`
	Validity uint32 `json:"u"`
	UID      uint32 `json:"i,omitempty"`
	Before   uint32 `json:"b,omitempty"`
	Filter   Filter `json:"f,omitempty"`
}

func encodeReference(r reference) string {
	b, _ := json.Marshal(r)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeReference(s string) (reference, error) {
	var r reference
	if len(s) == 0 || len(s) > 16384 {
		return r, fault.New("usage", "invalid mail reference or cursor")
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return r, fault.New("usage", "invalid mail reference or cursor")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.Version != 1 || len(r.Account) != 64 || r.Validity == 0 || !validMailbox(r.Mailbox) {
		return reference{}, fault.New("usage", "invalid mail reference or cursor")
	}
	return r, nil
}
func validMailbox(s string) bool {
	return s != "" && len(s) <= 1024 && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) })
}
func normalizeMailbox(s string) string {
	if strings.EqualFold(s, "INBOX") {
		return "INBOX"
	}
	return s
}

// ValidateList validates local options without credentials or network requests.
func (o ListOptions) Validate() error {
	if o.Limit < 0 || o.Limit > 100 {
		return fault.New("usage", "mail limit must be between 1 and 100")
	}
	if o.Mailbox != "" && !validMailbox(o.Mailbox) {
		return fault.New("usage", "invalid mailbox name")
	}
	for _, s := range []string{o.Filter.From, o.Filter.To, o.Filter.Subject} {
		if len(s) > 1024 || strings.ContainsAny(s, "\x00\r\n") {
			return fault.New("usage", "mail search fields must be at most 1024 bytes without newlines")
		}
	}
	var since, before time.Time
	var err error
	if o.Filter.Since != "" {
		since, err = time.Parse("2006-01-02", o.Filter.Since)
		if err != nil {
			return fault.New("usage", "since must be YYYY-MM-DD")
		}
	}
	if o.Filter.Before != "" {
		before, err = time.Parse("2006-01-02", o.Filter.Before)
		if err != nil {
			return fault.New("usage", "before must be YYYY-MM-DD")
		}
	}
	if !since.IsZero() && !before.IsZero() && !since.Before(before) {
		return fault.New("usage", "before must be later than since")
	}
	if o.Cursor != "" {
		r, err := decodeReference(o.Cursor)
		if err != nil {
			return err
		}
		if r.UID != 0 || r.Before == 0 {
			return fault.New("usage", "invalid mail cursor")
		}
	}
	return nil
}

// ValidateID validates the reference shape without accessing a mailbox.
func ValidateID(id string) error {
	r, err := decodeReference(id)
	if err != nil {
		return err
	}
	if r.UID == 0 || r.Before != 0 {
		return fault.New("usage", "invalid mail message ID")
	}
	return nil
}

// ValidateList is the function form of ListOptions.Validate.
func ValidateList(o ListOptions) error { return o.Validate() }
