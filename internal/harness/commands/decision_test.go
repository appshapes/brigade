package commands

import (
	"encoding/json/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

const (
	slackGatewayID = "55555555555555555555555555555555"
	mailGatewayID  = "66666666666666666666666666666666"
	bobSessionID   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// rosterWith is a canned `session list` result: the session itself, bob
// (the person decisions are put to, idle) and the gateways asked for.
func rosterWith(slack, mail bool) string {
	rec := func(id, name, label, harness, state string) map[string]any {
		return map[string]any{
			"session_id": id, "session_name": name, "human_label": label, "principal_ref": id[:8] + "-principal",
			"state": state, "activity": "idle", "inbound": "accept", "harness": harness,
			"last_seen_at": fixtureNow.Add(-time.Minute), "lease_until": fixtureNow.Add(time.Minute), "created_at": fixtureNow.Add(-time.Hour), "is_self": id == selfSessionID,
		}
	}
	sessions := []any{
		rec(selfSessionID, selfSessionName, "alice@example.com", "claude-code", "active"),
		rec(bobSessionID, "billing", "bob@example.com", "claude-code", "idle"),
	}
	if slack {
		sessions = append(sessions, rec(slackGatewayID, "slack-gateway", "slack gateway", "gateway-slack", "idle"))
	}
	if mail {
		sessions = append(sessions, rec(mailGatewayID, "mail-gateway", "mail gateway", "gateway-email", "idle"))
	}
	b, err := json.Marshal(map[string]any{"team_ref": fixtureTeamRef, "team_name": fixtureTeamName, "server_time": fixtureNow, "truncated": false, "sessions": sessions})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// writeReports puts a reports.json in the folder.
func writeReports(t *testing.T, folder, doc string) {
	t.Helper()
	if err := os.MkdirAll(folder, 0o750); err != nil { //nolint:gosec // G301: a checkout tree
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportsPath(folder), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// reportsDoc subscribes bob with his recipients.
const reportsDoc = `{"version":2,"subscribers":[{"label":"bob@example.com","recipients":["@bob","bob@example.com"],"every":"24h"}]}`

// sendDoc decodes the one `message send` the recorder saw.
func sendDoc(t *testing.T, f *fixture) map[string]any {
	t.Helper()
	sends := f.rec.specsOf("message send")
	if len(sends) != 1 {
		t.Fatalf("sends = %d, verbs %v", len(sends), f.rec.verbs())
	}
	var doc map[string]any
	if err := json.Unmarshal(sends[0].Stdin, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestDecisionAsksThroughTheSlackGateway: an ask writes the decision to
// both copies with the asker's facts and the addressee, sends it once to
// the Slack gateway — the first of the addressee's recipients a gateway
// carries — under a `to:` first line in the labelled shape, records how
// it went, and says so.
func TestDecisionAsksThroughTheSlackGateway(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withStateFolder(t)
	f.seedEntry(t, folder)
	writeReports(t, folder, reportsDoc)
	f.rec.on("session list", okAnswer(rosterWith(true, true)))
	f.rec.on("message send", okAnswer(sendResultTo("msg-1", slackGatewayID, false)))
	inv := f.inv(f.sessionEnv(), "Ship 0.43.0 now,\nor wait for the Stop hook?\n")
	if err := Decision(inv, DecisionOptions{To: " BOB@example.com ", Options: " ship now | wait ", Default: "wait", By: "2026-10-31", Card: 162}); err != nil {
		t.Fatalf("decision: %v", err)
	}
	if got := f.rec.verbs(); !slices.Equal(got, []string{"describe", "session list", "message send"}) {
		t.Fatalf("verbs %v", got)
	}
	lines := strings.Split(strings.TrimSpace(f.out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "recorded: decision d-20260902-") || !strings.HasPrefix(lines[1], "sent: to BOB@example.com at @bob through the slack gateway, message msg-1") {
		t.Fatalf("stdout %q", f.out.String())
	}
	id := strings.TrimPrefix(lines[0], "recorded: decision ")
	d, err := f.stateStore(folder).LoadDecision(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Question != "Ship 0.43.0 now, or wait for the Stop hook?" || d.To != "BOB@example.com" || !slices.Equal(d.Options, []string{"ship now", "wait"}) || d.Default != "wait" || d.By != "2026-10-31" || d.Card != 162 ||
		d.Member != "alice@example.com" || d.SessionName != selfSessionName || d.SessionID != selfSessionID || d.Repository != "payments" ||
		d.Sent != state.SentSlack || d.SentTo != "@bob" || !d.Open() || !d.CreatedAt.Equal(fixtureNow) {
		t.Fatalf("decision %+v", *d)
	}
	if theirs := state.ReadDecisions(folder); len(theirs) != 1 || theirs[0].Sent != state.SentSlack {
		t.Fatalf("folder copy %+v", theirs)
	}
	doc := sendDoc(t, f)
	body, _ := doc["body"].(string)
	if doc["recipient_session_id"] != slackGatewayID || doc["sender_session_id"] != selfSessionID || !strings.HasPrefix(doc["summary"].(string), "Decision "+id+": Ship 0.43.0 now") {
		t.Fatalf("send %v", doc)
	}
	want := "to: @bob\n\nBrigade decision for BOB@example.com from team ops. Answer however you wish; the session acts on your answer and closes the decision.\n" +
		strings.Join(d.Lines(), "\n")
	if body != want {
		t.Fatalf("body = %q\nwant %q", body, want)
	}
	if !strings.Contains(body, "options: ship now | wait") || !strings.Contains(body, "  for BOB@example.com, asked 2026-09-02 by alice@example.com (session payments-api), repository payments, card 162, answer by 2026-10-31") {
		t.Fatalf("body %q", body)
	}
}

// TestDecisionFallsBackToTheirSession: with no gateway of the addressee's
// recipients on the roster, or no subscription of theirs at all, the
// ask goes to their own best session as a plain team message, no `to:`
// line; with the roster unreadable, no session of theirs either, or a
// label two members share, it is recorded and the line says why it was
// not sent.
func TestDecisionFallsBackToTheirSession(t *testing.T) {
	t.Parallel()
	t.Run("subscribed, no gateway on the roster", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		f.seedEntry(t, folder)
		writeReports(t, folder, reportsDoc)
		f.rec.on("session list", okAnswer(rosterWith(false, false)))
		f.rec.on("message send", okAnswer(sendResultTo("msg-2", bobSessionID, false)))
		inv := f.inv(f.sessionEnv(), "Which backend?")
		inv.JSON = true
		if err := Decision(inv, DecisionOptions{To: "bob@example.com"}); err != nil {
			t.Fatal(err)
		}
		ok, result := envelopeOf(t, f.out.String())
		sent, _ := result["sent"].(map[string]any)
		if !ok || sent["how"] != state.SentSession || sent["session_id"] != bobSessionID || sent["message_id"] != "msg-2" || result["not_sent"] != nil || result["note"] != DecisionNote {
			t.Fatalf("result %v", result)
		}
		doc := sendDoc(t, f)
		if doc["recipient_session_id"] != bobSessionID || strings.HasPrefix(doc["body"].(string), "to:") {
			t.Fatalf("send %v", doc)
		}
		if got := state.ReadDecisions(folder); len(got) != 1 || got[0].Sent != state.SentSession || got[0].SentTo != bobSessionID || len(got[0].Options) != 0 {
			t.Fatalf("folder %+v", got)
		}
	})
	t.Run("not subscribed, no reports.json at all", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		f.seedEntry(t, folder)
		f.rec.on("session list", okAnswer(rosterWith(true, true)))
		f.rec.on("message send", okAnswer(sendResultTo("msg-4", bobSessionID, false)))
		if err := Decision(f.inv(f.sessionEnv(), "Which backend?"), DecisionOptions{To: "bob@example.com"}); err != nil {
			t.Fatal(err)
		}
		if lines := strings.Split(strings.TrimSpace(f.out.String()), "\n"); len(lines) != 2 || !strings.HasPrefix(lines[1], "sent: to bob@example.com at their session "+bobSessionID[:5]+" as a team message (not subscribed, or no gateway of their recipients on the roster), message msg-4") {
			t.Fatalf("stdout %q", f.out.String())
		}
		doc := sendDoc(t, f)
		if doc["recipient_session_id"] != bobSessionID || !strings.HasPrefix(doc["body"].(string), "Brigade decision for bob@example.com from team ops.") {
			t.Fatalf("send %v", doc)
		}
	})
	t.Run("nobody to send to", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		f.seedEntry(t, folder)
		writeReports(t, folder, `{"version":2,"subscribers":[{"label":"nobody@example.com","recipients":["@nobody"]}]}`)
		f.rec.on("session list", okAnswer(rosterWith(false, false)))
		if err := Decision(f.inv(f.sessionEnv(), "Which backend?"), DecisionOptions{To: "nobody@example.com"}); err != nil {
			t.Fatal(err)
		}
		if lines := strings.Split(strings.TrimSpace(f.out.String()), "\n"); len(lines) != 2 || lines[1] != "not sent: no gateway of their recipients is on the roster, and they have no session that can receive a message"+DecisionNotSentSuffix {
			t.Fatalf("stdout %q", f.out.String())
		}
		if len(f.rec.specsOf("message send")) != 0 {
			t.Fatal("a send went out with nobody to send to")
		}
		if got := state.ReadDecisions(folder); len(got) != 1 || got[0].Sent != "" {
			t.Fatalf("folder %+v", got)
		}
	})
	t.Run("the roster unreadable", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		writeReports(t, folder, reportsDoc)
		f.rec.on("session list", failAnswer(protocol.CodeUnavailable, "down"))
		if err := Decision(f.inv(f.sessionEnv(), "Which backend?"), DecisionOptions{To: "bob@example.com"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(f.out.String(), "not sent: the roster could not be read (unavailable)"+DecisionNotSentSuffix) {
			t.Fatalf("stdout %q", f.out.String())
		}
		// No entry seeded: the decision carries no member, and stands.
		if got := state.ReadDecisions(folder); len(got) != 1 || got[0].Member != "" {
			t.Fatalf("folder %+v", got)
		}
	})
}

// TestDecisionRefusals: the flags' and the text's refusals come before
// anything is resolved and spawn nothing — an ask with no --to first of
// all (owner ruling 2026-10-29: an error, not a guess); a project with
// no state folder refuses after the map is read, with the describe
// alone.
func TestDecisionRefusals(t *testing.T) {
	t.Parallel()
	eleven := strings.Repeat("o|", state.MaxOptions) + "o"
	bob := "bob@example.com"
	for _, tc := range []struct {
		name   string
		stdin  string
		args   []string
		opts   DecisionOptions
		setup  func(t *testing.T, f *fixture)
		code   protocol.Code
		reason string
		spawns int
	}{
		{"an argument", "q", []string{"x"}, DecisionOptions{To: bob}, nil, protocol.CodeUsage, "", 0},
		{"no --to", "q", nil, DecisionOptions{Options: "a | b"}, nil, protocol.CodeInvalidInput, ReasonToNeeded, 0},
		{"a blank --to", "q", nil, DecisionOptions{To: "  "}, nil, protocol.CodeInvalidInput, ReasonToNeeded, 0},
		{"a --to with a credential", "q", nil, DecisionOptions{To: "sbp_0123456789abcdef"}, nil, protocol.CodeInvalidInput, doing.ReasonSecretShaped, 0},
		{"too many options", "q", nil, DecisionOptions{To: bob, Options: eleven}, nil, protocol.CodeInvalidInput, ReasonTooManyOptions, 0},
		{"an option with a credential", "q", nil, DecisionOptions{To: bob, Options: "a | sbp_0123456789abcdef"}, nil, protocol.CodeInvalidInput, doing.ReasonSecretShaped, 0},
		{"a default that is no option", "q", nil, DecisionOptions{To: bob, Options: "a | b", Default: "c"}, nil, protocol.CodeInvalidInput, ReasonDefaultNotOption, 0},
		{"a default with no options", "q", nil, DecisionOptions{To: bob, Default: "c"}, nil, protocol.CodeInvalidInput, ReasonDefaultNotOption, 0},
		{"a by that is no date", "q", nil, DecisionOptions{To: bob, By: "Friday"}, nil, protocol.CodeInvalidInput, ReasonByNotADate, 0},
		{"a negative card", "q", nil, DecisionOptions{To: bob, Card: -1}, nil, protocol.CodeInvalidInput, ReasonCardNegative, 0},
		{"an empty question", "\n", nil, DecisionOptions{To: bob}, nil, protocol.CodeInvalidInput, doing.ReasonEmpty, 0},
		{"a question over the cap", strings.Repeat("q", state.MaxQuestionChars+1), nil, DecisionOptions{To: bob}, nil, protocol.CodeInvalidInput, doing.ReasonTooLong, 0},
		{"no state folder", "q", nil, DecisionOptions{To: bob}, nil, protocol.CodeConfig, ReasonNoStateFolder, 1},
		{"a close with a bad id", "done", nil, DecisionOptions{Close: "../x"}, nil, protocol.CodeInvalidInput, "invalid_id", 0},
		{"a close of an unknown id", "done", nil, DecisionOptions{Close: "d-20260902-ffffff"}, func(t *testing.T, f *fixture) { t.Helper(); f.withStateFolder(t) }, protocol.CodeNotFound, "no_match", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			perr := wantCodeErr(t, Decision(f.inv(f.sessionEnv(), tc.stdin, tc.args...), tc.opts), tc.code, tc.reason)
			if strings.Contains(perr.Message, "sbp_") || strings.Contains(perr.Message, "Friday") {
				t.Errorf("the message echoes an argument: %q", perr.Message)
			}
			if f.rec.count() != tc.spawns || f.out.Len() != 0 {
				t.Errorf("spawns %d (want %d) stdout %q", f.rec.count(), tc.spawns, f.out.String())
			}
		})
	}
	t.Run("outside a session", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		wantCode(t, Decision(f.inv(f.terminalEnv(), "q"), DecisionOptions{To: "bob@example.com"}), protocol.CodeConfig, "not_in_session")
		wantCode(t, Decision(f.inv(f.terminalEnv(), "done"), DecisionOptions{Close: "d-1"}), protocol.CodeConfig, "not_in_session")
	})
}

// TestDecisionClose: a close by the asking session, and by another
// session of the member who asked (reading the folder's copy), records
// the outcome and the closer in both copies and spawns nothing; a second
// close is a conflict; another member's close is refused.
func TestDecisionClose(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withStateFolder(t)
	f.seedEntry(t, folder)
	// A decision another session of alice's asked: in the folder alone.
	theirs := state.Store{StateDir: t.TempDir(), Folder: folder}
	asked := &state.Decision{Version: state.Version, ID: "d-20260901-aaaaaa", Question: "Which backend?", Member: "alice@example.com", SessionName: "other", SessionID: "other-sess", CreatedAt: fixtureNow.Add(-time.Hour)}
	if err := theirs.SaveDecision(asked); err != nil {
		t.Fatal(err)
	}
	bobs := &state.Decision{Version: state.Version, ID: "d-20260901-bbbbbb", Question: "Bob's", Member: "bob@example.com", SessionName: "billing", SessionID: bobSessionID, CreatedAt: fixtureNow.Add(-time.Hour)}
	if err := theirs.SaveDecision(bobs); err != nil {
		t.Fatal(err)
	}
	if err := Decision(f.inv(f.sessionEnv(), "Supabase stays;\nthe swap is card 156\n"), DecisionOptions{Close: asked.ID}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if f.out.String() != "closed: decision d-20260901-aaaaaa (Supabase stays; the swap is card 156)\n" || f.rec.count() != 0 {
		t.Fatalf("stdout %q spawns %d", f.out.String(), f.rec.count())
	}
	d, err := f.stateStore(folder).LoadDecision(asked.ID)
	if err != nil {
		t.Fatalf("the closer has no canonical copy: %v", err)
	}
	if d.Open() || !d.ClosedAt.Equal(fixtureNow) || d.Outcome != "Supabase stays; the swap is card 156" || d.ClosedBy == nil ||
		d.ClosedBy.Member != "alice@example.com" || d.ClosedBy.SessionName != selfSessionName || d.ClosedBy.SessionID != selfSessionID {
		t.Fatalf("closed %+v", *d)
	}
	if got, err := state.ReadDecision(folder, asked.ID); err != nil || got.Open() {
		t.Fatalf("folder copy %+v %v", got, err)
	}
	wantCode(t, Decision(f.inv(f.sessionEnv(), "again"), DecisionOptions{Close: asked.ID}), protocol.CodeConflict, ReasonAlreadyClosed)
	wantCode(t, Decision(f.inv(f.sessionEnv(), "mine now"), DecisionOptions{Close: bobs.ID}), protocol.CodeUnauthorized, ReasonNotTheAsker)
	if got, _ := state.ReadDecision(folder, bobs.ID); !got.Open() {
		t.Fatal("bob's decision was closed by alice")
	}
	// The asking session itself, with a --json close.
	f.out.Reset()
	own := &state.Decision{Version: state.Version, ID: "d-20260902-cccccc", Question: "Mine", SessionName: selfSessionName, SessionID: selfSessionID, CreatedAt: fixtureNow}
	if err := f.stateStore(folder).SaveDecision(own); err != nil {
		t.Fatal(err)
	}
	inv := f.inv(f.sessionEnv(), "ship")
	inv.JSON = true
	if err := Decision(inv, DecisionOptions{Close: own.ID}); err != nil {
		t.Fatal(err)
	}
	ok, result := envelopeOf(t, f.out.String())
	dec, _ := result["decision"].(map[string]any)
	if !ok || dec["outcome"] != "ship" || result["sent"] != nil || result["note"] != DecisionClosedNote {
		t.Fatalf("result %v", result)
	}
}
