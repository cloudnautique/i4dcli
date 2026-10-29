// Package mail provides bounded, read-only access to iCloud Mail over IMAP.
package mail

import "time"

type Options struct{ Username, Password, AccountID string }
type Filter struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Subject string `json:"subject,omitempty"`
	Since   string `json:"since,omitempty"`
	Before  string `json:"before,omitempty"`
	Unread  bool   `json:"unread,omitempty"`
}
type ListOptions struct {
	Mailbox string `json:"mailbox,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Filter  Filter `json:"filter"`
}
type Folder struct {
	Name       string   `json:"name"`
	Delimiter  string   `json:"delimiter"`
	Attributes []string `json:"attributes"`
}
type Summary struct {
	ID           string    `json:"id"`
	Mailbox      string    `json:"mailbox"`
	UID          uint32    `json:"uid"`
	Subject      string    `json:"subject"`
	From         []string  `json:"from"`
	To           []string  `json:"to"`
	Date         time.Time `json:"date"`
	InternalDate time.Time `json:"internal_date"`
	Flags        []string  `json:"flags"`
	Size         int64     `json:"size"`
}
type Page struct {
	Messages   []Summary `json:"messages"`
	Returned   int       `json:"returned"`
	NextCursor string    `json:"next_cursor,omitempty"`
	Truncated  bool      `json:"truncated"`
}
type Attachment struct {
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}
type Message struct {
	Summary
	Text          string       `json:"text,omitempty"`
	HTML          string       `json:"html,omitempty"`
	Attachments   []Attachment `json:"attachments"`
	BodyTruncated bool         `json:"body_truncated"`
}
