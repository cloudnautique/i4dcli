package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"i4dcli/internal/fault"
)

func testMailID() string {
	raw, _ := json.Marshal(map[string]any{"v": 1, "a": strings.Repeat("a", 64), "m": "INBOX", "u": 1, "i": 42})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestMailWritePreviewsNoCredentials(t *testing.T) {
	installConfig(t, mailConfig)
	id := testMailID()
	for _, args := range [][]string{
		{"mail", "messages", "mark", "--id", id, "--seen=false"},
		{"mail", "messages", "move", "--id", id, "--destination", "Archive"},
		{"mail", "messages", "trash", "--id", id},
		{"mail", "messages", "delete", "--id", id},
		{"mail", "attachments", "get", "--id", id, "--attachment", "0", "--output", filepath.Join(t.TempDir(), "attachment")},
		{"mail", "drafts", "create", "--subject", "Incomplete draft"},
		{"mail", "drafts", "edit", "--id", id, "--subject", "Replacement"},
		{"mail", "drafts", "delete", "--id", id},
		{"mail", "drafts", "send", "--id", id},
		{"mail", "send", "--to", "recipient@example.com", "--body", "Hello", "--subject", "Test"},
		{"mail", "reply", "--id", id, "--body", "Reply"},
		{"mail", "forward", "--id", id, "--to", "recipient@example.com"},
	} {
		var out bytes.Buffer
		err := Run(context.Background(), append(args, "--dry-run", "--json", "--no-input"), &out, io.Discard)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var preview struct {
			DryRun  bool           `json:"dry_run"`
			Remote  bool           `json:"remote_checked"`
			Request map[string]any `json:"request"`
		}
		if err := json.Unmarshal(out.Bytes(), &preview); err != nil || !preview.DryRun || preview.Remote {
			t.Fatalf("%v %s", err, out.String())
		}
		if args[1] == "messages" && args[2] == "mark" {
			changes := preview.Request["changes"].(map[string]any)
			if changes["seen"] != false || len(changes) != 1 {
				t.Fatalf("preview changes %v", changes)
			}
		}
	}
}

func TestMailWritePoliciesAndConfirmations(t *testing.T) {
	installConfig(t, mailConfig)
	id := testMailID()
	tests := []struct {
		args []string
		code int
	}{
		{[]string{"--readonly", "mail", "send", "--force"}, 6},
		{[]string{"--profile", "editor", "mail", "send", "--force"}, 6},
		{[]string{"--profile", "editor", "mail", "messages", "move", "--force"}, 6},
		{[]string{"--readonly", "mail", "drafts", "create", "--dry-run"}, 6},
		{[]string{"mail", "send", "--to", "recipient@example.com", "--body", "Hello", "--no-input"}, 11},
		{[]string{"mail", "messages", "delete", "--id", id, "--no-input"}, 11},
		{[]string{"mail", "drafts", "send", "--id", id, "--no-input"}, 11},
		{[]string{"mail", "messages", "mark", "--id", id}, 2},
		{[]string{"mail", "messages", "move", "--id", id}, 2},
		{[]string{"mail", "messages", "mark", "--id", id, "--seen", "--force"}, 2},
		{[]string{"mail", "send", "--to", "bad", "--body", "Hello", "--force"}, 2},
	}
	for _, tc := range tests {
		var out bytes.Buffer
		err := Run(context.Background(), tc.args, &out, io.Discard)
		if fault.ExitCode(err) != tc.code {
			t.Errorf("%v: %v want %d", tc.args, err, tc.code)
		}
	}
	for _, answer := range []string{"yes\n", "no\n", ""} {
		err := readMailConfirmation(strings.NewReader(answer), io.Discard, "Send mail")
		if (err == nil) != (answer == "yes\n") {
			t.Fatalf("%q: %v", answer, err)
		}
	}
}

func TestAttachmentExplicitPathAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "attachment.bin")
	if err := writeMailAttachment(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeMailAttachment(path, []byte("second")); fault.ExitCode(err) != 9 {
		t.Fatalf("overwrite allowed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first" {
		t.Fatalf("%s %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("private file mode %o", info.Mode().Perm())
	}
	loaded, err := loadMailAttachments([]string{path})
	if err != nil || len(loaded) != 1 || loaded[0].Filename != "attachment.bin" || string(loaded[0].Data) != "first" {
		t.Fatalf("%+v %v", loaded, err)
	}
	if _, err := loadMailAttachments([]string{dir}); err == nil {
		t.Fatal("directory accepted as attachment")
	}
}

func TestMailWriteGlobalValuesAndHelp(t *testing.T) {
	args, g, err := parseGlobal([]string{"mail", "send", "--body", "--readonly", "--attach", "--json", "--subject", "--force"})
	if err != nil || g.readonly || g.asJSON || g.force || len(args) != 8 {
		t.Fatalf("%v %+v %v", args, g, err)
	}
	for _, args := range [][]string{{"mail", "send", "--help"}, {"help", "mail", "reply"}, {"mail", "drafts", "--help"}, {"help", "mail", "attachments", "get"}} {
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, &out); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestOversizedDraftRejectedBeforeAuthenticationAndInPreview(t *testing.T) {
	installConfig(t, mailConfig)
	for _, preview := range []bool{false, true} {
		args := []string{"mail", "drafts", "create", "--body", strings.Repeat("x", 2<<20), "--no-input"}
		if preview {
			args = append(args, "--dry-run")
		}
		var out bytes.Buffer
		err := Run(context.Background(), args, &out, io.Discard)
		if fault.ExitCode(err) != 2 || strings.Contains(err.Error(), "password") {
			t.Fatalf("preview=%v: %v", preview, err)
		}
	}
}

func TestSMTPConfigUsesFullAddressAndPasswordResolver(t *testing.T) {
	installConfig(t, `{"version":1,"default_account":"personal","accounts":{"personal":{"mail":{"address":"from@icloud.com","imap_username":"from","smtp_username":"login@icloud.com"}}}}`)
	t.Setenv("ICLOUD_APP_PASSWORD", "test-password")
	_, g, _ := parseGlobal(nil)
	account, err := configuredAccount(&g)
	if err != nil {
		t.Fatal(err)
	}
	smtp, err := mailSMTPOptions(g, account, true, io.Discard)
	if err != nil || smtp.Username != "login@icloud.com" || smtp.Password != "test-password" {
		t.Fatal("SMTP credential resolution failed")
	}
}
