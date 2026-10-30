package mcpserver

import (
	"context"
	"encoding/base64"
	"github.com/mark3labs/mcp-go/mcp"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
	"strings"
	"testing"
)

func TestMailSendAuthorizationAndPreview(t *testing.T) {
	for _, profile := range []string{"readonly", "editor"} {
		p, _ := policy.New(policy.Options{Profile: profile})
		if p.Check("mail.send") == nil {
			t.Fatal("profile permits sending", profile)
		}
	}
	p, _ := policy.New(policy.Options{})
	o := options()
	o.AllowWrite = true
	o.AllowSend = true
	o.DryRun = true
	o.MailIdentity = func() (string, error) { return "me@icloud.com", nil }
	o.MailFactory = func(context.Context) (MailClient, error) { t.Fatal("preview opened IMAP"); return nil, nil }
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"message": map[string]any{"to": []string{"you@example.com"}, "subject": "Hello", "text": "Body"}}
	if _, err := executeMail(t.Context(), p, o, spec{command: "mail.send"}, req); err != nil {
		t.Fatal(err)
	}
	o.DryRun = false
	if _, err := executeMail(t.Context(), p, o, spec{command: "mail.send"}, req); fault.ExitCode(err) != 11 {
		t.Fatal(err)
	}
	o.AllowSend = false
	if _, err := executeMail(t.Context(), p, o, spec{command: "mail.send"}, req); fault.ExitCode(err) != 6 {
		t.Fatal(err)
	}
}
func TestMailSendToolsRequireStartupGate(t *testing.T) {
	p, _ := policy.New(policy.Options{})
	o := options()
	o.AllowWrite = true
	tools, err := Tools(p, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name == "mail_send" || tool.Name == "mail_reply" || tool.Name == "mail_forward" || tool.Name == "mail_drafts_send" {
			t.Fatal("submission exposed without allow-send")
		}
	}
	o.AllowSend = true
	tools, err = Tools(p, o)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools {
		if tool.Name == "mail_send" {
			found = true
		}
	}
	if !found {
		t.Fatal("send gate did not enable tool")
	}
}

func TestMailDestructiveGateBeforeFactory(t *testing.T) {
	p, _ := policy.New(policy.Options{})
	o := options()
	o.AllowWrite = true
	o.AllowDelete = true
	o.MailFactory = func(context.Context) (MailClient, error) {
		t.Fatal("unauthorized delete opened connection")
		return nil, nil
	}
	id := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"a":"` + strings.Repeat("a", 64) + `","m":"INBOX","u":42,"i":1}`))
	r := mcp.CallToolRequest{}
	r.Params.Arguments = map[string]any{"id": id}
	if _, err := executeMail(t.Context(), p, o, spec{command: "mail.messages.delete"}, r); fault.ExitCode(err) != 11 {
		t.Fatal(err)
	}
	o.DryRun = true
	if _, err := executeMail(t.Context(), p, o, spec{command: "mail.messages.delete"}, r); err != nil {
		t.Fatal(err)
	}
}
