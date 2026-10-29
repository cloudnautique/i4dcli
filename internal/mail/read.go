package mail

import (
	"context"
	"sort"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"i4dcli/internal/fault"
)

func (c *Client) Folders(ctx context.Context) ([]Folder, error) {
	ctx, end := c.operation(ctx)
	defer end()
	cmd := c.imap.List("", "*", nil)
	result := []Folder{}
	for m := cmd.Next(); m != nil; m = cmd.Next() {
		if len(result) >= 1000 {
			_ = c.conn.Close()
			return nil, fault.New("error", "mailbox count exceeds 1000")
		}
		f := Folder{Name: m.Mailbox, Attributes: []string{}}
		if m.Delim != 0 {
			f.Delimiter = string(m.Delim)
		}
		for _, a := range m.Attrs {
			f.Attributes = append(f.Attributes, string(a))
		}
		result = append(result, f)
	}
	if err := cmd.Close(); err != nil {
		return nil, mailError(ctx, err, "list folders")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (c *Client) selectMailbox(ctx context.Context, name string) (*imap.SelectData, error) {
	m, err := c.imap.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, mailError(ctx, err, "select mailbox")
	}
	if m.UIDValidity == 0 || m.UIDNext == 0 {
		return nil, fault.New("error", "mailbox lacks stable UID metadata")
	}
	return m, nil
}

// List returns messages in descending UID order, not sender-supplied date order.
// Four bounded UID windows per call prevent sparse searches from scanning a
// whole mailbox. Cursors preserve the scan position even for empty pages.
func (c *Client) List(ctx context.Context, o ListOptions) (Page, error) {
	empty := Page{Messages: []Summary{}}
	if err := o.Validate(); err != nil {
		return empty, err
	}
	if o.Limit == 0 {
		o.Limit = 50
	}
	var r reference
	if o.Cursor != "" {
		r, _ = decodeReference(o.Cursor)
		if r.Account != c.account {
			return empty, fault.New("conflict", "mail cursor belongs to another account")
		}
		if o.Mailbox != "" && normalizeMailbox(o.Mailbox) != r.Mailbox {
			return empty, fault.New("usage", "mailbox conflicts with mail cursor")
		}
		if o.Filter != (Filter{}) && o.Filter != r.Filter {
			return empty, fault.New("usage", "search filter conflicts with mail cursor")
		}
		o.Mailbox = r.Mailbox
		o.Filter = r.Filter
		if err := o.Validate(); err != nil {
			return empty, err
		}
	}
	if o.Mailbox == "" {
		o.Mailbox = "INBOX"
	}
	o.Mailbox = normalizeMailbox(o.Mailbox)
	ctx, end := c.operation(ctx)
	defer end()
	m, err := c.selectMailbox(ctx, o.Mailbox)
	if err != nil {
		return empty, err
	}
	before := uint32(m.UIDNext)
	if o.Cursor != "" {
		if r.Validity != m.UIDValidity {
			return empty, fault.New("conflict", "mailbox UID validity changed; restart listing")
		}
		before = r.Before
	}
	if m.NumMessages == 0 {
		return empty, nil
	}
	result := empty
	windows := 0
	for before > 1 && len(result.Messages) < o.Limit && windows < 4 {
		windows++
		hi := before - 1
		lo := uint32(1)
		if hi > 255 {
			lo = hi - 255
		}
		criteria := searchCriteria(o.Filter)
		criteria.UID = []imap.UIDSet{{imap.UIDRange{Start: imap.UID(lo), Stop: imap.UID(hi)}}}
		found, err := c.imap.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return empty, mailError(ctx, err, "search messages")
		}
		set, ok := found.All.(imap.UIDSet)
		if !ok {
			return empty, fault.New("error", "server returned invalid UID search data")
		}
		uids := []imap.UID{}
		for _, span := range set {
			if uint32(span.Start) < lo || uint32(span.Stop) > hi || span.Start > span.Stop {
				return empty, fault.New("error", "server returned UIDs outside requested window")
			}
			for uid := span.Start; ; uid++ {
				uids = append(uids, uid)
				if len(uids) > 256 {
					return empty, fault.New("error", "server returned too many UIDs")
				}
				if uid == span.Stop {
					break
				}
			}
		}
		sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })
		remaining := o.Limit - len(result.Messages)
		cut := len(uids) > remaining
		if cut {
			uids = uids[:remaining]
		}
		if len(uids) > 0 {
			buffers, err := c.imap.Fetch(imap.UIDSetNum(uids...), summaryOptions()).Collect()
			if err != nil {
				return empty, mailError(ctx, err, "fetch metadata")
			}
			wanted := map[imap.UID]bool{}
			for _, uid := range uids {
				wanted[uid] = true
			}
			for _, b := range buffers {
				if !wanted[b.UID] {
					return empty, fault.New("error", "server returned unexpected message UID")
				}
				delete(wanted, b.UID)
				result.Messages = append(result.Messages, c.summary(b, o.Mailbox, m.UIDValidity))
			}
		}
		if cut {
			before = uint32(uids[len(uids)-1])
		} else {
			before = lo
		}
	}
	sort.Slice(result.Messages, func(i, j int) bool { return result.Messages[i].UID > result.Messages[j].UID })
	result.Returned = len(result.Messages)
	if before > 1 {
		result.NextCursor = encodeReference(reference{Version: 1, Account: c.account, Mailbox: o.Mailbox, Validity: m.UIDValidity, Before: before, Filter: o.Filter})
		result.Truncated = windows >= 4 && len(result.Messages) < o.Limit
	}
	return result, nil
}

func searchCriteria(f Filter) *imap.SearchCriteria {
	c := &imap.SearchCriteria{}
	for _, kv := range [][2]string{{"From", f.From}, {"To", f.To}, {"Subject", f.Subject}} {
		if kv[1] != "" {
			c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: kv[0], Value: kv[1]})
		}
	}
	if f.Unread {
		c.NotFlag = []imap.Flag{imap.FlagSeen}
	}
	if f.Since != "" {
		c.Since, _ = time.Parse("2006-01-02", f.Since)
	}
	if f.Before != "" {
		c.Before, _ = time.Parse("2006-01-02", f.Before)
	}
	return c
}
func summaryOptions() *imap.FetchOptions {
	return &imap.FetchOptions{UID: true, Envelope: true, Flags: true, InternalDate: true, RFC822Size: true}
}
func (c *Client) summary(b *imapclient.FetchMessageBuffer, mailbox string, validity uint32) Summary {
	s := Summary{ID: encodeReference(reference{Version: 1, Account: c.account, Mailbox: mailbox, Validity: validity, UID: uint32(b.UID)}), Mailbox: mailbox, UID: uint32(b.UID), InternalDate: b.InternalDate, Size: b.RFC822Size, From: []string{}, To: []string{}, Flags: []string{}}
	if b.Envelope != nil {
		s.Subject = b.Envelope.Subject
		s.Date = b.Envelope.Date
		for _, a := range b.Envelope.From {
			if a.Addr() != "" {
				s.From = append(s.From, a.Addr())
			}
		}
		for _, a := range b.Envelope.To {
			if a.Addr() != "" {
				s.To = append(s.To, a.Addr())
			}
		}
	}
	for _, f := range b.Flags {
		s.Flags = append(s.Flags, string(f))
	}
	return s
}
