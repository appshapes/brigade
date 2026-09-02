package fs

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// The three mutants of plan 9.2 are build-tagged twins inside this
// package, so the NORMAL build contains no mutant code path at all:
// nothing for a stray variable or a repository `env` block to switch on.
// P1-6's own mutants_test.go will assert that each fails EXACTLY its
// conformance cases; this file asserts the two things P1-6 would otherwise
// have to take on trust — that each mutation is actually LIVE in its
// build, and that the normal build is not mutated.
//
// Every subtest drives a real child process, because that is the only
// place a build tag can be observed.

// adapterBinary builds ./cmd/brigade-adapter-fs with the given build tags
// into a directory belonging to the test.
func adapterBinary(t *testing.T, tags string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "brigade-adapter-fs")
	args := []string{"build", "-trimpath"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "-o", out, "./cmd/brigade-adapter-fs")
	cmd := exec.CommandContext(t.Context(), "go", args...) //nolint:gosec // a fixed argv; no shell
	cmd.Dir = testutil.RepoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build -tags %q: %v\n%s", tags, err, out)
	}
	return out
}

// child is one adapter binary with its own isolated directories.
type child struct {
	t      *testing.T
	binary string
	env    []string
}

func newChild(t *testing.T, tags string) *child {
	t.Helper()
	base := t.TempDir()
	return &child{
		t:      t,
		binary: adapterBinary(t, tags),
		env: []string{
			"HOME=" + filepath.Join(base, "home"),
			"BRIGADE_CONFIG_DIR=" + filepath.Join(base, "config"),
			"BRIGADE_STATE_DIR=" + filepath.Join(base, "state"),
			"BRIGADE_FS_ROOT=" + filepath.Join(base, "root"),
		},
	}
}

// run executes one command against the child binary.
func (c *child) run(input string, args ...string) (int, map[string]any) {
	c.t.Helper()
	cmd := exec.CommandContext(c.t.Context(), c.binary, args...) //nolint:gosec // argv built by this test
	cmd.Env = c.env
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		c.t.Fatalf("%v: %v (stderr %s)", args, err, stderr.String())
	}
	var envelope map[string]any
	if uerr := json.Unmarshal(stdout.Bytes(), &envelope); uerr != nil {
		c.t.Fatalf("%v: stdout is not one envelope: %v (%q)", args, uerr, stdout.String())
	}
	return cmd.ProcessState.ExitCode(), envelope
}

// ok runs a command that must succeed and returns its result object.
func (c *child) ok(input string, args ...string) map[string]any {
	c.t.Helper()
	code, envelope := c.run(input, args...)
	if code != 0 {
		c.t.Fatalf("%v: exit %d (%v)", args, code, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	return result
}

// fixture creates one team with two sessions and returns their ids and the
// team's join secret.
func (c *child) fixture() (alice, bob, secret string) {
	c.t.Helper()
	created := c.ok(`{"team_name":"ops","human_label":"alice@example.com"}`, "team", "create")
	first := c.ok(`{"harness":"h","harness_version":"1","session_name":"alice-1",`+
		`"activity":"busy","inbound":"accept"}`, "session", "register")
	second := c.ok(`{"harness":"h","harness_version":"1","session_name":"bob-1",`+
		`"activity":"busy","inbound":"accept"}`, "session", "register")
	return str(c.t, first, "session_id"), str(c.t, second, "session_id"), str(c.t, created, "join_secret")
}

// TestMutantNoAckDoesNotPersist proves the mutant_noack mutation is live
// and that the normal build is not mutated (C-30, C-36).
func TestMutantNoAckDoesNotPersist(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds two adapter binaries")
	}
	for tag, wantRemaining := range map[string]int{"": 0, "mutant_noack": 1} {
		t.Run(nameOfTag(tag), func(t *testing.T) {
			t.Parallel()
			c := newChild(t, tag)
			alice, bob, _ := c.fixture()
			id := str(t, c.ok(`{"sender_session_id":"`+alice+`","recipient_session_id":"`+bob+
				`","body":"ack me"}`, "message", "send"), "message_id")
			acked := c.ok(`{"message_ids":["`+id+`"]}`, "message", "ack", "--session", bob)
			if got := mustStrings(t, acked, "acked"); len(got) != 1 || got[0] != id {
				t.Fatalf("ack answered %v; both builds must REPORT the ack", acked)
			}
			left := mustMessages(t, c.ok("", "message", "receive", "--session", bob))
			if len(left) != wantRemaining {
				t.Fatalf("after the ack %d message(s) remain, want %d", len(left), wantRemaining)
			}
		})
	}
}

// TestMutantTeamLeakListsEveryTeam proves the mutant_teamleak mutation is
// live and that the normal build is team-scoped (C-12, C-26).
func TestMutantTeamLeakListsEveryTeam(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds two adapter binaries")
	}
	for tag, wantSessions := range map[string]int{"": 1, "mutant_teamleak": 2} {
		t.Run(nameOfTag(tag), func(t *testing.T) {
			t.Parallel()
			c := newChild(t, tag)
			c.ok(`{"team_name":"ops"}`, "--profile", "alice", "team", "create")
			c.ok(`{"harness":"h","harness_version":"1","session_name":"alice-1",`+
				`"activity":"busy","inbound":"accept"}`, "--profile", "alice", "session", "register")
			c.ok(`{"team_name":"other"}`, "--profile", "carol", "team", "create")
			c.ok(`{"harness":"h","harness_version":"1","session_name":"carol-1",`+
				`"activity":"busy","inbound":"accept"}`, "--profile", "carol", "session", "register")

			listed := mustSessions(t, c.ok("", "--profile", "alice", "session", "list"))
			if len(listed) != wantSessions {
				t.Fatalf("session list returned %d session(s), want %d: %v", len(listed), wantSessions, listed)
			}
		})
	}
}

// TestMutantTrustSenderAcceptsForgedIdentity proves the
// mutant_trustsender mutation is live on both its halves — the forbidden
// members of 4.4.6 and the ownership rule of 4.5.7 — and that the normal
// build refuses both (C-23, C-24).
func TestMutantTrustSenderAcceptsForgedIdentity(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds two adapter binaries")
	}
	for tag, wantExit := range map[string]int{"": 3, "mutant_trustsender": 0} {
		t.Run(nameOfTag(tag), func(t *testing.T) {
			t.Parallel()
			c := newChild(t, tag)
			alice, bob, secret := c.fixture()
			forged := `{"sender_session_id":"` + alice + `","recipient_session_id":"` + bob +
				`","body":"who sent this?","sender":{"session_id":"` + bob + `"}}`
			code, envelope := c.run(forged, "message", "send")
			if code != wantExit {
				t.Fatalf("a forged sender member: exit %d, want %d (%v)", code, wantExit, envelope)
			}
			// The ownership half of 4.5.7 (C-24): a second member of the
			// SAME team tries to send AS a session it does not own. The
			// real build answers the uniform not_found; the mutant, which
			// skips the ownership check, accepts it.
			c.ok(`{"join_secret":"`+secret+`","human_label":"carol@example.com"}`,
				"--profile", "carol", "team", "join")
			carolCode, envelope := c.run(`{"sender_session_id":"`+alice+`","recipient_session_id":"`+bob+
				`","body":"not mine"}`, "--profile", "carol", "message", "send")
			wantOwnership := 6
			if tag != "" {
				wantOwnership = 0
			}
			if carolCode != wantOwnership {
				t.Fatalf("impersonation: exit %d, want %d (%v)", carolCode, wantOwnership, envelope)
			}
		})
	}
}

// nameOfTag labels the normal build readably in the subtest name.
func nameOfTag(tag string) string {
	if tag == "" {
		return "normal build"
	}
	return tag
}
