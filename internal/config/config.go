// Package config reads non-secret account defaults from the user's XDG directory.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	netmail "net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const maxSize = 1 << 20

type Mail struct {
	Address        string `json:"address"`
	IMAPUsername   string `json:"imap_username,omitempty"`
	DefaultMailbox string `json:"default_mailbox,omitempty"`
}

// Validate checks local settings only; actual mailbox ownership requires IMAP authentication.
func (m Mail) Validate() error {
	address, err := netmail.ParseAddress(m.Address)
	if err != nil || address.Address != m.Address || strings.ContainsAny(m.Address, "\r\n") || len(m.Address) > 320 {
		return fmt.Errorf("mail.address must be a full email address without a display name")
	}
	if strings.TrimSpace(m.IMAPUsername) != m.IMAPUsername || len(m.IMAPUsername) > 320 || strings.IndexFunc(m.IMAPUsername, unicode.IsControl) >= 0 {
		return fmt.Errorf("mail.imap_username must be a login without whitespace edges or control characters")
	}
	if strings.TrimSpace(m.DefaultMailbox) != m.DefaultMailbox || len(m.DefaultMailbox) > 1024 || strings.IndexFunc(m.DefaultMailbox, unicode.IsControl) >= 0 {
		return fmt.Errorf("mail.default_mailbox must be an exact mailbox name without whitespace edges or control characters")
	}
	return nil
}

type Account struct {
	Username  string   `json:"username,omitempty"`
	Mail      *Mail    `json:"mail,omitempty"`
	Calendars []string `json:"calendars,omitempty"`
	Timezone  string   `json:"timezone,omitempty"`
}

type File struct {
	Version        int                `json:"version"`
	DefaultAccount string             `json:"default_account,omitempty"`
	Accounts       map[string]Account `json:"accounts"`
}

// Path follows XDG on every platform, including macOS and Windows. Relative
// XDG_CONFIG_HOME values are ignored as required by the XDG specification.
func Path() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate config home: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "i4d", "config.json"), nil
}

// Load distinguishes an absent optional file from unreadable or invalid config.
// No values in this format are credentials; unknown fields (including password)
// are rejected, and error messages never echo field values or file contents.
func Load(path string) (File, bool, error) {
	empty := File{Version: 1, Accounts: map[string]Account{}}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return empty, false, nil
	}
	if err != nil {
		return File{}, false, fmt.Errorf("inspect config: %w", err)
	}
	if !info.Mode().IsRegular() {
		return File{}, false, fmt.Errorf("config must be a regular file")
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return empty, false, nil
	}
	if err != nil {
		return File{}, false, fmt.Errorf("read config: %w", err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return File{}, false, fmt.Errorf("inspect config: %w", err)
	}
	if !info.Mode().IsRegular() {
		return File{}, false, fmt.Errorf("config must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return File{}, false, fmt.Errorf("read config: %w", err)
	}
	if len(data) > maxSize {
		return File{}, false, fmt.Errorf("config exceeds 1 MiB")
	}
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return File{}, false, fmt.Errorf("config must be a JSON object")
	}
	var cfg File
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return File{}, false, fmt.Errorf("invalid config JSON or unsupported fields; see the config example in README")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return File{}, false, fmt.Errorf("config must contain exactly one JSON object")
	}
	if err := cfg.validate(); err != nil {
		return File{}, false, err
	}
	return cfg, true, nil
}

var accountName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (f File) validate() error {
	if f.Version != 1 {
		return fmt.Errorf("unsupported config version; expected version 1")
	}
	if f.Accounts == nil {
		return fmt.Errorf("config accounts must be an object")
	}
	if f.DefaultAccount != "" {
		if _, ok := f.Accounts[f.DefaultAccount]; !ok {
			return fmt.Errorf("default_account must name a configured account")
		}
	}
	for name, account := range f.Accounts {
		if !accountName.MatchString(name) || len(name) > 128 {
			return fmt.Errorf("account names must be 1–128 letters, digits, dots, underscores, or hyphens, starting with a letter or digit")
		}
		if strings.TrimSpace(account.Username) == "" && account.Mail == nil {
			return fmt.Errorf("each account requires a calendar username or mail settings")
		}
		if account.Mail != nil {
			if err := account.Mail.Validate(); err != nil {
				return err
			}
		}
		if len(account.Calendars) > 50 {
			return fmt.Errorf("each account may configure at most 50 calendars")
		}
		for _, calendar := range account.Calendars {
			if strings.TrimSpace(calendar) == "" {
				return fmt.Errorf("configured calendar names cannot be empty")
			}
		}
		if account.Timezone != "" {
			if _, err := time.LoadLocation(account.Timezone); err != nil {
				return fmt.Errorf("configured timezone must be Local, UTC, or an installed IANA timezone")
			}
		}
	}
	return nil
}

func (f File) Select(name string) (Account, error) {
	if name == "" {
		name = f.DefaultAccount
	}
	if name == "" {
		return Account{}, nil
	}
	account, ok := f.Accounts[name]
	if !ok {
		return Account{}, fmt.Errorf("selected account is not configured; inspect config show or choose --account NAME")
	}
	return account, nil
}
