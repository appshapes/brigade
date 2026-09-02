package conformance_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/conformance/cases"
	"github.com/appshapes/brigade/internal/testutil"
)

// mutantFailures is the suite's own positive control (plan 9.2, brief
// section 8): each deliberately broken fs adapter fails EXACTLY these
// cases and no other, and the normal build fails none. The plan's table
// said mutant_noack fails C-30 and C-36; the P1-5 verifier proved C-29b
// cannot avoid acks and C-41's restart check is a stdin-ack check, so
// both belong to the set. C-28, C-29, C-32 and C-40 are written ack-free
// precisely so that they do not join it. A case that turns out to need an
// ack the table does not foresee is reported, never silently added here.
var mutantFailures = map[string][]string{
	"mutant_noack":       {"C-29b", "C-30", "C-36", "C-41"},
	"mutant_teamleak":    {"C-12", "C-26"},
	"mutant_trustsender": {"C-23", "C-24"},
	"mutant_caporder":    {"C-28"},
}

// buildMutant compiles the fs adapter with one build tag.
func buildMutant(tb testing.TB, tag string) string {
	tb.Helper()
	out := filepath.Join(tb.TempDir(), "adapter")
	args := []string{"build", "-trimpath"}
	if tag != "" {
		args = append(args, "-tags", tag)
	}
	args = append(args, "-o", out, "./cmd/brigade-adapter-fs")
	cmd := exec.CommandContext(tb.Context(), "go", args...)
	cmd.Dir = testutil.RepoRoot(tb)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if combined, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, combined)
	}
	return out
}

// failingIDs runs every non-slow case against adapter and returns the ids
// that failed, sorted.
func failingIDs(tb testing.TB, adapter string) []string {
	tb.Helper()
	_, rep, _ := runSuite(tb, adapter, conformance.Options{})
	var ids []string
	for _, res := range rep.Results {
		if res.Status == conformance.StatusFail {
			ids = append(ids, res.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func TestMutantsFailExactlyTheirCases(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds four adapter binaries")
	}
	present := map[string]bool{}
	for _, c := range cases.All() {
		present[c.ID] = true
	}
	t.Run("normal build fails none", func(t *testing.T) {
		t.Parallel()
		ids := failingIDs(t, buildMutant(t, ""))
		t.Logf("normal build fails %v", ids)
		if len(ids) != 0 {
			t.Fatalf("the normal build fails %v", ids)
		}
	})
	for tag, want := range mutantFailures {
		t.Run(tag, func(t *testing.T) {
			t.Parallel()
			var missing []string
			for _, id := range want {
				if !present[id] {
					missing = append(missing, id)
				}
			}
			if len(missing) > 0 {
				// The expectation is EXACT and is not widened or narrowed
				// to fit an incomplete case list: until every named case
				// exists this subtest cannot be meaningful, and says so.
				t.Skipf("cases.All() lacks %v; the exact-set assertion needs the full suite", missing)
			}
			got := failingIDs(t, buildMutant(t, tag))
			t.Logf("%s fails %v", tag, got)
			sorted := slices.Clone(want)
			sort.Strings(sorted)
			if !slices.Equal(got, sorted) {
				t.Fatalf("%s fails %v, want exactly %v", tag, got, sorted)
			}
		})
	}
}
