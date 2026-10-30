package mail

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"strings"
	"testing"
)

func TestComposeMIMEAndBcc(t *testing.T) {
	c := Compose{From: "Sender <sender@example.com>", To: []string{"recipient@example.com"}, Bcc: []string{"hidden@example.com"}, Subject: "Café", Text: "plain text", HTML: "<p>HTML</p>", Attachments: []OutgoingAttachment{{Filename: "notes.txt", Data: []byte("attachment")}}}
	p, err := BuildMessage(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Recipients) != 2 || bytes.Contains(p.Data, []byte("hidden@example.com")) {
		t.Fatal("Bcc leaked or omitted from envelope")
	}
	m, err := netmail.ReadMessage(bytes.NewReader(p.Data))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != "Café" {
		t.Fatal(subject, err)
	}
	typ, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if typ != "multipart/mixed" {
		t.Fatal(typ)
	}
	r := multipart.NewReader(m.Body, params["boundary"])
	body, err := r.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body.Header.Get("Content-Type"), "multipart/alternative") {
		t.Fatal(body.Header)
	}
	a, err := r.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, a))
	if string(data) != "attachment" {
		t.Fatal(string(data))
	}
	raw, err := BuildDraft(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("hidden@example.com")) {
		t.Fatal("draft Bcc lost")
	}
	prepared, err := PrepareDraft(raw, "sender@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Recipients) != 2 || bytes.Contains(prepared.Data, []byte("hidden@example.com")) {
		t.Fatal("draft Bcc handling")
	}
	if _, err = PrepareDraft(raw, "other@example.com"); err == nil {
		t.Fatal("foreign sender accepted")
	}
}
func TestComposeValidation(t *testing.T) {
	valid := Compose{From: "sender@example.com", To: []string{"to@example.com"}, Subject: "Hi", Text: "Body"}
	cases := []Compose{valid, valid, valid, valid, valid, valid}
	cases[0].Subject = "hello\r\nBcc: attacker@example.com"
	cases[1].To = []string{"victim@example.com\nCc: attacker@example.com"}
	cases[2].Text = ""
	cases[3].Attachments = []OutgoingAttachment{{Filename: "../file"}}
	cases[4].References = []string{"bad\nheader"}
	cases[5].Text = strings.Repeat("x", MaxOutgoingBytes+1)
	for i, c := range cases {
		if _, err := BuildMessage(c); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}
func TestReplyAndForward(t *testing.T) {
	original := Message{Summary: Summary{From: []string{"author@example.com"}, To: []string{"me@example.com", "other@example.com"}, Subject: "Original"}, ReplyTo: []string{"replies@example.com"}, MessageID: "<original@example.com>", References: []string{"<prior@example.com>"}, Text: "Original text"}
	c, err := Reply(original, "me@example.com", "My reply", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.To) != 1 || c.To[0] != "replies@example.com" || len(c.Cc) != 1 || c.Cc[0] != "other@example.com" || c.InReplyTo != original.MessageID || len(c.References) != 2 {
		t.Fatalf("%+v", c)
	}
	f, err := Forward(original, "me@example.com", "FYI", []string{"forward@example.com"})
	if err != nil || !strings.Contains(f.Text, "Original text") || f.Subject != "Fwd: Original" {
		t.Fatalf("%+v %v", f, err)
	}
}
