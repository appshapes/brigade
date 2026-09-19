package commands

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/protocol"
)

// heartbeatResultJSON is a canned `session heartbeat` result for the
// fixture's session.
func heartbeatResultJSON() string {
	b, err := json.Marshal(map[string]any{
		"session_id": selfSessionID, "state": "active", "lease_until": fixtureNow.Add(90 * time.Second), "server_time": fixtureNow,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// withMode rewrites the fixture's map with the given doing mode.
func (f *fixture) withMode(t *testing.T, mode string) {
	t.Helper()
	m := f.byPID()
	m.DoingMode = mode
	f.writeMap(t, m)
}

// heartbeatDoc decodes the one heartbeat the recorder saw as a raw member
// map, so the test can count members as well as read them.
func heartbeatDoc(t *testing.T, f *fixture) map[string]any {
	t.Helper()
	if got := f.rec.verbs(); !slices.Equal(got, []string{"describe", "session heartbeat"}) {
		t.Fatalf("spawned %v, want describe then one heartbeat", got)
	}
	spec := f.rec.spec(t, 1)
	if i := slices.Index(spec.Argv, "--session"); i < 0 || i+1 >= len(spec.Argv) || spec.Argv[i+1] != selfSessionID {
		t.Fatalf("argv %v does not carry --session %s", spec.Argv, selfSessionID)
	}
	var doc map[string]any
	if err := json.Unmarshal(spec.Stdin, &doc); err != nil {
		t.Fatalf("heartbeat stdin %q: %v", spec.Stdin, err)
	}
	return doc
}

// TestDoingRefusesInputBeforeAnySpawn is U-05 for the verb: every input
// refusal — empty, over the byte limit, over the code-point cap, not
// UTF-8, a credential shape, a local path, positional arguments, a
// terminal on stdin — is answered before a child is spawned, not even the
// describe; the happy path at the end is the positive control.
func TestDoingRefusesInputBeforeAnySpawn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		stdin  string
		args   []string
		tty    bool
		code   protocol.Code
		reason string
	}{
		{"empty", "", nil, false, protocol.CodeInvalidInput, doing.ReasonEmpty},
		{"whitespace only", " \n\t\n", nil, false, protocol.CodeInvalidInput, doing.ReasonEmpty},
		{"one byte over the read limit", strings.Repeat("x", doingReadLimit+1), nil, false, protocol.CodeInvalidInput, doing.ReasonTooLong},
		{"one code point over the cap", strings.Repeat("é", doing.MaxChars+1), nil, false, protocol.CodeInvalidInput, doing.ReasonTooLong},
		{"an indented 300-code-point paragraph", strings.Repeat("    "+strings.Repeat("x", 56)+"\n", 5), nil, false, protocol.CodeInvalidInput, doing.ReasonTooLong},
		{"not utf-8", "ok\xff\xfe", nil, false, protocol.CodeInvalidInput, doing.ReasonNotUTF8},
		{"a join secret", "rotating brg1.team-7.deadbeefdeadbeefdeadbeefdeadbeef", nil, false, protocol.CodeInvalidInput, doing.ReasonSecretShaped},
		{"the messaging token", "posting with " + msgTok, nil, false, protocol.CodeInvalidInput, doing.ReasonSecretShaped},
		{"a local path", "editing /work/repo/main.go", nil, false, protocol.CodeInvalidInput, doing.ReasonLocalPath},
		{"the home directory", "in " + "HOMEDIR" + " now", nil, false, protocol.CodeInvalidInput, doing.ReasonLocalPath},
		{"a positional argument", "fine", []string{"fixing the roster"}, false, protocol.CodeUsage, ""},
		{"a terminal on stdin", "typed", nil, true, protocol.CodeUsage, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			stdin := strings.ReplaceAll(tc.stdin, "HOMEDIR", f.dirs.Home)
			inv := f.inv(f.sessionEnv("CLAUDE_CODE_MESSAGING_TOKEN="+msgTok), stdin, tc.args...)
			if tc.tty {
				inv.Deps.IsTerminal = func(io.Reader) bool { return true }
			}
			err := Doing(inv, DoingOptions{})
			perr := wantCodeErr(t, err, tc.code, tc.reason)
			if tc.code == protocol.CodeInvalidInput {
				if perr.Code.Exit() != 3 || perr.Details["field"] != "session_description" {
					t.Errorf("exit %d details %v", perr.Code.Exit(), perr.Details)
				}
				if tc.reason == doing.ReasonTooLong && (perr.Details["limit"] != "160" || perr.Details["unit"] != "codepoints") {
					t.Errorf("too_long details %v", perr.Details)
				}
				if strings.Contains(perr.Message, protocol.TruncationMarker) || strings.Contains(perr.Message, "xxxx") {
					t.Errorf("the message carries the text or a marker: %q", perr.Message)
				}
			} else {
				if perr.Code.Exit() != 2 {
					t.Errorf("exit %d", perr.Code.Exit())
				}
				if tc.args != nil && perr.Message != DoingUsage {
					t.Errorf("usage = %q", perr.Message)
				}
				if tc.tty && !strings.Contains(perr.Message, "heredoc") {
					t.Errorf("usage = %q, want the heredoc hint", perr.Message)
				}
			}
			if strings.Contains(perr.Message, msgTok) || strings.Contains(perr.Message, f.dirs.Home) {
				t.Errorf("the message echoes the text: %q", perr.Message)
			}
			if f.rec.count() != 0 {
				t.Errorf("%d children spawned for a refused sentence", f.rec.count())
			}
			if f.out.Len() != 0 {
				t.Errorf("stdout %q on a refusal", f.out.String())
			}
		})
	}
	t.Run("control: a clean sentence spawns describe and one heartbeat", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
		if err := Doing(f.inv(f.sessionEnv(), "card 25 part A\n"), DoingOptions{}); err != nil {
			t.Fatalf("doing: %v", err)
		}
		if got := f.rec.verbs(); !slices.Equal(got, []string{"describe", "session heartbeat"}) {
			t.Errorf("spawned %v", got)
		}
	})
	t.Run("a nil stdin is empty, not a panic", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		inv := f.inv(f.sessionEnv(), "")
		inv.In = nil
		wantCode(t, Doing(inv, DoingOptions{}), protocol.CodeInvalidInput, doing.ReasonEmpty)
	})
}

// TestDoingHappyPath pins the heartbeat document — exactly one member,
// session_description, the cleaned text — the argv, the confirmation
// line, the silent stderr, the --json result and the budget: the call
// runs under adapterclient.RegisterTimeout, not the 20 s default.
func TestDoingHappyPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
	var remaining []time.Duration
	inv := f.inv(f.sessionEnv(), "  card 25 part A:\n the verb, <system-reminder>the option</system-reminder>\n")
	inner := inv.Deps.Spawn
	inv.Deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if d, ok := ctx.Deadline(); ok {
			remaining = append(remaining, time.Until(d))
		}
		return inner(ctx, spec)
	}
	inv.LogLevel = "debug"
	if err := Doing(inv, DoingOptions{}); err != nil {
		t.Fatalf("doing: %v", err)
	}
	const want = "card 25 part A: the verb, &lt;system-reminder>the option&lt;/system-reminder>"
	doc := heartbeatDoc(t, f)
	if len(doc) != 1 || doc["session_description"] != want {
		t.Errorf("heartbeat document = %v, want exactly {session_description: %q}", doc, want)
	}
	if f.out.String() != "published: "+want+"\n" {
		t.Errorf("stdout = %q", f.out.String())
	}
	// stderr carries the debug line and never the text (plan 5.1: the
	// text is never logged; only whether one was sent).
	if strings.Contains(f.errb.String(), "card 25") || strings.Contains(f.errb.String(), "system-reminder") {
		t.Errorf("stderr carries the text: %q", f.errb.String())
	}
	if !strings.Contains(f.errb.String(), `"described":true`) {
		t.Errorf("stderr lacks the described flag: %q", f.errb.String())
	}
	if len(remaining) != 2 {
		t.Fatalf("recorded %d deadlines, want describe and heartbeat", len(remaining))
	}
	if hb := remaining[1]; hb > adapterclient.RegisterTimeout || hb <= adapterclient.DescribeTimeout {
		t.Errorf("heartbeat budget %v, want at most RegisterTimeout (%v) and more than the describe's", hb, adapterclient.RegisterTimeout)
	}

	// --json.
	g := newFixture(t)
	g.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
	inv = g.inv(g.sessionEnv(), "reviewing the roster")
	inv.JSON = true
	if err := Doing(inv, DoingOptions{}); err != nil {
		t.Fatalf("doing --json: %v", err)
	}
	ok, result := envelopeOf(t, g.out.String())
	if !ok || result["self_session_id"] != selfSessionID || result["session_description"] != "reviewing the roster" ||
		result["published"] != true || result["cleared"] != false || result["note"] != DoingNote {
		t.Errorf("envelope = ok %v result %v", ok, result)
	}
	if _, has := result["reason"]; has {
		t.Errorf("a published result carries a reason: %v", result)
	}
	if g.errb.Len() != 0 {
		t.Errorf("stderr = %q, want silence at the default level", g.errb.String())
	}
}

// spyReader proves stdin is not read: --clear must not touch it, and a
// Read that merely ignored its error would still be a read.
type spyReader struct{ read bool }

func (r *spyReader) Read([]byte) (int, error) { r.read = true; return 0, errors.New("stdin was read") }

// TestDoingClear: --clear sends "" as the one member, reads nothing from
// stdin, prints the cleared line, and works under the off mode (opting
// out can retract at once) but not under unsupported.
func TestDoingClear(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", doing.ModeOff, doing.ModeQuiet, doing.ModeUnasked, doing.ModeAllowed} {
		t.Run("mode "+mode, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.withMode(t, mode)
			f.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
			inv := f.inv(f.sessionEnv(), "")
			spy := &spyReader{}
			inv.In = spy
			if err := Doing(inv, DoingOptions{Clear: true}); err != nil {
				t.Fatalf("doing --clear: %v", err)
			}
			if spy.read {
				t.Error("--clear read stdin")
			}
			doc := heartbeatDoc(t, f)
			if len(doc) != 1 || doc["session_description"] != "" {
				t.Errorf("heartbeat document = %v, want exactly {session_description: \"\"}", doc)
			}
			if f.out.String() != DoingClearedLine+"\n" {
				t.Errorf("stdout = %q", f.out.String())
			}
		})
	}
	t.Run("--json", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
		inv := f.inv(f.sessionEnv(), "")
		inv.In = &spyReader{}
		inv.JSON = true
		if err := Doing(inv, DoingOptions{Clear: true}); err != nil {
			t.Fatalf("doing --clear --json: %v", err)
		}
		ok, result := envelopeOf(t, f.out.String())
		if !ok || result["session_description"] != "" || result["published"] != false || result["cleared"] != true || result["note"] != DoingClearedNote {
			t.Errorf("envelope = ok %v result %v", ok, result)
		}
	})
	t.Run("unsupported refuses a clear too", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.withMode(t, doing.ModeUnsupported)
		inv := f.inv(f.sessionEnv(), "")
		inv.In = &spyReader{}
		if err := Doing(inv, DoingOptions{Clear: true}); err != nil {
			t.Fatalf("doing --clear: %v", err)
		}
		if got := f.rec.verbs(); !slices.Equal(got, []string{"describe"}) {
			t.Errorf("spawned %v, want no heartbeat", got)
		}
		if f.out.String() != DoingNotPublishedLine+"\n" {
			t.Errorf("stdout = %q", f.out.String())
		}
	})
}

// TestDoingModeGate is the five modes as the verb reads them from the map
// (plan 5.2): absent, quiet, unasked and allowed publish; off and
// unsupported exit 0 with the not-published line — no heartbeat, no error,
// no option named — and say why only under --json.
func TestDoingModeGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode    string
		publish bool
		reason  string
	}{
		{"", true, ""},
		{doing.ModeQuiet, true, ""},
		{doing.ModeUnasked, true, ""},
		{doing.ModeAllowed, true, ""},
		{doing.ModeOff, false, DoingReasonNotShared},
		{doing.ModeUnsupported, false, DoingReasonUnsupported},
	} {
		t.Run("mode "+tc.mode, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.withMode(t, tc.mode)
			f.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
			if err := Doing(f.inv(f.sessionEnv(), "fixing the roster"), DoingOptions{}); err != nil {
				t.Fatalf("doing: %v", err)
			}
			if tc.publish {
				if doc := heartbeatDoc(t, f); doc["session_description"] != "fixing the roster" {
					t.Errorf("document %v", doc)
				}
				if f.out.String() != "published: fixing the roster\n" {
					t.Errorf("stdout = %q", f.out.String())
				}
				return
			}
			if got := f.rec.verbs(); !slices.Equal(got, []string{"describe"}) {
				t.Errorf("spawned %v, want no heartbeat", got)
			}
			if f.out.String() != DoingNotPublishedLine+"\n" {
				t.Errorf("stdout = %q", f.out.String())
			}
			for _, word := range []string{"share_doing", "option", "setting"} {
				if strings.Contains(f.out.String(), word) {
					t.Errorf("the not-published line names %q", word)
				}
			}
			f.out.Reset()
			inv := f.inv(f.sessionEnv(), "fixing the roster")
			inv.JSON = true
			if err := Doing(inv, DoingOptions{}); err != nil {
				t.Fatalf("doing --json: %v", err)
			}
			ok, result := envelopeOf(t, f.out.String())
			if !ok || result["published"] != false || result["cleared"] != false || result["reason"] != tc.reason || result["self_session_id"] != selfSessionID {
				t.Errorf("envelope = ok %v result %v", ok, result)
			}
		})
	}
}

// TestDoingOutsideSessionIsConfig: a terminal has no session to describe,
// and an unregistered pid is not_registered; both before any spawn.
func TestDoingOutsideSessionIsConfig(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	err := Doing(f.inv(f.terminalEnv(), "fixing the roster"), DoingOptions{})
	perr := wantCodeErr(t, err, protocol.CodeConfig, "not_in_session")
	if perr.Code.Exit() != 11 {
		t.Errorf("exit %d, want 11", perr.Code.Exit())
	}
	env := f.sessionEnv()
	env[len(env)-1] = "CLAUDE_PID=99999"
	wantCode(t, Doing(f.inv(env, "fixing the roster"), DoingOptions{}), protocol.CodeConfig, "not_registered")
	if f.rec.count() != 0 {
		t.Errorf("%d children spawned", f.rec.count())
	}
}

// TestDoingAdapterFailuresPassThroughWithTheSuffix: an adapter failure
// keeps its code and gains the fixed suffix — the retry form for
// `unavailable` (the harness's own timeout included), the housekeeping
// form for everything else — and nothing is retried: describe and ONE
// heartbeat, whatever the answer.
func TestDoingAdapterFailuresPassThroughWithTheSuffix(t *testing.T) {
	t.Parallel()
	timeout := &protocol.Error{Code: protocol.CodeUnavailable, Message: "adapter did not finish within its deadline", Details: map[string]string{"reason": "timeout"}}
	for _, tc := range []struct {
		name   string
		answer answer
		code   protocol.Code
		suffix string
	}{
		{"unavailable", failAnswer(protocol.CodeUnavailable, "backend down"), protocol.CodeUnavailable, DoingRetrySuffix},
		{"the harness timeout", answer{spawnErr: timeout}, protocol.CodeUnavailable, DoingRetrySuffix},
		{"not_found (the session is gone)", failAnswer(protocol.CodeNotFound, "no such session"), protocol.CodeNotFound, DoingFailedSuffix},
		{"conflict", failAnswer(protocol.CodeConflict, "closed"), protocol.CodeConflict, DoingFailedSuffix},
		{"internal", failAnswer(protocol.CodeInternal, "boom"), protocol.CodeInternal, DoingFailedSuffix},
		{"invalid_input from the adapter", failAnswer(protocol.CodeInvalidInput, "refused"), protocol.CodeInvalidInput, DoingFailedSuffix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.rec.on("session heartbeat", tc.answer, okAnswer(heartbeatResultJSON()))
			err := Doing(f.inv(f.sessionEnv(), "fixing the roster"), DoingOptions{})
			perr := wantCodeErr(t, err, tc.code, "")
			if !strings.HasSuffix(perr.Message, tc.suffix) {
				t.Errorf("message = %q, want the suffix %q", perr.Message, tc.suffix)
			}
			if n := f.rec.count(); n != 2 {
				t.Errorf("spawned %d, want 2 (describe + one heartbeat, never a retry)", n)
			}
			if len(f.sleeps) != 0 {
				t.Errorf("slept %v before a retry that must not happen", f.sleeps)
			}
			if f.out.Len() != 0 {
				t.Errorf("stdout %q on a failure", f.out.String())
			}
		})
	}
}

// TestDoingIgnoresHostileEnvironment is U-27's unit half for the verb:
// hostile inherited BRIGADE_* values change nothing, the child carries the
// map's values, and the token reaches no child.
func TestDoingIgnoresHostileEnvironment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
	hostile := []string{
		"BRIGADE_PROFILE=evil",
		"BRIGADE_CONFIG_DIR=" + filepath.Join(t.TempDir(), "evil-config"),
		"BRIGADE_STATE_DIR=" + filepath.Join(t.TempDir(), "evil-state"),
		"BRIGADE_TEAM_INBOUND=refuse",
		"BRIGADE_ADAPTER_COMMAND=/evil/adapter",
		"CLAUDE_CODE_MESSAGING_SOCKET=/tmp/cc-socks/x.sock",
		"CLAUDE_CODE_MESSAGING_TOKEN=" + msgTok,
	}
	if err := Doing(f.inv(f.sessionEnv(hostile...), "fixing the roster"), DoingOptions{}); err != nil {
		t.Fatalf("doing under a hostile environment: %v", err)
	}
	assertChildIsolation(t, f.rec, fixtureProfile)
	if spec := f.rec.spec(t, 1); spec.Argv[0] != f.adapterPath {
		t.Errorf("the heartbeat ran %s, want the map's adapter", spec.Argv[0])
	}
}

// TestDoingWritesNothingLocally is ruling 12 and U-28's unit half for the
// verb: no stamp, no copy of the text, no state directory — nothing is
// written anywhere under the fixture root beyond the adapter log every
// command's child gets. The writable arm is the one a best-effort write
// cannot hide from; the read-only arm (HOME, the config dir and the state
// dir at 0500, the Bash sandbox's shape) proves the publish still
// succeeds there.
func TestDoingWritesNothingLocally(t *testing.T) {
	t.Parallel()
	t.Run("writable", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
		before := snapshotTree(t, f.dirs.Root, filepath.Join(f.stateDir, "logs"))
		if err := Doing(f.inv(f.sessionEnv(), "fixing the roster"), DoingOptions{}); err != nil {
			t.Fatalf("doing: %v", err)
		}
		if after := snapshotTree(t, f.dirs.Root, filepath.Join(f.stateDir, "logs")); !slices.Equal(before, after) {
			t.Errorf("the verb wrote under the fixture root:\nbefore %v\nafter  %v", before, after)
		}
	})
	t.Run("read-only", func(t *testing.T) {
		t.Parallel()
		if os.Getuid() == 0 {
			t.Skip("root ignores directory modes")
		}
		f := newFixture(t)
		f.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
		before := snapshotTree(t, f.dirs.Root, "")
		for _, dir := range []string{f.dirs.Home, f.dirs.BrigadeConfig, f.stateDir} {
			//nolint:gosec // G302: read-only directories are the PRECONDITION of U-28
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			//nolint:gosec // G302: restoring the 0700 the fixture created
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		}
		if err := Doing(f.inv(f.sessionEnv(), "fixing the roster"), DoingOptions{}); err != nil {
			t.Fatalf("doing with a read-only HOME: %v", err)
		}
		if f.out.String() != "published: fixing the roster\n" || f.errb.Len() != 0 {
			t.Errorf("stdout %q stderr %q", f.out.String(), f.errb.String())
		}
		if after := snapshotTree(t, f.dirs.Root, ""); !slices.Equal(before, after) {
			t.Errorf("the verb wrote under the fixture root:\nbefore %v\nafter  %v", before, after)
		}
	})
}

// snapshotTree lists every path under root, sorted, leaving out the
// subtree at except (the adapter log directory, which is not the verb's).
func snapshotTree(t *testing.T, root, except string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if except != "" && (p == except || strings.HasPrefix(p, except+string(filepath.Separator))) {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	return paths
}
