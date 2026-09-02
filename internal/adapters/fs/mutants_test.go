package fs

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// The four mutants of plan 9.2 are build-tagged twins inside this
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

// register is the registration document the cap-order fixture sends.
func register(name string) string {
	return `{"harness":"h","harness_version":"1","session_name":"` + name +
		`","activity":"busy","inbound":"accept"}`
}

// sendTo is a minimal `message send` document.
func sendTo(sender, recipient, body string) string {
	return `{"sender_session_id":"` + sender + `","recipient_session_id":"` + recipient + `","body":"` + body + `"}`
}

// TestMutantCapOrderSwapsTheTwoCaps proves the mutant_caporder mutation is
// live and that the normal build checks the two unacknowledged caps of
// 4.5.12 in the spec's order. The ORDER is observable only when BOTH caps
// sit at their limit at once, which is the whole reason this mutant
// exists: every other property of the caps survives the swap.
//
// The fixture puts the probe sender's per-pair cap and the recipient's
// inbox at their limits simultaneously without tripping any rate window:
// the probe principal's one session sends max_unacked_per_sender_recipient
// (15, under the 20-per-minute session budget), and a second principal
// fills the remaining 45 from three sessions of 15 (under its
// 60-per-minute principal budget). The probe's next send is then refused
// by whichever cap the adapter checks first.
func TestMutantCapOrderSwapsTheTwoCaps(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds two adapter binaries")
	}
	for tag, wantReason := range map[string]string{
		"":                "sender_quota_for_recipient",
		"mutant_caporder": "recipient_inbox_full",
	} {
		t.Run(nameOfTag(tag), func(t *testing.T) {
			t.Parallel()
			limits := protocol.DefaultLimits()
			pair, inbox := limits.MaxUnackedPerSenderRecipient, limits.MaxUnackedPerRecipient
			c := newChild(t, tag)
			created := c.ok(`{"team_name":"ops","human_label":"alice@example.com"}`, "team", "create")
			secret := str(t, created, "join_secret")
			recipient := str(t, c.ok(register("r0"), "session", "register"), "session_id")

			// The probe principal fills its own per-pair quota on the recipient.
			c.ok(`{"join_secret":"`+secret+`","human_label":"probe@example.com"}`, "--profile", "probe", "team", "join")
			probe := str(t, c.ok(register("probe-0"), "--profile", "probe", "session", "register"), "session_id")
			for i := 0; i < pair; i++ {
				c.ok(sendTo(probe, recipient, "quota"), "--profile", "probe", "message", "send")
			}
			// A second principal fills the rest of the recipient's inbox.
			c.ok(`{"join_secret":"`+secret+`","human_label":"filler@example.com"}`, "--profile", "filler", "team", "join")
			for filled, n := pair, 0; filled < inbox; n++ {
				sender := str(t, c.ok(register("filler-"+strconv.Itoa(n)), "--profile", "filler", "session", "register"), "session_id")
				for i := 0; i < pair && filled < inbox; i++ {
					c.ok(sendTo(sender, recipient, "fill"), "--profile", "filler", "message", "send")
					filled++
				}
			}

			code, envelope := c.run(sendTo(probe, recipient, "one more"), "--profile", "probe", "message", "send")
			if code != protocol.CodeRateLimited.Exit() {
				t.Fatalf("the send past both caps: exit %d, want %d (%v)", code, protocol.CodeRateLimited.Exit(), envelope)
			}
			failure, _ := envelope["error"].(map[string]any)
			details, _ := failure["details"].(map[string]any)
			if got, _ := details["reason"].(string); got != wantReason {
				t.Fatalf("details.reason %q, want %q (both caps are at their limit; 4.5.12 fixes the order)", got, wantReason)
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
