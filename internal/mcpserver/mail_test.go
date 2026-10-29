package mcpserver

import (
	"context"
	"github.com/mark3labs/mcp-go/mcp"
	"i4dcli/internal/app"
	"i4dcli/internal/mail"
	"i4dcli/internal/policy"
	"testing"
)

type fakeMail struct{ closed bool }

func (f *fakeMail) Folders(context.Context) ([]mail.Folder, error) {
	return []mail.Folder{{Name: "INBOX"}}, nil
}
func (f *fakeMail) List(context.Context, mail.ListOptions) (mail.Page, error) {
	return mail.Page{}, nil
}
func (f *fakeMail) Get(context.Context, string) (mail.Message, error) { return mail.Message{}, nil }
func (f *fakeMail) Close() error                                      { f.closed = true; return nil }
func TestMailToolsIsolation(t *testing.T) {
	p, _ := policy.New(policy.Options{ReadOnly: true, EnabledExact: "mcp,mail.folders.list"})
	o := options()
	o.Force = true // Calendar deletion authorization must not block mail reads.
	f := &fakeMail{}
	calls := 0
	o.MailFactory = func(context.Context) (MailClient, error) { calls++; return f, nil }
	c := connect(t, p, o, func(context.Context) (*app.Service, func(), error) {
		t.Fatal("mail called calendar factory")
		return nil, nil, nil
	})
	tools, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
	if err != nil || len(tools.Tools) != 1 {
		t.Fatalf("%v %v", tools, err)
	}
	r := call(t, c, "mail_folders_list", map[string]any{})
	if r.IsError || calls != 1 || !f.closed {
		t.Fatalf("%v calls=%d closed=%v", r, calls, f.closed)
	}
	r = call(t, c, "mail_folders_list", map[string]any{"account": "other"})
	if !r.IsError || calls != 1 {
		t.Fatal("invalid tool input reached factory")
	}
}

func TestMailValidationBeforeFactory(t *testing.T) {
	p, _ := policy.New(policy.Options{})
	o := options()
	o.MailFactory = func(context.Context) (MailClient, error) { t.Fatal("invalid request reached factory"); return nil, nil }
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"mail.messages.get", map[string]any{"id": "bad"}},
		{"mail.messages.list", map[string]any{"cursor": "bad"}},
		{"mail.messages.search", map[string]any{"since": "yesterday"}},
		{"mail.messages.search", map[string]any{"limit": 101}},
	} {
		r := mcp.CallToolRequest{}
		r.Params.Arguments = tc.args
		if _, err := executeMail(t.Context(), p, o, spec{command: tc.name}, r); err == nil {
			t.Fatalf("accepted %v", tc.args)
		}
	}
}
