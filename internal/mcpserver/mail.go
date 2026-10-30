package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mark3labs/mcp-go/mcp"
	"i4dcli/internal/fault"
	"i4dcli/internal/mail"
	"i4dcli/internal/policy"
)

type MailClient interface {
	Folders(context.Context) ([]mail.Folder, error)
	List(context.Context, mail.ListOptions) (mail.Page, error)
	Get(context.Context, string) (mail.Message, error)
	Close() error
}
type MailFactory func(context.Context) (MailClient, error)

type mailInput struct {
	Mailbox string `json:"mailbox"`
	Cursor  string `json:"cursor"`
	ID      string `json:"id"`
	Limit   int    `json:"limit"`
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Since   string `json:"since"`
	Before  string `json:"before"`
	Unread  bool   `json:"unread"`
}

func executeMail(ctx context.Context, p policy.Policy, o Options, s spec, req mcp.CallToolRequest) (any, error) {
	if err := p.Check(s.command); err != nil {
		return nil, err
	}
	if extendedMail(s.command) {
		return executeMailExtended(ctx, p, o, s, req)
	}
	if o.DryRun {
		return nil, fault.New("usage", "dry-run does not apply to mail reads")
	}
	raw, err := json.Marshal(req.GetArguments())
	if err != nil {
		return nil, fault.Usage(err)
	}
	var in mailInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&in); err != nil {
		return nil, fault.Usage(err)
	}
	if in.Limit == 0 {
		in.Limit = min(50, o.MaxResults)
	}
	if in.Limit < 1 || in.Limit > min(100, o.MaxResults) {
		return nil, fault.New("usage", "limit must be between 1 and server max-results")
	}
	if s.command == "mail.messages.get" {
		if err := mail.ValidateID(in.ID); err != nil {
			return nil, err
		}
	}
	if s.command == "mail.messages.list" || s.command == "mail.messages.search" {
		if err := (mail.ListOptions{Mailbox: in.Mailbox, Cursor: in.Cursor, Limit: in.Limit, Filter: mail.Filter{From: in.From, To: in.To, Subject: in.Subject, Since: in.Since, Before: in.Before, Unread: in.Unread}}).Validate(); err != nil {
			return nil, err
		}
	}
	if o.MailFactory == nil {
		return nil, fault.New("config", "mail is not configured")
	}
	c, err := o.MailFactory(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	switch s.command {
	case "mail.folders.list":
		folders, err := c.Folders(ctx)
		if err != nil {
			return nil, err
		}
		if len(folders) > o.MaxResults {
			return nil, fault.New("error", "folder count exceeds server max-results")
		}
		return folders, nil
	case "mail.messages.get":
		return c.Get(ctx, in.ID)
	default:
		return c.List(ctx, mail.ListOptions{Mailbox: in.Mailbox, Cursor: in.Cursor, Limit: in.Limit, Filter: mail.Filter{From: in.From, To: in.To, Subject: in.Subject, Since: in.Since, Before: in.Before, Unread: in.Unread}})
	}
}
func mailTool(s spec) mcp.Tool {
	if extendedMail(s.command) {
		return extendedMailTool(s)
	}
	opts := []mcp.ToolOption{mcp.WithDescription(s.description), mcp.WithSchemaAdditionalProperties(false), mcp.WithReadOnlyHintAnnotation(true), mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(true)}
	switch s.command {
	case "mail.messages.get":
		opts = append(opts, mcp.WithString("id", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(8192)))
	case "mail.messages.list", "mail.messages.search":
		opts = append(opts, mcp.WithString("mailbox", mcp.MinLength(1), mcp.MaxLength(1024)), mcp.WithString("cursor", mcp.MinLength(1), mcp.MaxLength(8192)), mcp.WithInteger("limit", mcp.Min(1), mcp.Max(100)))
		if s.command == "mail.messages.search" {
			for _, key := range []string{"from", "to", "subject", "since", "before"} {
				opts = append(opts, mcp.WithString(key, mcp.MinLength(1), mcp.MaxLength(1024)))
			}
			opts = append(opts, mcp.WithBoolean("unread"))
		}
	}
	return mcp.NewTool(s.name, opts...)
}
