package webhook

import (
	"bytes"
	"encoding/json/v2"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/testutil"
)

func TestCheckTarget(t *testing.T) {
	for _, c := range []struct {
		url       string
		allowHTTP bool
		ok        bool
	}{
		{"https://example.com/hook", false, true},
		{"http://127.0.0.1:9000/hook", false, true},
		{"http://localhost/hook", false, true},
		{"http://[::1]:80/", false, true},
		{"http://example.com/hook", false, false},
		{"http://example.com/hook", true, true},
		{"https://user:pw@example.com/hook", false, false},
		{"ftp://example.com/", true, false},
		{"/relative", false, false},
	} {
		if err := checkTarget(c.url, c.allowHTTP); (err == nil) != c.ok {
			t.Errorf("checkTarget(%q, %v) = %v, want ok=%v", c.url, c.allowHTTP, err, c.ok)
		}
	}
}

func writeSecret(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, append(testSecret, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The client half: `send` and `sessions` sign with the secret file and
// talk to a running gateway's handler.
func TestClientSendAndSessions(t *testing.T) {
	fb, _, _, g := setup(t)
	fb.sessions = roster()
	// The handler checks signatures against real time here: the client signs with time.Now.
	g.cfg.Now = time.Now
	srv := httptest.NewServer(g.Handler(0))
	t.Cleanup(srv.Close)
	secret := writeSecret(t)
	run := func(stdin string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Run(t.Context(), args, strings.NewReader(stdin), &out, &errb, nil)
		return code, out.String(), errb.String()
	}
	code, out, errs := run("from stdin", "send", "--gateway", srv.URL, "--secret-file", secret, "--to", "4-bob", "--id", "c-1")
	if code != 0 {
		t.Fatalf("send = %d %s %s", code, out, errs)
	}
	var resp apiResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.RecipientSessionID != "bbbb-4444-bob" {
		t.Fatalf("send output %q: %v", out, err)
	}
	sent, _ := fb.snapshot()
	if len(sent) != 1 || !strings.HasSuffix(sent[0].Body, "\n\nfrom stdin") {
		t.Fatalf("sent = %+v", sent)
	}
	if code, out, _ = run("", "sessions", "--gateway", srv.URL, "--secret-file", secret); code != 0 || !strings.Contains(out, "aaaa-2222-codex") {
		t.Fatalf("sessions = %d %s", code, out)
	}
	if code, _, _ = run("", "send", "--gateway", srv.URL, "--secret-file", secret, "--to", "nothing-like-it", "--text", "x"); code != 1 {
		t.Fatalf("a refused send exited %d", code)
	}
	if code, _, _ = run("", "send", "--gateway", srv.URL, "--secret-file", "relative", "--to", "4-bob", "--text", "x"); code != 2 {
		t.Fatalf("a relative secret path exited %d", code)
	}
	if code, _, _ = run("", "send", "--gateway", "http://example.com", "--secret-file", secret, "--to", "4-bob", "--text", "x"); code != 2 {
		t.Fatalf("a non-loopback http gateway exited %d", code)
	}
}

// The binary is multi-call so that a bundled adapter (`adapter supabase`)
// resolves to the gateway's own executable.
func TestBinaryBundlesTheSupabaseAdapter(t *testing.T) {
	bin := testutil.Build(t, "./cmd/brigade-webhook-gateway")
	//nolint:gosec // G204: the binary this test just built, with fixed arguments
	cmd := exec.CommandContext(t.Context(), bin, "adapter", "supabase", "--profile", "default", "describe")
	cmd.Env = []string{"HOME=" + t.TempDir()}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("adapter supabase: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), `"brigade-adapter-supabase"`) {
		t.Fatalf("describe = %s", out)
	}
}

func TestRunWithoutAJoinedTeamExplains(t *testing.T) {
	var out, errb bytes.Buffer
	dir := t.TempDir()
	code := Run(t.Context(), []string{"--target-url", "https://example.com/hook", "--secret-file", writeSecret(t)},
		strings.NewReader(""), &out, &errb,
		// Started from a coding session's shell, which is how an operator
		// following the documentation first tries it.
		[]string{"BRIGADE_CONFIG_DIR=" + dir, "HOME=" + dir, "CLAUDE_PID=1", "CLAUDECODE=1"})
	if code == 0 || !strings.Contains(errb.String(), "brigade team join") {
		t.Fatalf("Run = %d, stderr %q", code, errb.String())
	}
	// Diagnostics come from the redacting logger, which is a JSON handler;
	// a bare slog.NewTextHandler would redact nothing.
	if !strings.HasPrefix(strings.TrimSpace(errb.String()), "{") {
		t.Errorf("diagnostics did not come from the redacting logger: %q", errb.String())
	}
}

// The gateway secret must live outside the team's checkout, which is
// what docs/security.md 14.1 says of it and what `brigade team create`
// already enforces for the join secret. The checkout is knowable
// wherever --team-file names one, and the operator page tells the
// gateway's machine to keep that checkout.
func TestSecretFileMustBeOutsideTheTeamsCheckout(t *testing.T) {
	base := t.TempDir()
	checkout := filepath.Join(base, "checkout")
	if err := os.MkdirAll(filepath.Join(checkout, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teamFile := filepath.Join(checkout, ".brigade.json")
	outside := filepath.Join(base, "secrets")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	// A symlink out of the checkout into it: the literal spelling is
	// outside, the resolved one is not.
	link := filepath.Join(outside, "into-checkout")
	if err := os.Symlink(checkout, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name       string
		secretFile string
		teamFile   string
		ok         bool
	}{
		{"outside the checkout", filepath.Join(outside, "secret"), teamFile, true},
		{"relative", "secret", teamFile, false},
		{"empty", "", teamFile, false},
		{"at the toplevel", filepath.Join(checkout, "secret"), teamFile, false},
		{"under the checkout", filepath.Join(checkout, "sub", "secret"), teamFile, false},
		{"through a symlink into the checkout", filepath.Join(link, "secret"), teamFile, false},
		// Spelled by hand: filepath.Join would Clean the `..` away.
		{"a `..` component", outside + "/../secrets/secret", teamFile, false},
		// Without --team-file no project directory is knowable: only the
		// absolute-path and `..` rules apply (the `send` client's case).
		{"no team file, outside", filepath.Join(outside, "secret"), "", true},
		{"no team file, relative", "secret", "", false},
		{"no team file, `..`", outside + "/../secret", "", false},
		// A team file outside any repository names only its own directory.
		{"team file outside a repository", filepath.Join(outside, "secret"), filepath.Join(outside, ".brigade.json"), false},
	} {
		if err := checkSecretFile(c.secretFile, c.teamFile); (err == nil) != c.ok {
			t.Errorf("%s: checkSecretFile(%q, %q) = %v, want ok=%v", c.name, c.secretFile, c.teamFile, err, c.ok)
		}
	}
	// And the whole binary refuses it, before the secret is read.
	var out, errb bytes.Buffer
	secret := filepath.Join(checkout, "secret")
	if err := os.WriteFile(secret, append(testSecret, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	code := Run(t.Context(), []string{"--target-url", "https://example.com/hook", "--team-file", teamFile, "--secret-file", secret},
		strings.NewReader(""), &out, &errb, []string{"BRIGADE_CONFIG_DIR=" + base, "HOME=" + base})
	if code == 0 || !strings.Contains(errb.String(), "must not be inside the team's checkout") {
		t.Fatalf("Run with an in-checkout secret = %d, stderr %q", code, errb.String())
	}
}

// --log-level takes the four words of the repository's grammar, not
// slog's "INFO" / "WARN-1" / "DEBUG+4".
func TestRunRefusesALogLevelOutsideTheGrammar(t *testing.T) {
	for _, level := range []string{"INFO", "WARN-1", "DEBUG+4", "chatty"} {
		var out, errb bytes.Buffer
		code := Run(t.Context(), []string{"--log-level", level, "--target-url", "https://example.com/hook", "--secret-file", writeSecret(t)},
			strings.NewReader(""), &out, &errb, nil)
		if code != 2 || strings.Contains(errb.String(), level) {
			t.Errorf("--log-level %q: exit %d, stderr %q", level, code, errb.String())
		}
	}
}

// The gateway is never inside a coding session, so BRIGADE_CONFIG_DIR and
// BRIGADE_STATE_DIR are its identity even when a session's variables are
// inherited: the harness's rules, which drop every BRIGADE_* (and XDG_*
// for the pointed hosts) because a by-pid map decides identity there, must
// not reach this binary, which has no such map.
func TestGatewayDirsHonourTheBrigadeVariablesInsideASession(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	base := []string{
		"BRIGADE_CONFIG_DIR=" + configDir, "BRIGADE_STATE_DIR=" + stateDir,
		"HOME=/home/someone-else", "XDG_CONFIG_HOME=/xdg/config", "XDG_STATE_HOME=/xdg/state",
		"CLAUDE_CONFIG_DIR=/home/someone-else/.claude",
	}
	for _, session := range []string{
		"", "CLAUDE_PID=1", "CLAUDECODE=1", "CLAUDE_CODE_BRIDGE_SESSION_ID=x",
		"MUSE_SESSION_ID=6f1a", "CODEX_THREAD_ID=6f1a", "GROK_SESSION_ID=6f1a", "AI_AGENT=1",
	} {
		env := base
		if session != "" {
			env = append(append([]string{}, base...), session)
		}
		gotConfig, gotState, err := gatewayDirs(env)
		if err != nil || gotConfig != configDir || gotState != stateDir {
			t.Errorf("with %q: gatewayDirs = %q, %q, %v; want %q, %q", session, gotConfig, gotState, err, configDir, stateDir)
		}
	}
	// CLAUDE_CONFIG_DIR is the one session-shaped variable that survives.
	if kept := stripSession([]string{"CLAUDE_CONFIG_DIR=/c", "CLAUDE_PID=1", "PATH=/bin"}); len(kept) != 2 ||
		kept[0] != "CLAUDE_CONFIG_DIR=/c" || kept[1] != "PATH=/bin" {
		t.Errorf("stripSession kept %v", kept)
	}
}
