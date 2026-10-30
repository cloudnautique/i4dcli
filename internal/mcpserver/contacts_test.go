package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"i4dcli/internal/app"
	"i4dcli/internal/contacts"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
)

type fakeContacts struct {
	limit    int
	mutation contacts.Mutation
}

func (f *fakeContacts) AddressBooks(context.Context) ([]contacts.AddressBook, error) {
	return []contacts.AddressBook{{Name: "A"}, {Name: "B"}}, nil
}
func (f *fakeContacts) List(_ context.Context, q contacts.Query) (contacts.Page, error) {
	f.limit = q.Limit
	return contacts.Page{Contacts: []contacts.Contact{}, Returned: 0}, nil
}
func (f *fakeContacts) Get(context.Context, string, string) (contacts.Contact, error) {
	return contacts.Contact{Name: "Ada"}, nil
}
func (f *fakeContacts) Mutate(_ context.Context, m contacts.Mutation) (contacts.Result, error) {
	f.mutation = m
	return contacts.Result{Action: m.Action, Path: "/book/a.vcf"}, nil
}
func (f *fakeContacts) Close() error { return nil }
func TestContactsMCPPreviewReadsAndGates(t *testing.T) {
	p, _ := policy.New(policy.Options{})
	factoryCalls := 0
	fake := &fakeContacts{}
	o := options()
	o.AllowTools = []string{"addressbooks_list", "contacts_list", "contacts_get", "contacts_create", "contacts_delete"}
	o.AllowWrite = true
	o.AllowDelete = true
	o.DryRun = true
	o.MaxResults = 1
	o.ContactsFactory = func(context.Context) (ContactsClient, error) { factoryCalls++; return fake, nil }
	noCalendar := func(context.Context) (*app.Service, func(), error) {
		t.Fatal("contacts used calendar credentials")
		return nil, nil, nil
	}
	c := connect(t, p, o, noCalendar)
	result := call(t, c, "contacts_create", map[string]any{"addressbook": "Personal", "name": "Ada"})
	if result.IsError || factoryCalls != 0 || !strings.Contains(resultText(result), `"remote_checked":false`) {
		t.Fatal(resultText(result))
	}
	o.DryRun = false
	c2 := connect(t, p, o, noCalendar)
	result = call(t, c2, "contacts_delete", map[string]any{"addressbook": "Personal", "path": "/book/a.vcf"})
	if !result.IsError || factoryCalls != 0 || !strings.Contains(resultText(result), "confirmation_required") {
		t.Fatal(resultText(result))
	}
	result = call(t, c2, "addressbooks_list", map[string]any{})
	if result.IsError || !strings.Contains(resultText(result), `"truncated":true`) {
		t.Fatal(resultText(result))
	}
	result = call(t, c2, "contacts_list", map[string]any{"addressbook": "Personal", "limit": 100})
	if result.IsError || fake.limit != 1 {
		t.Fatalf("%s limit=%d", resultText(result), fake.limit)
	}
	o.Force = true
	c3 := connect(t, p, o, noCalendar)
	result = call(t, c3, "contacts_delete", map[string]any{"addressbook": "Personal", "path": "/book/a.vcf"})
	if result.IsError || fake.mutation.Action != "delete" {
		t.Fatal(resultText(result))
	}
}
func TestContactsMCPInvalidLimitsAndExactPolicyBeforeFactory(t *testing.T) {
	p, _ := policy.New(policy.Options{})
	o := options()
	o.ContactsFactory = func(context.Context) (ContactsClient, error) {
		t.Fatal("invalid request touched credentials")
		return nil, nil
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"limit": -1}
	if _, err := executeContacts(context.Background(), p, o, spec{command: "addressbooks.list"}, req); fault.ExitCode(err) != 2 {
		t.Fatalf("%v", err)
	}
	p, _ = policy.New(policy.Options{EnabledExact: "mcp,events.list"})
	req.Params.Arguments = map[string]any{}
	if _, err := executeContacts(context.Background(), p, o, spec{command: "addressbooks.list"}, req); fault.ExitCode(err) != 6 {
		t.Fatalf("%v", err)
	}
}
