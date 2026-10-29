package mail

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"i4dcli/internal/fault"
)

const testBody = "From: sender@example.com\r\nTo: to@example.com\r\nSubject: Hello\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello world!"

type fake struct {
	mu             sync.Mutex
	commands       []string
	validity, next uint32
	uids           []uint32
	body           string
	blockList      bool
}

func (f *fake) transcript() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.commands, "\n")
}
func connectFake(t *testing.T, f *fake, account string) *Client {
	t.Helper()
	a, b := net.Pipe()
	if f.validity == 0 {
		f.validity = 42
	}
	if f.next == 0 {
		f.next = 1001
	}
	if f.body == "" {
		f.body = testBody
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	go func() {
		defer b.Close()
		r := bufio.NewReader(b)
		fmt.Fprint(b, "* OK [CAPABILITY IMAP4rev1] ready\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			f.mu.Lock()
			f.commands = append(f.commands, line)
			f.mu.Unlock()
			fields := strings.Fields(line)
			tag := fields[0]
			switch {
			case strings.Contains(line, " LOGIN "):
				fmt.Fprintf(b, "%s OK login\r\n", tag)
			case strings.Contains(line, " CAPABILITY"):
				fmt.Fprintf(b, "* CAPABILITY IMAP4rev1\r\n%s OK capabilities\r\n", tag)
			case strings.Contains(line, " LIST "):
				if f.blockList {
					continue
				}
				fmt.Fprintf(b, "* LIST (\\HasNoChildren) \"/\" \"INBOX\"\r\n* LIST (\\Noselect) \"/\" \"Parent\"\r\n%s OK list\r\n", tag)
			case strings.Contains(line, " EXAMINE "):
				fmt.Fprintf(b, "* %d EXISTS\r\n* OK [UIDVALIDITY %d] ids\r\n* OK [UIDNEXT %d] next\r\n%s OK [READ-ONLY] examine\r\n", len(f.uids), f.validity, f.next, tag)
			case strings.Contains(line, " UID SEARCH "):
				re := regexp.MustCompile(`UID (\d+):(\d+)`)
				matches := re.FindStringSubmatch(line)
				if len(matches) != 3 {
					fmt.Fprintf(b, "%s BAD missing bounded range\r\n", tag)
					continue
				}
				low, _ := strconv.Atoi(matches[1])
				high, _ := strconv.Atoi(matches[2])
				fmt.Fprint(b, "* SEARCH")
				for _, uid := range f.uids {
					if int(uid) >= low && int(uid) <= high {
						fmt.Fprintf(b, " %d", uid)
					}
				}
				fmt.Fprintf(b, "\r\n%s OK search\r\n", tag)
			case strings.Contains(line, " UID FETCH "):
				set := fields[3]
				ids := map[string]bool{}
				for _, s := range strings.Split(set, ",") {
					ids[s] = true
				}
				for i, uid := range f.uids {
					if !ids[strconv.Itoa(int(uid))] {
						continue
					}
					if strings.Contains(line, "BODY.PEEK") {
						fmt.Fprintf(b, "* %d FETCH (UID %d BODY[]<0> {%d}\r\n%s)\r\n", i+1, uid, len(f.body), f.body)
					} else {
						fmt.Fprintf(b, "* %d FETCH (UID %d FLAGS () INTERNALDATE \"01-Oct-2026 12:00:00 +0000\" RFC822.SIZE %d ENVELOPE (NIL \"Hello\" ((NIL NIL \"sender\" \"example.com\")) NIL NIL ((NIL NIL \"to\" \"example.com\")) NIL NIL NIL \"<id>\"))\r\n", i+1, uid, len(f.body))
					}
				}
				fmt.Fprintf(b, "%s OK fetch\r\n", tag)
			default:
				fmt.Fprintf(b, "%s BAD unexpected command\r\n", tag)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	c, err := newClient(ctx, a, Options{Username: "person@icloud.com", Password: "secret", AccountID: account})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestReadOnlyTranscriptAndMessage(t *testing.T) {
	f := &fake{uids: []uint32{1000, 999, 998}}
	c := connectFake(t, f, "personal")
	folders, err := c.Folders(context.Background())
	if err != nil || len(folders) != 2 {
		t.Fatalf("folders: %#v %v", folders, err)
	}
	page, err := c.List(context.Background(), ListOptions{Mailbox: "inbox", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].UID != 1000 || page.NextCursor == "" {
		t.Fatalf("page %#v", page)
	}
	next, err := c.List(context.Background(), ListOptions{Cursor: page.NextCursor, Limit: 1})
	if err != nil || len(next.Messages) != 1 || next.Messages[0].UID != 999 {
		t.Fatalf("next %#v %v", next, err)
	}
	msg, err := c.Get(context.Background(), page.Messages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Text != "Hello world!" || msg.Subject != "Hello" || msg.From[0] != "sender@example.com" || msg.BodyTruncated {
		t.Fatalf("message %#v", msg)
	}
	transcript := f.transcript()
	if !strings.Contains(transcript, "EXAMINE") || !strings.Contains(transcript, "BODY.PEEK[]<0.2097153>") {
		t.Fatalf("missing read-only requests: %s", transcript)
	}
	for _, mutating := range []string{" SELECT ", " STORE ", " CLOSE", " EXPUNGE", " APPEND", "BODY[]"} {
		if strings.Contains(transcript, mutating) {
			t.Fatalf("unexpected mutation %q: %s", mutating, transcript)
		}
	}
}

func TestReferencesRejectChangedMailboxAndAccount(t *testing.T) {
	c := connectFake(t, &fake{uids: []uint32{1000, 999}}, "personal")
	p, err := c.List(context.Background(), ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{uids: []uint32{1000}, validity: 43}
	other := connectFake(t, f, "personal")
	_, err = other.Get(context.Background(), p.Messages[0].ID)
	if fault.Describe(err).Class != "conflict" {
		t.Fatalf("validity err: %v", err)
	}
	f2 := &fake{uids: []uint32{1000}}
	otherAccount := connectFake(t, f2, "work")
	_, err = otherAccount.List(context.Background(), ListOptions{Cursor: p.NextCursor})
	if fault.Describe(err).Class != "conflict" {
		t.Fatalf("account err: %v", err)
	}
	if strings.Contains(f2.transcript(), "EXAMINE") {
		t.Fatal("account mismatch must fail before mailbox access")
	}
}

func TestSearchBoundsAndSparseCursor(t *testing.T) {
	f := &fake{next: 100000, uids: []uint32{5}}
	c := connectFake(t, f, "personal")
	opts := ListOptions{Filter: Filter{From: "sender@example.com", Subject: "Hello", Since: "2026-10-01", Unread: true}}
	p, err := c.List(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Truncated || p.NextCursor == "" || len(p.Messages) != 0 {
		t.Fatalf("sparse page %#v", p)
	}
	transcript := f.transcript()
	if strings.Count(transcript, "UID SEARCH") != 4 || !strings.Contains(transcript, "UNSEEN") || !strings.Contains(transcript, `SINCE "1-Oct-2026"`) {
		t.Fatalf("search transcript %s", transcript)
	}
	_, err = c.List(context.Background(), ListOptions{Cursor: p.NextCursor, Filter: Filter{Subject: "different"}})
	if fault.Describe(err).Class != "usage" {
		t.Fatalf("conflicting filter: %v", err)
	}
}

func TestCancellationClosesConnection(t *testing.T) {
	c := connectFake(t, &fake{blockList: true}, "personal")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Folders(ctx)
	if err != context.DeadlineExceeded || time.Since(start) > time.Second {
		t.Fatalf("cancel err=%v elapsed=%v", err, time.Since(start))
	}
}

func TestLocalValidationAndMIMEBounds(t *testing.T) {
	for _, o := range []ListOptions{{Limit: 101}, {Filter: Filter{Since: "bad"}}, {Filter: Filter{Since: "2026-10-02", Before: "2026-10-01"}}, {Cursor: "bogus"}, {Mailbox: "bad\nname"}} {
		if o.Validate() == nil {
			t.Fatalf("accepted %#v", o)
		}
	}
	if ValidateID("bad") == nil {
		t.Fatal("accepted bad ID")
	}
	for _, raw := range []string{"Content-Type: text/plain; charset=not-a-charset\r\n\r\nhello", "Content-Type: text/plain\r\n\r\n" + strings.Repeat("x", maxDecodedBody+1), "Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\ninvalid"} {
		if decodeBody([]byte(raw), &Message{}) == nil {
			t.Fatal("accepted malformed or excessive MIME")
		}
	}
	var msg Message
	raw := "Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nhello\r\n--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=../secret\r\nContent-Transfer-Encoding: base64\r\n\r\nYWJj\r\n--x--\r\n"
	if err := decodeBody([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Text != "hello" || len(msg.Attachments) != 1 || msg.Attachments[0].Size != 3 {
		t.Fatalf("MIME %#v", msg)
	}
}

func TestPaginationStableUpperBoundary(t *testing.T) {
	f := &fake{uids: []uint32{1000, 999, 998}}
	c := connectFake(t, f, "personal")
	p, err := c.List(context.Background(), ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	// A separate session sees a newly arrived UID but must honor the old cursor.
	f2 := &fake{next: 1002, uids: []uint32{1001, 1000, 999, 998}}
	c2 := connectFake(t, f2, "personal")
	p2, err := c2.List(context.Background(), ListOptions{Limit: 1, Cursor: p.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	uids := []uint32{}
	for _, m := range p2.Messages {
		uids = append(uids, m.UID)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	if len(uids) != 1 || uids[0] != 999 {
		t.Fatalf("new arrival shifted page: %v", uids)
	}
}
