package watch_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// The member's default human_label rides EVERY registration the watcher
// sends (card 24, part C), so a membership whose label is empty is filled
// by the first registration that reaches an adapter announcing
// session.human_label — the session start, or the watcher replacement a
// plugin version change causes, whichever comes first. The re-open path is
// the registration a watcher sends on its own, so it is the one these
// tests drive: the fake adapter closes the session under the watcher and
// the recorded `session register` document is read back.
//
// The `label` option decides what is sent, exactly as it does for a
// `team create` or `team join`: `account` (the default, and what an empty
// option means) sends the Claude account email, `none` sends nothing at
// all, and a literal sends that text. The map carries the OPTION, never a
// label — writeAccountEmail below is the only place the email exists, and
// the assertions prove the watcher read it from there and not from the
// map.

// writeAccountEmail puts an `oauthAccount.emailAddress` into the
// fixture's $CLAUDE_CONFIG_DIR/.claude.json, the file
// internal/harness/account reads.
func writeAccountEmail(t *testing.T, dir, email string) {
	t.Helper()
	doc := `{"oauthAccount":{"emailAddress":"` + email + `"},"numStartups":3}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(doc), 0o600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
}

func TestReopenRegistrationCarriesTheLabelForEachOptionValue(t *testing.T) {
	t.Parallel()
	const email = "member@example.com"
	for _, tc := range []struct {
		name   string
		option string
		want   string // "" means the member must be absent from the wire
	}{
		{"account resolves to the claude account email", "account", email},
		{"an absent option defaults to the account email", "", email},
		{"none sends no label at all", "none", ""},
		{"a literal sends that text", "Rae on the laptop", "Rae on the laptop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t, fixtureOptions{sink: true})
			writeAccountEmail(t, fx.dirs.ClaudeConfig, email)
			now := time.Now().UTC().Format(time.RFC3339)
			fx.useFake(fakeadapter.Script{
				Describe: fakeadapter.DescribeJSON("1", "message.receive", "session.inbound", "session.resume", "session.human_label"),
				Responses: map[string][]fakeadapter.Response{
					"session heartbeat": {
						{Error: closedConflict()},
						{Result: []byte(`{"session_id":"` + reopenSessionID + `","state":"idle","lease_until":"` + now + `","server_time":"` + now + `"}`)},
					},
					"session register": {{Result: resumedRegister(now)}},
					"session close":    {{Result: []byte(`{"session_id":"` + reopenSessionID + `","state":"offline"}`)}},
				},
				Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t)}},
			})
			fx.writeMapWith(func(m *sessionmap.ByPID) {
				m.BrigadeSessionID = reopenSessionID
				m.LabelOption = tc.option
			})
			rr := &registerRecorder{}
			deps := fx.deps()
			deps.HeartbeatInterval = 150 * time.Millisecond
			deps.Spawn = rr.spawn
			r := fx.start(deps, fx.args()...)
			fx.waitLog("session re-opened", map[string]any{"session_id": reopenSessionID})
			regs := rr.registrations()
			if len(regs) != 1 {
				t.Fatalf("registrations %+v, want exactly one", regs)
			}
			switch {
			case tc.want == "" && regs[0].HumanLabel != nil:
				t.Errorf("label option %q: human_label %q on the wire, want the member absent", tc.option, *regs[0].HumanLabel)
			case tc.want != "" && regs[0].HumanLabel == nil:
				t.Errorf("label option %q: no human_label on the wire, want %q", tc.option, tc.want)
			case tc.want != "" && *regs[0].HumanLabel != tc.want:
				t.Errorf("label option %q: human_label %q, want %q", tc.option, *regs[0].HumanLabel, tc.want)
			}
			if code := r.stopAndWait(); code != 0 {
				t.Fatalf("exit %d", code)
			}
		})
	}
}

// TestReopenRegistrationCarriesTheLabelEveryTime: the label is not a
// once-only offer. A watcher whose session is closed under it TWICE sends
// it on both registrations — which is what makes the fill survive a
// backend that gained the capability between the two, and costs nothing
// against one that already adopted a label, because adopting is the
// adapter's decision and it never overwrites.
func TestReopenRegistrationCarriesTheLabelEveryTime(t *testing.T) {
	t.Parallel()
	const email = "member@example.com"
	fx := newFixture(t, fixtureOptions{sink: true})
	writeAccountEmail(t, fx.dirs.ClaudeConfig, email)
	now := time.Now().UTC().Format(time.RFC3339)
	ok := fakeadapter.Response{Result: []byte(`{"session_id":"` + reopenSessionID + `","state":"idle","lease_until":"` + now + `","server_time":"` + now + `"}`)}
	fx.useFake(fakeadapter.Script{
		Describe: fakeadapter.DescribeJSON("1", "message.receive", "session.inbound", "session.resume", "session.human_label"),
		Responses: map[string][]fakeadapter.Response{
			// Closed, re-opened, closed again, re-opened again.
			"session heartbeat": {{Error: closedConflict()}, ok, {Error: closedConflict()}, ok},
			"session register":  {{Result: resumedRegister(now)}, {Result: resumedRegister(now)}},
			"session close":     {{Result: []byte(`{"session_id":"` + reopenSessionID + `","state":"offline"}`)}},
		},
		Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t)}},
	})
	fx.writeMapWith(func(m *sessionmap.ByPID) {
		m.BrigadeSessionID = reopenSessionID
		m.LabelOption = "account"
	})
	rr := &registerRecorder{}
	deps := fx.deps()
	deps.HeartbeatInterval = 150 * time.Millisecond
	deps.Spawn = rr.spawn
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(rr.registrations()) >= 2 })
	for i, reg := range rr.registrations() {
		if reg.HumanLabel == nil || *reg.HumanLabel != email {
			t.Errorf("re-registration %d: human_label %v, want %q on every registration", i, reg.HumanLabel, email)
		}
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}
