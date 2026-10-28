package policy

import (
	"strings"
	"testing"
)

func TestProfilesAndExactGrants(t *testing.T) {
	for _, embedded := range []string{"readonly", "editor", "full"} {
		for _, runtime := range []string{"readonly", "editor", "full"} {
			for _, ro := range []bool{false, true} {
				p, err := newPolicy(embedded, Options{Profile: runtime, ReadOnly: ro})
				if err != nil {
					t.Fatal(err)
				}
				for _, tc := range []struct {
					name             string
					readonly, editor bool
				}{
					{"calendars.list", true, true}, {"events.list", true, true}, {"events.create", false, true}, {"events.edit", false, true}, {"events.update", false, true}, {"events.delete", false, false}, {"policy", true, true},
				} {
					want := true
					if embedded == "readonly" || runtime == "readonly" || ro {
						want = tc.readonly
					} else if embedded == "editor" || runtime == "editor" {
						want = tc.editor
					}
					if got := p.Check(tc.name) == nil; got != want {
						t.Errorf("%s/%s readonly=%t %s: allowed=%t want %t", embedded, runtime, ro, tc.name, got, want)
					}
				}
			}
		}
	}
	p, err := newPolicy("full", Options{EnabledExact: " events.update, events.list "})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"events.edit", "events.update", "events.list", "policy"} {
		if err := p.Check(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Check("calendars.list"); err == nil {
		t.Fatal("exact grants expanded")
	}
	if err := p.Check("events.delete"); err == nil {
		t.Fatal("delete allowed")
	}
}
func TestInvalidPolicyFailsClosed(t *testing.T) {
	for _, o := range []Options{{Profile: "typo"}, {EnabledExact: ", ,"}, {EnabledExact: "events"}, {EnabledExact: "events.*"}, {EnabledExact: "events.typo"}} {
		if _, err := New(o); err == nil {
			t.Errorf("accepted %+v", o)
		}
	}
	if _, err := newPolicy("typo", Options{}); err == nil {
		t.Fatal("accepted invalid embedded profile")
	}
	if err := (Policy{}).Check("events.create"); err == nil {
		t.Fatal("zero policy allowed writes")
	}
	p, _ := newPolicy("readonly", Options{EnabledExact: "events.update"})
	if err := p.Check("events.edit"); err == nil || !strings.Contains(err.Error(), "embedded profile") {
		t.Fatalf("missing reason: %v", err)
	}
}
