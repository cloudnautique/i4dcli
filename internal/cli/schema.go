package cli

import (
	"encoding/json"
	"flag"
	"io"
	"reflect"
	"strings"

	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
)

type flagInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
	Repeatable  bool   `json:"repeatable,omitempty"`
}

func describeFlags(fs *flag.FlagSet) []flagInfo {
	result := []flagInfo{}
	fs.VisitAll(func(f *flag.Flag) {
		kind := "string"
		if getter, ok := f.Value.(flag.Getter); ok {
			kind = reflect.TypeOf(getter.Get()).String()
		}
		result = append(result, flagInfo{Name: f.Name, Type: kind, Default: f.DefValue, Description: f.Usage, Repeatable: f.Name == "calendar" || f.Name == "allow-tool" || f.Name == "attach" || f.Name == "cc" || f.Name == "bcc"})
	})
	return result
}
func printSchema(out io.Writer, p policy.Policy) error {
	type entry struct {
		policy.Command
		Flags       []flagInfo `json:"flags"`
		Constraints []string   `json:"constraints,omitempty"`
	}
	entries := []entry{}
	for _, c := range policy.Commands() {
		if p.Check(c.Name) != nil {
			continue
		}
		fs, _ := commandFlags(c, io.Discard)
		if strings.HasPrefix(c.Name, "mail.") {
			fs, _ = mailFlags(c, io.Discard)
		}
		if c.Name == "mcp" {
			fs, _ = mcpFlags(io.Discard)
		}
		info := describeFlags(fs)
		for i := range info {
			name := info[i].Name
			if name == "to" && (c.Name == "mail.send" || c.Name == "mail.forward" || c.Name == "mail.drafts.create" || c.Name == "mail.drafts.edit") {
				info[i].Repeatable = true
			}
			info[i].Required = name == "path" || name == "id" || (c.Name == "mail.attachments.get" && (name == "attachment" || name == "output")) || (c.Name == "mail.messages.move" && name == "destination") || (c.Name == "events.create" && (name == "summary" || name == "start" || name == "end"))
		}
		constraints := map[string][]string{
			"events.list":          {"Calendar flags are required unless the selected account config supplies defaults.", "Choose exactly one range: today, tomorrow, days, or the from/to pair.", "days must be 1–3660."},
			"events.get":           {"Exactly one calendar is required, from --calendar or account defaults."},
			"events.create":        {"Exactly one calendar is required, from --calendar or account defaults.", "start and end must share a date or RFC3339 format; end is exclusive and later than start."},
			"events.edit":          {"Exactly one calendar is required, from --calendar or account defaults; at least one changed field is required.", "start and end must be changed together."},
			"events.delete":        {"Exactly one calendar is required, from --calendar or account defaults.", "Execution requires interactive confirmation or --force; previews do not."},
			"mail.folders.list":    {"Read-only IMAP folder discovery; requires a configured mail identity."},
			"mail.messages.list":   {"limit must be 1–100; order is descending UID.", "New queries default to the account mailbox or INBOX; a cursor preserves its original mailbox.", "next_cursor indicates another page; truncated indicates a scan safety limit."},
			"mail.messages.get":    {"id is an opaque account/mailbox/UIDVALIDITY/UID reference returned by list/search.", "Bodies use PEEK and attachments are metadata only."},
			"mail.messages.search": {"Search fields are combined with AND; since/before use IMAP internal dates (since inclusive, before exclusive).", "limit must be 1–100; a continuation cursor retains filters; supplied nonempty filters must match."},
			"mail.messages.mark":   {"Provide seen and/or flagged assignments; omitted flags are preserved."},
			"mail.messages.move":   {"Requires server-side MOVE and explicit destination; requires confirmation or --force."},
			"mail.messages.trash":  {"Omitted destination discovers special-use Trash; ambiguous/missing discovery fails. Requires confirmation or --force."},
			"mail.messages.delete": {"Permanently deletes only the selected UID using UIDPLUS; requires confirmation or --force."},
			"mail.attachments.get": {"CLI writes only to explicit output using no-overwrite creation; force cannot overwrite. Index is zero-based."},
			"mail.drafts.create":   {"Recipient/body may be incomplete; configured From required. Omitted mailbox discovers special-use Drafts."},
			"mail.drafts.get":      {"Requires message still marked Draft."},
			"mail.drafts.edit":     {"Full replacement of draft content, not a patch. Returns replacement ID and explicit partial progress on failure."},
			"mail.drafts.delete":   {"Requires Draft flag, UIDPLUS, and confirmation or --force."},
			"mail.drafts.send":     {"Validates stored draft and configured sender; requires confirmation or --force; original draft retained."},
			"mail.send":            {"Requires valid recipients/body and configured sender; confirmation or --force; no automatic retries."},
			"mail.reply":           {"Derives recipients/subject/threading from source; --all includes other recipients; confirmation or --force."},
			"mail.forward":         {"Includes source body and attachments plus explicit new attachments; confirmation or --force."},
			"config.show":          {"Always emits JSON containing path, exists, and validated config; does not resolve account selection. --plain is not supported."},
			"config.path":          {"Prints the XDG config file path without reading the file. --plain is not supported."},
			"mcp":                  {"allow-delete requires allow-write; execution also requires operator --force.", "Tool permissions intersect command policy; tool calls cannot change server permissions."},
		}
		entries = append(entries, entry{c, info, constraints[c.Name]})
	}
	g := globals{}
	return json.NewEncoder(out).Encode(map[string]any{
		"schema_version": 1, "commands": entries, "global_flags": describeFlags(globalFlags(&g)),
		"output_formats": []string{"human", "json", "plain"}, "exit_codes": fault.Codes, "safety": p.Snapshot(),
	})
}
