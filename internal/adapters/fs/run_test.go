package fs

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestNoArgumentsIsUsage pins the shape cmd/brigade's smoke.txtar asserts
// through a real process: exit 2, the envelope on stdout, the program name
// on stderr.
func TestNoArgumentsIsUsage(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := r.fails("usage", 2, "")
	if !strings.Contains(got.stderr, progName+": ") {
		t.Fatalf("stderr = %q, want a line beginning %q", got.stderr, progName+": ")
	}
	if strings.Count(got.stderr, "\n") != 1 {
		t.Fatalf("stderr = %q, want exactly one line", got.stderr)
	}
}

// TestUnknownArgvIsUsage covers C-02's argv half: an unknown group, an
// unknown verb, a verb after `describe` and an unknown flag on a core
// command are all `usage`.
func TestUnknownArgvIsUsage(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"unknown group":       {"nosuchgroup", "list"},
		"unknown verb":        {"session", "nosuchverb"},
		"verb after describe": {"describe", "extra"},
		"unknown flag":        {"session", "list", "--nosuchflag"},
		"missing verb":        {"session"},
		"root after the verb": {"session", "list", "--root", "/tmp"},
		"leading unknown":     {"--nosuchflag", "describe"},
		"leading value gone":  {"--root"},
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
	r := newRig(t)
	got := r.fails("usage", 2, "", "--help")
	if !strings.Contains(got.stderr, "groups:") {
		t.Fatalf("stderr carries no usage text: %q", got.stderr)
	}
	if strings.Contains(got.stdout, "groups:") {
		t.Fatalf("stdout carries free text: %q", got.stdout)
	}
}

// TestRelativeRootIsConfig: a relative root would resolve against the
// hook's working directory, which is the project tree (3.2).
func TestRelativeRootIsConfig(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.fails("config", 11, "", "--root", "relative/path", "session", "list")

	env := append([]string{}, r.env...)
	env = append(env, "BRIGADE_FS_ROOT=also/relative")
	got := r.execEnv(env, "", "session", "list")
	if got.code != 11 {
		t.Fatalf("relative BRIGADE_FS_ROOT: exit %d, want 11", got.code)
	}
}

// TestLogLevelSourceDecidesTheCode: an invalid FLAG value is `usage`, an
// invalid ENVIRONMENT value is `config` (4.1, 4.6).
func TestLogLevelSourceDecidesTheCode(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.fails("usage", 2, "", "--log-level", "loud", "describe")
	r.fails("usage", 2, "", "describe", "--log-level", "loud")

	env := append([]string{}, r.env...)
	env = append(env, "BRIGADE_LOG_LEVEL=loud")
	if got := r.execEnv(env, "", "describe"); got.code != 11 {
		t.Fatalf("invalid BRIGADE_LOG_LEVEL: exit %d, want 11", got.code)
	}
}

// TestDebugLoggingNeverReachesStdout is the other half of 4.1's stdout
// rule: the suite runs every adapter at BRIGADE_LOG_LEVEL=debug, so every
// case is also this check.
func TestDebugLoggingNeverReachesStdout(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	env := append([]string{}, r.env...)
	env = append(env, "BRIGADE_LOG_LEVEL=debug")
	got := r.execEnv(env, "", "--profile", "alice", "session", "list")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if strings.Count(strings.TrimRight(got.stdout, "\n"), "\n") != 0 {
		t.Fatalf("stdout carries more than one document: %q", got.stdout)
	}
	decode(t, got.stdout)
}

// TestLeadingFlagsFromTheHarness: the harness prepends adapter_command's
// fixed arguments, so --root, --profile and --log-level must be accepted
// BEFORE the group (4.1, decision 10).
func TestLeadingFlagsFromTheHarness(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	env := []string{"HOME=" + r.base, "BRIGADE_CONFIG_DIR=" + r.cfg, "BRIGADE_STATE_DIR=" + r.state}
	got := r.execEnv(env, "", "--root", r.root, "--profile", "alice", "--log-level=debug", "describe")
	if got.code != 0 {
		t.Fatalf("exit %d: %s %s", got.code, got.stdout, got.stderr)
	}
	profile, _ := decode(t, got.stdout)["result"].(map[string]any)["profile"].(map[string]any)
	if name, _ := profile["name"].(string); name != "alice" {
		t.Fatalf("profile.name = %q, want alice", name)
	}
}

// TestUnboundProfileIsConfigNotAStackTrace covers C-06 for this adapter:
// a credential with no team bound is exit 11 `config` on every session and
// message command, and exit 4 when there is no credential at all.
func TestUnboundProfileIsConfigNotAStackTrace(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	commands := [][]string{
		{"session", "list"},
		{"session", "register"},
		{"message", "receive", "--session", "x"},
		{"message", "send"},
	}
	for _, args := range commands {
		r.fails("config", 11, "{}", append([]string{"--profile", "nobody"}, args...)...)
	}
	r.ok("", "--profile", "nobody", "profile", "init")
	for _, args := range commands {
		r.fails("config", 11, "{}", append([]string{"--profile", "nobody"}, args...)...)
	}
	r.ok("", "--profile", "nobody", "profile", "revoke-credentials")
	for _, args := range commands {
		r.fails("unauthenticated", 4, "{}", append([]string{"--profile", "nobody"}, args...)...)
	}
}

// TestCommandsThatTakeNoInputNeverReadStdin covers B-1: the harness spawns
// such commands with stdin ignored, and a human running them by hand would
// otherwise hang forever.
func TestCommandsThatTakeNoInputNeverReadStdin(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	for name, args := range map[string][]string{
		"describe":     {"describe"},
		"session list": {"--profile", "alice", "session", "list"},
		"team members": {"--profile", "alice", "team", "members"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			done := make(chan int, 1)
			go func() {
				var stdout, stderr bytes.Buffer
				done <- run(args, newBlockingStdin(t), &stdout, &stderr, r.env, r.clock())
			}()
			select {
			case code := <-done:
				if code != 0 {
					t.Fatalf("exit %d", code)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the command read stdin and blocked")
			}
		})
	}
}

// TestProfileNameIsPathSafe: a hostile --profile must not traverse out of
// the profiles directory.
func TestProfileNameIsPathSafe(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, name := range []string{"../escape", ".hidden", "with/slash"} {
		r.fails("config", 11, "", "--profile", name, "describe")
		r.fails("config", 11, "", "describe", "--profile", name)
	}
	// An empty leading value is argv nonsense, not a profile name.
	r.fails("usage", 2, "", "--profile", "", "describe")
	r.fails("config", 11, "", "describe", "--profile", "")
}
