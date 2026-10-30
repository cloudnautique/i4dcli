package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"i4dcli/internal/fault"
)

func TestContactPreviewsAndRestrictions(t *testing.T) {
	t.Setenv("ICLOUD_USERNAME", "")
	t.Setenv("ICLOUD_APP_PASSWORD", "")
	for _, args := range [][]string{
		{"contacts", "create", "--addressbook", "Personal", "--name", "Ada", "--email", "ada@example.com", "--given-name", "Ada", "--family-name", "Lovelace"},
		{"contacts", "edit", "--addressbook", "Personal", "--path", "/book/a.vcf", "--clear-emails", "--note", ""},
		{"contacts", "delete", "--addressbook", "Personal", "--path", "/book/a.vcf"},
	} {
		var out bytes.Buffer
		if err := Run(context.Background(), append(args, "--dry-run", "--json", "--no-input"), &out, io.Discard); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var value map[string]any
		if err := json.Unmarshal(out.Bytes(), &value); err != nil || value["remote_checked"] != false {
			t.Fatalf("%s %v", out.String(), err)
		}
		if err := Run(context.Background(), append(args, "--dry-run", "--readonly"), io.Discard, io.Discard); fault.ExitCode(err) != 6 {
			t.Fatalf("readonly bypass: %v", err)
		}
	}
	if err := Run(context.Background(), []string{"contacts", "delete", "--addressbook", "Personal", "--path", "/book/a.vcf", "--no-input"}, io.Discard, io.Discard); fault.ExitCode(err) != 11 {
		t.Fatalf("confirmation: %v", err)
	}
}
func TestContactArgumentsAndHelp(t *testing.T) {
	for _, args := range [][]string{
		{"contacts", "create", "--addressbook", "Personal"},
		{"contacts", "create", "--addressbook", "Personal", "--name", "Ada", "--email", "bad"},
		{"contacts", "edit", "--addressbook", "Personal", "--path", "/book/a.vcf", "--clear-emails", "--email", "a@example.com"},
		{"contacts", "search", "--addressbook", "Personal"},
		{"contacts", "list", "--addressbook", "Personal", "--limit", "0"},
		{"contacts", "get", "--addressbook", "Personal"},
	} {
		if err := Run(context.Background(), args, io.Discard, io.Discard); fault.ExitCode(err) != 2 {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"addressbooks", "--help"}, {"help", "contacts", "create"}, {"contacts", "list", "--help"}} {
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, &out); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	normalized, g, err := parseGlobal([]string{"contacts", "create", "--name", "--readonly", "--note", "--json", "--addressbook", "--force"})
	if err != nil || g.readonly || g.asJSON || g.force || !strings.Contains(strings.Join(normalized, "|"), "--name|--readonly") {
		t.Fatalf("%v %+v %v", normalized, g, err)
	}
}
