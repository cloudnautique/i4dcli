package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/mark3labs/mcp-go/mcp"
	"i4dcli/internal/fault"
	"i4dcli/internal/mail"
	"i4dcli/internal/policy"
	"strings"
)

func extendedMail(command string) bool {
	return command != "mail.folders.list" && command != "mail.messages.list" && command != "mail.messages.get" && command != "mail.messages.search"
}

type MailWriter interface {
	Mark(context.Context, string, mail.FlagChanges) (mail.MutationResult, error)
	Move(context.Context, string, string) (mail.MutationResult, error)
	Trash(context.Context, string, string) (mail.MutationResult, error)
	Delete(context.Context, string) (mail.MutationResult, error)
	CreateDraft(context.Context, string, []byte) (mail.MutationResult, error)
	EditDraft(context.Context, string, []byte) (mail.MutationResult, error)
	DeleteDraft(context.Context, string) (mail.MutationResult, error)
	GetDraft(context.Context, string) (mail.Message, error)
	RawDraft(context.Context, string) ([]byte, error)
	Attachment(context.Context, string, int) (mail.AttachmentData, error)
}
type mailWriteInput struct {
	ID          string       `json:"id"`
	Destination string       `json:"destination"`
	Mailbox     string       `json:"mailbox"`
	Seen        *bool        `json:"seen"`
	Flagged     *bool        `json:"flagged"`
	Attachment  int          `json:"attachment"`
	ReplyAll    bool         `json:"reply_all"`
	Message     mail.Compose `json:"message"`
}

func executeMailExtended(ctx context.Context, p policy.Policy, o Options, s spec, req mcp.CallToolRequest) (any, error) {
	command, _ := policy.Resolve(s.command)
	if command.Capability != policy.Read && !o.AllowWrite {
		return nil, fault.New("permission_denied", "mail writes require --allow-write")
	}
	if command.Capability == policy.Send && !o.AllowSend {
		return nil, fault.New("permission_denied", "mail submission requires --allow-send")
	}
	if command.Capability == policy.Delete && !o.AllowDelete {
		return nil, fault.New("permission_denied", "mail deletion requires --allow-delete")
	}
	raw, err := json.Marshal(req.GetArguments())
	if err != nil {
		return nil, fault.Usage(err)
	}
	var in mailWriteInput
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&in); err != nil {
		return nil, fault.Usage(err)
	}
	needsID := s.command != "mail.send" && s.command != "mail.drafts.create"
	if needsID {
		if err = mail.ValidateID(in.ID); err != nil {
			return nil, err
		}
	}
	if in.Destination != "" {
		if err := mail.ValidateMailbox(in.Destination); err != nil {
			return nil, err
		}
	}
	if in.Mailbox != "" {
		if err := mail.ValidateMailbox(in.Mailbox); err != nil {
			return nil, err
		}
	}
	if in.Attachment < 0 {
		return nil, fault.New("usage", "attachment index must be nonnegative")
	}
	if s.command == "mail.messages.mark" && in.Seen == nil && in.Flagged == nil {
		return nil, fault.New("usage", "seen or flagged is required")
	}
	if s.command == "mail.messages.move" && strings.TrimSpace(in.Destination) == "" {
		return nil, fault.New("usage", "destination is required")
	}
	compose := s.command == "mail.send" || s.command == "mail.drafts.create" || s.command == "mail.drafts.edit"
	if compose || command.Capability == policy.Send {
		if o.MailIdentity == nil {
			return nil, fault.New("config", "mail sender is not configured")
		}
		in.Message.From, err = o.MailIdentity()
		if err != nil {
			return nil, err
		}
	}
	if s.command == "mail.forward" && len(in.Message.To) == 0 {
		return nil, fault.New("usage", "forward requires recipients")
	}
	if s.command == "mail.reply" && in.Message.Text == "" {
		return nil, fault.New("usage", "reply requires text")
	}
	if s.command == "mail.reply" || s.command == "mail.forward" {
		if err = in.Message.ValidateDraft(); err != nil {
			return nil, err
		}
	}
	if compose {
		if strings.HasPrefix(s.command, "mail.drafts.") {
			err = in.Message.ValidateDraft()
		} else {
			err = in.Message.Validate()
		}
		if err != nil {
			return nil, err
		}
	}
	if s.command == "mail.drafts.create" || s.command == "mail.drafts.edit" {
		raw, err := mail.BuildDraft(in.Message)
		if err != nil {
			return nil, err
		}
		if err = mail.ValidateDraft(raw); err != nil {
			return nil, err
		}
	}
	if o.DryRun {
		if command.Capability == policy.Read {
			return nil, fault.New("usage", "dry-run requires a mutation")
		}
		return map[string]any{"action": s.command, "dry_run": true, "validation": "local", "remote_checked": false, "request": in}, nil
	}
	if (command.Capability == policy.Delete || command.Capability == policy.Send) && !o.Force {
		return nil, fault.New("confirmation_required", "operator --force is required for mail deletion or sending")
	}
	if s.command == "mail.send" {
		prepared, err := mail.BuildMessage(in.Message)
		if err != nil {
			return nil, err
		}
		if o.MailSubmit == nil {
			return nil, fault.New("config", "SMTP is not configured")
		}
		return o.MailSubmit(ctx, prepared)
	}
	if o.MailFactory == nil {
		return nil, fault.New("config", "mail is not configured")
	}
	c, err := o.MailFactory(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	w, ok := c.(MailWriter)
	if !ok {
		return nil, fault.New("config", "mail backend does not support this operation")
	}
	switch s.command {
	case "mail.messages.mark":
		return w.Mark(ctx, in.ID, mail.FlagChanges{Seen: in.Seen, Flagged: in.Flagged})
	case "mail.messages.move":
		return w.Move(ctx, in.ID, in.Destination)
	case "mail.messages.trash":
		return w.Trash(ctx, in.ID, in.Destination)
	case "mail.messages.delete":
		return w.Delete(ctx, in.ID)
	case "mail.attachments.get":
		return w.Attachment(ctx, in.ID, in.Attachment)
	case "mail.drafts.get":
		return w.GetDraft(ctx, in.ID)
	case "mail.drafts.delete":
		return w.DeleteDraft(ctx, in.ID)
	case "mail.drafts.create", "mail.drafts.edit":
		data, err := mail.BuildDraft(in.Message)
		if err != nil {
			return nil, err
		}
		if s.command == "mail.drafts.create" {
			return w.CreateDraft(ctx, in.Mailbox, data)
		}
		return w.EditDraft(ctx, in.ID, data)
	case "mail.drafts.send":
		data, err := w.RawDraft(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		prepared, err := mail.PrepareDraft(data, in.Message.From)
		if err != nil {
			return nil, err
		}

		if o.MailSubmit == nil {
			return nil, fault.New("config", "SMTP is not configured")
		}
		result, err := o.MailSubmit(ctx, prepared)
		if err == nil {
			result.Note += " Original draft retained; do not resubmit unless another delivery is intended."
		}
		return result, err
	case "mail.reply", "mail.forward":
		original, err := c.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		var message mail.Compose
		if s.command == "mail.reply" {
			message, err = mail.Reply(original, in.Message.From, in.Message.Text, in.ReplyAll)
		} else {
			message, err = mail.Forward(original, in.Message.From, in.Message.Text, in.Message.To)
		}
		if err != nil {
			return nil, err
		}
		if s.command == "mail.forward" {
			for _, attachment := range original.Attachments {
				data, err := w.Attachment(ctx, in.ID, attachment.Index)
				if err != nil {
					return nil, err
				}
				filename := data.Filename
				if filename == "" || strings.ContainsAny(filename, "/\\\r\n\x00") {
					filename = fmt.Sprintf("attachment-%d", attachment.Index)
				}
				message.Attachments = append(message.Attachments, mail.OutgoingAttachment{Filename: filename, ContentType: data.ContentType, Data: data.Data})
			}
		}
		message.Attachments = append(message.Attachments, in.Message.Attachments...)
		message.Cc = append(message.Cc, in.Message.Cc...)
		message.Bcc = in.Message.Bcc
		prepared, err := mail.BuildMessage(message)
		if err != nil {
			return nil, err
		}
		if o.MailSubmit == nil {
			return nil, fault.New("config", "SMTP is not configured")
		}
		return o.MailSubmit(ctx, prepared)
	}
	return nil, fault.New("usage", "unknown mail operation")
}
func extendedMailTool(s spec) mcp.Tool {
	c, _ := policy.Resolve(s.command)
	opts := []mcp.ToolOption{mcp.WithDescription(s.description), mcp.WithSchemaAdditionalProperties(false), mcp.WithReadOnlyHintAnnotation(c.Capability == policy.Read), mcp.WithDestructiveHintAnnotation(c.Capability == policy.Delete), mcp.WithIdempotentHintAnnotation(c.Capability == policy.Read), mcp.WithOpenWorldHintAnnotation(true)}
	if s.command != "mail.send" && s.command != "mail.drafts.create" {
		opts = append(opts, mcp.WithString("id", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(8192)))
	}
	switch s.command {
	case "mail.messages.mark":
		opts = append(opts, mcp.WithBoolean("seen"), mcp.WithBoolean("flagged"))
	case "mail.messages.move":
		opts = append(opts, mcp.WithString("destination", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(1024)))
	case "mail.messages.trash":
		opts = append(opts, mcp.WithString("destination", mcp.MinLength(1), mcp.MaxLength(1024)))
	case "mail.attachments.get":
		opts = append(opts, mcp.WithInteger("attachment", mcp.Required(), mcp.Min(0)))
	case "mail.drafts.create":
		opts = append(opts, mcp.WithString("mailbox", mcp.MinLength(1), mcp.MaxLength(1024)))
	}
	if s.command == "mail.reply" {
		opts = append(opts, mcp.WithBoolean("reply_all"))
	}
	if s.command == "mail.send" || s.command == "mail.reply" || s.command == "mail.forward" || s.command == "mail.drafts.create" || s.command == "mail.drafts.edit" {
		props := map[string]any{}
		for _, k := range []string{"subject", "text", "html"} {
			props[k] = map[string]any{"type": "string", "maxLength": 2 << 20}
		}
		for _, k := range []string{"to", "cc", "bcc"} {
			props[k] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 320}, "maxItems": 100}
		}
		props["attachments"] = map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"filename": map[string]any{"type": "string"}, "content_type": map[string]any{"type": "string"}, "data": map[string]any{"type": "string", "maxLength": 3 << 20}}}}
		opts = append(opts, mcp.WithObject("message", mcp.Required(), mcp.Properties(props), mcp.AdditionalProperties(false)))
	}
	return mcp.NewTool(s.name, opts...)
}
