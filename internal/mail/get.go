package mail

import (
	"bytes"
	"context"
	"io"
	netmail "net/mail"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	mime "github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	"i4dcli/internal/fault"
)

const maxRawBody = 2 << 20
const maxDecodedBody = 1 << 20

func (c *Client) Get(ctx context.Context, id string) (Message, error) {
	msg, raw, err := c.getRaw(ctx, id, false)
	if err != nil {
		return Message{}, err
	}
	if err := decodeBody(raw, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}
func (c *Client) GetDraft(ctx context.Context, id string) (Message, error) {
	msg, raw, err := c.getRaw(ctx, id, true)
	if err != nil {
		return Message{}, err
	}
	if err := decodeBody(raw, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}
func (c *Client) RawDraft(ctx context.Context, id string) ([]byte, error) {
	_, raw, err := c.getRaw(ctx, id, true)
	return raw, err
}
func (c *Client) Attachment(ctx context.Context, id string, index int) (AttachmentData, error) {
	if index < 0 || index >= 100 {
		return AttachmentData{}, fault.New("usage", "attachment index must be 0–99")
	}
	msg, raw, err := c.getRaw(ctx, id, false)
	if err != nil {
		return AttachmentData{}, err
	}
	return decodeContent(raw, &msg, index)
}
func (c *Client) getRaw(ctx context.Context, id string, draft bool) (Message, []byte, error) {
	empty := Message{}
	if err := ValidateID(id); err != nil {
		return empty, nil, err
	}
	r, _ := decodeReference(id)
	if r.Account != c.account {
		return empty, nil, fault.New("conflict", "mail message ID belongs to another account")
	}
	ctx, end := c.operation(ctx)
	defer end()
	object, err := c.selectObject(ctx, r, false, draft)
	if err != nil {
		return empty, nil, err
	}
	metadata := []*imapclient.FetchMessageBuffer{object}
	uidset := imap.UIDSetNum(imap.UID(r.UID))
	if metadata[0].RFC822Size > maxRawBody {
		return empty, nil, fault.New("error", "message exceeds 2 MiB raw body limit")
	}
	msg := Message{Summary: c.summary(metadata[0], r.Mailbox, r.Validity), Attachments: []Attachment{}}
	section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: maxRawBody + 1}}
	cmd := c.imap.Fetch(uidset, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}})
	var raw []byte
	seen := false
	for data := cmd.Next(); data != nil; data = cmd.Next() {
		gotUID := imap.UID(0)
		for item := data.Next(); item != nil; item = data.Next() {
			switch item := item.(type) {
			case imapclient.FetchItemDataUID:
				gotUID = item.UID
			case imapclient.FetchItemDataBodySection:
				if seen || item.Literal == nil {
					_ = c.conn.Close()
					return empty, nil, fault.New("error", "unexpected message body response")
				}
				seen = true
				raw, err = io.ReadAll(io.LimitReader(item.Literal, maxRawBody+1))
				if err != nil {
					_ = c.conn.Close()
					return empty, nil, mailError(ctx, err, "fetch body")
				}
				if len(raw) > maxRawBody {
					_ = c.conn.Close()
					return empty, nil, fault.New("error", "message exceeds 2 MiB raw body limit")
				}
			}
		}
		if uint32(gotUID) != r.UID {
			_ = c.conn.Close()
			return empty, nil, fault.New("error", "server returned unexpected message UID")
		}
	}
	if err := cmd.Close(); err != nil {
		return empty, nil, mailError(ctx, err, "fetch body")
	}
	if !seen {
		return empty, nil, fault.New("not_found", "mail message no longer exists")
	}
	if metadata[0].RFC822Size > 0 && int64(len(raw)) != metadata[0].RFC822Size {
		return empty, nil, fault.New("error", "server returned incomplete message body")
	}
	return msg, raw, nil
}

func decodeBody(raw []byte, msg *Message) error { _, err := decodeContent(raw, msg, -1); return err }
func decodeContent(raw []byte, msg *Message, attachmentIndex int) (AttachmentData, error) {
	var selected AttachmentData

	entity, err := mime.ReadWithOptions(bytes.NewReader(raw), &mime.ReadOptions{MaxHeaderBytes: 64 << 10})
	if err != nil {
		return selected, fault.New("error", "invalid message MIME or charset")
	}
	header, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return selected, fault.New("error", "invalid message headers")
	}
	msg.MessageID = header.Header.Get("Message-ID")
	msg.InReplyTo = header.Header.Get("In-Reply-To")
	msg.References = strings.Fields(header.Header.Get("References"))
	for _, field := range []struct {
		name string
		dst  *[]string
	}{{"Reply-To", &msg.ReplyTo}, {"Cc", &msg.Cc}} {
		addresses, err := header.Header.AddressList(field.name)
		if err == nil {
			for _, address := range addresses {
				*field.dst = append(*field.dst, address.Address)
			}
		}
	}
	addAttachment := func(a Attachment, data []byte) {
		a.Index = len(msg.Attachments)
		msg.Attachments = append(msg.Attachments, a)
		if a.Index == attachmentIndex {
			selected = AttachmentData{Attachment: a, Data: data}
		}
	}
	parts := 0
	remaining := int64(maxDecodedBody)
	var walk func(*mime.Entity, int) error
	walk = func(e *mime.Entity, depth int) error {
		parts++
		if depth > 10 || parts > 100 {
			return fault.New("error", "message MIME nesting or part limit exceeded")
		}
		if multi := e.MultipartReader(); multi != nil {
			for {
				child, err := multi.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					return fault.New("error", "invalid multipart message or charset")
				}
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		contentType, _, err := e.Header.ContentType()
		if err != nil {
			return fault.New("error", "invalid message content type")
		}
		disposition, params, _ := e.Header.ContentDisposition()
		data, err := io.ReadAll(io.LimitReader(e.Body, remaining+1))
		if err != nil {
			return fault.New("error", "invalid message transfer encoding")
		}
		remaining -= int64(len(data))
		if remaining < 0 {
			return fault.New("error", "message exceeds 1 MiB decoded body limit")
		}
		if disposition == "attachment" || params["filename"] != "" || (!strings.HasPrefix(contentType, "text/") && contentType != "") {
			addAttachment(Attachment{Filename: params["filename"], ContentType: contentType, Size: int64(len(data))}, data)
			return nil
		}
		switch contentType {
		case "text/plain", "":
			msg.Text += string(data)
		case "text/html":
			msg.HTML += string(data)
		default:
			addAttachment(Attachment{ContentType: contentType, Size: int64(len(data))}, data)
		}
		return nil
	}
	if err := walk(entity, 0); err != nil {
		return AttachmentData{}, err
	}
	if attachmentIndex >= len(msg.Attachments) {
		return AttachmentData{}, fault.New("not_found", "attachment index does not exist")
	}
	return selected, nil
}
