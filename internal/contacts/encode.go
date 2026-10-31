package contacts

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-vcard"
)

// encodeContact preserves untouched content lines, including parameter quoting
// and folding. The pinned vCard codec cannot safely round-trip these headers.
// Every changed property is rebuilt by Changes.apply without old parameters.
func encodeContact(out io.Writer, card vcard.Card, original []byte, changes Changes) error {
	if original == nil {
		return vcard.NewEncoder(out).Encode(card)
	}
	changed := map[string]bool{"REV": true}
	for key, value := range map[string]*string{"FN": changes.Name, "ORG": changes.Organization, "NOTE": changes.Note, "BDAY": changes.Birthday} {
		if value != nil {
			changed[key] = true
		}
	}
	if changes.GivenName != nil || changes.FamilyName != nil {
		changed["N"] = true
	}
	for key, value := range map[string]*[]string{"EMAIL": changes.Emails, "TEL": changes.Phones, "URL": changes.URLs} {
		if value != nil {
			changed[key] = true
		}
	}
	replacement := vcard.Card{"VERSION": card["VERSION"]}
	for key := range changed {
		for _, field := range card[key] {
			if field.Group != "" || len(field.Params) > 0 {
				return fmt.Errorf("changed contact properties must not retain old parameters")
			}
		}
		if fields := card[key]; len(fields) > 0 {
			replacement[key] = fields
		}
	}
	var encoded bytes.Buffer
	if err := vcard.NewEncoder(&encoded).Encode(replacement); err != nil {
		return err
	}
	newLines := strings.Split(strings.TrimSuffix(encoded.String(), "\r\n"), "\r\n")
	// The encoder puts BEGIN and VERSION first, and END last.
	newLines = newLines[2 : len(newLines)-1]
	physical := strings.Split(strings.ReplaceAll(string(original), "\r\n", "\n"), "\n")
	records := []string{}
	for _, line := range physical {
		if line == "" {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && len(records) > 0 {
			records[len(records)-1] += "\r\n" + line
		} else {
			records = append(records, line)
		}
	}
	for _, record := range records {
		name := record
		if i := strings.IndexAny(name, ":;"); i >= 0 {
			name = name[:i]
		}
		if i := strings.IndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		name = strings.ToUpper(name)
		if changed[name] {
			continue
		}
		if name == "END" {
			for _, line := range newLines {
				if _, err := io.WriteString(out, line+"\r\n"); err != nil {
					return err
				}
			}
		}
		if _, err := io.WriteString(out, record+"\r\n"); err != nil {
			return err
		}
	}
	return nil
}
