package commands

import (
	"encoding/json/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
)

const siblingSessionID = "cccccccccccccccccccccccccccccccc"

// ownRoster is a canned `session list` result for the hold: the session
// itself under the label its membership carries, a second session of the
// same principal, bob, and the Slack gateway.
func ownRoster(selfLabel, siblingPrincipal string) string {
	rec := func(id, name, label, principal, harness string) map[string]any {
		return map[string]any{
			"session_id": id, "session_name": name, "human_label": label, "principal_ref": principal,
			"state": "active", "activity": "idle", "inbound": "accept", "harness": harness,
			"last_seen_at": fixtureNow.Add(-time.Minute), "lease_until": fixtureNow.Add(time.Minute), "created_at": fixtureNow.Add(-time.Hour), "is_self": id == selfSessionID,
		}
	}
	b, err := json.Marshal(map[string]any{"team_ref": fixtureTeamRef, "team_name": fixtureTeamName, "server_time": fixtureNow, "truncated": false, "sessions": []any{
		rec(selfSessionID, selfSessionName, selfLabel, "alice-principal", "claude-code"),
		rec(siblingSessionID, "payments-web", selfLabel, siblingPrincipal, "claude-code"),
		rec(bobSessionID, "billing", "bob@example.com", "bob-principal", "claude-code"),
		rec(slackGatewayID, "slack-gateway", "slack gateway", "gateway-principal", "gateway-slack"),
	}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// heldFixture is a session with a state folder, its entry and a live
// watcher: the three a hold needs.
func heldFixture(t *testing.T) (*fixture, string, Invocation) {
	t.Helper()
	f := newFixture(t)
	folder := f.withStateFolder(t)
	f.seedEntry(t, folder)
	inv := f.inv(f.sessionEnv(), "Ship 0.48.0 now, or wait?")
	// A pidfile for a watcher of this session and this build, and a lookup
	// under which that process is alive.
	if err := pidfile.Create(pidfile.Path(f.stateDir, fixturePID), pidfile.Entry{PID: 777, StartToken: "tok-777", BrigadeSessionID: selfSessionID, Version: buildinfo.String()}); err != nil {
		t.Fatal(err)
	}
	inv.Deps.Lookup = func(pid int) (procutil.Info, error) {
		return procutil.Info{PID: pid, Exists: true, StartToken: "tok-777"}, nil
	}
	return f, folder, inv
}

// noProcess is a lookup under which no process is alive.
func noProcess(pid int) (procutil.Info, error) { return procutil.Info{PID: pid}, nil }

// TestDecisionIsHeldForTheSessionsOwnUser (card 236): an ask whose --to
// names the asking session's own member is recorded in both copies and
// not sent — the lines say to ask in the conversation and when it would
// go out — for the member's wait: the default, or their subscription's.
// The member is known by the label the session's entry carries, or by
// the principal of the roster sessions under the label.
func TestDecisionIsHeldForTheSessionsOwnUser(t *testing.T) {
	t.Parallel()
	t.Run("by the entry's label, the default wait", func(t *testing.T) {
		t.Parallel()
		f, folder, inv := heldFixture(t)
		f.rec.on("session list", okAnswer(rosterWith(true, true)))
		if err := Decision(inv, DecisionOptions{To: "Alice@example.com", Options: "ship now | wait", Default: "wait"}); err != nil {
			t.Fatalf("decision: %v", err)
		}
		if got := f.rec.verbs(); !slices.Equal(got, []string{"describe", "session list"}) {
			t.Fatalf("verbs %v: a held ask sends nothing", got)
		}
		lines := strings.Split(strings.TrimSpace(f.out.String()), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "recorded: decision d-20260902-") ||
			lines[1] != "held: Alice@example.com is this session's own user, so ask them here in the conversation; it is sent to them at 12:10 UTC (in 10m0s) only if no prompt arrives here and it is still open — close it with the outcome when they answer" {
			t.Fatalf("stdout %q", f.out.String())
		}
		d, err := f.stateStore(folder).LoadDecision(strings.TrimPrefix(lines[0], "recorded: decision "))
		if err != nil {
			t.Fatal(err)
		}
		if !d.Held() || !d.Holding(fixtureNow) || d.Holding(fixtureNow.Add(state.DefaultWait)) || !d.HeldUntil.Equal(fixtureNow.Add(state.DefaultWait)) || d.Sent != "" || d.HoldEnded != "" || !d.Open() {
			t.Fatalf("decision %+v", *d)
		}
		if theirs := state.ReadDecisions(folder); len(theirs) != 1 || !theirs[0].Held() || !theirs[0].HeldUntil.Equal(d.HeldUntil) {
			t.Fatalf("folder copy %+v", theirs)
		}
		if held := f.stateStore(folder).HeldBy(selfSessionID); len(held) != 1 || held[0].ID != d.ID {
			t.Fatalf("HeldBy %+v", held)
		}
	})
	t.Run("the subscription's wait, as JSON", func(t *testing.T) {
		t.Parallel()
		f, folder, inv := heldFixture(t)
		writeReports(t, folder, `{"version":2,"subscribers":[{"label":"alice@example.com","recipients":["@alice"],"wait":"30m0s"}]}`)
		f.rec.on("session list", okAnswer(rosterWith(true, true)))
		inv.JSON = true
		if err := Decision(inv, DecisionOptions{To: "alice@example.com"}); err != nil {
			t.Fatal(err)
		}
		ok, result := envelopeOf(t, f.out.String())
		held, _ := result["held"].(map[string]any)
		decision, _ := result["decision"].(map[string]any)
		if !ok || held["wait"] != "30m0s" || held["until"] != "2026-09-02T12:30:30Z" || decision["held_until"] != "2026-09-02T12:30:30Z" || result["sent"] != nil || result["not_sent"] != nil || result["note"] != DecisionHeldNote {
			t.Fatalf("result %v", result)
		}
	})
	t.Run("by the roster's principal", func(t *testing.T) {
		// Card 191: a second Claude profile's entry carries its own account's
		// label while the roster shows the label the membership joined with.
		t.Parallel()
		f, folder, inv := heldFixture(t)
		f.rec.on("session list", okAnswer(ownRoster("rjae@example.com", "alice-principal")))
		if err := Decision(inv, DecisionOptions{To: "rjae@example.com"}); err != nil {
			t.Fatal(err)
		}
		if got := state.ReadDecisions(folder); len(got) != 1 || !got[0].Held() || len(f.rec.specsOf("message send")) != 0 {
			t.Fatalf("folder %+v sends %d", got, len(f.rec.specsOf("message send")))
		}
	})
	t.Run("the roster unreadable, the entry's label decides", func(t *testing.T) {
		t.Parallel()
		f, folder, inv := heldFixture(t)
		f.rec.on("session list", failAnswer(protocol.CodeUnavailable, "down"))
		if err := Decision(inv, DecisionOptions{To: "alice@example.com"}); err != nil {
			t.Fatal(err)
		}
		if got := state.ReadDecisions(folder); len(got) != 1 || !got[0].Held() || !strings.Contains(f.out.String(), "\nheld: alice@example.com is this session's own user") {
			t.Fatalf("folder %+v stdout %q", got, f.out.String())
		}
	})
}

// TestDecisionIsSentAtOnceWhenNothingHoldsIt (card 236): an ask goes out
// at once, as before, when it is for another person, when a second member
// carries the label too — the session's own entry label included — when
// the member's wait is zero, when the session has no conversation to ask
// in (a non-interactive one), when it has no watcher to send the ask
// later, and when its watcher is another build's, which may know no hold.
func TestDecisionIsSentAtOnceWhenNothingHoldsIt(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		to, roster, reports string
		setup               func(t *testing.T, f *fixture, inv *Invocation)
		recipient           string
	}{
		"another person": {to: "bob@example.com", roster: rosterWith(false, false), recipient: bobSessionID},
		"a label a second member carries": {
			to: "rjae@example.com", roster: ownRoster("rjae@example.com", "someone-else-principal"), recipient: "",
		},
		"a wait of zero": {
			to: "alice@example.com", roster: rosterWith(true, false), recipient: slackGatewayID,
			reports: `{"version":2,"subscribers":[{"label":"alice@example.com","recipients":["@alice"],"wait":"0s"}]}`,
		},
		"a non-interactive session": {
			to: "alice@example.com", roster: rosterWith(true, false), recipient: slackGatewayID,
			reports: `{"version":2,"subscribers":[{"label":"alice@example.com","recipients":["@alice"]}]}`,
			setup: func(t *testing.T, f *fixture, _ *Invocation) {
				t.Helper()
				m := f.readMap(t)
				m.NonInteractive = true
				f.writeMap(t, m)
			},
		},
		"no watcher": {
			to: "alice@example.com", roster: rosterWith(true, false), recipient: slackGatewayID,
			reports: `{"version":2,"subscribers":[{"label":"alice@example.com","recipients":["@alice"]}]}`,
			setup:   func(_ *testing.T, _ *fixture, inv *Invocation) { inv.Deps.Lookup = noProcess },
		},
		"a watcher of another build": {
			to: "alice@example.com", roster: rosterWith(true, false), recipient: slackGatewayID,
			reports: `{"version":2,"subscribers":[{"label":"alice@example.com","recipients":["@alice"]}]}`,
			setup: func(t *testing.T, f *fixture, _ *Invocation) {
				t.Helper()
				path := pidfile.Path(f.stateDir, fixturePID)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := pidfile.Create(path, pidfile.Entry{PID: 777, StartToken: "tok-777", BrigadeSessionID: selfSessionID, Version: "0.47.2"}); err != nil {
					t.Fatal(err)
				}
			},
		},
		"the entry's own label, which a second member carries too": {
			to: "alice@example.com", roster: ownRoster("alice@example.com", "someone-else-principal"), recipient: "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, folder, inv := heldFixture(t)
			if tc.reports != "" {
				writeReports(t, folder, tc.reports)
			}
			if tc.setup != nil {
				tc.setup(t, f, &inv)
			}
			f.rec.on("session list", okAnswer(tc.roster))
			f.rec.on("message send", okAnswer(sendResultTo("msg-9", tc.recipient, false)))
			if err := Decision(inv, DecisionOptions{To: tc.to}); err != nil {
				t.Fatal(err)
			}
			got := state.ReadDecisions(folder)
			if len(got) != 1 || got[0].Held() || !got[0].HeldUntil.IsZero() || strings.Contains(f.out.String(), "held:") {
				t.Fatalf("held although nothing holds it: %+v stdout %q", got, f.out.String())
			}
			sends := f.rec.specsOf("message send")
			if tc.recipient == "" {
				// The label names two members: nobody is picked, as before.
				if len(sends) != 0 || !strings.Contains(f.out.String(), "not sent: the label names more than one member") {
					t.Fatalf("sends %d stdout %q", len(sends), f.out.String())
				}
				return
			}
			if doc := sendDoc(t, f); doc["recipient_session_id"] != tc.recipient {
				t.Fatalf("send %v", doc)
			}
		})
	}
}

// TestOwnAddressee pins the rule that says whose user a label is.
func TestOwnAddressee(t *testing.T) {
	t.Parallel()
	rec := func(id, label, principal, harness string) protocol.SessionRecord {
		return protocol.SessionRecord{SessionID: id, HumanLabel: label, PrincipalRef: principal, Harness: harness}
	}
	roster := []protocol.SessionRecord{
		rec("self", "rjae@example.com", "p1", "claude-code"),
		rec("sibling", "RJAE@example.com", "p1", "codex"),
		rec("bob", "bob@example.com", "p2", "claude-code"),
		rec("gw", "rjae@example.com", "p3", "gateway-slack"),
	}
	for name, tc := range map[string]struct {
		to, member, self string
		sessions         []protocol.SessionRecord
		want             bool
	}{
		"the entry's label, no roster":       {"Alyne@example.com", "alyne@example.com", "self", nil, true},
		"the entry's label, nobody under it": {"alyne@example.com", "alyne@example.com", "self", roster, true},
		"the entry's label, another's too": {"alyne@example.com", "alyne@example.com", "self",
			append(slices.Clone(roster), rec("other", "Alyne@example.com", "p9", "claude-code")), false},
		"the roster's label, the same principal": {"rjae@example.com", "alyne@example.com", "self", roster, true},
		"a gateway under the label is left out":  {"rjae@example.com", "", "self", roster, true},
		"another member":                         {"bob@example.com", "alyne@example.com", "self", roster, false},
		"a label nobody carries":                 {"carol@example.com", "alyne@example.com", "self", roster, false},
		"the asking session not on the roster":   {"rjae@example.com", "alyne@example.com", "gone", roster, false},
		"a second member under the label": {"rjae@example.com", "alyne@example.com", "self",
			append(slices.Clone(roster), rec("other", "rjae@example.com", "p9", "claude-code")), false},
		"no label of its own, no roster": {"rjae@example.com", "", "self", nil, false},
	} {
		if got := ownAddressee(tc.to, tc.member, tc.self, tc.sessions); got != tc.want {
			t.Errorf("%s: ownAddressee = %v, want %v", name, got, tc.want)
		}
	}
}
