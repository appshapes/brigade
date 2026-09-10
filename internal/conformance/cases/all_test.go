package cases

import (
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/conformance"
)

// wantIDs pins the case list of plan 9.2 plus C-44 (the session model and
// context facts, added after the plan): 46 ids in id order, each once.
var wantIDs = []string{
	"C-01", "C-02", "C-03", "C-03b", "C-04", "C-05", "C-06", "C-07", "C-08",
	"C-10", "C-11", "C-12", "C-13", "C-14", "C-15", "C-16", "C-17", "C-18", "C-19", "C-19b",
	"C-20", "C-21", "C-22", "C-23", "C-24", "C-25", "C-26", "C-27", "C-28", "C-29", "C-29b",
	"C-30", "C-31", "C-32", "C-33", "C-34", "C-35", "C-36", "C-37", "C-38", "C-39",
	"C-40", "C-41", "C-42", "C-43", "C-44",
}

func TestAllIsThePlanListInOrder(t *testing.T) {
	t.Parallel()
	all := All()
	var got []string
	for _, c := range all {
		got = append(got, c.ID)
	}
	if !slices.Equal(got, wantIDs) {
		t.Fatalf("All() ids:\n got %v\nwant %v", got, wantIDs)
	}
	if len(all) != 46 {
		t.Fatalf("%d cases, want 46", len(all))
	}
}

func TestEveryCaseIsWellFormed(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, c := range All() {
		if seen[c.ID] {
			t.Errorf("%s listed twice", c.ID)
		}
		seen[c.ID] = true
		if c.Rule == "" || c.Title == "" || c.Run == nil {
			t.Errorf("%s: Rule %q, Title %q, Run nil %v; all three are required", c.ID, c.Rule, c.Title, c.Run == nil)
		}
		if !c.HasTag(conformance.TagCore) {
			t.Errorf("%s: every case carries the %q tag", c.ID, conformance.TagCore)
		}
		for _, tag := range c.Tags {
			switch {
			case tag == conformance.TagCore, tag == conformance.TagSlow:
			case strings.HasPrefix(tag, "cap:") && len(tag) > len("cap:"):
			default:
				t.Errorf("%s: unknown tag %q", c.ID, tag)
			}
		}
		if c.IsSlow() != (c.ID == "C-14") {
			t.Errorf("%s: slow %v; only C-14 is slow", c.ID, c.IsSlow())
		}
	}
}
