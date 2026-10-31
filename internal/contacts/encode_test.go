package contacts

import (
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
)

func TestEditPreservesQuotedAndFoldedProperties(t *testing.T) {
	for _, version := range []string{"3.0", "4.0"} {
		t.Run(version, func(t *testing.T) {
			c, f := newFixture(t)
			object := "/books/personal/quoted.vcf"
			properties := []string{
				`ADR;LABEL="123 Main St: Apt 1; second floor":;;123 Main St;London;;;UK`,
				`item1.EMAIL;TYPE=HOME;X-LABEL="Personal: friends; family":ada@example.com`,
				"X-CUSTOM;X-PARAM=\"alpha: beta; gamma\":a long\r\n  folded value",
				`X-OTHER;X-PARAM="one,two":unchanged`,
			}
			raw := "BEGIN:VCARD\r\nVERSION:" + version + "\r\nUID:quoted\r\nFN:Ada\r\n" + strings.Join(properties, "\r\n") + "\r\nEND:VCARD\r\n"
			f.objects[object] = raw
			before, err := vcard.NewDecoder(strings.NewReader(raw)).Decode()
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Mutate(t.Context(), Mutation{Action: "edit", AddressBook: "Personal", Path: object, Changes: Changes{Name: str("Ada Updated"), Note: str("New note")}})
			if err != nil {
				t.Fatal(err)
			}
			saved := f.objects[object]
			for _, property := range properties {
				if !strings.Contains(saved, property+"\r\n") {
					t.Fatalf("untouched property changed: %q\n%s", property, saved)
				}
			}
			after, err := vcard.NewDecoder(strings.NewReader(saved)).Decode()
			if err != nil {
				t.Fatal(err)
			}
			if after.Value("ADR") != before.Value("ADR") || after.Get("ADR").Params.Get("LABEL") != before.Get("ADR").Params.Get("LABEL") {
				t.Fatal("address or label changed")
			}
			if after.Value("FN") != "Ada Updated" || after.Value("NOTE") != "New note" {
				t.Fatal("edited fields were not written")
			}
		})
	}
}

func TestEditParameterizedStructuredName(t *testing.T) {
	for _, tc := range []struct{ version, property string }{
		{"4.0", `N;SORT-AS="Lovelace,Ada":Lovelace;Ada;Augusta;Countess;PhD`},
		{"3.0", `N;CHARSET=UTF-8:Lovelace;Ada;Augusta;Countess;PhD`},
		{"4.0", `item1.N;SORT-AS="Lovelace,Ada":Lovelace;Ada;Augusta;Countess;PhD`},
	} {
		t.Run(tc.version+tc.property, func(t *testing.T) {
			c, f := newFixture(t)
			object := "/books/personal/name.vcf"
			f.objects[object] = "BEGIN:VCARD\r\nVERSION:" + tc.version + "\r\nUID:name\r\nFN:Ada Lovelace\r\n" + tc.property + "\r\nEND:VCARD\r\n"
			for _, changes := range []Changes{{GivenName: str("Ada Updated")}, {FamilyName: str("Byron")}} {
				if _, err := c.Mutate(t.Context(), Mutation{Action: "edit", AddressBook: "Personal", Path: object, Changes: changes}); err != nil {
					t.Fatal(err)
				}
			}
			saved, err := vcard.NewDecoder(strings.NewReader(f.objects[object])).Decode()
			if err != nil {
				t.Fatal(err)
			}
			name := saved.Name()
			if name == nil || name.GivenName != "Ada Updated" || name.FamilyName != "Byron" || name.AdditionalName != "Augusta" || name.HonorificPrefix != "Countess" || name.HonorificSuffix != "PhD" {
				t.Fatalf("name components lost: %+v", name)
			}
			if name.Group != "" || len(name.Params) > 0 {
				t.Fatal("changed name retained original field metadata")
			}
		})
	}
}
