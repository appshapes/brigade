package conformance

import (
	"context"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// testEnviron is the suite's own environment in every launcher test: PATH
// and TMPDIR must be copied, the other two must never reach a child.
var testEnviron = []string{"PATH=/usr/bin:/bin", "TMPDIR=/tmp/brigade-test", "CLAUDE_CONFIG_DIR=/never", "BRIGADE_FS_ROOT=/never"}

// TestChildEnvironmentIsExactlyTheSectionThreeSet spawns /usr/bin/env as
// the "adapter" and compares the variable names the child saw with the
// brief's list — nothing missing, nothing extra.
func TestChildEnvironmentIsExactlyTheSectionThreeSet(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, "/usr/bin/env", Options{Env: []string{"FOO=bar", "SUPABASE_KEY=k"}, SharedEnv: "BRIGADE_FS_ROOT"}, testEnviron)
	p := scratchPrincipal(t, r, "env")
	res, _ := r.launcher.spawn(t.Context(), "T", p, []string{"BRIGADE_TEST_OFFLINE=1"}, nil, false)
	if res.err != nil {
		t.Fatal(res.err)
	}
	got := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(res.Stdout)), "\n") {
		name, value, _ := strings.Cut(line, "=")
		got[name] = value
	}
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{"BRIGADE_CONFIG_DIR", "BRIGADE_FS_ROOT", "BRIGADE_LOG_LEVEL", "BRIGADE_PROFILE", "BRIGADE_STATE_DIR", "BRIGADE_TEST_OFFLINE", "FOO", "HOME", "PATH", "SUPABASE_KEY", "TMPDIR"}
	if !slices.Equal(names, want) {
		t.Fatalf("child environment names:\n got %v\nwant %v", names, want)
	}
	if got["HOME"] != p.Home || got["BRIGADE_CONFIG_DIR"] != p.ConfigDir || got["BRIGADE_STATE_DIR"] != p.StateDir ||
		got["BRIGADE_PROFILE"] != "default" || got["BRIGADE_LOG_LEVEL"] != "debug" || got["PATH"] != "/usr/bin:/bin" ||
		got["TMPDIR"] != "/tmp/brigade-test" || got["BRIGADE_FS_ROOT"] != r.launcher.sharedDir() || got["FOO"] != "bar" {
		t.Fatalf("child environment values: %v", got)
	}
}

func TestArgvPrependsFixedArgs(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, "/x/adapter", Options{FixedArgs: []string{"adapter", "supabase"}}, testEnviron)
	got := r.launcher.argv([]string{"session", "list"})
	if !slices.Equal(got, []string{"/x/adapter", "adapter", "supabase", "session", "list"}) {
		t.Fatalf("argv: %v", got)
	}
}

func TestOneEnvelope(t *testing.T) {
	t.Parallel()
	ok := `{"ok":true,"protocol_version":"1","result":{}}`
	for name, tc := range map[string]struct {
		stdout string
		valid  bool
	}{
		"one envelope":         {ok + "\n", true},
		"no newline":           {ok, false},
		"two newlines":         {ok + "\n\n", false},
		"second document":      {ok + "\n" + ok + "\n", false},
		"trailing text":        {ok + "\ndone\n", false},
		"bare line first":      {"starting\n" + ok + "\n", false},
		"leading space":        {" " + ok + "\n", false},
		"array":                {`[1]` + "\n", false},
		"empty":                {"", false},
		"not json":             {"{oops\n", false},
		"result and error":     {`{"ok":true,"protocol_version":"1","result":{},"error":{"code":"usage","message":"x","retryable":false}}` + "\n", false},
		"failing envelope":     {`{"ok":false,"protocol_version":"1","error":{"code":"usage","message":"x","retryable":false}}` + "\n", true},
		"missing retryable ok": {`{"ok":false,"protocol_version":"1","error":{"code":"usage","message":"x"}}` + "\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env, msg := oneEnvelope([]byte(tc.stdout))
			if (env != nil) != tc.valid || (msg == "") != tc.valid {
				t.Fatalf("oneEnvelope(%q): env %v, msg %q; want valid=%v", tc.stdout, env != nil, msg, tc.valid)
			}
		})
	}
}

// failingScript is a fake adapter that prints body (a shell fragment) for
// every command.
func failingScript(tb testing.TB, name, body string) string {
	tb.Helper()
	return writeScript(tb, name, body)
}

func spawnCase(t *testing.T, adapter string, opts Options, args ...string) CaseResult {
	t.Helper()
	r := newTestRunner(t, adapter, opts, testEnviron)
	return runFake(t, r, func(ct *T) {
		p := ct.Scratch("x")
		ct.Exec(p, nil, args...)
	})
}

func TestGlobalChecksFireOnRealViolations(t *testing.T) {
	t.Parallel()
	ok := `{"ok":true,"protocol_version":"1","result":{}}`
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"two documents": {"printf '%s\\n%s\\n' '" + ok + "' '" + ok + "'", "after the envelope"},
		"bare line":     {"printf 'hello\\n%s\\n' '" + ok + "'", "does not start with a JSON object"},
		"exit 127":      {"printf '%s\\n' '" + ok + "'; exit 127", "exit status 127 is outside 0..12 (B-11)"},
		"exit 13":       {"printf '%s\\n' '" + ok + "'; exit 13", "exit status 13 is outside 0..12 (B-11)"},
		"empty stdout":  {"exit 0", "stdout is empty"},
		"no newline":    {"printf '%s' '" + ok + "'", "lacks the newline"},
		"secret on stdout": {
			`printf '%s\n' '{"ok":false,"protocol_version":"1","error":{"code":"usage","message":"leak brg1.x.y","retryable":false}}'; exit 2`,
			"C-05: stdout of session list contains the join-secret prefix brg1.",
		},
		"secret on stderr": {"printf '%s\\n' '" + ok + "'; echo 'debug token brg1.t.s' >&2", "C-05: stderr of session list contains the join-secret prefix brg1."},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := spawnCase(t, failingScript(t, "adapter", tc.body), Options{}, "session", "list")
			if res.Status != StatusFail || !strings.Contains(res.Reason, tc.want) {
				t.Fatalf("status %s, reason %q; want fail with %q", res.Status, res.Reason, tc.want)
			}
		})
	}
}

func TestSecretShapedScan(t *testing.T) {
	t.Parallel()
	for text, hit := range map[string]bool{
		"leak brg1.x.y": true,
		`"join_secret":"brg1.0123abcd.89ef0123456789abcdef"`:          true,
		"brg1.a1b2c3d4-e5f6-7890-abcd-ef1234567890.secretsecret":      true,
		"brg1.team.with.dots.secret":                                  true,
		"join_secret is malformed; expected brg1.<team_ref>.<secret>": false,
		"the prefix brg1. alone":                                      false,
		"brg1.onlyoneword":                                            false,
		"brg1.trailing.":                                              false,
	} {
		if got := secretShaped.MatchString(text); got != hit {
			t.Errorf("%q: match %v, want %v", text, got, hit)
		}
	}
}

func TestGlobalChecksPassOnAConformingSpawn(t *testing.T) {
	t.Parallel()
	ok := `{"ok":true,"protocol_version":"1","result":{}}`
	res := spawnCase(t, failingScript(t, "adapter", "printf '%s\\n' '"+ok+"'; echo 'debug line' >&2"), Options{}, "session", "list")
	if res.Status != StatusPass {
		t.Fatalf("status %s, reason %q; want pass", res.Status, res.Reason)
	}
}

func TestKnownSecretScanFiresAndNotOtherwise(t *testing.T) {
	t.Parallel()
	ok := `{"ok":true,"protocol_version":"1","result":{}}`
	const secret = "s3cr3t-value-without-prefix"
	adapter := failingScript(t, "adapter", "printf '%s\\n' '"+ok+"'; echo 'note "+secret+"' >&2")
	for _, known := range []bool{false, true} {
		r := newTestRunner(t, adapter, Options{}, testEnviron)
		if known {
			r.launcher.addSecret(secret)
		}
		res := runFake(t, r, func(ct *T) { ct.Exec(ct.Scratch("x"), nil, "describe") })
		if known && (res.Status != StatusFail || !strings.Contains(res.Reason, "C-05: stderr of describe contains a join secret")) {
			t.Fatalf("known secret: status %s, reason %q", res.Status, res.Reason)
		}
		if !known && res.Status != StatusPass {
			t.Fatalf("unknown string: status %s, reason %q; want pass", res.Status, res.Reason)
		}
	}
}

func TestTeamCreateStdoutIsExemptButLearned(t *testing.T) {
	t.Parallel()
	create := `{"ok":true,"protocol_version":"1","result":{"team_ref":"t","team_name":"ops","join_secret":"brg1.t.abc","principal_ref":"p"}}`
	adapter := failingScript(t, "adapter", "printf '%s\\n' '"+create+"'")
	r := newTestRunner(t, adapter, Options{}, testEnviron)
	res := runFake(t, r, func(ct *T) { ct.Exec(ct.Scratch("x"), nil, "team", "create") })
	if res.Status != StatusPass {
		t.Fatalf("team create stdout flagged: %q", res.Reason)
	}
	if !slices.Contains(r.launcher.knownSecrets(), "brg1.t.abc") {
		t.Fatal("the created secret was not learned")
	}
	// The same bytes from any other command are a C-05 hit.
	res = runFake(t, r, func(ct *T) { ct.Exec(ct.Scratch("y"), nil, "describe") })
	if res.Status != StatusFail || !strings.Contains(res.Reason, "C-05") {
		t.Fatalf("secret on describe stdout not flagged: %s %q", res.Status, res.Reason)
	}
}

func TestRunDirectoryScan(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, "/usr/bin/env", Options{}, testEnviron)
	p := scratchPrincipal(t, r, "x")
	if hits := r.launcher.scanDir(); len(hits) != 0 {
		t.Fatalf("clean directory flagged: %v", hits)
	}
	writeScript(t, "unused", "")
	if err := writeFile(p.StateDir+"/adapter.log", "level=debug secret=brg1.aaa.bbb\n"); err != nil {
		t.Fatal(err)
	}
	hits := r.launcher.scanDir()
	if len(hits) != 1 || !strings.Contains(hits[0], "adapter.log") || strings.Contains(hits[0], "aaa.bbb") {
		t.Fatalf("planted secret: %v", hits)
	}
}

func TestTimeoutIsAFailureNamingTheCommand(t *testing.T) {
	t.Parallel()
	adapter := failingScript(t, "adapter", "sleep 5")
	res := spawnCase(t, adapter, Options{Timeout: 300 * time.Millisecond}, "session", "list")
	if res.Status != StatusFail || !strings.Contains(res.Reason, "session list: did not exit within 300ms") {
		t.Fatalf("status %s, reason %q", res.Status, res.Reason)
	}
}

func TestExecStdinOpenCatchesAStdinReader(t *testing.T) {
	t.Parallel()
	ok := `{"ok":true,"protocol_version":"1","result":{}}`
	reader := failingScript(t, "reader", "cat >/dev/null; printf '%s\\n' '"+ok+"'")
	r := newTestRunner(t, reader, Options{Timeout: 500 * time.Millisecond}, testEnviron)
	res := runFake(t, r, func(ct *T) { ct.ExecStdinOpen(ct.Scratch("x"), "describe") })
	if res.Status != StatusFail || !strings.Contains(res.Reason, "did not exit within") {
		t.Fatalf("stdin reader: status %s, reason %q", res.Status, res.Reason)
	}
	// Positive control: a command that never reads stdin returns at once.
	silent := failingScript(t, "silent", "printf '%s\\n' '"+ok+"'")
	r = newTestRunner(t, silent, Options{Timeout: 5 * time.Second}, testEnviron)
	res = runFake(t, r, func(ct *T) {
		got := ct.ExecStdinOpen(ct.Scratch("x"), "describe")
		if got.Envelope == nil || !got.Envelope.OK {
			ct.Errorf("no envelope")
		}
	})
	if res.Status != StatusPass {
		t.Fatalf("non-reader: status %s, reason %q", res.Status, res.Reason)
	}
}

func TestStdinBytesReachTheChild(t *testing.T) {
	t.Parallel()
	echo := failingScript(t, "echo", `printf '{"ok":true,"protocol_version":"1","result":{"got":"'"$(cat)"'"}}\n'`)
	r := newTestRunner(t, echo, Options{}, testEnviron)
	res := runFake(t, r, func(ct *T) {
		raw := ct.OKRaw(ct.Exec(ct.Scratch("x"), []byte("hello"), "session", "register"))
		if raw["got"] != "hello" {
			ct.Errorf("child read %v", raw["got"])
		}
	})
	if res.Status != StatusPass {
		t.Fatalf("status %s, reason %q", res.Status, res.Reason)
	}
}

func TestAssertionsOKAndFail(t *testing.T) {
	t.Parallel()
	notFound := `{"ok":false,"protocol_version":"1","error":{"code":"not_found","message":"no","retryable":false}}`
	noRetryable := `{"ok":false,"protocol_version":"1","error":{"code":"not_found","message":"no"}}`
	wrongExit := failingScript(t, "wrongexit", "printf '%s\\n' '"+notFound+"'; exit 7")
	adapter := failingScript(t, "adapter", `
case "$1" in
  ok) printf '%s\n' '{"ok":true,"protocol_version":"1","result":{"session_id":"s","state":"offline","lease_until":"2026-08-30T12:00:00Z","server_time":"2026-08-30T12:00:00Z"}}' ;;
  nf) printf '%s\n' '`+notFound+`'; exit 6 ;;
  nr) printf '%s\n' '`+noRetryable+`'; exit 6 ;;
esac
`)
	r := newTestRunner(t, adapter, Options{}, testEnviron)
	check := func(name string, body func(*T), wantStatus Status, wantReason string) {
		t.Helper()
		res := runFake(t, r, body)
		if res.Status != wantStatus || !strings.Contains(res.Reason, wantReason) {
			t.Errorf("%s: status %s, reason %q; want %s containing %q", name, res.Status, res.Reason, wantStatus, wantReason)
		}
	}
	check("OK decodes", func(ct *T) {
		var h protocol.HeartbeatResult
		ct.OK(ct.Exec(ct.Scratch("x"), nil, "ok"), &h)
		if h.SessionID != "s" {
			ct.Errorf("decoded %+v", h)
		}
	}, StatusPass, "")
	check("OK on a failure aborts", func(ct *T) {
		ct.OK(ct.Exec(ct.Scratch("x"), nil, "nf"), nil)
		ct.Errorf("not reached")
	}, StatusFail, "nf: want ok:true, got ok:false (not_found: no)")
	check("Fail matches code, exit and retryable", func(ct *T) {
		e := ct.Fail(ct.Exec(ct.Scratch("x"), nil, "nf"), protocol.CodeNotFound)
		if e.Message != "no" {
			ct.Errorf("error object %+v", e)
		}
	}, StatusPass, "")
	check("Fail wants the retryable key (B-2)", func(ct *T) {
		ct.Fail(ct.Exec(ct.Scratch("x"), nil, "nr"), protocol.CodeNotFound)
	}, StatusFail, "error.retryable is absent (B-2)")
	check("Fail on the wrong code", func(ct *T) {
		ct.Fail(ct.Exec(ct.Scratch("x"), nil, "nf"), protocol.CodeConflict)
	}, StatusFail, "want error.code conflict, got not_found")
	check("Fail on ok:true", func(ct *T) {
		ct.Fail(ct.Exec(ct.Scratch("x"), nil, "ok"), protocol.CodeConflict)
	}, StatusFail, "want ok:false (conflict), got ok:true")
	check("SameBytes and DifferentBytes", func(ct *T) {
		a := ct.Exec(ct.Scratch("x"), nil, "nf")
		b := ct.Exec(ct.Scratch("y"), nil, "nf")
		c := ct.Exec(ct.Scratch("z"), nil, "ok")
		ct.SameBytes("same", a, b)
		ct.DifferentBytes("different", a, c)
		ct.DifferentBytes("control", a, b)
	}, StatusFail, "control: stdout of nf and nf are identical")
	r2 := newTestRunner(t, wrongExit, Options{}, testEnviron)
	res := runFake(t, r2, func(ct *T) { ct.Fail(ct.Exec(ct.Scratch("x"), nil, "nf"), protocol.CodeNotFound) })
	if res.Status != StatusFail || !strings.Contains(res.Reason, "want exit 6 for not_found, got 7") {
		t.Fatalf("exit mismatch: %s %q", res.Status, res.Reason)
	}
}

func TestSkipNoteAndPanicOutcomes(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, "/usr/bin/env", Options{}, testEnviron)
	if res := runFake(t, r, func(ct *T) { ct.Note("seen %d", 1); ct.Skip("not today") }); res.Status != StatusSkip || res.Reason != "not today" || len(res.Notes) != 1 {
		t.Fatalf("skip: %+v", res)
	}
	if res := runFake(t, r, func(ct *T) { ct.Errorf("bad"); ct.Skip("late") }); res.Status != StatusFail || res.Reason != "bad" {
		t.Fatalf("failure then skip: %+v", res)
	}
	if res := runFake(t, r, func(*T) { panic("boom") }); res.Status != StatusFail || !strings.Contains(res.Reason, "panic: boom") {
		t.Fatalf("panic: %+v", res)
	}
	if res := runFake(t, r, func(ct *T) { ct.Fatalf("x"); ct.Errorf("unreachable") }); res.Reason != "x" {
		t.Fatalf("fatalf: %+v", res)
	}
	deferred := false
	runFake(t, r, func(ct *T) { defer func() { deferred = true }(); ct.Fatalf("x") })
	if !deferred {
		t.Fatal("deferred cleanup did not run after Fatalf")
	}
	if res := runFake(t, r, func(ct *T) { ct.Scratch("a"); ct.Scratch("a") }); res.Status != StatusFail || !strings.Contains(res.Reason, "called twice") {
		t.Fatalf("duplicate scratch: %+v", res)
	}
}

func TestSleepHonoursTheContext(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, "/usr/bin/env", Options{}, testEnviron)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ct := newT(ctx, r, Case{ID: "T-02"})
	start := time.Now()
	ct.Sleep(10 * time.Second)
	if time.Since(start) > time.Second {
		t.Fatal("Sleep ignored the cancelled context")
	}
}

func TestRebindAndRestoreRewriteProfileJSON(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, "/usr/bin/env", Options{}, testEnviron)
	p := scratchPrincipal(t, r, "c")
	p.TeamRef = "t2"
	path := p.ConfigDir + "/profiles/default/profile.json"
	if err := os.MkdirAll(p.ConfigDir+"/profiles/default", 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{"version":1,"adapter":"fs","team_ref":"t2","team_name":"ops"}` + "\n"
	if err := writeFile(path, original); err != nil {
		t.Fatal(err)
	}
	res := runFake(t, r, func(ct *T) {
		ct.Rebind(p, "t1")
		ct.Rebind(p, "random")
		data, err := readFile(path)
		if err != nil || !strings.Contains(data, `"team_ref":"random"`) || !strings.Contains(data, `"team_name":"ops"`) {
			ct.Errorf("rebound profile: %q %v", data, err)
		}
		ct.Restore(p)
		if data, _ := readFile(path); data != original {
			ct.Errorf("restored profile: %q", data)
		}
		ct.Restore(p) // idempotent
	})
	if res.Status != StatusPass {
		t.Fatalf("%s %q", res.Status, res.Reason)
	}
}

func TestRebindThroughTheOperatorCommand(t *testing.T) {
	t.Parallel()
	rebind := failingScript(t, "rebind", `cat > "$BRIGADE_CONFIG_DIR/rebound.json"`)
	r := newTestRunner(t, "/usr/bin/env", Options{Rebind: rebind}, testEnviron)
	p := scratchPrincipal(t, r, "c")
	p.TeamRef = "orig"
	res := runFake(t, r, func(ct *T) {
		ct.Rebind(p, "t1")
		if data, _ := readFile(p.ConfigDir + "/rebound.json"); data != `{"team_ref":"t1"}` {
			ct.Errorf("rebind stdin: %q", data)
		}
		ct.Restore(p)
		if data, _ := readFile(p.ConfigDir + "/rebound.json"); data != `{"team_ref":"orig"}` {
			ct.Errorf("restore stdin: %q", data)
		}
	})
	if res.Status != StatusPass {
		t.Fatalf("%s %q", res.Status, res.Reason)
	}
	failing := failingScript(t, "rebind-fail", "exit 3")
	r = newTestRunner(t, "/usr/bin/env", Options{Rebind: failing}, testEnviron)
	res = runFake(t, r, func(ct *T) { ct.Rebind(scratchPrincipal(t, r, "d"), "t1") })
	if res.Status != StatusFail || !strings.Contains(res.Reason, "--rebind command for d exited 3") {
		t.Fatalf("%s %q", res.Status, res.Reason)
	}
}
