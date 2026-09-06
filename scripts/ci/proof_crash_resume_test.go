// The Docker-free, model-free half of scripts/proof-crash-resume.sh (P4-4).
//
// The live script SIGKILLs a real `claude -p` receiver, sends five messages while it is down, resumes it with
// `claude -p --resume <native id>` and proves the catch-up delivers those five exactly once; it spends real
// sessions and never runs in CI. What this file owns is the part of it that is a pure function and therefore
// testable here: `catchup <dir>`, the jq analyser that scores one arm from saved artefacts.
//
// Every class the analyser can reach has a small hand-sized fixture under testdata/proof-crash-resume/<case>/,
// built in the shapes the REAL 2026-09-04 run recorded on Claude Code 2.1.260 with every identifier re-minted --
// including two shapes nothing else in the tree has: a resumed session's transcript that still carries the DEAD
// session's own frame enqueues (`claude -p --resume` interleaves into the original transcript, E0-5 (f)), and an
// isMeta record that carries SEVERAL messages at once (Claude Code batches a queued backlog into one record).
// The mutation table then makes ONE change to a clean fixture and requires the verdict to flip, with the
// unmutated copy and four deliberate non-flips as controls, so each row is shown to bite.
//
// It also closes the frame-literal quadruplication: proof-crash-resume.sh's delimited block is compared against
// scripts/proof.sh's, which scripts/ci/proof_test.go already joins to the Go sources; and it joins the script's
// lease and liveness budgets to internal/protocol, internal/harness/watch and the schema migration, so a lease
// that moves in one place and not the other cannot silently turn arm B's claim into a tautology.
package ci_test

import (
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const (
	crashResumeScriptRel   = "scripts/proof-crash-resume.sh"
	crashResumeFixturesRel = "scripts/ci/testdata/proof-crash-resume"
	crashResumeCleanCase   = "arm-a-clean"
	crashResumeArmBCase    = "arm-b-clean"
)

// The two marker lines delimiting proof-crash-resume.sh's own frame-literal block. Matched in full, so a
// renamed or deleted marker fails loudly rather than silently emptying the block.
const (
	crashResumeBlockOpen  = "# ---- frame literals, byte-identical to scripts/proof.sh's drift-checked block and joined to it by scripts/ci/proof_crash_resume_test.go (do not edit by hand) ----"
	crashResumeBlockClose = "# ---- end frame literals ----"
)

// The same two markers in scripts/proof.sh. proof_test.go owns that file's join to the Go sources; this test
// only reads it, and never edits proof_test.go.
const (
	crashResumeProofShRel   = "scripts/proof.sh"
	crashResumeProofShOpen  = "# ---- constants checked against the Go sources by scripts/ci/proof_test.go (do not edit by hand) ----"
	crashResumeProofShClose = "# ---- end constants ----"
)

// crashResumeSharedLiterals are the constants proof-crash-resume.sh copies out of proof.sh. Every one of them
// must be byte-identical in both files: a frame the script rebuilds from a drifted literal would "prove"
// byte-exactness against itself.
var crashResumeSharedLiterals = []string{
	"join_secret_prefix",
	"frame_open_tag",
	"frame_close_tag",
	"frame_wrapper_open",
	"frame_wrapper_close",
	"frame_separator",
	"frame_summary_prefix",
	"frame_unverified_suffix",
	"frame_level_default",
	"frame_preamble_head_shared",
	"frame_clause_open",
	"frame_clause_guarded",
	"frame_clause_strict",
	"frame_preamble_reply_intro",
	"frame_preamble_reply",
	"frame_preamble_tail",
}

var crashResumeAssignment = regexp.MustCompile(`^([a-z][a-z0-9_]*)=(.*)$`)

// crashResumeBlock splits a script into the parsed literal block and the rest of the file (the "rest" is what
// the used-somewhere-else rule is checked against). A missing or empty block is reported, never tolerated.
func crashResumeBlock(t *testing.T, rel, open, closeMarker string) (map[string]string, string) {
	t.Helper()
	//nolint:gosec // G304: a fixed path under the repository under test
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	text := string(data)
	from := strings.Index(text, open)
	if from < 0 {
		t.Fatalf("%s has no literal-block opening marker: the drift join has lost its authority", rel)
	}
	to := strings.Index(text[from:], closeMarker)
	if to < 0 {
		t.Fatalf("%s has no literal-block closing marker", rel)
	}
	to += from
	block := text[from+len(open) : to]
	rest := text[:from] + text[to+len(closeMarker):]

	out := map[string]string{}
	for line := range strings.SplitSeq(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := crashResumeAssignment.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("%s: literal-block line is not a `name=value` assignment: %q", rel, line)
			continue
		}
		out[m[1]] = crashResumeUnquote(m[2])
	}
	if len(out) == 0 {
		t.Fatalf("%s: the literal block is empty", rel)
	}
	return out, rest
}

// crashResumeUnquote undoes the scripts' own quoting: a single-quoted value, with the `'"'"'` idiom standing
// for a literal apostrophe. (The same rules as scripts/ci/proof_test.go's proofUnquote, written here so that
// this test never edits that file.)
func crashResumeUnquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
		v = v[1 : len(v)-1]
		v = strings.ReplaceAll(v, `'"'"'`, "'")
	}
	return v
}

// TestProofCrashResumeScriptParses: `sh -n`, the shebang and the 100755 index mode (`make proof` EXECs it).
func TestProofCrashResumeScriptParses(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	path := filepath.Join(root, crashResumeScriptRel)
	//nolint:gosec // G204: a fixed path under the repository under test
	cmd := exec.CommandContext(t.Context(), "sh", "-n", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n %s: %v\n%s", crashResumeScriptRel, err, out)
	}
	//nolint:gosec // G304: as above
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", crashResumeScriptRel, err)
	}
	if !strings.HasPrefix(string(data), "#!/bin/sh\n") {
		t.Errorf("%s does not begin with the `#!/bin/sh` shebang", crashResumeScriptRel)
	}
	//nolint:gosec // G204: `git ls-files -s` on a fixed path in the repository under test
	ls := exec.CommandContext(t.Context(), "git", "ls-files", "-s", crashResumeScriptRel)
	ls.Dir = root
	out, err := ls.Output()
	if err != nil {
		t.Skipf("git ls-files is unavailable here: %v", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		// Before the first commit the file is untracked, so there is no index entry to read. The executable
		// bit on disk is what `git add` will record, so assert THAT rather than skipping the mode question.
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", crashResumeScriptRel, statErr)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s is not executable on disk (%v): `make proof` EXECs it, and the commit would record 100644", crashResumeScriptRel, info.Mode().Perm())
		}
		t.Skipf("%s is not in the git index yet (mode %v on disk); the index check bites from the commit on", crashResumeScriptRel, info.Mode().Perm())
	}
	if !strings.HasPrefix(string(out), "100755 ") {
		t.Errorf("%s is not recorded 100755 in the index (%q): `make proof` EXECs it", crashResumeScriptRel, strings.TrimSpace(string(out)))
	}
}

// TestProofCrashResumeFrameLiteralsMatchProofSh joins P4-4's copy of the frame literals to proof.sh's block,
// which scripts/ci/proof_test.go already joins to internal/harness/frame. Without this the copy would be a
// fourth, unguarded one, and a drifted literal would make the script's byte-exactness assertion compare the
// frame with itself.
func TestProofCrashResumeFrameLiteralsMatchProofSh(t *testing.T) {
	t.Parallel()
	mine, rest := crashResumeBlock(t, crashResumeScriptRel, crashResumeBlockOpen, crashResumeBlockClose)
	theirs, _ := crashResumeBlock(t, crashResumeProofShRel, crashResumeProofShOpen, crashResumeProofShClose)

	for _, name := range crashResumeSharedLiterals {
		got, ok := mine[name]
		if !ok {
			t.Errorf("%s declares no %s: the frame it rebuilds is no longer pinned", crashResumeScriptRel, name)
			continue
		}
		want, ok := theirs[name]
		if !ok {
			t.Errorf("%s declares no %s: this test's expectation list has drifted from the source block", crashResumeProofShRel, name)
			continue
		}
		if got != want {
			t.Errorf("%s: %s = %q, want %s's %q", crashResumeScriptRel, name, got, crashResumeProofShRel, want)
		}
	}
	for name := range mine {
		if !slices.Contains(crashResumeSharedLiterals, name) {
			t.Errorf("%s declares %s inside the joined block, but this test pins no proof.sh source for it", crashResumeScriptRel, name)
		}
		// A constant nothing reads has silently stopped being asserted.
		if !strings.Contains(rest, "$"+name) && !strings.Contains(rest, "${"+name+"}") {
			t.Errorf("%s declares %s but never uses it: the literal is no longer asserted against anything", crashResumeScriptRel, name)
		}
	}
}

// crashResumeShellInt reads a `name=<integer>` assignment out of proof-crash-resume.sh's constants.
func crashResumeShellInt(t *testing.T, name string) int {
	t.Helper()
	//nolint:gosec // G304: a fixed path under the repository under test
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), crashResumeScriptRel))
	if err != nil {
		t.Fatalf("reading %s: %v", crashResumeScriptRel, err)
	}
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=([0-9]+)\b`)
	m := re.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("%s declares no integer constant %s", crashResumeScriptRel, name)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("%s: %s is not an integer: %v", crashResumeScriptRel, name, err)
	}
	return n
}

// crashResumeSourceInt reads an integer out of a source file with a caller-supplied pattern whose first group
// is the number, and fails loudly when the pattern no longer matches (a moved constant must be loud).
func crashResumeSourceInt(t *testing.T, rel, pattern string) int {
	t.Helper()
	//nolint:gosec // G304: a fixed path under the repository under test
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	m := regexp.MustCompile(pattern).FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("%s no longer matches %q: the drift join has lost its source", rel, pattern)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("%s: %q is not an integer: %v", rel, m[1], err)
	}
	return n
}

// TestProofCrashResumeTimingConstantsMatchTheSources is the reason P4-4 needs a drift test of its own: arm B's
// whole claim is `offline_observed <= last_seen_at + lease_seconds + 5 s`, computed in the script from numbers
// the backend owns. A lease that moves in the migration and not in the script would turn that claim into a
// tautology (a longer lease in the script) or a false red (a shorter one), silently and with a green suite.
func TestProofCrashResumeTimingConstantsMatchTheSources(t *testing.T) {
	t.Parallel()

	lease := crashResumeShellInt(t, "lease_seconds")
	proto := crashResumeSourceInt(t, "internal/protocol/limits.go", `LeaseDefaultSeconds\s*=\s*([0-9]+)`)
	if lease != proto {
		t.Errorf("lease_seconds = %d in %s but protocol.LeaseDefaultSeconds = %d: arm B's claim is computed against the wrong lease",
			lease, crashResumeScriptRel, proto)
	}
	schema := crashResumeSourceInt(t, "supabase/migrations/20260830120000_brigade_schema.sql",
		`lease_seconds\s+integer not null default ([0-9]+)`)
	if lease != schema {
		t.Errorf("lease_seconds = %d in %s but the sessions table defaults to %d: the backend and the proof disagree",
			lease, crashResumeScriptRel, schema)
	}

	// The row's own "+ 5 s" (.context/plans/implementation/08-phases.md:109). It is a plan number, not a code
	// constant, so the only thing that can be asserted is that nobody widened it to make a red run green.
	if slack := crashResumeShellInt(t, "lease_claim_slack"); slack != 5 {
		t.Errorf("lease_claim_slack = %d, want 5: the P4-4 row says \"within lease + 5 s\" and widening it would make the claim weaker without saying so", slack)
	}

	// budget_watcher_exit is a hang catcher on the CLOSE path, so it must exceed one full liveness tick plus
	// the death close budget; below that sum a healthy run would time out.
	poll := crashResumeSourceInt(t, "internal/harness/watch/watch.go", `DefaultPollInterval\s*=\s*([0-9]+) \* time\.Second`)
	death := crashResumeSourceInt(t, "internal/harness/watch/watch.go", `DefaultCloseWaitDeath\s*=\s*([0-9]+) \* time\.Second`)
	if got := crashResumeShellInt(t, "budget_watcher_exit"); got <= poll+death {
		t.Errorf("budget_watcher_exit = %ds but one liveness tick (%ds) plus the death close budget (%ds) is %ds: a healthy close would time out",
			got, poll, death, poll+death)
	}
	// The quiet window has to span both of the adapter's settling drains and give the polling drain one turn.
	if got := crashResumeShellInt(t, "quiet_after_catchup"); got < 10 {
		t.Errorf("quiet_after_catchup = %ds: the adapter's polling drain is 10 s (internal/adapters/supabase/watch.go), so a shorter window cannot show a repeat", got)
	}
	// Five is the plan row's own number and the analyser's exactly-once arithmetic rests on it.
	if got := crashResumeShellInt(t, "n_messages"); got != 5 {
		t.Errorf("n_messages = %d, want 5: the P4-4 row says \"alice sends 5 messages\"", got)
	}
}

// runCrashResumeAnalyser drives the real script's `catchup` mode over dir and reads the catchup.json it wrote.
func runCrashResumeAnalyser(t *testing.T, dir string) map[string]any {
	t.Helper()
	script := filepath.Join(testutil.RepoRoot(t), crashResumeScriptRel)
	//nolint:gosec // G204: a fixed script path under the repository and this test's own fixture directory
	cmd := exec.CommandContext(t.Context(), "sh", script, "catchup", dir)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("catchup %s: %v\n%s", dir, err, out)
	}
	//nolint:gosec // G304: the catchup.json the analyser just wrote under a t.TempDir() copy
	data, err := os.ReadFile(filepath.Join(dir, "catchup.json"))
	if err != nil {
		t.Fatalf("the analyser wrote no catchup.json in %s: %v\n%s", dir, err, out)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("catchup.json is not an object: %v\n%s", err, data)
	}
	return v
}

// copyCrashResumeFixture copies one fixture directory tree into a fresh t.TempDir() so the analyser's
// catchup.json never lands in the repository and a mutation never touches the committed fixture. Unlike the
// idle-wake fixtures, a crash-resume fixture is a tree: precrash/, resumed/, sessions/ and inbox/.
func copyCrashResumeFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join(testutil.RepoRoot(t), crashResumeFixturesRel, name)
	dst := filepath.Join(t.TempDir(), name)
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, p)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		//nolint:gosec // G304: a file of the committed fixture
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		//nolint:gosec // G306: a 0600 copy under t.TempDir()
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("copying fixture %s: %v", name, err)
	}
	return dst
}

func crashResumeFixtureNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(testutil.RepoRoot(t), crashResumeFixturesRel))
	if err != nil {
		t.Fatalf("reading the fixture directory: %v", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) < 15 {
		t.Fatalf("only %d fixtures under %s; the brief pins at least fifteen analyser classes", len(names), crashResumeFixturesRel)
	}
	return names
}

// crashResumeExpect reads a fixture's expect.json: exactly the fields that define its class. Comparison is
// field-by-field and total over the declared keys, so an unexpected extra finding fails rather than passing
// unnoticed, and a key the analyser stopped emitting fails too.
func crashResumeExpect(t *testing.T, dir string) map[string]any {
	t.Helper()
	//nolint:gosec // G304: the fixture's own expectation file
	data, err := os.ReadFile(filepath.Join(dir, "expect.json"))
	if err != nil {
		t.Fatalf("fixture %s has no expect.json: %v", dir, err)
	}
	var want map[string]any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("expect.json in %s: %v", dir, err)
	}
	return want
}

// crashResumeMustDeclare are the keys every fixture has to pin, so a fixture can never be vacuous: without the
// verdict and the exactly-once arithmetic a case would "pass" while asserting nothing.
var crashResumeMustDeclare = []string{"verdict", "exactly_once", "delivered_count", "resumed", "because"}

// TestProofCrashResumeAnalyser: every fixture scores to the class its expect.json declares, and re-scoring the
// same directory is idempotent (the verifier and P4-6 re-score saved bundles, so a second run must agree).
func TestProofCrashResumeAnalyser(t *testing.T) {
	t.Parallel()
	for _, name := range crashResumeFixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := copyCrashResumeFixture(t, name)
			want := crashResumeExpect(t, dir)
			for _, k := range crashResumeMustDeclare {
				if _, ok := want[k]; !ok {
					t.Errorf("expect.json declares no %q: a fixture must say what class it stands for", k)
				}
			}
			if s, _ := want["because"].(string); strings.TrimSpace(s) == "" {
				t.Errorf("expect.json declares an empty `because`: a fixture must say what it is evidence for")
			}
			got := runCrashResumeAnalyser(t, dir)
			for k, wv := range want {
				if k == "because" {
					continue
				}
				gv, ok := got[k]
				if !ok {
					t.Errorf("catchup.json has no %q, which expect.json pins as %v", k, wv)
					continue
				}
				if !reflect.DeepEqual(gv, wv) {
					t.Errorf("%s = %#v, want %#v", k, gv, wv)
				}
			}
			again := runCrashResumeAnalyser(t, dir)
			if !reflect.DeepEqual(got, again) {
				t.Errorf("re-scoring the same directory produced a different catchup.json: the analyser is not idempotent")
			}
		})
	}
}

// crashResumeDropLine deletes the nth (1-based) line of a fixture file containing needle, failing loudly when
// there is no such line so a mutation can never silently apply to nothing.
func crashResumeDropLine(rel, needle string, nth int) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		path := filepath.Join(root, rel)
		//nolint:gosec // G304: path is filepath.Join(t.TempDir(), <constant from the table below>).
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		lines := strings.Split(string(data), "\n")
		seen, cut := 0, -1
		for i, l := range lines {
			if strings.Contains(l, needle) {
				seen++
				if seen == nth {
					cut = i
					break
				}
			}
		}
		if cut < 0 {
			t.Fatalf("%s has no line %d containing %q (only %d), so this mutation would be vacuous", rel, nth, needle, seen)
		}
		out := append(append([]string{}, lines[:cut]...), lines[cut+1:]...)
		//nolint:gosec // G306: a 0600 file under t.TempDir()
		if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

// crashResumeEditJSON rewrites one top-level key of a fixture's JSON document.
func crashResumeEditJSON(rel, key string, value any) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		path := filepath.Join(root, rel)
		//nolint:gosec // G304: path is filepath.Join(t.TempDir(), <constant from the table below>).
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s is not a JSON object: %v", rel, err)
		}
		if _, ok := doc[key]; !ok {
			t.Fatalf("%s has no key %q, so this mutation would be vacuous", rel, key)
		}
		doc[key] = value
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("re-encoding %s: %v", rel, err)
		}
		//nolint:gosec // G306: a 0600 file under t.TempDir()
		if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

// crashResumeAddSession appends one more session for bob's principal to a saved `session list` result, which
// is exactly the shape a fresh registration leaves behind.
func crashResumeAddSession(rel string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		path := filepath.Join(root, rel)
		//nolint:gosec // G304: path is filepath.Join(t.TempDir(), <constant from the table below>).
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s is not a JSON object: %v", rel, err)
		}
		result, ok := doc["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no result object, so this mutation would be vacuous", rel)
		}
		sessions, ok := result["sessions"].([]any)
		if !ok || len(sessions) == 0 {
			t.Fatalf("%s has no sessions array, so this mutation would be vacuous", rel)
		}
		result["sessions"] = append(sessions, map[string]any{
			"session_id":    "1f1f1f1f-1f1f-4f1f-8f1f-1f1f1f1f1f1f",
			"session_name":  "bob-crash-a",
			"principal_ref": "33333333-3333-4333-8333-333333333333",
			"human_label":   "bob@proof.invalid",
			"state":         "offline",
			"activity":      "idle",
			"inbound":       "accept",
		})
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("re-encoding %s: %v", rel, err)
		}
		//nolint:gosec // G306: a 0600 file under t.TempDir()
		if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
			t.Fatalf("writing %s: %v", rel, err)
		}
	}
}

func crashResumeNum(v map[string]any, key string) float64 {
	n, _ := v[key].(float64)
	return n
}

func crashResumeBool(v map[string]any, key string) bool {
	b, _ := v[key].(bool)
	return b
}

func crashResumeStr(v map[string]any, key string) string {
	s, _ := v[key].(string)
	return s
}

func crashResumeLen(v map[string]any, key string) int {
	a, _ := v[key].([]any)
	return len(a)
}

// crashResumeDropFromInjection removes the one <cross-session-message> block carrying id from every isMeta
// INJECTION record, leaving every queue-operation enqueue untouched. That is the batched-backlog shape the
// 2026-09-04 run measured (Claude Code packs several queued frames into one isMeta record, 2 ids then 3): the
// frame was queued, but the record the model actually saw is one id short.
func crashResumeDropFromInjection(rel, id string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		path := filepath.Join(root, rel)
		//nolint:gosec // G304: path is filepath.Join(t.TempDir(), <constant from the table below>).
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		edits := 0
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		for i, line := range lines {
			var rec map[string]any
			if json.Unmarshal([]byte(line), &rec) != nil {
				continue
			}
			if rec["type"] != "user" || rec["isMeta"] != true {
				continue
			}
			msg, ok := rec["message"].(map[string]any)
			if !ok {
				continue
			}
			content, ok := msg["content"].(string)
			if !ok || !strings.Contains(content, id) {
				continue
			}
			const open = "<cross-session-message from-name="
			parts := strings.Split(content, open)
			kept := parts[:1]
			for _, b := range parts[1:] {
				if strings.Contains(b, id) {
					continue
				}
				kept = append(kept, b)
			}
			msg["content"] = strings.Join(kept, open)
			out, merr := json.Marshal(rec)
			if merr != nil {
				t.Fatalf("re-encoding %s line %d: %v", rel, i+1, merr)
			}
			lines[i] = string(out)
			edits++
		}
		if edits == 0 {
			t.Fatalf("%s has no isMeta injection record carrying %s, so this mutation would be vacuous", rel, id)
		}
		//nolint:gosec // G306: a 0600 file under t.TempDir()
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

// The rows the mutations splice in, in the fixtures' own shapes and with the fixtures' own minted ids.
const (
	crashResumeMintedM3    = "a0000003-0000-4000-8000-000000000003"
	crashResumeMintedM6    = "b0000006-0000-4000-8000-000000000006"
	crashResumeMintedM0    = "a0000000-0000-4000-8000-000000000000"
	crashResumeTaskNote    = `{"type": "queue-operation", "operation": "enqueue", "content": "<task-notification>\n<task-id>fixture-bg-1</task-id>\n</task-notification>", "timestamp": "2026-09-04T20:00:21.500Z"}` + "\n" + `{"type": "queue-operation", "operation": "dequeue", "timestamp": "2026-09-04T20:00:21.540Z"}` + "\n"
	crashResumeQuoting     = `{"type": "assistant", "timestamp": "2026-09-04T20:00:22.500Z", "uuid": "f0000099-0000-4000-8000-000000000099", "message": {"role": "assistant", "content": [{"type": "text", "text": "For the record, one of them carried message-id=\"a0000003-0000-4000-8000-000000000003\"."}]}}` + "\n"
	crashResumeSecondStdin = `{"event":"stdin_write","seq":2,"at_ms":1788552018900,"note":"a second write, after the first result"}` + "\n"
	crashResumeDeferred    = `{"time":"2026-09-04T16:00:18.740-04:00","level":"INFO","msg":"message offered","message_id":"a0000004-0000-4000-8000-000000000004","outcome":"deferred","reason":"identical body","ack":false}` + "\n"
	crashResumeCloseFail   = `{"time":"2026-09-04T16:00:15.900-04:00","level":"WARN","msg":"session close failed","error":"context deadline exceeded"}` + "\n"
	crashResumeRateLimit   = `{"time":"2026-09-04T16:00:18.741-04:00","level":"INFO","msg":"message offered","message_id":"a0000004-0000-4000-8000-000000000004","outcome":"rate_limited","reason":"sender bucket","ack":false}` + "\n"
)

// proofCrashResumeMutations is the table: ONE change to a fixture, and the predicate the verdict must then
// satisfy. `because` says what the row is evidence for; `base` names the fixture when it is not the clean one.
var proofCrashResumeMutations = []struct {
	name    string
	base    string
	control bool
	mutate  mutation
	want    func(map[string]any) bool
	because string
}{
	{
		name:   "one frame enqueue is duplicated",
		mutate: appendToFile("resumed/transcript.jsonl", `{"type": "queue-operation", "operation": "enqueue", "content": "<cross-session-message from-name=\"payments-api\">\n<brigade-message team=\"ops\" message-id=\"`+crashResumeMintedM3+`\" hops=\"0\">\nbody\n</brigade-message>\n</cross-session-message>", "timestamp": "2026-09-04T20:00:21.500Z"}`+"\n"),
		want: func(v map[string]any) bool {
			return !crashResumeBool(v, "exactly_once") && crashResumeLen(v, "duplicate_ids") == 1 &&
				crashResumeNum(v, "delivered_count") == 5 && crashResumeNum(v, "frame_enqueues") == 6
		},
		because: "two frames carrying ONE id is the failure the whole proof exists to catch, and a frame count of five would hide it: the arithmetic is by id",
	},
	{
		name:   "one frame enqueue is deleted",
		mutate: crashResumeDropLine("resumed/transcript.jsonl", `message-id=\"a0000005`, 1),
		want: func(v map[string]any) bool {
			return !crashResumeBool(v, "exactly_once") && crashResumeLen(v, "missing_ids") == 1 &&
				crashResumeNum(v, "delivered_count") == 4
		},
		because: "a catch-up that loses one message must name WHICH id is missing, not merely count to four",
	},
	{
		name:   "a sixth id is enqueued",
		mutate: appendToFile("resumed/transcript.jsonl", `{"type": "queue-operation", "operation": "enqueue", "content": "<cross-session-message from-name=\"payments-api\">\n<brigade-message team=\"ops\" message-id=\"`+crashResumeMintedM6+`\" hops=\"0\">\nbody\n</brigade-message>\n</cross-session-message>", "timestamp": "2026-09-04T20:00:21.500Z"}`+"\n"),
		want: func(v map[string]any) bool {
			return !crashResumeBool(v, "exactly_once") && crashResumeLen(v, "unexpected_ids") == 1
		},
		because: "an id the sender never minted arriving in the catch-up is a backend or adapter fault, not a count that happens to be six",
	},
	{
		name:   "the pre-crash baseline M0 is enqueued again after the resume",
		mutate: appendToFile("resumed/transcript.jsonl", `{"type": "queue-operation", "operation": "enqueue", "content": "<cross-session-message from-name=\"payments-api\">\n<brigade-message team=\"ops\" message-id=\"`+crashResumeMintedM0+`\" hops=\"0\">\nbody\n</brigade-message>\n</cross-session-message>", "timestamp": "2026-09-04T20:00:21.500Z"}`+"\n"),
		want: func(v map[string]any) bool {
			return crashResumeBool(v, "precrash_replayed") && crashResumeStr(v, "verdict") == "fail:precrash_replayed"
		},
		because: "M0 was delivered AND acknowledged before the crash; the backend re-serving it after a re-registration is the single most interesting thing this proof could find",
	},
	{
		name:   "a frame enqueue's timestamp moves to BEFORE the resume launch",
		mutate: replaceInFile("resumed/transcript.jsonl", "2026-09-04T20:00:18.632Z", "2026-09-04T20:00:10.632Z"),
		want: func(v map[string]any) bool {
			return crashResumeNum(v, "delivered_count") == 4 && crashResumeNum(v, "precrash_frame_enqueues") == 2 &&
				!crashResumeBool(v, "exactly_once")
		},
		because: "a `claude -p --resume` INTERLEAVES into the ORIGINAL transcript, so the dead session's own frame enqueues are in the same file; the cut at the resume launch is the only thing that keeps them out of the arithmetic, and it has to be able to move a frame out",
	},
	{
		name:   "the resumed by-pid map names a DIFFERENT Brigade session",
		mutate: crashResumeEditJSON("resumed/map.json", "brigade_session_id", "1f1f1f1f-1f1f-4f1f-8f1f-1f1f1f1f1f1f"),
		want: func(v map[string]any) bool {
			return !crashResumeBool(v, "resumed") && crashResumeStr(v, "verdict") == "fail:resumed"
		},
		because: "a resume that registers a FRESH Brigade session is plan 3.7 case 3, not case 2, and the by-pid map's session id is where it shows",
	},
	{
		name:   "the pre-crash by-pid map names a different session",
		mutate: crashResumeEditJSON("precrash/map.json", "brigade_session_id", "1f1f1f1f-1f1f-4f1f-8f1f-1f1f1f1f1f1f"),
		want: func(v map[string]any) bool {
			return !crashResumeBool(v, "resumed")
		},
		because: "the re-attach is the equality of the two ids, so a drift on EITHER side must break it",
	},
	{
		name:   "the resumed init carries a new native session id",
		mutate: replaceInFile("resumed/stream.jsonl", `"subtype":"init","session_id":"22222222-2222-4222-8222-222222222222"`, `"subtype":"init","session_id":"2f2f2f2f-2f2f-4f2f-8f2f-2f2f2f2f2f2f"`),
		want: func(v map[string]any) bool {
			return !crashResumeBool(v, "native_reused") && crashResumeStr(v, "verdict") == "fail:native_reused"
		},
		because: "a NEW native id is what --fork-session does; it would miss the by-native map, and the proof must see it rather than infer it",
	},
	{
		name:   "a second session for bob's principal appears in the post-resume roster",
		mutate: crashResumeAddSession("sessions/post-resume.json"),
		want: func(v map[string]any) bool {
			return crashResumeNum(v, "new_bob_sessions") == 1 &&
				crashResumeStr(v, "verdict") == "fail:new_bob_sessions"
		},
		because: "a fresh registration leaves the crashed session behind until retention, so the roster GROWS by one: that growth is the single field that separates 3.7 case 2 from case 3 and cannot be satisfied by accident",
	},
	{
		name:   "the resumed SessionStart logged `resume hint refused`",
		mutate: replaceInFile("resumed/stream.jsonl", `"msg\":\"watcher started\"`, `"msg\":\"resume hint refused; registering a fresh session\",\"error\":\"conflict\"},{\"msg\":\"watcher started\"`),
		want: func(v map[string]any) bool {
			return crashResumeBool(v, "hint_refused") && crashResumeStr(v, "verdict") == "fail:hint_refused"
		},
		because: "start.go:257-269 retries once with NO hint and says so; that line is the silent path to a fresh session",
	},
	{
		name:   "the resumed SessionStart logged `resume hint skipped`",
		mutate: replaceInFile("resumed/stream.jsonl", `"msg\":\"watcher started\"`, `"msg\":\"resume hint skipped: another live watcher serves that session\"},{\"msg\":\"watcher started\"`),
		want: func(v map[string]any) bool {
			return crashResumeBool(v, "hint_skipped") && crashResumeStr(v, "verdict") == "fail:hint_skipped"
		},
		because: "a LIVE pidfile of another pid naming the session makes resumeHint drop the hint and register fresh",
	},
	{
		name:   "arm a's lease_until at the offline instant moves into the PAST",
		mutate: crashResumeEditJSON("meta.json", "lease_until_at_offline", "2026-09-04T19:59:00.000Z"),
		want: func(v map[string]any) bool {
			return crashResumeStr(v, "offline_source") == "lease" && crashResumeStr(v, "verdict") == "fail:offline_source"
		},
		because: "`lease_until` still in the future at the moment offline is first seen is the ONE field that distinguishes a close from a lapse from outside (schema.sql:163-166)",
	},
	{
		name:   "arm b's lease_until at the offline instant moves into the FUTURE",
		base:   crashResumeArmBCase,
		mutate: crashResumeEditJSON("meta.json", "lease_until_at_offline", "2026-09-04T21:00:00.000Z"),
		want: func(v map[string]any) bool {
			return crashResumeStr(v, "offline_source") == "closed" && crashResumeStr(v, "verdict") == "fail:offline_source"
		},
		because: "arm b's premise is that NOTHING closed the session; a close would make it arm a with a longer wait, and the ordering of the two kills is what keeps them different",
	},
	{
		name:   "arm b's offline is observed past the computed deadline",
		base:   crashResumeArmBCase,
		mutate: crashResumeEditJSON("meta.json", "offline_observed_ms", 1788552099000),
		want: func(v map[string]any) bool {
			b, ok := v["lease_claim_ok"].(bool)
			return ok && !b && crashResumeStr(v, "verdict") == "fail:lease_claim_ok"
		},
		because: "the P4-4 row's fourth clause made falsifiable: `offline` later than last_seen_at + 90 s + 5 s is the claim failing",
	},
	{
		name:   "arm a computes a lease claim",
		mutate: crashResumeEditJSON("meta.json", "arm", "b"),
		want: func(v map[string]any) bool {
			_, isBool := v["lease_claim_ok"].(bool)
			return isBool
		},
		because: "lease_claim_ok must be the literal null for arm a and a boolean for arm b; a null that reads as false in a shell test is how the wrong thing gets scored",
	},
	{
		name:   "a frame enqueue's content becomes a <task-notification>",
		mutate: replaceInFile("resumed/transcript.jsonl", `"content":"<cross-session-message from-name=\"payments-api\">\n<brigade-message team=\"ops\" message-id=\"a0000001`, `"content":"<task-notification>\n<x message-id=\"a0000001`),
		want: func(v map[string]any) bool {
			return crashResumeNum(v, "delivered_count") == 4 && crashResumeNum(v, "task_notification_enqueues") == 1
		},
		because: "a background task's enqueue/dequeue pair is not a delivery; selecting frames by CONTENT rather than by position is what keeps it out",
	},
	{
		name:   "an id appears only in a stdout result.origin.body",
		mutate: replaceInFile("resumed/stream.jsonl", `"origin":{"kind":"peer","body":"<cross-session-message from-name=\"payments-api\">\n<brigade-message team=\"ops\" message-id=\"a0000003`, `"origin":{"kind":"peer","body":"<brigade-message message-id=\"`+crashResumeMintedM6+`\">\nx\n<cross-session-message from-name=\"payments-api\">\n<brigade-message team=\"ops\" message-id=\"a0000003`),
		want: func(v map[string]any) bool {
			return crashResumeLen(v, "origin_body_only_ids") == 1 && crashResumeStr(v, "verdict") == "fail:origin_body_only_ids"
		},
		because: "on 2.1.260 a woken result carries the WHOLE frame on stdout in origin.body; an id that appears only there was echoed by Claude Code, never delivered",
	},
	{
		name:    "a second stdin write appears in the resumed session's log",
		mutate:  appendToFile("resumed/stdin-writes.log", crashResumeSecondStdin),
		want:    func(v map[string]any) bool { return crashResumeNum(v, "stdin_writes_resumed") == 2 },
		because: "a second write means the catch-up is no longer attributable to the resume alone",
	},
	{
		name:    "a second stdin write appears in the pre-crash session's log",
		mutate:  appendToFile("precrash/stdin-writes.log", crashResumeSecondStdin),
		want:    func(v map[string]any) bool { return crashResumeNum(v, "stdin_writes_precrash") == 2 },
		because: "the same rule on the other side: a pre-crash session that was typed at twice was not idle at the kill",
	},
	{
		name:   "the backend inbox is not empty after the catch-up",
		mutate: writeFile("inbox/after-catchup.json", `{"ok":true,"result":{"messages":[{"message_id":"a0000005-0000-4000-8000-000000000005","delivery_state":"accepted"}]}}`),
		want: func(v map[string]any) bool {
			return crashResumeNum(v, "inbox_after_catchup") == 1 && crashResumeStr(v, "verdict") == "fail:inbox_after_catchup"
		},
		because: "the backend inbox is the one instrument that does not depend on Claude Code at all; a row still `accepted` after the catch-up was injected without being acknowledged",
	},
	{
		name:    "the resumed watcher deferred a delivery",
		mutate:  appendToFile("resumed/watcher.log", crashResumeDeferred),
		want:    func(v map[string]any) bool { return crashResumeNum(v, "deferred_count") == 1 },
		because: "the five bodies are distinct on purpose; a deferral means the identical-body window swallowed one and the arithmetic would be measuring the limiter",
	},
	{
		name:    "the resumed watcher rate-limited a delivery",
		mutate:  appendToFile("resumed/watcher.log", crashResumeRateLimit),
		want:    func(v map[string]any) bool { return crashResumeNum(v, "rate_limited_count") == 1 },
		because: "five sends is well under the watcher's 10/min per-sender bucket, so a rate-limited outcome means something else is sending",
	},
	{
		name:   "the resumed watcher never acknowledged anything",
		mutate: replaceAllInFile("resumed/watcher.log", `"msg":"ack sent"`, `"msg":"ack skipped"`),
		want: func(v map[string]any) bool {
			return crashResumeNum(v, "acks_after_catchup") == 0 && crashResumeStr(v, "verdict") == "fail:acks_after_catchup"
		},
		because: "the ack is what flips delivery_state to `injected`; without it the five would be re-served on the next drain",
	},
	{
		name:    "the pre-crash liveness verdict says zombie",
		mutate:  replaceInFile("precrash/watcher.log", `"msg":"claude process is gone"`, `"msg":"claude process is a zombie"`),
		want:    func(v map[string]any) bool { return crashResumeStr(v, "claude_gone_reason") == "zombie" },
		because: "E0-5 defect 1's trap: which of the four death verdicts fired is the difference between E0-5's 27.6 s world and the shipped one, and it must be visible",
	},
	{
		name:    "the harness preamble anchor is stripped from the model-visible text",
		mutate:  replaceAllInFile("resumed/transcript.jsonl", "Brigade team message from another person", "A message from somebody"),
		want:    func(v map[string]any) bool { return !crashResumeBool(v, "anchor_present") },
		because: "the anchor is the harness's own preamble; a delivery record without it is not the shipped wrapper",
	},
	{
		name:   "the resumed context line names a different session",
		mutate: replaceAllInFile("resumed/stream.jsonl", `(11111111-1111-4111-8111-111111111111)`, `(1f1f1f1f-1f1f-4f1f-8f1f-1f1f1f1f1f1f)`),
		want: func(v map[string]any) bool {
			return !crashResumeBool(v, "context_line_names_session") &&
				crashResumeStr(v, "verdict") == "fail:context_line_names_session"
		},
		because: "the SessionStart context line is what the MODEL is told its session is; a mismatch with the map would mean the two halves of the resume disagree",
	},

	{
		name:   "every frame enqueue predates the resume launch",
		mutate: replaceAllInFile("resumed/transcript.jsonl", `"timestamp":"2026-09-04T20:00:18.`, `"timestamp":"2026-09-04T20:00:17.`),
		want: func(v map[string]any) bool {
			return crashResumeNum(v, "delivered_count") == 0 && !crashResumeBool(v, "exactly_once") &&
				crashResumeLen(v, "missing_ids") == 5 && crashResumeStr(v, "verdict") == "fail:exactly_once"
		},
		because: "the whole-file case of the interleave, not the one-frame case: if EVERY frame in the resumed transcript belongs to the dead session then nothing was caught up at all, and an analyser that fell back to counting the file when the post-cut set came out empty would score that five-of-five",
	},
	{
		name:   "the resumed by-pid map carries the pre-crash session's own pid",
		mutate: crashResumeEditJSON("resumed/map.json", "claude_pid", 4242),
		want: func(v map[string]any) bool {
			return !crashResumeBool(v, "pid_changed") && crashResumeStr(v, "verdict") == "fail:pid_changed"
		},
		because: "the crash deliberately leaves the pre-crash by-pid map on disk (4.8), so `resumed/map.json` being that STALE file rather than the resumed session's own is the collection mistake this bundle is most exposed to -- and it would make `resumed` true for exactly the wrong reason",
	},
	{
		name:   "the batched isMeta backlog record is one id short",
		mutate: crashResumeDropFromInjection("resumed/transcript.jsonl", "a0000005-0000-4000-8000-000000000005"),
		want: func(v map[string]any) bool {
			return crashResumeNum(v, "delivered_count") == 5 && crashResumeNum(v, "injected_count") == 4 &&
				crashResumeLen(v, "enqueued_not_injected") == 1 &&
				crashResumeStr(v, "verdict") == "fail:enqueued_not_injected"
		},
		because: "an enqueue is a QUEUED frame and an injection is what the MODEL saw; Claude Code batches the backlog into one isMeta record, so a batch that drops an id leaves five enqueues and four injections and the id count alone still reads five of five",
	},
	{
		name:   "the pre-crash watcher could not close the session",
		mutate: appendToFile("precrash/watcher.log", crashResumeCloseFail),
		want: func(v map[string]any) bool {
			return crashResumeNum(v, "session_close_failed") == 1 &&
				crashResumeStr(v, "verdict") == "fail:session_close_failed"
		},
		because: "arm a's whole path is the close; the live run asserts the absence of this line but the OFFLINE re-score did not, and acceptance item 5 and the verifier lane both re-score saved bundles rather than re-run them",
	},

	// ---- the controls: four changes that must NOT flip anything ------------------------------------------
	{
		name:    "control: a <task-notification> enqueue/dequeue pair is appended",
		control: true,
		mutate:  appendToFile("resumed/transcript.jsonl", crashResumeTaskNote),
		want: func(v map[string]any) bool {
			return crashResumeBool(v, "exactly_once") && crashResumeNum(v, "delivered_count") == 5 &&
				crashResumeNum(v, "task_notification_enqueues") == 1 && crashResumeStr(v, "verdict") == "pass"
		},
		because: "a background task travels through the same queue and must be counted separately, never as a delivery and never as a failure",
	},
	{
		name:    "control: an assistant text block quotes one of the five ids",
		control: true,
		mutate:  appendToFile("resumed/transcript.jsonl", crashResumeQuoting),
		want: func(v map[string]any) bool {
			return crashResumeBool(v, "exactly_once") && crashResumeNum(v, "delivered_count") == 5 &&
				crashResumeStr(v, "verdict") == "pass"
		},
		because: "a model paraphrasing its own context is not a second delivery: ids are read out of queue-operation enqueues, never out of model prose",
	},
	{
		name:    "control: the delivery is mid-turn rather than at a boundary",
		base:    "mid-turn-delivery",
		control: true,
		mutate:  appendToFile("resumed/transcript.jsonl", crashResumeTaskNote),
		want: func(v map[string]any) bool {
			return crashResumeStr(v, "mode") == "mid-turn" && crashResumeBool(v, "exactly_once") &&
				crashResumeStr(v, "verdict") == "pass"
		},
		because: "the watcher fetches before it announces ready, so a mid-turn catch-up is correct behaviour; P4-3's boundary requirement is DROPPED here, not inverted",
	},
	{
		name:    "control: the liveness verdict was a zombie",
		base:    "zombie-verdict",
		control: true,
		mutate:  appendToFile("resumed/transcript.jsonl", crashResumeTaskNote),
		want: func(v map[string]any) bool {
			return crashResumeStr(v, "claude_gone_reason") == "zombie" && crashResumeStr(v, "verdict") == "pass"
		},
		because: "the zombie branch is RECORDED, never asserted: lifecycle.go:79 treats it as dead and the run must stay green",
	},
}

// TestProofCrashResumeAnalyserMutations: the positive control, then every row shown to bite.
func TestProofCrashResumeAnalyserMutations(t *testing.T) {
	t.Parallel()
	t.Run("the unmutated clean fixture scores as a clean re-attach", func(t *testing.T) {
		t.Parallel()
		v := runCrashResumeAnalyser(t, copyCrashResumeFixture(t, crashResumeCleanCase))
		if crashResumeStr(v, "verdict") != "pass" || !crashResumeBool(v, "exactly_once") ||
			!crashResumeBool(v, "resumed") || !crashResumeBool(v, "native_reused") ||
			crashResumeNum(v, "delivered_count") != 5 || crashResumeBool(v, "precrash_replayed") ||
			crashResumeNum(v, "new_bob_sessions") != 0 {
			t.Fatalf("the clean fixture does not score clean: %#v", v)
		}
	})
	for _, m := range proofCrashResumeMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			base := m.base
			if base == "" {
				base = crashResumeCleanCase
			}
			// A row whose predicate already holds on the UNMUTATED fixture is vacuous: it would pass whatever
			// the analyser did. Controls are the deliberate exception -- their predicate is "nothing moved".
			if !m.control {
				if v0 := runCrashResumeAnalyser(t, copyCrashResumeFixture(t, base)); m.want(v0) {
					t.Fatalf("VACUOUS row: the predicate already holds on the unmutated %s fixture, so it proves nothing (%s)", base, m.because)
				}
			}
			dir := copyCrashResumeFixture(t, base)
			m.mutate(t, dir)
			v := runCrashResumeAnalyser(t, dir)
			if !m.want(v) {
				t.Fatalf("the mutation did not move the verdict as expected (%s): verdict=%q exactly_once=%v delivered=%v duplicate=%d missing=%d unexpected=%d mode=%q offline_source=%q lease_claim_ok=%v",
					m.because, crashResumeStr(v, "verdict"), crashResumeBool(v, "exactly_once"),
					crashResumeNum(v, "delivered_count"), crashResumeLen(v, "duplicate_ids"),
					crashResumeLen(v, "missing_ids"), crashResumeLen(v, "unexpected_ids"),
					crashResumeStr(v, "mode"), crashResumeStr(v, "offline_source"), v["lease_claim_ok"])
			}
			if m.control {
				t.Logf("control held: %s -> verdict %q", m.name, crashResumeStr(v, "verdict"))
			} else {
				t.Logf("row bit: %s -> verdict %q", m.name, crashResumeStr(v, "verdict"))
			}
		})
	}
}
