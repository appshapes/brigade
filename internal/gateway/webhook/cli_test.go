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
		strings.NewReader(""), &out, &errb, []string{"BRIGADE_CONFIG_DIR=" + dir, "HOME=" + dir})
	if code == 0 || !strings.Contains(errb.String(), "brigade team join") {
		t.Fatalf("Run = %d, stderr %q", code, errb.String())
	}
}
