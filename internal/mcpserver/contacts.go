package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"i4dcli/internal/contacts"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
)

type ContactsClient interface {
	AddressBooks(context.Context) ([]contacts.AddressBook, error)
	List(context.Context, contacts.Query) (contacts.Page, error)
	Get(context.Context, string, string) (contacts.Contact, error)
	Mutate(context.Context, contacts.Mutation) (contacts.Result, error)
	Close() error
}
type ContactsFactory func(context.Context) (ContactsClient, error)
type contactsInput struct {
	AddressBook string `json:"addressbook"`
	Path        string `json:"path"`
	Query       string `json:"query"`
	Limit       int    `json:"limit"`
	contacts.Changes
}

func executeContacts(ctx context.Context, p policy.Policy, o Options, s spec, req mcp.CallToolRequest) (any, error) {
	if err := p.Check(s.command); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req.GetArguments())
	if err != nil {
		return nil, fault.Usage(err)
	}
	var in contactsInput
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return nil, fault.Usage(err)
	}
	command, _ := policy.Resolve(s.command)
	mutation := command.Capability != policy.Read
	if mutation && !o.AllowWrite {
		return nil, fault.New("permission_denied", "contact writes require allow-write")
	}
	if command.Capability == policy.Delete && !o.AllowDelete {
		return nil, fault.New("permission_denied", "contact deletion requires allow-delete")
	}
	if !mutation && o.DryRun {
		return nil, fault.New("usage", "dry-run does not apply to contact reads")
	}
	if in.Limit < 0 || in.Limit > 1000 {
		return nil, fault.New("usage", "contact limit must be 1–1000")
	}
	limit := in.Limit
	if limit == 0 {
		limit = min(o.MaxResults, 1000)
	}
	if limit > o.MaxResults {
		limit = o.MaxResults
	}
	q := contacts.Query{AddressBook: in.AddressBook, Search: in.Query, Limit: limit}
	if s.command != "addressbooks.list" {
		if err := q.Validate(); err != nil {
			return nil, err
		}
	}
	if s.command == "contacts.search" && strings.TrimSpace(in.Query) == "" {
		return nil, fault.New("usage", "query is required")
	}
	if s.command == "contacts.get" && in.Path == "" {
		return nil, fault.New("usage", "path is required")
	}
	m := contacts.Mutation{Action: strings.TrimPrefix(s.command, "contacts."), AddressBook: in.AddressBook, Path: in.Path, Changes: in.Changes}
	if mutation {
		if err := m.Validate(); err != nil {
			return nil, err
		}
		if o.DryRun {
			return contacts.Preview(m)
		}
		if command.Capability == policy.Delete && !o.Force {
			return nil, fault.New("confirmation_required", "server operator must supply force for contact deletion")
		}
	}
	if o.ContactsFactory == nil {
		return nil, fault.New("config", "contacts are not configured")
	}
	client, err := o.ContactsFactory(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	switch s.command {
	case "addressbooks.list":
		books, err := client.AddressBooks(ctx)
		if err != nil {
			return nil, err
		}
		truncated := len(books) > limit
		if truncated {
			books = books[:limit]
		}
		return map[string]any{"addressbooks": books, "returned": len(books), "truncated": truncated}, nil
	case "contacts.list", "contacts.search":
		return client.List(ctx, q)
	case "contacts.get":
		return client.Get(ctx, in.AddressBook, in.Path)
	default:
		return client.Mutate(ctx, m)
	}
}
func contactsTool(s spec) mcp.Tool {
	c, _ := policy.Resolve(s.command)
	opts := []mcp.ToolOption{mcp.WithDescription(s.description), mcp.WithSchemaAdditionalProperties(false), mcp.WithReadOnlyHintAnnotation(c.Capability == policy.Read), mcp.WithDestructiveHintAnnotation(c.Capability == policy.Delete || c.Capability == policy.Edit), mcp.WithIdempotentHintAnnotation(c.Capability == policy.Read), mcp.WithOpenWorldHintAnnotation(true)}
	if s.command != "addressbooks.list" {
		opts = append(opts, mcp.WithString("addressbook", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(4096)))
	}
	switch s.command {
	case "addressbooks.list", "contacts.list", "contacts.search":
		opts = append(opts, mcp.WithInteger("limit", mcp.Min(1), mcp.Max(1000)))
	}
	if s.command == "contacts.search" {
		opts = append(opts, mcp.WithString("query", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(1024)))
	}
	if s.command == "contacts.get" || s.command == "contacts.edit" || s.command == "contacts.delete" {
		opts = append(opts, mcp.WithString("path", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(8192)))
	}
	if c.Capability == policy.Create || c.Capability == policy.Edit {
		nameOptions := []mcp.PropertyOption{mcp.MinLength(1), mcp.MaxLength(10000)}
		if c.Capability == policy.Create {
			nameOptions = append(nameOptions, mcp.Required())
		}
		opts = append(opts, mcp.WithString("name", nameOptions...))
		for _, name := range []string{"given_name", "family_name", "organization", "note", "birthday"} {
			opts = append(opts, mcp.WithString(name, mcp.MaxLength(10000)))
		}
		for _, name := range []string{"emails", "phones", "urls"} {
			opts = append(opts, mcp.WithArray(name, mcp.MaxItems(50), mcp.WithStringItems(mcp.MinLength(1), mcp.MaxLength(4096))))
		}
	}
	return mcp.NewTool(s.name, opts...)
}
