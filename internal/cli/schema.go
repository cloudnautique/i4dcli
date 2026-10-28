package cli

import (
	"encoding/json"
	"flag"
	"io"
	"reflect"

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
		result = append(result, flagInfo{Name: f.Name, Type: kind, Default: f.DefValue, Description: f.Usage, Repeatable: f.Name == "calendar" || f.Name == "allow-tool"})
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
		if c.Name == "mcp" {
			fs, _ = mcpFlags(io.Discard)
		}
		info := describeFlags(fs)
		for i := range info {
			name := info[i].Name
			info[i].Required = name == "calendar" || name == "path" || (c.Name == "events.create" && (name == "summary" || name == "start" || name == "end"))
		}
		constraints := map[string][]string{
			"events.list":   {"Choose exactly one range: today, tomorrow, days, or the from/to pair.", "days must be 1–3660."},
			"events.get":    {"Exactly one calendar is required."},
			"events.create": {"Exactly one calendar is required.", "start and end must share a date or RFC3339 format; end is exclusive and later than start."},
			"events.edit":   {"Exactly one calendar and at least one changed field are required.", "start and end must be changed together."},
			"events.delete": {"Exactly one calendar is required.", "Execution requires interactive confirmation or --force; previews do not."},
			"mcp":           {"allow-delete requires allow-write; execution also requires operator --force.", "Tool permissions intersect command policy; tool calls cannot change server permissions."},
		}
		entries = append(entries, entry{c, info, constraints[c.Name]})
	}
	g := globals{}
	return json.NewEncoder(out).Encode(map[string]any{
		"schema_version": 1, "commands": entries, "global_flags": describeFlags(globalFlags(&g)),
		"output_formats": []string{"human", "json", "plain"}, "exit_codes": fault.Codes, "safety": p.Snapshot(),
	})
}
