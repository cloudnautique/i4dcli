package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"i4dcli/internal/config"
	"i4dcli/internal/fault"
	"i4dcli/internal/mail"
)

const mailConfig = `{"version":1,"default_account":"personal","accounts":{"personal":{"username":"calendar@example.com","mail":{"address":"person@icloud.com","imap_username":"person","default_mailbox":"Archive"}}}}`

func TestMailIdentityAndCredentialPrecedence(t *testing.T) {
	installConfig(t, mailConfig)
	t.Setenv("ICLOUD_APP_PASSWORD", "test-password")
	for _, tc := range []struct {
		name, envAddress, calendarUsername string
		flags                              []string
		address, login, mailbox            string
		wantError                          bool
	}{
		{name: "configured", address: "person@icloud.com", login: "person", mailbox: "Archive"},
		{name: "calendar override stays separate", calendarUsername: "other@example.com", address: "person@icloud.com", login: "person", mailbox: "Archive"},
		{name: "same mail identity keeps defaults", envAddress: "PERSON@icloud.com", address: "PERSON@icloud.com", login: "person", mailbox: "Archive"},
		{name: "different mail environment identity", envAddress: "other@icloud.com", address: "other@icloud.com", login: "other@icloud.com", mailbox: "INBOX"},
		{name: "mail flag wins", envAddress: "other@icloud.com", flags: []string{"--mail-address", "flag@icloud.com"}, address: "flag@icloud.com", login: "flag@icloud.com", mailbox: "INBOX"},
		{name: "explicit blank", flags: []string{"--mail-address="}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ICLOUD_MAIL_ADDRESS", tc.envAddress)
			t.Setenv("ICLOUD_USERNAME", tc.calendarUsername)
			_, g, err := parseGlobal(tc.flags)
			if err != nil {
				t.Fatal(err)
			}
			account, err := configuredAccount(&g)
			if err != nil {
				t.Fatal(err)
			}
			settings, err := mailSettings(g, account)
			if tc.wantError {
				if fault.ExitCode(err) != 10 {
					t.Fatalf("%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if settings.Address != tc.address || settings.IMAPUsername != tc.login || settings.DefaultMailbox != tc.mailbox {
				t.Fatalf("%+v", settings)
			}
			credentials, err := mailCredentials(g, account, true, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if credentials.Username != tc.login || credentials.Password != "test-password" || credentials.AccountID == "" {
				t.Fatal("incorrect resolved mail credentials")
			}
		})
	}
}

func TestMailDoesNotInferCalendarIdentity(t *testing.T) {
	installConfig(t, exampleConfig)
	t.Setenv("ICLOUD_MAIL_ADDRESS", "")
	_, g, _ := parseGlobal(nil)
	account, err := configuredAccount(&g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mailSettings(g, account); fault.ExitCode(err) != 10 {
		t.Fatalf("calendar address used for mail: %v", err)
	}
	t.Setenv("ICLOUD_MAIL_ADDRESS", "environment@icloud.com")
	_, g, _ = parseGlobal(nil)
	settings, err := mailSettings(g, config.Account{})
	if err != nil || settings.IMAPUsername != "environment@icloud.com" {
		t.Fatalf("%+v %v", settings, err)
	}
	if _, err := mailCredentials(g, config.Account{}, true, io.Discard); fault.ExitCode(err) != 10 || !strings.Contains(err.Error(), "ICLOUD_APP_PASSWORD") {
		t.Fatalf("%v", err)
	}
}

func TestMailValidationBeforeCredentials(t *testing.T) {
	installConfig(t, mailConfig)
	t.Setenv("ICLOUD_MAIL_ADDRESS", "")
	for _, args := range [][]string{
		{"mail", "messages", "get"},
		{"mail", "messages", "get", "--id", "invalid"},
		{"mail", "messages", "list", "--limit", "0"},
		{"mail", "messages", "list", "--limit", "101"},
		{"mail", "messages", "list", "--cursor", "invalid"},
		{"mail", "messages", "list", "--mailbox", ""},
		{"mail", "messages", "search", "--since", "not-a-date"},
		{"mail", "messages", "search", "--since", "2026-11-01", "--before", "2026-10-01"},
		{"mail", "messages", "list", "--dry-run"},
		{"mail", "folders", "list", "--force"},
	} {
		var out bytes.Buffer
		err := Run(context.Background(), append(args, "--json", "--no-input"), &out, io.Discard)
		if fault.ExitCode(err) != 2 {
			t.Errorf("%v: %v", args, err)
		}
		if strings.Contains(err.Error(), "password") {
			t.Errorf("authentication happened before validation: %v", err)
		}
	}
}

func TestMailPolicyAndHelpBeforeConfigOrCredentials(t *testing.T) {
	installConfig(t, `{`)
	for _, args := range [][]string{{"mail", "folders", "list"}, {"mail", "messages", "list"}, {"mail", "messages", "get"}, {"mail", "messages", "search"}} {
		var out bytes.Buffer
		err := Run(context.Background(), append(args, "--enable-commands-exact", "calendars.list"), &out, io.Discard)
		if fault.ExitCode(err) != 6 {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"mail", "--help"}, {"help", "mail", "messages"}, {"mail", "messages", "search", "--help"}, {"help", "mail", "messages", "get"}, {"schema", "--readonly"}} {
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, &out); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "mail") {
			t.Fatal(out.String())
		}
	}
}

func TestMailGlobalExtractionPreservesFilterValues(t *testing.T) {
	args, g, err := parseGlobal([]string{"mail", "messages", "search", "--subject", "--readonly", "--from", "--json", "--mail-address", "person@icloud.com"})
	if err != nil || g.readonly || g.asJSON || g.mailAddress != "person@icloud.com" {
		t.Fatalf("%+v %v", g, err)
	}
	if strings.Join(args, "|") != "mail|messages|search|--subject|--readonly|--from|--json" {
		t.Fatal(args)
	}
}

func TestMailOutputAndPagingMetadata(t *testing.T) {
	page := mail.Page{Messages: []mail.Summary{{ID: "opaque", Mailbox: "INBOX", From: []string{"sender@example.com"}, Subject: "invoice\t\x1b[31m\nnext", InternalDate: time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)}}, Returned: 1, NextCursor: "next", Truncated: true}
	var out, stderr bytes.Buffer
	if err := printMailPage(&out, &stderr, page, globals{plain: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "Next cursor") || !strings.Contains(stderr.String(), "Next cursor: next") || len(strings.Split(strings.TrimSpace(out.String()), "\n")) != 2 {
		t.Fatalf("out=%q err=%q", out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if err := printMailPage(&out, &stderr, page, globals{asJSON: true}); err != nil {
		t.Fatal(err)
	}
	var decoded mail.Page
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || decoded.Messages[0].Subject != page.Messages[0].Subject || decoded.NextCursor != "next" || stderr.Len() != 0 {
		t.Fatalf("%s %v", out.String(), err)
	}
	out.Reset()
	message := mail.Message{Summary: page.Messages[0], Text: "body\n\x1b[31m", HTML: "<img src=\"https://example.com/tracker\">", Attachments: []mail.Attachment{{Filename: "../unsafe\nfile", ContentType: "text/plain", Size: 10}}, BodyTruncated: true}
	if err := printMailMessage(&out, message, globals{plain: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "body_truncated\ttrue") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := printMailFolders(&out, nil, globals{asJSON: true}); err != nil || out.String() != "[]\n" {
		t.Fatalf("%s %v", out.String(), err)
	}
}
