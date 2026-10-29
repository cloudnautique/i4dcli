package mail

import (
	"bytes"
	"context"
	"io"
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
	empty := Message{}
	if err := ValidateID(id); err != nil {
		return empty, err
	}
	r, _ := decodeReference(id)
	if r.Account != c.account {
		return empty, fault.New("conflict", "mail message ID belongs to another account")
	}
	ctx, end := c.operation(ctx)
	defer end()
	m, err := c.selectMailbox(ctx, r.Mailbox)
	if err != nil {
		return empty, err
	}
	if r.Validity != m.UIDValidity {
		return empty, fault.New("conflict", "mailbox UID validity changed; list messages again")
	}
	uidset := imap.UIDSetNum(imap.UID(r.UID))
	metadata, err := c.imap.Fetch(uidset, summaryOptions()).Collect()
	if err != nil {
		return empty, mailError(ctx, err, "fetch metadata")
	}
	if len(metadata) == 0 {
		return empty, fault.New("not_found", "mail message no longer exists")
	}
	if len(metadata) != 1 || uint32(metadata[0].UID) != r.UID {
		return empty, fault.New("error", "server returned unexpected message UID")
	}
	if metadata[0].RFC822Size > maxRawBody {
		return empty, fault.New("error", "message exceeds 2 MiB raw body limit")
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
					return empty, fault.New("error", "unexpected message body response")
				}
				seen = true
				raw, err = io.ReadAll(io.LimitReader(item.Literal, maxRawBody+1))
				if err != nil {
					_ = c.conn.Close()
					return empty, mailError(ctx, err, "fetch body")
				}
				if len(raw) > maxRawBody {
					_ = c.conn.Close()
					return empty, fault.New("error", "message exceeds 2 MiB raw body limit")
				}
			}
		}
		if uint32(gotUID) != r.UID {
			_ = c.conn.Close()
			return empty, fault.New("error", "server returned unexpected message UID")
		}
	}
	if err := cmd.Close(); err != nil {
		return empty, mailError(ctx, err, "fetch body")
	}
	if !seen {
		return empty, fault.New("not_found", "mail message no longer exists")
	}
	if metadata[0].RFC822Size > 0 && int64(len(raw)) != metadata[0].RFC822Size {
		return empty, fault.New("error", "server returned incomplete message body")
	}
	if err := decodeBody(raw, &msg); err != nil {
		return empty, err
	}
	return msg, nil
}

func decodeBody(raw []byte, msg *Message) error {
	entity, err := mime.ReadWithOptions(bytes.NewReader(raw), &mime.ReadOptions{MaxHeaderBytes: 64 << 10})
	if err != nil {
		return fault.New("error", "invalid message MIME or charset")
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
			msg.Attachments = append(msg.Attachments, Attachment{Filename: params["filename"], ContentType: contentType, Size: int64(len(data))})
			return nil
		}
		switch contentType {
		case "text/plain", "":
			msg.Text += string(data)
		case "text/html":
			msg.HTML += string(data)
		default:
			msg.Attachments = append(msg.Attachments, Attachment{ContentType: contentType, Size: int64(len(data))})
		}
		return nil
	}
	return walk(entity, 0)
}
