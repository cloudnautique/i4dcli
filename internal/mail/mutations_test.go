package mail

import (
	"context"
	"errors"
	"i4dcli/internal/fault"
	"strings"
	"testing"
)

func fakeID(c *Client) string {
	return encodeReference(reference{Version: 1, Account: c.account, Mailbox: "INBOX", Validity: 42, UID: 1000})
}
func TestMutationsRequireWriteSession(t *testing.T) {
	f := &fake{uids: []uint32{1000}, caps: "MOVE UIDPLUS"}
	c := connectFake(t, f, "personal")
	yes := true
	methods := []func() (MutationResult, error){func() (MutationResult, error) { return c.Mark(t.Context(), fakeID(c), FlagChanges{Seen: &yes}) }, func() (MutationResult, error) { return c.Move(t.Context(), fakeID(c), "Other") }, func() (MutationResult, error) { return c.Delete(t.Context(), fakeID(c)) }, func() (MutationResult, error) { return c.CreateDraft(t.Context(), "Drafts", []byte(testBody)) }}
	for _, method := range methods {
		_, err := method()
		if err == nil || fault.Describe(err).Class != "permission_denied" {
			t.Fatalf("write session restriction: %v", err)
		}
	}
	if strings.Contains(f.transcript(), "SELECT") || strings.Contains(f.transcript(), "APPEND") {
		t.Fatal("write reached server")
	}
}
func TestMoveAndDeleteNeverFallback(t *testing.T) {
	for _, op := range []string{"move", "delete"} {
		t.Run(op, func(t *testing.T) {
			f := &fake{allowWrite: true, uids: []uint32{1000}}
			c := connectFake(t, f, "personal")
			var err error
			if op == "move" {
				_, err = c.Move(t.Context(), fakeID(c), "Other")
			} else {
				_, err = c.Delete(t.Context(), fakeID(c))
			}
			if err == nil {
				t.Fatal("missing capability accepted")
			}
			for _, bad := range []string{"COPY", "STORE", "EXPUNGE", "MOVE", "SELECT"} {
				if strings.Contains(f.transcript(), bad) {
					t.Fatalf("fallback reached server: %s", f.transcript())
				}
			}
		})
	}
}
func TestAtomicMoveAndExplicitFlagAssignment(t *testing.T) {
	f := &fake{allowWrite: true, caps: "MOVE UIDPLUS", uids: []uint32{1000}}
	c := connectFake(t, f, "personal")
	yes, no := true, false
	marked, err := c.Mark(t.Context(), fakeID(c), FlagChanges{Seen: &yes, Flagged: &no})
	if err != nil || !marked.Completed {
		t.Fatalf("mark %#v %v", marked, err)
	}
	moved, err := c.Move(t.Context(), fakeID(c), "Archive")
	if err != nil || !moved.Completed || moved.ID == "" {
		t.Fatalf("move %#v %v", moved, err)
	}
	ref, _ := decodeReference(moved.ID)
	if ref.Mailbox != "Archive" || ref.UID != 77 {
		t.Fatalf("move reference %#v", ref)
	}
	text := f.transcript()
	for _, want := range []string{"UID STORE 1000 +FLAGS.SILENT (\\Seen)", "UID STORE 1000 -FLAGS.SILENT (\\Flagged)", `UID MOVE 1000 "Archive"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	if strings.Contains(text, "COPY") || strings.Contains(text, "EXPUNGE") {
		t.Fatal("move fallback used")
	}
}
func TestTargetedDeleteAndPartialOutcome(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "partial"}[fail], func(t *testing.T) {
			f := &fake{allowWrite: true, caps: "UIDPLUS", uids: []uint32{1000}, failExpunge: fail}
			c := connectFake(t, f, "personal")
			result, err := c.Delete(t.Context(), fakeID(c))
			if fail {
				var outcome interface{ MutationOutcome() MutationResult }
				if err == nil || !errors.As(err, &outcome) || !outcome.MutationOutcome().Partial {
					t.Fatalf("lost partial outcome %#v %v", result, err)
				}
			} else if err != nil || !result.Completed {
				t.Fatalf("delete %#v %v", result, err)
			}
			text := f.transcript()
			if !strings.Contains(text, "UID EXPUNGE 1000") {
				t.Fatalf("missing targeted expunge %s", text)
			}
			for _, line := range strings.Split(text, "\n") {
				if strings.HasSuffix(line, " EXPUNGE") {
					t.Fatal("global expunge used")
				}
			}
		})
	}
}
func TestDraftAppendReplacementAndPartialID(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "partial"}[fail], func(t *testing.T) {
			f := &fake{allowWrite: true, caps: "UIDPLUS", uids: []uint32{1000}, draft: true, failExpunge: fail}
			c := connectFake(t, f, "personal")
			result, err := c.EditDraft(t.Context(), fakeID(c), []byte(testBody))
			if result.ID == "" {
				t.Fatalf("new draft ID lost %#v %v", result, err)
			}
			if fail {
				var outcome *MutationError
				if !errors.As(err, &outcome) || !outcome.Result.Partial || outcome.Result.ID == "" {
					t.Fatalf("partial error %#v %v", result, err)
				}
			} else if err != nil || !result.Completed {
				t.Fatalf("edit %#v %v", result, err)
			}
			text := f.transcript()
			if strings.Index(text, "APPEND") > strings.Index(text, "UID STORE") {
				t.Fatal("deleted original before append")
			}
			if !strings.Contains(text, "(\\Draft)") {
				t.Fatal("draft flag missing")
			}
		})
	}
}
func TestDraftUnknownAndMissingUIDOutcomes(t *testing.T) {
	for _, drop := range []bool{false, true} {
		f := &fake{allowWrite: true, caps: "UIDPLUS", uids: []uint32{1000}, draft: true, omitAppendUID: !drop, dropAppendReply: drop}
		c := connectFake(t, f, "personal")
		result, err := c.EditDraft(context.Background(), fakeID(c), []byte(testBody))
		var mutation *MutationError
		if !errors.As(err, &mutation) {
			t.Fatalf("missing mutation error %#v %v", result, err)
		}
		if drop && !result.OutcomeUnknown {
			t.Fatal("lost uncertain append outcome")
		}
		if !drop && !result.Partial {
			t.Fatal("lost saved-new-draft partial outcome")
		}
		if strings.Contains(f.transcript(), "UID STORE") {
			t.Fatal("original draft removed without new ID")
		}
	}
}
func TestDraftReadsRejectNonDraft(t *testing.T) {
	f := &fake{uids: []uint32{1000}}
	c := connectFake(t, f, "personal")
	_, err := c.RawDraft(t.Context(), fakeID(c))
	if err == nil {
		t.Fatal("non-draft accepted")
	}
	if strings.Contains(f.transcript(), "BODY.PEEK") {
		t.Fatal("read non-draft body")
	}
	draft := &fake{uids: []uint32{1000}, draft: true}
	dc := connectFake(t, draft, "personal")
	raw, err := dc.RawDraft(t.Context(), fakeID(dc))
	if err != nil || string(raw) != testBody {
		t.Fatalf("rawdraft %q %v", raw, err)
	}
}
func TestSpecialUseTrashAndDrafts(t *testing.T) {
	f := &fake{allowWrite: true, caps: "UIDPLUS MOVE SPECIAL-USE", uids: []uint32{1000}}
	c := connectFake(t, f, "personal")
	result, err := c.Trash(t.Context(), fakeID(c), "")
	if err != nil || result.Mailbox != "Deleted Messages" {
		t.Fatalf("trash %#v %v", result, err)
	}
	created, err := c.CreateDraft(t.Context(), "", []byte(testBody))
	if err != nil || created.Mailbox != "Drafts" || !created.Completed {
		t.Fatalf("draft %#v %v", created, err)
	}
	if !strings.Contains(f.transcript(), "RETURN (SPECIAL-USE)") {
		t.Fatalf("special-use not requested %s", f.transcript())
	}
}
func TestAttachmentAndReplyHeaders(t *testing.T) {
	body := "From: sender@example.com\r\nReply-To: reply@example.com\r\nCc: cc@example.com\r\nMessage-ID: <original@example.com>\r\nReferences: <prior@example.com>\r\nIn-Reply-To: <prior@example.com>\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nhello\r\n--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=../secret\r\nContent-Transfer-Encoding: base64\r\n\r\nYWJj\r\n--x--\r\n"
	f := &fake{uids: []uint32{1000}, body: body}
	c := connectFake(t, f, "personal")
	attachment, err := c.Attachment(t.Context(), fakeID(c), 0)
	if err != nil || string(attachment.Data) != "abc" || attachment.Index != 0 {
		t.Fatalf("attachment %#v %v", attachment, err)
	}
	msg, err := c.Get(t.Context(), fakeID(c))
	if err != nil || msg.MessageID != "<original@example.com>" || len(msg.ReplyTo) != 1 || len(msg.Cc) != 1 || len(msg.References) != 1 {
		t.Fatalf("headers %#v %v", msg, err)
	}
	if _, err := c.Attachment(t.Context(), fakeID(c), 1); err == nil {
		t.Fatal("out of range attachment accepted")
	}
	for _, bad := range []string{" SELECT ", " STORE ", "BODY[]"} {
		if strings.Contains(f.transcript(), bad) {
			t.Fatalf("attachment mutated mailbox %s", f.transcript())
		}
	}
}
