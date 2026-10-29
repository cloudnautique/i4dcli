package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"i4dcli/internal/mail"
)

func printMailFolders(out io.Writer, folders []mail.Folder, g globals) error {
	if folders == nil {
		folders = []mail.Folder{}
	}
	if g.asJSON {
		return json.NewEncoder(out).Encode(folders)
	}
	w, flush := tableWriter(out, g.plain)
	fmt.Fprintln(w, "NAME\tDELIMITER\tATTRIBUTES")
	for _, folder := range folders {
		fmt.Fprintf(w, "%s\t%s\t%s\n", clean(folder.Name), clean(folder.Delimiter), clean(strings.Join(folder.Attributes, ",")))
	}
	return flush()
}

func mailDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func printMailPage(out, stderr io.Writer, page mail.Page, g globals) error {
	if page.Messages == nil {
		page.Messages = []mail.Summary{}
	}
	if g.asJSON {
		return json.NewEncoder(out).Encode(page)
	}
	w, flush := tableWriter(out, g.plain)
	fmt.Fprintln(w, "INTERNAL_DATE\tMAILBOX\tFROM\tSUBJECT\tFLAGS\tSIZE\tID")
	for _, m := range page.Messages {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", mailDate(m.InternalDate), clean(m.Mailbox), clean(strings.Join(m.From, ", ")), clean(m.Subject), clean(strings.Join(m.Flags, ",")), m.Size, clean(m.ID))
	}
	if err := flush(); err != nil {
		return err
	}
	if page.Truncated {
		if _, err := fmt.Fprintln(stderr, "Mail scan reached its safety limit; this page may be incomplete."); err != nil {
			return err
		}
	}
	if page.NextCursor != "" {
		_, err := fmt.Fprintf(stderr, "Next cursor: %s\n", clean(page.NextCursor))
		return err
	}
	return nil
}

func printMailMessage(out io.Writer, m mail.Message, g globals) error {
	if m.Attachments == nil {
		m.Attachments = []mail.Attachment{}
	}
	if g.asJSON {
		return json.NewEncoder(out).Encode(m)
	}
	w, flush := tableWriter(out, g.plain)
	fmt.Fprintln(w, "FIELD\tVALUE")
	for _, field := range []struct{ name, value string }{
		{"id", m.ID}, {"mailbox", m.Mailbox}, {"uid", strconv.FormatUint(uint64(m.UID), 10)},
		{"subject", m.Subject}, {"from", strings.Join(m.From, ", ")}, {"to", strings.Join(m.To, ", ")},
		{"internal_date", mailDate(m.InternalDate)}, {"date", mailDate(m.Date)},
		{"flags", strings.Join(m.Flags, ",")}, {"size", strconv.FormatInt(m.Size, 10)},
		{"text", m.Text}, {"html", m.HTML}, {"body_truncated", strconv.FormatBool(m.BodyTruncated)},
	} {
		fmt.Fprintf(w, "%s\t%s\n", field.name, clean(field.value))
	}
	for i, a := range m.Attachments {
		fmt.Fprintf(w, "attachments.%d.filename\t%s\nattachments.%d.content_type\t%s\nattachments.%d.size\t%d\n", i, clean(a.Filename), i, clean(a.ContentType), i, a.Size)
	}
	return flush()
}
