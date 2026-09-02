package supabase

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestNoArgumentsIsUsage pins the shape: exit 2, the envelope on stdout,
// the program name on stderr.
func TestNoArgumentsIsUsage(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := r.fails("usage", 2, "")
	if !strings.Contains(got.stderr, progName+": ") {
		t.Fatalf("stderr = %q, want a line beginning %q", got.stderr, progName+": ")
	}
}

// TestUnknownArgvIsUsage covers C-02's argv half and the fs adapter's
// measured answers: an unknown group, an unknown verb, a verb after
// `describe`, an unknown flag on a core command, a flag that belongs to
// another command, a leading --root (this adapter has none), an empty
// leading flag value and a bad --log-level flag are all `usage`.
func TestUnknownArgvIsUsage(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"unknown group":          {"nosuchgroup", "list"},
		"unknown verb":           {"session", "nosuchverb"},
		"unknown team verb":      {"team", "nosuchverb"},
		"unknown message verb":   {"message", "nosuchverb"},
		"unknown profile verb":   {"profile", "nosuchverb"},
		"verb after describe":    {"describe", "extra"},
		"unknown flag":           {"session", "list", "--nosuchflag"},
		"flag of another verb":   {"describe", "--include-offline"},
		"limit on list":          {"session", "list", "--limit", "5"},
		"missing verb":           {"session"},
		"root anywhere":          {"--root", "/tmp", "describe"},
		"root after the verb":    {"session", "list", "--root", "/tmp"},
		"leading unknown":        {"--nosuchflag", "describe"},
		"leading value gone":     {"--profile"},
		"leading empty profile":  {"--profile", "", "describe"},
		"leading empty level":    {"--log-level=", "describe"},
		"bad log-level flag":     {"--log-level", "shout", "describe"},
		"bad log-level after":    {"describe", "--log-level", "shout"},
		"positional after verb":  {"session", "list", "extra"},
		"missing session on ack": {"message", "ack"},
		"limit zero":             {"message", "receive", "--session", testTeamID, "--limit", "0"},
		"limit too high":         {"message", "receive", "--session", testTeamID, "--limit", "201"},
		"limit not a number":     {"message", "receive", "--session", testTeamID, "--limit", "abc"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			newRig(t).fails("usage", 2, "", args...)
		})
	}
}

// TestJoinSecretOnArgvIsPoison covers C-05: --join-secret is refused with
// `usage` on EVERY command, and its value never appears in the output.
func TestJoinSecretOnArgvIsPoison(t *testing.T) {
	t.Parallel()
	const poison = "brg1.abc.NOTAREALSECRET"
	for name, args := range map[string][]string{
		"core command":       {"session", "list", "--join-secret", poison},
		"convention command": {"team", "join", "--join-secret=" + poison},
		"before the group":   {"--join-secret", poison, "describe"},
		"underscore form":    {"profile", "status", "--join_secret", poison},
		"after a separator":  {"describe", "--", "--join-secret", poison},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := newRig(t).fails("usage", 2, "", args...)
			if strings.Contains(got.stdout+got.stderr, "NOTAREALSECRET") {
				t.Fatalf("the secret was echoed: %q %q", got.stdout, got.stderr)
			}
		})
	}
}

// TestHelpWritesTextOnStderrOnly pins 4.1's stdout discipline: stdout
// carries the envelope and never free text, even for help.
func TestHelpWritesTextOnStderrOnly(t *testing.T) {
	t.Parallel()
	got := newRig(t).fails("usage", 2, "", "--help")
	if !strings.Contains(got.stderr, "groups:") {
		t.Fatalf("stderr carries no usage text: %q", got.stderr)
	}
	if strings.Contains(got.stdout, "groups:") {
		t.Fatalf("stdout carries free text: %q", got.stdout)
	}
}

// TestEmptyProfileAfterTheVerbIsConfig is the guide's measured rule: an
// empty --profile after the verb is a bad profile name (`config`), while
// an empty BRIGADE_PROFILE counts as unset and resolves to default.
func TestEmptyProfileAfterTheVerbIsConfig(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := r.fails("config", 11, "", "describe", "--profile", "")
	if details(t, got.stdout)["reason"] != "invalid_profile_name" {
		t.Fatalf("details = %v, want invalid_profile_name", details(t, got.stdout))
	}
	result := r.ok("describe")
	if str(t, result["profile"].(map[string]any), "name") != "default" {
		t.Fatalf("describe did not run against the default profile: %v", result)
	}
	env := append(append([]string{}, r.env...), "BRIGADE_PROFILE=")
	out := r.execEnv(env, "", "describe")
	if out.code != 0 || !strings.Contains(out.stdout, `"name":"default"`) {
		t.Fatalf("empty BRIGADE_PROFILE: exit %d stdout %s, want default", out.code, out.stdout)
	}
}

// TestLogLevelEnvironmentIsConfig: an invalid BRIGADE_LOG_LEVEL in the
// ENVIRONMENT is `config`, an invalid FLAG value `usage` (4.1, 4.6), and
// debug logging never reaches stdout.
func TestLogLevelEnvironmentIsConfig(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	env := append(append([]string{}, r.env...), "BRIGADE_LOG_LEVEL=shout")
	got := r.execEnv(env, "", "describe")
	if got.code != 11 {
		t.Fatalf("exit %d, want 11 (config)", got.code)
	}
	assertErrorCode(t, got.stdout, "config")
	env = append(append([]string{}, r.env...), "BRIGADE_LOG_LEVEL=debug")
	got = r.execEnv(env, "", "session", "list")
	assertOneEnvelope(t, got.stdout)
	if !strings.Contains(got.stderr, `"level":"DEBUG"`) {
		t.Fatalf("stderr carries no debug line at BRIGADE_LOG_LEVEL=debug: %q", got.stderr)
	}
}

// TestOrderOfChecks is section 3 of the brief (C-02, C-06, C-23): the
// profile is resolved FIRST — no profile is `config`, a profile without a
// backend is `config`, a profile with no usable session.json is
// `unauthenticated`, a credential with no team is `config` — and only
// then is stdin read and validated, and only then is the backend
// called. A malformed document on an unconfigured profile therefore
// answers `config`, and the same document on a joined profile answers
// `invalid_input` with no network call.
func TestOrderOfChecks(t *testing.T) {
	t.Parallel()
	register := `{"harness":"claude-code","harness_version":"1","session_name":"a","activity":"busy","inbound":"accept"}`
	send := `{"sender_session_id":"` + testTeamID + `","recipient_session_id":"` + testUserID + `","body":"hi"}`
	commands := []struct {
		name  string
		input string
		args  []string
	}{
		{"session list", "", []string{"session", "list"}},
		{"session register", register, []string{"session", "register"}},
		{"session heartbeat", `{}`, []string{"session", "heartbeat", "--session", testTeamID}},
		{"session close", "", []string{"session", "close", "--session", testTeamID}},
		{"message send", send, []string{"message", "send"}},
		{"message receive", "", []string{"message", "receive", "--session", testTeamID}},
		{"message ack", `{"message_ids":["x"]}`, []string{"message", "ack", "--session", testTeamID}},
		{"team members", "", []string{"team", "members"}},
	}

	t.Run("no profile is config", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		for _, c := range commands {
			got := r.fails("config", 11, "{", c.args...)
			if details(t, got.stdout)["reason"] != reasonProfileMissing {
				t.Errorf("%s: details = %v, want profile_missing", c.name, details(t, got.stdout))
			}
			if strings.Contains(got.stderr, "goroutine ") || strings.Contains(got.stderr, "panic:") {
				t.Errorf("%s: stderr carries a stack trace", c.name)
			}
		}
		if r.be.total() != 0 {
			t.Fatalf("the backend was called %d times on an unconfigured profile", r.be.total())
		}
	})

	t.Run("no backend is config", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.initProfile()
		p, _, _ := r.command("describe").loadProfile()
		p.URL, p.PublishableKey = "", ""
		r.bindTeam(testTeamID, "ops")
		rewriteProfile(t, r, func(m map[string]any) { delete(m, "url"); delete(m, "publishable_key") })
		got := r.fails("config", 11, "", "session", "list")
		if details(t, got.stdout)["reason"] != reasonBackendUnconfigured {
			t.Fatalf("details = %v, want backend_unconfigured", details(t, got.stdout))
		}
	})

	t.Run("no credential is unauthenticated", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.initProfile()
		r.bindTeam(testTeamID, "ops")
		for _, c := range commands {
			got := r.fails("unauthenticated", 4, c.input, c.args...)
			if details(t, got.stdout)["reason"] != reasonCredentialMissing {
				t.Errorf("%s: details = %v, want credential_missing", c.name, details(t, got.stdout))
			}
		}
		if r.be.total() != 0 {
			t.Fatalf("the backend was called %d times without a credential", r.be.total())
		}
	})

	t.Run("no team is config", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.initProfile()
		r.writeSession(r.session(time.Hour, "rt-1"))
		for _, c := range commands {
			got := r.fails("config", 11, c.input, c.args...)
			if details(t, got.stdout)["reason"] != reasonNoTeamBound {
				t.Errorf("%s: details = %v, want no_team_bound", c.name, details(t, got.stdout))
			}
		}
		if r.be.total() != 0 {
			t.Fatalf("the backend was called %d times without a team", r.be.total())
		}
	})

	t.Run("joined then stdin then the backend", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.joined()
		for _, c := range commands {
			if c.input == "" {
				continue
			}
			got := r.fails("invalid_input", 3, "{", c.args...)
			if details(t, got.stdout)["reason"] != "malformed_json" {
				t.Errorf("%s: details = %v, want malformed_json", c.name, details(t, got.stdout))
			}
			r.fails("invalid_input", 3, "", c.args...)
		}
		forged := `{"sender_session_id":"` + testTeamID + `","recipient_session_id":"` + testUserID + `","body":"hi","sender":{"principal_ref":"x"}}`
		got := r.fails("invalid_input", 3, forged, "message", "send")
		if details(t, got.stdout)["field"] != "sender" {
			t.Errorf("forged sender: details = %v, want field sender", details(t, got.stdout))
		}
		big := `{"a":"` + strings.Repeat("x", 1<<20) + `"}`
		r.fails("invalid_input", 3, big, "session", "register")
		if r.be.total() != 0 {
			t.Fatalf("the backend was called %d times before stdin was validated", r.be.total())
		}
		// A valid document on a joined profile is past every LOCAL check,
		// so the command reaches the backend.
		for _, c := range commands {
			before := r.be.total()
			got := r.exec(c.input, c.args...)
			if r.be.total() == before {
				t.Errorf("%s: exit %d without reaching the backend (stdout %s)", c.name, got.code, got.stdout)
			}
		}
	})

	t.Run("a bound team_ref that is not a uuid is the uniform unauthorized", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.joined()
		r.bindTeam("0123456789abcdef0123456789abcdef", "ops")
		got := r.fails("unauthorized", 5, "", "session", "list")
		if errorJSON(t, got.stdout) != errorJSON(t, newFailure(t, errNotMember())) {
			t.Fatalf("unauthorized envelope differs from the uniform one: %s", got.stdout)
		}
		if r.be.total() != 0 {
			t.Fatalf("the backend was called for a team_ref that cannot name a team")
		}
	})
}

// TestNoInputCommandsNeverReadStdin pins B-1: describe, session list,
// session close, message receive, team leave and team members exit
// without touching a stdin that never delivers.
func TestNoInputCommandsNeverReadStdin(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	for _, args := range [][]string{
		{"describe"},
		{"profile", "status"},
		{"session", "list"},
		{"session", "close", "--session", testTeamID},
		{"message", "receive", "--session", testTeamID},
		{"team", "leave"},
		{"team", "members"},
	} {
		done := make(chan int, 1)
		go func() {
			done <- run(args, newBlockingStdin(t), io.Discard, io.Discard, r.env, r.clock())
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%v read stdin (B-1)", args)
		}
	}
}

// TestWatchRefusalsAreOneErrorEvent is C-37's positive control (4.1): a
// pre-catch-up failure of `message watch` — a missing --session, an
// unknown flag, an unconfigured profile, a backend that refuses the first
// drain — is ONE NDJSON error event with retryable false and the code's exit status,
// never a 4.3 envelope, and a session id that is not uuid-shaped is the
// uniform not_found. A failure raised during argv SETUP, before the verb
// is known — the poison flag, a bad leading --log-level — answers with a
// 4.3 envelope instead, exactly as the fs adapter does (BAP/1.x (e)).
func TestWatchRefusalsAreOneErrorEvent(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		joined  bool
		args    []string
		code    string
		exit    int
		backend func(w http.ResponseWriter)
	}{
		"missing session": {true, []string{"message", "watch"}, "usage", 2, nil},
		"unknown flag":    {true, []string{"message", "watch", "--session", testTeamID, "--bogus"}, "usage", 2, nil},
		"no profile":      {false, []string{"message", "watch", "--session", testTeamID}, "config", 11, nil},
		"not a uuid":      {true, []string{"message", "watch", "--session", "deadbeef"}, "not_found", 6, nil},
		"not a member": {true, []string{"message", "watch", "--session", testTeamID}, "unauthorized", 5,
			func(w http.ResponseWriter) { postgrest(w, http.StatusForbidden, "42501", "brigade:unauthorized") }},
		"backend down": {true, []string{"message", "watch", "--session", testTeamID}, "unavailable", 9,
			func(w http.ResponseWriter) { writeJSON(w, http.StatusBadGateway, `<html>bad gateway</html>`) }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			if tc.joined {
				r.joined()
			}
			if tc.backend != nil {
				r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) { tc.backend(w) }
			}
			got := r.exec("", tc.args...)
			if got.code != tc.exit {
				t.Fatalf("exit %d, want %d (stdout %s)", got.code, tc.exit, got.stdout)
			}
			env := decode(t, got.stdout)
			if env["event"] != "error" {
				t.Fatalf("stdout is not an error event: %s", got.stdout)
			}
			object, _ := env["error"].(map[string]any)
			if object["code"] != tc.code {
				t.Fatalf("error.code = %v, want %s", object["code"], tc.code)
			}
			if retryable, present := object["retryable"].(bool); !present || retryable {
				t.Fatalf("retryable = %v (present %v), want false", retryable, present)
			}
		})
	}
	// The uniform not_found: byte-identical to every other not_found.
	r := newRig(t)
	r.joined()
	got := r.exec("", "message", "watch", "--session", "deadbeef")
	if errorJSON(t, got.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("watch not_found differs from the uniform envelope: %s", got.stdout)
	}
	// The setup-time refusals: one 4.3 envelope, the fs adapter's answer.
	for _, args := range [][]string{
		{"message", "watch", "--session", testTeamID, "--join-secret", "brg1.x.y"},
		{"--log-level", "shout", "message", "watch"},
	} {
		got := r.fails("usage", 2, "", args...)
		if strings.Contains(got.stdout, `"event"`) {
			t.Fatalf("%v: a setup failure answered with an event: %s", args, got.stdout)
		}
	}
}

// blockingStdin is a stdin that never delivers and never ends.
type blockingStdin struct{ done chan struct{} }

func newBlockingStdin(t *testing.T) *blockingStdin {
	t.Helper()
	b := &blockingStdin{done: make(chan struct{})}
	t.Cleanup(func() { close(b.done) })
	return b
}

func (b *blockingStdin) Read([]byte) (int, error) {
	<-b.done
	return 0, io.EOF
}
