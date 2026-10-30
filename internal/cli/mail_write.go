package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	netmail "net/mail"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
	"i4dcli/internal/config"
	"i4dcli/internal/fault"
	"i4dcli/internal/mail"
	"i4dcli/internal/policy"
)

func mailSMTPOptions(g globals, account config.Account, noInput bool, stderr io.Writer) (mail.SMTPOptions, error) {
	settings, err := mailSettings(g, account)
	if err != nil {
		return mail.SMTPOptions{}, err
	}
	password, err := credentials(settings.SMTPUsername, noInput, stderr)
	if err != nil {
		return mail.SMTPOptions{}, err
	}
	return mail.SMTPOptions{Username: settings.SMTPUsername, Password: password}, nil
}

func confirmMail(in *os.File, out io.Writer, description string, noInput, force bool) error {
	if force {
		return nil
	}
	if noInput || !term.IsTerminal(int(in.Fd())) {
		return fault.New("confirmation_required", "%s requires confirmation; pass --force to authorize unattended execution", clean(description))
	}
	return readMailConfirmation(in, out, description)
}
func readMailConfirmation(in io.Reader, out io.Writer, description string) error {
	fmt.Fprintf(out, "%s? Type yes: ", clean(description))
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil || strings.TrimSpace(answer) != "yes" {
		return fault.New("cancelled", "mail operation cancelled")
	}
	return nil
}

// Attachment paths come only from explicit CLI flags, never remote filenames.
func loadMailAttachments(paths []string) ([]mail.OutgoingAttachment, error) {
	if len(paths) > 20 {
		return nil, fault.New("usage", "at most 20 outgoing attachments are allowed")
	}
	result := []mail.OutgoingAttachment{}
	remaining := int64(1 << 20)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fault.New("usage", "read attachment: %s", err)
		}
		if !info.Mode().IsRegular() || info.Size() > remaining {
			return nil, fault.New("usage", "attachments must be regular files totaling at most 1 MiB")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, fault.New("usage", "read attachment: %s", err)
		}
		data, err := io.ReadAll(io.LimitReader(f, remaining+1))
		_ = f.Close()
		if err != nil {
			return nil, fault.New("usage", "read attachment: %s", err)
		}
		if int64(len(data)) > remaining {
			return nil, fault.New("usage", "attachments exceed 1 MiB")
		}
		remaining -= int64(len(data))
		result = append(result, mail.OutgoingAttachment{Filename: filepath.Base(path), Data: data})
	}
	return result, nil
}

func mailPreview(out io.Writer, c policy.Command, o *mailOptions, g globals) error {
	request := map[string]any{"id": o.id, "destination": o.destination, "changes": o.changes, "from": o.compose.From, "to": o.to, "cc": o.cc, "bcc": o.bcc, "subject": o.compose.Subject, "body": o.compose.Text, "html": o.compose.HTML, "attachment_paths": o.attachments, "attachment_index": o.attachment, "output": o.output, "reply_all": o.replyAll}
	return printMailValue(out, map[string]any{"dry_run": true, "action": c.Name, "validation": "local", "remote_checked": false, "request": request, "note": "Remote identity, source content, recipients derived from a source message, mailbox capabilities, and server permissions have not been checked."}, g)
}

func printMailValue(out io.Writer, value any, g globals) error {
	if g.asJSON {
		return json.NewEncoder(out).Encode(value)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if g.plain {
		_, err = fmt.Fprintf(out, "RESULT_JSON\n%s\n", data)
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func writeMailAttachment(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return fault.New("conflict", "output already exists; choose a new --output path")
	}
	if err != nil {
		return err
	}
	info, _ := f.Stat()
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		if current, err := os.Lstat(path); err == nil && info != nil && os.SameFile(info, current) {
			_ = os.Remove(path)
		}
	}
	return writeErr
}

func runMailAction(ctx context.Context, c policy.Command, o *mailOptions, fs *flag.FlagSet, g globals, p policy.Policy, out, stderr io.Writer) error {
	supplied := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	if fs.Lookup("id") != nil {
		if err := mail.ValidateID(o.id); err != nil {
			return err
		}
	}
	flags := mail.FlagChanges{}
	if supplied["seen"] {
		flags.Seen = &o.seen
	}
	if supplied["flagged"] {
		flags.Flagged = &o.flagged
	}
	o.changes = flags
	if c.Name == "mail.messages.mark" {
		if err := flags.Validate(); err != nil {
			return err
		}
	}
	if c.Name == "mail.messages.move" || o.destination != "" {
		if err := mail.ValidateMailbox(o.destination); err != nil {
			return err
		}
	}
	if supplied["mailbox"] && o.destination == "" {
		return fault.New("usage", "--mailbox cannot be empty")
	}
	if c.Name == "mail.attachments.get" {
		if o.attachment < 0 || o.attachment >= 100 || o.output == "" {
			return fault.New("usage", "provide --attachment index (0–99) and --output filename")
		}
		if _, err := os.Lstat(o.output); err == nil {
			return fault.New("conflict", "output already exists; choose a new --output path")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	isSend := c.Capability == policy.Send
	destructive := c.Capability == policy.Delete
	if g.force && !isSend && !destructive {
		return fault.New("usage", "--force only applies to sending or destructive mail operations")
	}
	if g.dryRun && c.Name == "mail.drafts.get" {
		return fault.New("usage", "--dry-run does not apply to draft inspection")
	}
	o.compose.To = append([]string(nil), o.to...)
	o.compose.Cc = append([]string(nil), o.cc...)
	o.compose.Bcc = append([]string(nil), o.bcc...)
	if g.dryRun && (c.Name == "mail.messages.mark" || destructive || c.Name == "mail.attachments.get" || c.Name == "mail.drafts.send") {
		return mailPreview(out, c, o, g)
	}
	account, err := configuredAccount(&g)
	if err != nil {
		return err
	}
	settings, err := mailSettings(g, account)
	if err != nil {
		return err
	}
	o.compose.From = settings.Address
	o.compose.Attachments, err = loadMailAttachments(o.attachments)
	if err != nil {
		return err
	}
	switch c.Name {
	case "mail.drafts.create", "mail.drafts.edit":
		if err := o.compose.ValidateDraft(); err != nil {
			return err
		}
	case "mail.send":
		if err := o.compose.Validate(); err != nil {
			return err
		}
	case "mail.reply":
		if o.compose.Text == "" {
			return fault.New("usage", "--body is required for reply")
		}
		check := o.compose
		check.To = []string{settings.Address}
		check.Subject = "Reply"
		if err := check.Validate(); err != nil {
			return err
		}
	case "mail.forward":
		check := o.compose
		check.Subject = "Forward"
		if check.Text == "" {
			check.Text = "Forward"
		}
		if err := check.Validate(); err != nil {
			return err
		}
	}
	var draftRaw []byte
	if c.Name == "mail.drafts.create" || c.Name == "mail.drafts.edit" {
		draftRaw, err = mail.BuildDraft(o.compose)
		if err != nil {
			return err
		}
		if err := mail.ValidateDraft(draftRaw); err != nil {
			return err
		}
	}
	if g.dryRun {
		return mailPreview(out, c, o, g)
	}

	if destructive {
		description := c.Name + " " + o.id
		if c.Name == "mail.messages.delete" || c.Name == "mail.drafts.delete" {
			description = "Permanently delete " + o.id
		}
		if o.destination != "" {
			description += " to " + o.destination
		}
		if err := confirmMail(os.Stdin, stderr, description, p.NoInput(), g.force); err != nil {
			return err
		}
	}
	if isSend && (p.NoInput() || !term.IsTerminal(int(os.Stdin.Fd()))) && !g.force {
		return fault.New("confirmation_required", "sending requires confirmation; pass --force to authorize unattended sending")
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	if c.Name == "mail.send" {
		prepared, err := mail.BuildMessage(o.compose)
		if err != nil {
			return err
		}
		return submitMail(ctx, prepared, g, account, p, out, stderr, false)
	}
	opts, err := mailCredentials(g, account, p.NoInput(), stderr)
	if err != nil {
		return err
	}
	opts.AllowWrite = c.Capability == policy.Create || c.Capability == policy.Edit || destructive
	client, err := mail.Dial(ctx, opts)
	if err != nil {
		return err
	}
	defer client.Close()
	var result mail.MutationResult
	switch c.Name {
	case "mail.messages.mark":
		result, err = client.Mark(ctx, o.id, flags)
	case "mail.messages.move":
		result, err = client.Move(ctx, o.id, o.destination)
	case "mail.messages.trash":
		result, err = client.Trash(ctx, o.id, o.destination)
	case "mail.messages.delete":
		result, err = client.Delete(ctx, o.id)
	case "mail.drafts.delete":
		result, err = client.DeleteDraft(ctx, o.id)
	case "mail.drafts.get":
		message, err := client.GetDraft(ctx, o.id)
		if err != nil {
			return err
		}
		return printMailMessage(out, message, g)
	case "mail.attachments.get":
		attachment, err := client.Attachment(ctx, o.id, o.attachment)
		if err != nil {
			return err
		}
		if err := writeMailAttachment(o.output, attachment.Data); err != nil {
			return err
		}
		return printMailValue(out, map[string]any{"output": o.output, "bytes": len(attachment.Data), "filename": attachment.Filename, "content_type": attachment.ContentType}, g)
	case "mail.drafts.create", "mail.drafts.edit":
		if c.Name == "mail.drafts.create" {
			result, err = client.CreateDraft(ctx, o.destination, draftRaw)
		} else {
			result, err = client.EditDraft(ctx, o.id, draftRaw)
		}
	case "mail.drafts.send":
		raw, err := client.RawDraft(ctx, o.id)
		if err != nil {
			return err
		}
		prepared, err := mail.PrepareDraft(raw, settings.Address)
		if err != nil {
			return err
		}
		return submitMail(ctx, prepared, g, account, p, out, stderr, true)
	case "mail.reply", "mail.forward":
		original, err := client.Get(ctx, o.id)
		if err != nil {
			return err
		}
		var compose mail.Compose
		if c.Name == "mail.reply" {
			compose, err = mail.Reply(original, settings.Address, o.compose.Text, o.replyAll)
		} else {
			compose, err = mail.Forward(original, settings.Address, o.compose.Text, o.to)
		}
		if err != nil {
			return err
		}
		if c.Name == "mail.forward" {
			for _, metadata := range original.Attachments {
				attachment, err := client.Attachment(ctx, o.id, metadata.Index)
				if err != nil {
					return err
				}
				filename := attachment.Filename
				if filename == "" || len(filename) > 255 || strings.ContainsAny(filename, "/\\\r\n\x00") {
					filename = fmt.Sprintf("attachment-%d", metadata.Index)
				}
				compose.Attachments = append(compose.Attachments, mail.OutgoingAttachment{Filename: filename, ContentType: attachment.ContentType, Data: attachment.Data})
			}
		}
		compose.Attachments = append(compose.Attachments, o.compose.Attachments...)
		prepared, err := mail.BuildMessage(compose)
		if err != nil {
			return err
		}
		return submitMail(ctx, prepared, g, account, p, out, stderr, false)
	default:
		return fault.New("usage", "unsupported mail action")
	}
	if err != nil {
		if result.Completed || result.Partial || result.OutcomeUnknown {
			if outputErr := printMailValue(out, map[string]any{"result": result, "error": fault.Describe(err)}, g); outputErr != nil {
				return outputErr
			}
		}
		return err
	}
	return printMailValue(out, result, g)
}

func submitMail(ctx context.Context, prepared mail.PreparedMessage, g globals, account config.Account, p policy.Policy, out, stderr io.Writer, keepDraft bool) error {
	description := "Send mail from " + prepared.From + " to " + strings.Join(prepared.Recipients, ", ")
	if message, err := netmail.ReadMessage(bytes.NewReader(prepared.Data)); err == nil {
		subject := message.Header.Get("Subject")
		if decoded, err := new(mime.WordDecoder).DecodeHeader(subject); err == nil {
			subject = decoded
		}
		description += " with subject " + subject
	}
	if err := confirmMail(os.Stdin, stderr, description, p.NoInput(), g.force); err != nil {
		return err
	}
	opts, err := mailSMTPOptions(g, account, p.NoInput(), stderr)
	if err != nil {
		return err
	}
	result, err := mail.Send(ctx, opts, prepared)
	if err != nil {
		return err
	}
	if keepDraft {
		result.Note += " Original draft retained; do not send it again unless another delivery is intended."
	}
	return printMailValue(out, result, g)
}
