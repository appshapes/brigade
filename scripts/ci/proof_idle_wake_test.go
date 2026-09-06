// The Docker-free, model-free half of scripts/proof-idle-wake.sh (P4-3).
//
// The live script spends real `claude -p` sessions and never runs in CI; what this file owns is the part of it
// that is a pure function and therefore testable here: `wake <dir>`, the jq analyser that scores an idle wake
// from saved artefacts. Every class the analyser can reach has a small hand-sized fixture under
// testdata/proof-idle-wake/<case>/ (stream.jsonl, transcript.jsonl, meta.json, stdin-writes.log, expect.json),
// CUT FROM THE REAL 2026-09-04 run's evidence on Claude Code 2.1.260 with every identifier re-minted, never
// invented and never copied from testdata/proof-headless/ (whose boundary fixture carries a queued_command
// attachment and no isMeta record -- the opposite of an idle wake). The mutation table then makes ONE change to
// the clean fixture and requires the verdict to flip, with the unmutated copy and three deliberate non-flips as
// controls, so each row is shown to bite.
//
// It also closes the frame-literal triplication: proof-idle-wake.sh's delimited block is compared against
// scripts/proof.sh's, which scripts/ci/proof_test.go already joins to the Go sources.
package ci_test

import (
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const (
	idleWakeScriptRel   = "scripts/proof-idle-wake.sh"
	idleWakeFixturesRel = "scripts/ci/testdata/proof-idle-wake"
	idleWakeCleanCase   = "woke-boundary"
)

// The two marker lines delimiting proof-idle-wake.sh's own frame-literal block. Matched in full, so a renamed
// or deleted marker fails loudly rather than silently emptying the block.
const (
	idleWakeBlockOpen  = "# ---- frame literals, byte-identical to scripts/proof.sh's drift-checked block and joined to it by scripts/ci/proof_idle_wake_test.go (do not edit by hand) ----"
	idleWakeBlockClose = "# ---- end frame literals ----"
)

// The same two markers in scripts/proof.sh. proof_test.go owns that file's join to the Go sources; this test
// only reads it, and never edits proof_test.go.
const (
	idleWakeProofShRel   = "scripts/proof.sh"
	idleWakeProofShOpen  = "# ---- constants checked against the Go sources by scripts/ci/proof_test.go (do not edit by hand) ----"
	idleWakeProofShClose = "# ---- end constants ----"
)

// idleWakeSharedLiterals are the constants proof-idle-wake.sh copies out of proof.sh. Every one of them must be
// byte-identical in both files: a frame the script rebuilds from a drifted literal would "prove" byte-exactness
// against itself.
var idleWakeSharedLiterals = []string{
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

var idleWakeAssignment = regexp.MustCompile(`^([a-z][a-z0-9_]*)=(.*)$`)

// idleWakeBlock splits a script into the parsed literal block and the rest of the file (the "rest" is what the
// used-somewhere-else rule is checked against). A missing or empty block is reported, never tolerated.
func idleWakeBlock(t *testing.T, rel, open, closeMarker string) (map[string]string, string) {
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
		m := idleWakeAssignment.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("%s: literal-block line is not a `name=value` assignment: %q", rel, line)
			continue
		}
		out[m[1]] = idleWakeUnquote(m[2])
	}
	if len(out) == 0 {
		t.Fatalf("%s: the literal block is empty", rel)
	}
	return out, rest
}

// idleWakeUnquote undoes the scripts' own quoting: a single-quoted value, with the `'"'"'` idiom standing for a
// literal apostrophe. (The same rules as scripts/ci/proof_test.go's proofUnquote, written here so that this test
// never edits that file.)
func idleWakeUnquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
		v = v[1 : len(v)-1]
		v = strings.ReplaceAll(v, `'"'"'`, "'")
	}
	return v
}

// TestProofIdleWakeScriptParses: `sh -n`, the shebang and the 100755 index mode (`make proof` EXECs the file).
func TestProofIdleWakeScriptParses(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	path := filepath.Join(root, idleWakeScriptRel)
	//nolint:gosec // G204: a fixed path under the repository under test
	cmd := exec.CommandContext(t.Context(), "sh", "-n", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n %s: %v\n%s", idleWakeScriptRel, err, out)
	}
	//nolint:gosec // G304: as above
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", idleWakeScriptRel, err)
	}
	if !strings.HasPrefix(string(data), "#!/bin/sh\n") {
		t.Errorf("%s does not begin with the `#!/bin/sh` shebang", idleWakeScriptRel)
	}
	//nolint:gosec // G204: `git ls-files -s` on a fixed path in the repository under test
	ls := exec.CommandContext(t.Context(), "git", "ls-files", "-s", idleWakeScriptRel)
	ls.Dir = root
	out, err := ls.Output()
	if err != nil {
		t.Skipf("git ls-files is unavailable here: %v", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		// Before the first commit the file is untracked, so there is no index entry to read. The executable bit
		// on disk is what `git add` will record, so assert THAT rather than skipping the mode question entirely.
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", idleWakeScriptRel, statErr)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s is not executable on disk (%v): `make proof` EXECs it, and the commit would record 100644", idleWakeScriptRel, info.Mode().Perm())
		}
		t.Skipf("%s is not in the git index yet (mode %v on disk); the index check bites from the commit on", idleWakeScriptRel, info.Mode().Perm())
	}
	if !strings.HasPrefix(string(out), "100755 ") {
		t.Errorf("%s is not recorded 100755 in the index (%q): `make proof` EXECs it", idleWakeScriptRel, strings.TrimSpace(string(out)))
	}
}

// TestProofIdleWakeFrameLiteralsMatchProofSh joins P4-3's copy of the frame literals to proof.sh's block, which
// scripts/ci/proof_test.go already joins to internal/harness/frame. Without this the copy would be a third,
// unguarded one, and a drifted literal would make the script's byte-exactness assertion compare the frame with
// itself.
func TestProofIdleWakeFrameLiteralsMatchProofSh(t *testing.T) {
	t.Parallel()
	mine, rest := idleWakeBlock(t, idleWakeScriptRel, idleWakeBlockOpen, idleWakeBlockClose)
	theirs, _ := idleWakeBlock(t, idleWakeProofShRel, idleWakeProofShOpen, idleWakeProofShClose)

	for _, name := range idleWakeSharedLiterals {
		got, ok := mine[name]
		if !ok {
			t.Errorf("%s declares no %s: the frame it rebuilds is no longer pinned", idleWakeScriptRel, name)
			continue
		}
		want, ok := theirs[name]
		if !ok {
			t.Errorf("%s declares no %s: this test's expectation list has drifted from the source block", idleWakeProofShRel, name)
			continue
		}
		if got != want {
			t.Errorf("%s: %s = %q, want %s's %q", idleWakeScriptRel, name, got, idleWakeProofShRel, want)
		}
	}
	for name := range mine {
		if !slices.Contains(idleWakeSharedLiterals, name) {
			t.Errorf("%s declares %s inside the joined block, but this test pins no proof.sh source for it", idleWakeScriptRel, name)
		}
		// A constant nothing reads has silently stopped being asserted.
		if !strings.Contains(rest, "$"+name) && !strings.Contains(rest, "${"+name+"}") {
			t.Errorf("%s declares %s but never uses it: the literal is no longer asserted against anything", idleWakeScriptRel, name)
		}
	}
}

// idleWakeTopLevel reads one top-level `name='value'` assignment out of a script, unquoting it the same way
// idleWakeBlock does. Used for the literals that live OUTSIDE the joined block but are still copies of another
// script's, which the delimited-block join above cannot see.
func idleWakeTopLevel(t *testing.T, rel, name string) string {
	t.Helper()
	//nolint:gosec // G304: a fixed path under the repository under test
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=(.*)$`)
	m := re.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("%s declares no top-level %s=", rel, name)
	}
	return idleWakeUnquote(m[1])
}

// TestProofIdleWakeUnjoinedLiteralsMatchTheirSources closes the three copies the delimited block does not cover.
// `anchor` is the delivery anchor the analyser asserts `anchor_present` on and is a verbatim PREFIX of the joined
// frame_preamble_head_shared — the piece every level shares (P5-12), so the anchor detects delivery at every level —
// and a drift in one without the other would make the two disagree silently; the decoy marker and file lists are
// lifted verbatim from proof-headless.sh's plant_decoys and mean nothing if they drift from it.
func TestProofIdleWakeUnjoinedLiteralsMatchTheirSources(t *testing.T) {
	t.Parallel()
	mine, _ := idleWakeBlock(t, idleWakeScriptRel, idleWakeBlockOpen, idleWakeBlockClose)
	anchor := idleWakeTopLevel(t, idleWakeScriptRel, "anchor")
	if anchor == "" {
		t.Fatalf("%s declares an empty anchor: `anchor_present` would then be vacuously true", idleWakeScriptRel)
	}
	if !strings.HasPrefix(mine["frame_preamble_head_shared"], anchor) {
		t.Errorf("%s: anchor %q is not a prefix of the joined frame_preamble_head_shared %q: the analyser's anchor_present no longer tests the frame the script rebuilds",
			idleWakeScriptRel, anchor, mine["frame_preamble_head_shared"])
	}
	const headlessRel = "scripts/proof-headless.sh"
	if got, want := idleWakeTopLevel(t, idleWakeScriptRel, "anchor"), idleWakeTopLevel(t, headlessRel, "anchor"); got != want {
		t.Errorf("%s: anchor = %q, want %s's %q", idleWakeScriptRel, got, headlessRel, want)
	}
	for _, name := range []string{"decoy_markers", "decoy_files"} {
		if got, want := idleWakeTopLevel(t, idleWakeScriptRel, name), idleWakeTopLevel(t, headlessRel, name); got != want {
			t.Errorf("%s: %s = %q, want %s's %q (the decoy plant and the exfiltration scan look for these VALUES)",
				idleWakeScriptRel, name, got, headlessRel, want)
		}
	}
}

// idleWakeVerdict is the subset of wake.json the tests read.
type idleWakeVerdict struct {
	Session                     string        `json:"session"`
	Role                        string        `json:"role"`
	WakeCount                   int           `json:"wake_count"`
	WakesWoke                   int           `json:"wakes_woke"`
	Wakes                       []idleWakeOne `json:"wakes"`
	TaskNotificationPairs       int           `json:"task_notification_pairs"`
	QueuedCommandAttachments    int           `json:"queued_command_attachments"`
	InitCount                   int           `json:"init_count"`
	ResultCount                 int           `json:"result_count"`
	SecondInit                  bool          `json:"second_init"`
	ResultOriginKinds           []*string     `json:"result_origin_kinds"`
	StdinWritesTotal            int           `json:"stdin_writes_total"`
	StdinWritesAfterFirstResult int           `json:"stdin_writes_after_first_result"`
	StdinSealed                 bool          `json:"stdin_sealed"`
	ResultSubtypes              []string      `json:"result_subtypes"`
	WrapperPinned               *bool         `json:"wrapper_pinned"`
	RecordsAfterHoldStart       *int          `json:"records_after_hold_start"`
	ControlOK                   *bool         `json:"control_ok"`
	Verdict                     string        `json:"verdict"`
}

type idleWakeOne struct {
	Wake                      int   `json:"wake"`
	Woke                      bool  `json:"woke"`
	IsMetaPresent             bool  `json:"isMeta_present"`
	IsMetaRecordsSeen         int   `json:"isMeta_records_seen"`
	MarkerFound               bool  `json:"marker_found"`
	MarkerInAssistantText     bool  `json:"marker_in_assistant_text"`
	MarkerInSendCommand       bool  `json:"marker_in_send_command"`
	CompositionOK             bool  `json:"composition_ok"`
	AnchorPresent             bool  `json:"anchor_present"`
	WrapperPinned             *bool `json:"wrapper_pinned"`
	WrapperMidturnSeen        *bool `json:"wrapper_midturn_seen"`
	RecordsDuringIdle         int   `json:"records_during_idle"`
	RemovesAfterEnqueue       int   `json:"removes_after_enqueue"`
	LifecycleJoinOK           bool  `json:"lifecycle_join_ok"`
	AssistantRecords          int   `json:"assistant_records"`
	EnqueueToDequeueMs        *int  `json:"enqueue_to_dequeue_ms"`
	EnqueueToIsMetaMs         *int  `json:"enqueue_to_isMeta_ms"`
	EnqueueToFirstAssistantMs *int  `json:"enqueue_to_first_assistant_ms"`
	SendAcceptedToEnqueueMs   *int  `json:"send_accepted_to_enqueue_ms"`
	Origin                    *struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		From string `json:"from"`
	} `json:"origin"`
}

// idleWakeExpect is a fixture's expect.json: exactly the fields that define its class. Comparison is
// field-by-field and total, so an unexpected extra finding fails rather than passing unnoticed.
type idleWakeExpect struct {
	Role                        string              `json:"role"`
	Verdict                     string              `json:"verdict"`
	WakeCount                   int                 `json:"wake_count"`
	WakesWoke                   int                 `json:"wakes_woke"`
	TaskNotificationPairs       int                 `json:"task_notification_pairs"`
	QueuedCommandAttachments    int                 `json:"queued_command_attachments"`
	InitCount                   int                 `json:"init_count"`
	ResultCount                 int                 `json:"result_count"`
	SecondInit                  bool                `json:"second_init"`
	ResultOriginKinds           []*string           `json:"result_origin_kinds"`
	StdinWritesTotal            int                 `json:"stdin_writes_total"`
	StdinWritesAfterFirstResult int                 `json:"stdin_writes_after_first_result"`
	StdinSealed                 bool                `json:"stdin_sealed"`
	ResultSubtypes              []string            `json:"result_subtypes"`
	WrapperPinned               *bool               `json:"wrapper_pinned"`
	RecordsAfterHoldStart       *int                `json:"records_after_hold_start"`
	ControlOK                   *bool               `json:"control_ok"`
	Wakes                       []idleWakeExpectOne `json:"wakes"`
	Because                     string              `json:"because"`
}

type idleWakeExpectOne struct {
	Wake                      int    `json:"wake"`
	Woke                      bool   `json:"woke"`
	IsMetaPresent             bool   `json:"isMeta_present"`
	IsMetaRecordsSeen         int    `json:"isMeta_records_seen"`
	MarkerFound               bool   `json:"marker_found"`
	MarkerInAssistantText     bool   `json:"marker_in_assistant_text"`
	MarkerInSendCommand       bool   `json:"marker_in_send_command"`
	CompositionOK             bool   `json:"composition_ok"`
	AnchorPresent             bool   `json:"anchor_present"`
	WrapperPinned             *bool  `json:"wrapper_pinned"`
	WrapperMidturnSeen        *bool  `json:"wrapper_midturn_seen"`
	RecordsDuringIdle         int    `json:"records_during_idle"`
	RemovesAfterEnqueue       int    `json:"removes_after_enqueue"`
	LifecycleJoinOK           bool   `json:"lifecycle_join_ok"`
	AssistantRecords          int    `json:"assistant_records"`
	EnqueueToDequeueMs        *int   `json:"enqueue_to_dequeue_ms"`
	EnqueueToIsMetaMs         *int   `json:"enqueue_to_isMeta_ms"`
	EnqueueToFirstAssistantMs *int   `json:"enqueue_to_first_assistant_ms"`
	SendAcceptedToEnqueueMs   *int   `json:"send_accepted_to_enqueue_ms"`
	OriginKind                string `json:"origin_kind"`
	OriginName                string `json:"origin_name"`
}

// runIdleWakeAnalyser drives the real script's `wake` mode over dir and reads the wake.json it wrote.
func runIdleWakeAnalyser(t *testing.T, dir string) idleWakeVerdict {
	t.Helper()
	script := filepath.Join(testutil.RepoRoot(t), idleWakeScriptRel)
	//nolint:gosec // G204: a fixed script path under the repository and this test's own fixture directory
	cmd := exec.CommandContext(t.Context(), "sh", script, "wake", dir)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wake %s: %v\n%s", dir, err, out)
	}
	//nolint:gosec // G304: the wake.json the analyser just wrote under a t.TempDir() copy
	data, err := os.ReadFile(filepath.Join(dir, "wake.json"))
	if err != nil {
		t.Fatalf("the analyser wrote no wake.json in %s: %v\n%s", dir, err, out)
	}
	var v idleWakeVerdict
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("wake.json is not the expected shape: %v\n%s", err, data)
	}
	return v
}

// copyIdleWakeFixture copies one fixture directory into a fresh t.TempDir() so the analyser's wake.json never
// lands in the repository and a mutation never touches the committed fixture.
func copyIdleWakeFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join(testutil.RepoRoot(t), idleWakeFixturesRel, name)
	dst := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		//nolint:gosec // G304: a file of the committed fixture
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		//nolint:gosec // G306: a 0600 copy under t.TempDir()
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", e.Name(), err)
		}
	}
	return dst
}

func idleWakeFixtureNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(testutil.RepoRoot(t), idleWakeFixturesRel))
	if err != nil {
		t.Fatalf("reading the fixture directory: %v", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) < 13 {
		t.Fatalf("only %d fixtures under %s; the brief pins at least thirteen analyser classes", len(names), idleWakeFixturesRel)
	}
	return names
}

func idleWakeOriginKind(w idleWakeOne) string {
	if w.Origin == nil {
		return ""
	}
	return w.Origin.Kind
}

func idleWakeOriginName(w idleWakeOne) string {
	if w.Origin == nil {
		return ""
	}
	return w.Origin.Name
}

func idleWakeEqInt(t *testing.T, label string, got, want *int) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %s, want %s", label, idleWakeShowInt(got), idleWakeShowInt(want))
	case *got != *want:
		t.Errorf("%s = %d, want %d", label, *got, *want)
	}
}

func idleWakeShowInt(v *int) string {
	if v == nil {
		return "null"
	}
	return strconv.Itoa(*v)
}

func idleWakeEqBool(t *testing.T, label string, got, want *bool) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %s, want %s", label, idleWakeShowBool(got), idleWakeShowBool(want))
	case *got != *want:
		t.Errorf("%s = %v, want %v", label, *got, *want)
	}
}

func idleWakeShowBool(v *bool) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatBool(*v)
}

// TestProofIdleWakeAnalyser: every fixture scores to the class its expect.json declares.
func TestProofIdleWakeAnalyser(t *testing.T) {
	t.Parallel()
	for _, name := range idleWakeFixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := copyIdleWakeFixture(t, name)
			//nolint:gosec // G304: the fixture's own expectation file
			data, err := os.ReadFile(filepath.Join(dir, "expect.json"))
			if err != nil {
				t.Fatalf("fixture %s has no expect.json: %v", name, err)
			}
			var want idleWakeExpect
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatalf("expect.json: %v", err)
			}
			got := runIdleWakeAnalyser(t, dir)
			if want.Because == "" {
				t.Errorf("expect.json declares no `because`: a fixture must say what class it stands for")
			}
			if got.Role != want.Role {
				t.Errorf("role = %q, want %q", got.Role, want.Role)
			}
			if got.Verdict != want.Verdict {
				t.Errorf("verdict = %q, want %q", got.Verdict, want.Verdict)
			}
			if got.WakeCount != want.WakeCount {
				t.Errorf("wake_count = %d, want %d", got.WakeCount, want.WakeCount)
			}
			if got.WakesWoke != want.WakesWoke {
				t.Errorf("wakes_woke = %d, want %d", got.WakesWoke, want.WakesWoke)
			}
			if got.TaskNotificationPairs != want.TaskNotificationPairs {
				t.Errorf("task_notification_pairs = %d, want %d", got.TaskNotificationPairs, want.TaskNotificationPairs)
			}
			if got.QueuedCommandAttachments != want.QueuedCommandAttachments {
				t.Errorf("queued_command_attachments = %d, want %d", got.QueuedCommandAttachments, want.QueuedCommandAttachments)
			}
			if got.InitCount != want.InitCount {
				t.Errorf("init_count = %d, want %d", got.InitCount, want.InitCount)
			}
			if got.ResultCount != want.ResultCount {
				t.Errorf("result_count = %d, want %d", got.ResultCount, want.ResultCount)
			}
			if got.SecondInit != want.SecondInit {
				t.Errorf("second_init = %v, want %v", got.SecondInit, want.SecondInit)
			}
			if !idleWakeEqKinds(got.ResultOriginKinds, want.ResultOriginKinds) {
				t.Errorf("result_origin_kinds = %s, want %s", idleWakeShowKinds(got.ResultOriginKinds), idleWakeShowKinds(want.ResultOriginKinds))
			}
			if got.StdinWritesTotal != want.StdinWritesTotal {
				t.Errorf("stdin_writes_total = %d, want %d", got.StdinWritesTotal, want.StdinWritesTotal)
			}
			if got.StdinWritesAfterFirstResult != want.StdinWritesAfterFirstResult {
				t.Errorf("stdin_writes_after_first_result = %d, want %d", got.StdinWritesAfterFirstResult, want.StdinWritesAfterFirstResult)
			}
			if got.StdinSealed != want.StdinSealed {
				t.Errorf("stdin_sealed = %v, want %v", got.StdinSealed, want.StdinSealed)
			}
			if !slices.Equal(got.ResultSubtypes, want.ResultSubtypes) {
				t.Errorf("result_subtypes = %v, want %v", got.ResultSubtypes, want.ResultSubtypes)
			}
			idleWakeEqBool(t, "wrapper_pinned", got.WrapperPinned, want.WrapperPinned)
			idleWakeEqInt(t, "records_after_hold_start", got.RecordsAfterHoldStart, want.RecordsAfterHoldStart)
			idleWakeEqBool(t, "control_ok", got.ControlOK, want.ControlOK)
			if len(got.Wakes) != len(want.Wakes) {
				t.Fatalf("the analyser reported %d wake(s), want %d", len(got.Wakes), len(want.Wakes))
			}
			for i, w := range want.Wakes {
				g := got.Wakes[i]
				if g.Wake != w.Wake {
					t.Errorf("wakes[%d].wake = %d, want %d", i, g.Wake, w.Wake)
				}
				if g.Woke != w.Woke {
					t.Errorf("wakes[%d].woke = %v, want %v", i, g.Woke, w.Woke)
				}
				if g.IsMetaPresent != w.IsMetaPresent {
					t.Errorf("wakes[%d].isMeta_present = %v, want %v", i, g.IsMetaPresent, w.IsMetaPresent)
				}
				if g.IsMetaRecordsSeen != w.IsMetaRecordsSeen {
					t.Errorf("wakes[%d].isMeta_records_seen = %d, want %d", i, g.IsMetaRecordsSeen, w.IsMetaRecordsSeen)
				}
				if g.MarkerFound != w.MarkerFound {
					t.Errorf("wakes[%d].marker_found = %v, want %v", i, g.MarkerFound, w.MarkerFound)
				}
				if g.MarkerInAssistantText != w.MarkerInAssistantText {
					t.Errorf("wakes[%d].marker_in_assistant_text = %v, want %v", i, g.MarkerInAssistantText, w.MarkerInAssistantText)
				}
				if g.MarkerInSendCommand != w.MarkerInSendCommand {
					t.Errorf("wakes[%d].marker_in_send_command = %v, want %v", i, g.MarkerInSendCommand, w.MarkerInSendCommand)
				}
				if g.CompositionOK != w.CompositionOK {
					t.Errorf("wakes[%d].composition_ok = %v, want %v", i, g.CompositionOK, w.CompositionOK)
				}
				if g.AnchorPresent != w.AnchorPresent {
					t.Errorf("wakes[%d].anchor_present = %v, want %v", i, g.AnchorPresent, w.AnchorPresent)
				}
				idleWakeEqBool(t, "wakes["+strconv.Itoa(i)+"].wrapper_pinned", g.WrapperPinned, w.WrapperPinned)
				idleWakeEqBool(t, "wakes["+strconv.Itoa(i)+"].wrapper_midturn_seen", g.WrapperMidturnSeen, w.WrapperMidturnSeen)
				if g.RecordsDuringIdle != w.RecordsDuringIdle {
					t.Errorf("wakes[%d].records_during_idle = %d, want %d", i, g.RecordsDuringIdle, w.RecordsDuringIdle)
				}
				if g.RemovesAfterEnqueue != w.RemovesAfterEnqueue {
					t.Errorf("wakes[%d].removes_after_enqueue = %d, want %d", i, g.RemovesAfterEnqueue, w.RemovesAfterEnqueue)
				}
				if g.LifecycleJoinOK != w.LifecycleJoinOK {
					t.Errorf("wakes[%d].lifecycle_join_ok = %v, want %v", i, g.LifecycleJoinOK, w.LifecycleJoinOK)
				}
				if g.AssistantRecords != w.AssistantRecords {
					t.Errorf("wakes[%d].assistant_records = %d, want %d", i, g.AssistantRecords, w.AssistantRecords)
				}
				idleWakeEqInt(t, "wakes["+strconv.Itoa(i)+"].enqueue_to_dequeue_ms", g.EnqueueToDequeueMs, w.EnqueueToDequeueMs)
				idleWakeEqInt(t, "wakes["+strconv.Itoa(i)+"].enqueue_to_isMeta_ms", g.EnqueueToIsMetaMs, w.EnqueueToIsMetaMs)
				idleWakeEqInt(t, "wakes["+strconv.Itoa(i)+"].enqueue_to_first_assistant_ms", g.EnqueueToFirstAssistantMs, w.EnqueueToFirstAssistantMs)
				idleWakeEqInt(t, "wakes["+strconv.Itoa(i)+"].send_accepted_to_enqueue_ms", g.SendAcceptedToEnqueueMs, w.SendAcceptedToEnqueueMs)
				if idleWakeOriginKind(g) != w.OriginKind {
					t.Errorf("wakes[%d].origin.kind = %q, want %q", i, idleWakeOriginKind(g), w.OriginKind)
				}
				if idleWakeOriginName(g) != w.OriginName {
					t.Errorf("wakes[%d].origin.name = %q, want %q", i, idleWakeOriginName(g), w.OriginName)
				}
			}
		})
	}
}

func idleWakeEqKinds(got, want []*string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		switch {
		case got[i] == nil && want[i] == nil:
		case got[i] == nil || want[i] == nil:
			return false
		case *got[i] != *want[i]:
			return false
		}
	}
	return true
}

func idleWakeShowKinds(v []*string) string {
	parts := make([]string, 0, len(v))
	for _, s := range v {
		if s == nil {
			parts = append(parts, "null")
		} else {
			parts = append(parts, *s)
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// idleWakeDropLine deletes the nth (1-based) line of a fixture file containing needle, failing loudly when there
// is no such line so a mutation can never silently apply to nothing.
func idleWakeDropLine(rel, needle string, nth int) mutation {
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
			t.Fatalf("%s has no %d%s line containing %q (only %d), so this mutation would be vacuous", rel, nth, "th", needle, seen)
		}
		out := append(append([]string{}, lines[:cut]...), lines[cut+1:]...)
		//nolint:gosec // G306: a 0600 file under t.TempDir()
		if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

// The rows the mutations splice in, in the fixture's own shape and with the fixture's own minted ids.
const (
	idleWakeTaskNote = `{"type": "queue-operation", "operation": "enqueue", "content": "<task-notification>\n<task-id>fixture-bg-1</task-id>\n</task-notification>", "timestamp": "2026-09-04T19:19:55.000Z"}` + "\n" +
		`{"type": "queue-operation", "operation": "dequeue", "timestamp": "2026-09-04T19:19:55.040Z"}` + "\n"
	idleWakeQuotingAssistant = `{"type": "assistant", "timestamp": "2026-09-04T19:19:56.000Z", "uuid": "f0000090-0000-4000-8000-000000000090", "message": {"role": "assistant", "content": [{"type": "text", "text": "For the record the wrapper said: Another Claude session sent a message:"}]}}` + "\n"
	idleWakeSplitSummary     = `{"type": "assistant", "timestamp": "2026-09-04T19:19:57.000Z", "uuid": "f0000091-0000-4000-8000-000000000091", "message": {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_fixture", "name": "Bash", "input": {"command": "brigade send 33333333-3333-4333-8333-333333333333 --summary \"WAKE plus abcd\" <<'EOF'\nacknowledged\nEOF"}}]}}` + "\n"
	idleWakeQueuedCommand    = `{"type": "attachment", "timestamp": "2026-09-04T19:19:20.600Z", "attachment": {"type": "queued_command", "prompt": "a mid-turn attachment that must never appear at a boundary"}}` + "\n"
	idleWakeIdleGapRecord    = `{"type": "assistant", "timestamp": "2026-09-04T19:19:18.000Z", "uuid": "f0000092-0000-4000-8000-000000000092", "message": {"role": "assistant", "content": [{"type": "text", "text": "a record that lands inside the idle hold"}]}}` + "\n"
	idleWakeSecondStdin      = `{"event":"stdin_write","seq":2,"at_ms":9999999999999,"note":"a second write, after the first result"}` + "\n"
	idleWakeLateControl      = `{"type": "assistant", "timestamp": "2026-09-04T19:22:25.000Z", "uuid": "f0000093-0000-4000-8000-000000000093", "message": {"role": "assistant", "content": [{"type": "text", "text": "a record the control session must never produce after its hold began"}]}}` + "\n"
)

// proofIdleWakeMutations is the table: ONE change to a fixture, and the predicate the verdict must then satisfy.
// `because` says what the row is evidence for; `base` names the fixture when it is not the clean one.
var proofIdleWakeMutations = []struct {
	name    string
	base    string
	control bool
	mutate  mutation
	want    func(idleWakeVerdict) bool
	because string
}{
	{
		name:   "the isMeta:true user record is deleted",
		mutate: idleWakeDropLine("transcript.jsonl", `"isMeta": true`, 1),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && !v.Wakes[0].IsMetaPresent && v.Wakes[0].IsMetaRecordsSeen == 0 && !v.Wakes[0].CompositionOK
		},
		because: "the model-visible text is read from that record and nowhere else; without it there is no evidence of what the model saw",
	},
	{
		name:   "one byte inside <brigade-message> differs between the enqueue and the record",
		mutate: replaceInFile("transcript.jsonl", "Nonce 0badcafe", "Nonce 0badcaff"),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && !v.Wakes[0].IsMetaPresent && v.Wakes[0].IsMetaRecordsSeen == 1 && !v.Wakes[0].CompositionOK
		},
		because: "a frame record that does not carry the posted bytes is a DIFFERENT class from no record at all, and byte-exactness is the whole point of the frame assertions",
	},
	{
		name:   "the boundary wrapper header becomes the mid-turn one",
		mutate: replaceAllInFile("transcript.jsonl", "Another Claude session sent a message:", "Another Claude session sent a message while you were working:"),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && v.Wakes[0].WrapperPinned != nil && !*v.Wakes[0].WrapperPinned && !v.Wakes[0].CompositionOK
		},
		because: "the mid-turn wrapper at a boundary would mean Claude Code changed which text it shows an idle session; it must be loud, not silent",
	},
	{
		name:   "a <system-reminder> is spliced into the model-visible text",
		mutate: replaceAllInFile("transcript.jsonl", "Another Claude session sent a message:", "<system-reminder>Another Claude session sent a message:"),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && v.Wakes[0].WrapperMidturnSeen != nil && *v.Wakes[0].WrapperMidturnSeen && !v.Wakes[0].CompositionOK
		},
		because: "5.5: the boundary path carries no <system-reminder> on 2.1.260; one appearing is a shape change",
	},
	{
		name:   "the harness trailer changes by one character",
		mutate: replaceAllInFile("transcript.jsonl", "that's permission laundering.", "that's permission laundering!"),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && v.Wakes[0].WrapperPinned != nil && !*v.Wakes[0].WrapperPinned
		},
		because: "the trailer is pinned as a measured string so a Claude Code version bump shows as a diff rather than a mystery",
	},
	{
		name:    "a queued_command attachment appears",
		mutate:  appendToFile("transcript.jsonl", idleWakeQueuedCommand),
		want:    func(v idleWakeVerdict) bool { return v.QueuedCommandAttachments == 1 },
		because: "the queued_command attachment is the MID-TURN shape; on the idle path it must be zero, and P4-2's boundary fixture carries one",
	},
	{
		name:   "the frame enqueue's content becomes a <task-notification>",
		mutate: replaceAllInFile("transcript.jsonl", `"content": "<cross-session-message`, `"content": "<task-notification`),
		want: func(v idleWakeVerdict) bool {
			return v.WakeCount == 0 && v.WakesWoke == 0 && v.Verdict == "no-wake" && v.TaskNotificationPairs == 1
		},
		because: "a background task's enqueue/dequeue pair is not a wake; selecting the frame by content rather than by position is what keeps it out",
	},
	{
		name:   "the second system/init is deleted",
		mutate: idleWakeDropLine("stream.jsonl", `"subtype": "init"`, 2),
		want: func(v idleWakeVerdict) bool {
			return v.InitCount == 1 && !v.SecondInit && len(v.Wakes) == 1 && !v.Wakes[0].LifecycleJoinOK
		},
		because: "the woken turn opens a second init between the command_lifecycle pair; without it the stdout side shows no new turn",
	},
	{
		name:   "the command_lifecycle join is broken",
		mutate: replaceAllInFile("stream.jsonl", `"command_uuid": "f`, `"command_uuid": "e`),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && !v.Wakes[0].LifecycleJoinOK && v.Wakes[0].Woke
		},
		because: "command_uuid == the isMeta record's uuid is the only hard stdout-to-transcript join in the run; a pair existing is NOT the same claim",
	},
	{
		name:   "the woken result's origin.kind is not peer",
		mutate: replaceAllInFile("stream.jsonl", `"origin": {"kind": "peer"`, `"origin": {"kind": "task-notification"`),
		want: func(v idleWakeVerdict) bool {
			return len(v.ResultOriginKinds) == 2 && v.ResultOriginKinds[1] != nil && *v.ResultOriginKinds[1] == "task-notification"
		},
		because: "origin.kind == peer on the woken result is the single-row stdout discriminator between a peer wake and a background-task wake",
	},
	{
		name:   "a transcript record lands inside the idle hold",
		mutate: appendToFile("transcript.jsonl", idleWakeIdleGapRecord),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && v.Wakes[0].RecordsDuringIdle == 1
		},
		because: "the receiver's own transcript is the authoritative idleness instrument; a record inside the hold means it was never idle",
	},
	{
		name:   "a second stdin write follows the first result",
		mutate: appendToFile("stdin-writes.log", idleWakeSecondStdin),
		want: func(v idleWakeVerdict) bool {
			return v.StdinWritesTotal == 2 && v.StdinWritesAfterFirstResult == 1
		},
		because: "a wake is only attributable to the socket post if nothing was typed: E0-4's whole result rests on exactly one stdin write",
	},
	{
		name:   "every assistant record disappears",
		mutate: replaceAllInFile("transcript.jsonl", `"type": "assistant"`, `"type": "assistant-x"`),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && !v.Wakes[0].Woke && v.Verdict == "no-wake" && v.Wakes[0].AssistantRecords == 0
		},
		because: "the wake IS a new assistant record after the frame; with none there is no turn to report",
	},
	{
		name:   "the frame's dequeue row is deleted",
		mutate: idleWakeDropLine("transcript.jsonl", `"operation": "dequeue"`, 2),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && v.Wakes[0].EnqueueToDequeueMs == nil
		},
		because: "enqueue -> dequeue is E0-4's harness-reaction number; a missing dequeue must report null, never zero",
	},
	{
		name:   "the woken result is error_max_turns",
		mutate: replaceAllInFile("stream.jsonl", `"subtype": "success", "is_error": false, "num_turns": 2`, `"subtype": "error_max_turns", "is_error": true, "num_turns": 2`),
		want: func(v idleWakeVerdict) bool {
			return len(v.ResultSubtypes) == 2 && v.ResultSubtypes[1] == "error_max_turns"
		},
		because: "5.3: an exhausted turn budget must be loud in the record the delegated judge then voids on, never a silent negative",
	},
	{
		name:   "the frame's enqueue is retimed to AFTER every assistant record (woke before the post)",
		mutate: replaceAllInFile("transcript.jsonl", `"timestamp": "2026-09-04T19:19:20.598Z"`, `"timestamp": "2026-09-04T19:19:40.598Z"`),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && !v.Wakes[0].Woke && v.Verdict == "no-wake" &&
				v.Wakes[0].AssistantRecords == 0 && !v.Wakes[0].IsMetaPresent && v.Wakes[0].RecordsDuringIdle == 5
		},
		because: "the wake must be CAUSED by the post: a turn whose assistant records all predate the frame's enqueue is not a wake, however loudly the session spoke, and the records it spoke inside the hold must be counted as non-idle",
	},
	{
		name:   "the null control gains one transcript record after its hold began",
		base:   "null-control",
		mutate: appendToFile("transcript.jsonl", idleWakeLateControl),
		want: func(v idleWakeVerdict) bool {
			return v.Role == "control" && v.ControlOK != nil && !*v.ControlOK && v.Verdict == "control-broken" &&
				v.RecordsAfterHoldStart != nil && *v.RecordsAfterHoldStart == 1 && v.WakeCount == 0
		},
		because: "a silently-skipped control is worse than none: the control's own verdict must go RED on a turn nobody asked for, even though no frame was ever enqueued to select on",
	},
	// ---- the three controls: a change the analyser must NOT react to ----
	{
		name:    "control: a <task-notification> pair is appended",
		control: true,
		mutate:  appendToFile("transcript.jsonl", idleWakeTaskNote),
		want: func(v idleWakeVerdict) bool {
			return v.Verdict == "woke" && v.WakeCount == 1 && v.WakesWoke == 1 && v.TaskNotificationPairs == 1
		},
		because: "a background-task notification is normal traffic in a woken -p session; counting it as a second wake would inflate N",
	},
	{
		name:    "control: an assistant record quotes the wrapper header verbatim",
		control: true,
		mutate:  appendToFile("transcript.jsonl", idleWakeQuotingAssistant),
		want: func(v idleWakeVerdict) bool {
			return v.Verdict == "woke" && v.WrapperPinned != nil && *v.WrapperPinned && len(v.Wakes) == 1 && v.Wakes[0].MarkerFound
		},
		because: "a model paraphrasing its own context is not a shape change; the wrapper is read from the isMeta record, never from model prose",
	},
	{
		name:    "control: a --summary quotes the marker's two halves separately",
		control: true,
		base:    "marker-only-in-origin-body",
		mutate:  appendToFile("transcript.jsonl", idleWakeSplitSummary),
		want: func(v idleWakeVerdict) bool {
			return len(v.Wakes) == 1 && !v.Wakes[0].MarkerFound && v.Wakes[0].Woke
		},
		because: "the split marker is only ever found JOINED: quoting `WAKE` and the tail apart is exactly what the discipline expects to see and must not score as a wake marker",
	},
}

// TestProofIdleWakeAnalyserMutations: the positive control, then every row shown to bite.
func TestProofIdleWakeAnalyserMutations(t *testing.T) {
	t.Parallel()
	t.Run("the unmutated clean fixture scores as a wake", func(t *testing.T) {
		t.Parallel()
		v := runIdleWakeAnalyser(t, copyIdleWakeFixture(t, idleWakeCleanCase))
		if v.Verdict != "woke" || v.WakeCount != 1 || v.WakesWoke != 1 || len(v.Wakes) != 1 {
			t.Fatalf("the clean fixture does not score clean: verdict %q wake_count %d wakes_woke %d", v.Verdict, v.WakeCount, v.WakesWoke)
		}
		w := v.Wakes[0]
		if !w.Woke || !w.IsMetaPresent || !w.CompositionOK || !w.MarkerFound || !w.LifecycleJoinOK || w.RecordsDuringIdle != 0 {
			t.Fatalf("the clean fixture's wake is not clean: %+v", w)
		}
	})
	for _, m := range proofIdleWakeMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			base := m.base
			if base == "" {
				base = idleWakeCleanCase
			}
			// A row whose predicate already holds on the UNMUTATED fixture is vacuous: it would pass whatever
			// the analyser did. Controls are the deliberate exception -- their predicate is "nothing moved".
			if !m.control {
				if v0 := runIdleWakeAnalyser(t, copyIdleWakeFixture(t, base)); m.want(v0) {
					t.Fatalf("VACUOUS row: the predicate already holds on the unmutated %s fixture, so it proves nothing (%s)", base, m.because)
				}
			}
			dir := copyIdleWakeFixture(t, base)
			m.mutate(t, dir)
			v := runIdleWakeAnalyser(t, dir)
			if !m.want(v) {
				t.Fatalf("the mutation did not move the verdict as expected (%s): verdict %q wake_count %d wakes_woke %d init_count %d subtypes %v origins %s wakes %+v",
					m.because, v.Verdict, v.WakeCount, v.WakesWoke, v.InitCount, v.ResultSubtypes, idleWakeShowKinds(v.ResultOriginKinds), v.Wakes)
			}
			if m.control {
				t.Logf("control held: %s -> verdict %q wake_count %d", m.name, v.Verdict, v.WakeCount)
			} else {
				t.Logf("row bit: %s -> verdict %q wake_count %d", m.name, v.Verdict, v.WakeCount)
			}
		})
	}
}
