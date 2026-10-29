package commands

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/digest"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// gitInit makes the fixture's sync root a repository that ignores, or
// does not ignore, the state folder; skipped without git. The commands
// run git themselves (the address rule), so PATH must carry it.
func gitInit(t *testing.T, root string, ignore bool) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	for _, args := range [][]string{{"init", "-q", "-b", "master"}} {
		cmd := exec.CommandContext(t.Context(), git, append([]string{"-C", root}, args...)...) //nolint:gosec // G204: a test fixture
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if ignore {
		if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".brigade/state/\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return "PATH=" + filepath.Dir(git)
}

// subscribersDoc is a version-2 file: bob with his recipients, alice with
// a handle and the filter.
const subscribersDoc = `{"version":2,"subscribers":[{"label":"bob@example.com","recipients":["@bob","bob@example.com"],"every":"24h"},{"label":"alice@example.com","recipients":["@alice"],"only":"mine"}]}`

// mustReports reads the folder's file.
func mustReports(t *testing.T, folder string) *state.Reports {
	t.Helper()
	r, err := state.ReadReports(folder)
	if err != nil {
		t.Fatalf("reports.json: %v", err)
	}
	return r
}

// TestReportSubscribeWritesTheFile: subscribe writes the caller's own
// entry under their entry's label — the recipients in the order given,
// the period normalised, a second subscribe replacing the first — as a
// version-2 file; an address needs the folder ignored (the address
// rule), a Slack handle does not; a 0.43 file is carried over with its
// marker; a session with no entry or no label cannot subscribe; the flag
// refusals spawn nothing.
func TestReportSubscribeWritesTheFile(t *testing.T) {
	t.Parallel()
	t.Run("ignored folder, an address", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		f.seedEntry(t, folder)
		path := gitInit(t, filepath.Dir(filepath.Dir(folder)), true)
		if err := Report(f.inv(f.sessionEnv(path), "", "subscribe"), ReportOptions{Recipients: "@alice | alice@example.com", Every: "4h"}); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		if f.out.String() != "subscribed: alice@example.com, recipients @alice | alice@example.com, every 4h0m0s\n" || f.rec.count() != 0 {
			t.Fatalf("stdout %q spawns %d", f.out.String(), f.rec.count())
		}
		r := mustReports(t, folder)
		if r.Legacy || len(r.Subscribers) != 1 || r.Subscribers[0].Label != "alice@example.com" || strings.Join(r.Subscribers[0].Recipients, "|") != "@alice|alice@example.com" || r.Subscribers[0].Every != "4h0m0s" || r.Subscribers[0].Only != "" {
			t.Fatalf("reports %+v", r)
		}
		// Again, with no recipients and the filter: the label, an address,
		// is the recipient, and the entry is replaced, not added.
		f.out.Reset()
		inv := f.inv(f.sessionEnv(path), "", "subscribe")
		inv.JSON = true
		if err := Report(inv, ReportOptions{Only: "mine", At: "08:00", TZ: "America/Los_Angeles"}); err != nil {
			t.Fatalf("resubscribe: %v", err)
		}
		ok, result := envelopeOf(t, f.out.String())
		sub, _ := result["subscriber"].(map[string]any)
		reports, _ := result["reports"].(map[string]any)
		if !ok || sub["label"] != "alice@example.com" || sub["only"] != "mine" || sub["at"] != "08:00" || reports["version"] != float64(2) || result["note"] != ReportSubscribedNote {
			t.Fatalf("result %v", result)
		}
		r = mustReports(t, folder)
		if len(r.Subscribers) != 1 || strings.Join(r.Subscribers[0].Recipients, "|") != "alice@example.com" || r.Subscribers[0].Only != state.OnlyMine || r.Subscribers[0].TZ != "America/Los_Angeles" {
			t.Fatalf("reports after the resubscribe %+v", r.Subscribers)
		}
	})
	t.Run("not ignored, an address refused, a handle written", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		f.seedEntry(t, folder)
		path := gitInit(t, filepath.Dir(filepath.Dir(folder)), false)
		wantCode(t, Report(f.inv(f.sessionEnv(path), "", "subscribe"), ReportOptions{Recipients: "alice@example.com"}), protocol.CodeConfig, ReasonNotIgnored)
		// No recipients: the label, an address, would be written, so the
		// rule applies here too.
		wantCode(t, Report(f.inv(f.sessionEnv(path), "", "subscribe"), ReportOptions{}), protocol.CodeConfig, ReasonNotIgnored)
		if _, err := state.ReadReports(folder); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("reports.json was written despite the address rule: %v", err)
		}
		if err := Report(f.inv(f.sessionEnv(path), "", "subscribe"), ReportOptions{Recipients: "#digests"}); err != nil {
			t.Fatal(err)
		}
		if f.out.String() != "subscribed: alice@example.com, recipients #digests, every 24h0m0s\n" {
			t.Fatalf("stdout %q", f.out.String())
		}
	})
	t.Run("a label that is no address needs no git", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		e := state.New(selfSessionID, selfSessionName, fixtureNow.Add(-time.Hour))
		e.Member = "Alice"
		if err := f.stateStore(folder).Save(e); err != nil {
			t.Fatal(err)
		}
		if err := Report(f.inv(f.sessionEnv(), "", "subscribe"), ReportOptions{}); err != nil {
			t.Fatal(err)
		}
		if f.out.String() != "subscribed: Alice, recipients your own session (no handle, channel or address), every 24h0m0s\n" {
			t.Fatalf("stdout %q", f.out.String())
		}
	})
	t.Run("a 0.43 file is carried over with its marker", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		f.seedEntry(t, folder)
		writeReports(t, folder, `{"version":1,"manager":"bob@example.com","recipients":["@bob"],"every":"4h"}`)
		if err := os.MkdirAll(filepath.Dir(digest.LegacyMarkerPath(folder)), 0o750); err != nil { //nolint:gosec // G301: a checkout tree
			t.Fatal(err)
		}
		if err := os.WriteFile(digest.LegacyMarkerPath(folder), []byte(`{"version":1,"produced_at":"2026-09-02T10:00:00Z","session_id":"`+bobSessionID+`","session_name":"billing","decisions_open":0,"commits":1,"active":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Report(f.inv(f.sessionEnv(), "", "subscribe"), ReportOptions{Recipients: "#digests"}); err != nil {
			t.Fatal(err)
		}
		r := mustReports(t, folder)
		if r.Legacy || len(r.Subscribers) != 2 || r.Subscribers[0].Label != "bob@example.com" || r.Subscribers[0].Every != "4h" || r.Subscribers[1].Label != "alice@example.com" {
			t.Fatalf("reports %+v", r.Subscribers)
		}
		if m, err := digest.ReadMarker(folder, "bob@example.com"); err != nil || m.Label != "bob@example.com" || m.SessionID != bobSessionID {
			t.Fatalf("bob's marker %+v %v", m, err)
		}
		if _, err := os.Stat(digest.LegacyMarkerPath(folder)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("the legacy marker stayed")
		}
	})
	t.Run("no entry, no label, a broken file", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		wantCode(t, Report(f.inv(f.sessionEnv(), "", "subscribe"), ReportOptions{Recipients: "#x"}), protocol.CodeConfig, ReasonNoStateEntry)
		e := state.New(selfSessionID, selfSessionName, fixtureNow.Add(-time.Hour))
		if err := f.stateStore(folder).Save(e); err != nil {
			t.Fatal(err)
		}
		wantCode(t, Report(f.inv(f.sessionEnv(), "", "subscribe"), ReportOptions{Recipients: "#x"}), protocol.CodeConfig, ReasonLabelNeeded)
		e.Member = "Alice"
		if err := f.stateStore(folder).Save(e); err != nil {
			t.Fatal(err)
		}
		writeReports(t, folder, `{"version":9}`)
		wantCode(t, Report(f.inv(f.sessionEnv(), "", "subscribe"), ReportOptions{Recipients: "#x"}), protocol.CodeConfig, ReasonReportsBroken)
		if data, _ := os.ReadFile(state.ReportsPath(folder)); string(data) != `{"version":9}` {
			t.Fatalf("the broken file was overwritten: %s", data)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		t.Parallel()
		for name, tc := range map[string]struct {
			args   []string
			opts   ReportOptions
			code   protocol.Code
			reason string
		}{
			"no verb":              {nil, ReportOptions{}, protocol.CodeUsage, ""},
			"a bad verb":           {[]string{"configure"}, ReportOptions{}, protocol.CodeUsage, ""},
			"a recipient shape":    {[]string{"subscribe"}, ReportOptions{Recipients: "@x | nobody"}, protocol.CodeInvalidInput, "recipient_shape"},
			"a bad every":          {[]string{"subscribe"}, ReportOptions{Recipients: "@x", Every: "daily"}, protocol.CodeInvalidInput, ReasonBadEvery},
			"every too short":      {[]string{"subscribe"}, ReportOptions{Recipients: "@x", Every: "10s"}, protocol.CodeInvalidInput, ReasonBadEvery},
			"a bad at":             {[]string{"subscribe"}, ReportOptions{Recipients: "@x", At: "8am"}, protocol.CodeInvalidInput, ReasonBadAt},
			"a bad tz":             {[]string{"subscribe"}, ReportOptions{Recipients: "@x", TZ: "Mars/Olympus"}, protocol.CodeInvalidInput, ReasonBadTZ},
			"a bad only":           {[]string{"subscribe"}, ReportOptions{Recipients: "@x", Only: "theirs"}, protocol.CodeInvalidInput, ReasonBadOnly},
			"no state folder":      {[]string{"subscribe"}, ReportOptions{Recipients: "@x"}, protocol.CodeConfig, ReasonNoStateFolder},
			"unsubscribe, none":    {[]string{"unsubscribe"}, ReportOptions{}, protocol.CodeConfig, ReasonNoStateFolder},
			"show, no folder":      {[]string{"show"}, ReportOptions{}, protocol.CodeConfig, ReasonNoStateFolder},
			"send, no folder":      {[]string{"send"}, ReportOptions{}, protocol.CodeConfig, ReasonNoStateFolder},
			"two verbs":            {[]string{"show", "send"}, ReportOptions{}, protocol.CodeUsage, ""},
			"the old configure":    {[]string{"configure"}, ReportOptions{Recipients: "@x"}, protocol.CodeUsage, ""},
			"subscribe with an at": {[]string{"subscribe"}, ReportOptions{Recipients: "@x", At: "25:00"}, protocol.CodeInvalidInput, ReasonBadAt},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := newFixture(t)
				perr := wantCodeErr(t, Report(f.inv(f.sessionEnv(), "", tc.args...), tc.opts), tc.code, tc.reason)
				if strings.Contains(perr.Message, "Mars") || strings.Contains(perr.Message, "nobody") || strings.Contains(perr.Message, "theirs") {
					t.Errorf("the message echoes a value: %q", perr.Message)
				}
			})
		}
		f := newFixture(t)
		wantCode(t, Report(f.inv(f.terminalEnv(), "", "show"), ReportOptions{}), protocol.CodeConfig, "not_in_session")
		wantCode(t, Report(f.inv(f.terminalEnv(), "", "subscribe"), ReportOptions{}), protocol.CodeConfig, "not_in_session")
	})
}

// TestReportUnsubscribe: unsubscribe removes the caller's own entry and
// nobody else's, as a version-2 file; a second one is not_found; the one
// subscriber of a 0.43 file leaves an empty version-2 list.
func TestReportUnsubscribe(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withStateFolder(t)
	f.seedEntry(t, folder)
	writeReports(t, folder, subscribersDoc)
	if err := Report(f.inv(f.sessionEnv(), "", "unsubscribe"), ReportOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.out.String() != "unsubscribed: alice@example.com\n" || f.rec.count() != 0 {
		t.Fatalf("stdout %q spawns %d", f.out.String(), f.rec.count())
	}
	if r := mustReports(t, folder); len(r.Subscribers) != 1 || r.Subscribers[0].Label != "bob@example.com" {
		t.Fatalf("reports %+v", r.Subscribers)
	}
	wantCode(t, Report(f.inv(f.sessionEnv(), "", "unsubscribe"), ReportOptions{}), protocol.CodeNotFound, ReasonNotSubscribed)
	writeReports(t, folder, `{"version":1,"manager":"alice@example.com"}`)
	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "", "unsubscribe")
	inv.JSON = true
	if err := Report(inv, ReportOptions{}); err != nil {
		t.Fatal(err)
	}
	ok, result := envelopeOf(t, f.out.String())
	reports, _ := result["reports"].(map[string]any)
	subs, _ := reports["subscribers"].([]any)
	if !ok || result["label"] != "alice@example.com" || reports["version"] != float64(2) || len(subs) != 0 || result["note"] != ReportUnsubscribedNote {
		t.Fatalf("result %v", result)
	}
}

// TestReportShowAndSend: show prints the digest a subscriber would get now
// — the caller's own view when subscribed (here with the filter), the
// named subscriber's with --to, everything with nobody subscribed —
// built from the folder and the roster, and writes no marker; send --to
// sends that subscriber's digest through the Slack gateway and writes
// their marker alone; an unknown --to is not_found; a send with nobody
// subscribed refuses.
func TestReportShowAndSend(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withStateFolder(t)
	f.seedEntry(t, folder)
	e := f.mustEntry(t, folder)
	e.Commits = []state.Commit{{Hash: strings.Repeat("a", 40), Subject: "162: Add the report command", Card: 162, At: fixtureNow.Add(-time.Hour)}}
	e.AddNote("Done: the report command", fixtureNow.Add(-30*time.Minute))
	e.UpdatedAt = fixtureNow.Add(-30 * time.Minute)
	if err := f.stateStore(folder).Save(e); err != nil {
		t.Fatal(err)
	}
	forBob := &state.Decision{Version: state.Version, ID: "d-20260901-aaaaaa", Question: "Bob's call?", To: "bob@example.com", Member: "alice@example.com", SessionName: selfSessionName, SessionID: selfSessionID, CreatedAt: fixtureNow.Add(-time.Hour), UpdatedAt: fixtureNow.Add(-time.Hour)}
	forAlice := &state.Decision{Version: state.Version, ID: "d-20260901-bbbbbb", Question: "Alice's call?", To: "ALICE@example.com", Member: "bob@example.com", SessionName: "billing", SessionID: bobSessionID, CreatedAt: fixtureNow.Add(-2 * time.Hour), UpdatedAt: fixtureNow.Add(-2 * time.Hour)}
	for _, d := range []*state.Decision{forBob, forAlice} {
		if err := f.stateStore(folder).SaveDecision(d); err != nil {
			t.Fatal(err)
		}
	}
	f.rec.on("session list", okAnswer(rosterWith(true, true)))
	f.rec.on("message send", okAnswer(sendResultTo("msg-9", slackGatewayID, false)))
	// Nobody subscribed: show prints everything; send refuses.
	if err := Report(f.inv(f.sessionEnv(), "", "show"), ReportOptions{}); err != nil {
		t.Fatalf("show with nobody subscribed: %v", err)
	}
	if body := f.out.String(); !strings.Contains(body, "Decisions open (2):") || !strings.Contains(body, "since the start") {
		t.Fatalf("show with nobody subscribed:\n%s", body)
	}
	wantCode(t, Report(f.inv(f.sessionEnv(), "", "send"), ReportOptions{}), protocol.CodeConfig, ReasonNoSubscriber)
	writeReports(t, folder, subscribersDoc)
	// The caller's own view: alice, with the filter.
	f.out.Reset()
	if err := Report(f.inv(f.sessionEnv(), "", "show"), ReportOptions{}); err != nil {
		t.Fatalf("show: %v", err)
	}
	body := f.out.String()
	if !strings.HasPrefix(body, "Brigade digest: team ops, repository payments, since the start, produced 2026-09-02 12:00 UTC by session payments-api\n") ||
		!strings.Contains(body, "Decisions open for alice@example.com (1):\nDecision d-20260901-bbbbbb: Alice's call?\n  for ALICE@example.com, asked 2026-09-02 by bob@example.com (session billing)") || strings.Contains(body, "Bob's call?") ||
		!strings.Contains(body, "card 162 | aaaaaaa | 162: Add the report command | alice@example.com (session payments-api)") || !strings.Contains(body, "Active now (1):") {
		t.Fatalf("show:\n%s", body)
	}
	if len(f.rec.specsOf("message send")) != 0 {
		t.Fatal("show sent a message")
	}
	if _, err := digest.ReadMarker(folder, "alice@example.com"); err == nil {
		t.Fatal("show wrote the marker")
	}
	// Bob's view, unfiltered; an unknown subscriber.
	f.out.Reset()
	if err := Report(f.inv(f.sessionEnv(), "", "show"), ReportOptions{To: "BOB@example.com"}); err != nil {
		t.Fatalf("show --to: %v", err)
	}
	if body := f.out.String(); !strings.Contains(body, "Decisions open (2):") {
		t.Fatalf("show --to bob:\n%s", body)
	}
	wantCode(t, Report(f.inv(f.sessionEnv(), "", "show"), ReportOptions{To: "nobody@example.com"}), protocol.CodeNotFound, ReasonNoSubscriber)
	wantCode(t, Report(f.inv(f.sessionEnv(), "", "send"), ReportOptions{To: "nobody@example.com"}), protocol.CodeNotFound, ReasonNoSubscriber)
	// send --to bob: through the Slack gateway, his marker written, hers not.
	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "", "send")
	inv.JSON = true
	if err := Report(inv, ReportOptions{To: "bob@example.com"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	ok, result := envelopeOf(t, f.out.String())
	sent, _ := result["sent"].([]any)
	first, _ := sent[0].(map[string]any)
	dg, _ := first["digest"].(map[string]any)
	if !ok || len(sent) != 1 || first["label"] != "bob@example.com" || first["how"] != "slack" || first["to"] != "@bob" || first["message_id"] != "msg-9" || dg["commits"] != float64(1) || dg["decisions_open"] != float64(2) || result["note"] != ReportSentNote {
		t.Fatalf("result %v", result)
	}
	doc := sendDoc(t, f)
	if doc["recipient_session_id"] != slackGatewayID || !strings.HasPrefix(doc["body"].(string), "to: @bob\n\nBrigade digest: team ops") || doc["summary"] != "Brigade digest payments: 2 decisions open, 1 commits, 1 sessions active" {
		t.Fatalf("send %v", doc)
	}
	marker, err := digest.ReadMarker(folder, "bob@example.com")
	if err != nil || marker.Label != "bob@example.com" || marker.SessionID != selfSessionID || !marker.ProducedAt.Equal(fixtureNow) || marker.Commits != 1 {
		t.Fatalf("marker %+v %v", marker, err)
	}
	if _, err := digest.ReadMarker(folder, "alice@example.com"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("alice's marker: %v", err)
	}
	var m map[string]any
	data, _ := os.ReadFile(digest.MarkerPath(folder, "bob@example.com"))
	if err := json.Unmarshal(data, &m); err != nil || m["version"] != float64(1) || m["label"] != "bob@example.com" {
		t.Fatalf("marker file %s %v", data, err)
	}
}

// TestReportSendFansOut: a send with no --to goes to every subscriber,
// each by their own precedence — a gateway, else their own session —
// with one line and one marker each; a subscriber nobody can reach is
// said so and never stops the others; with nobody reachable the command
// refuses and writes no marker.
func TestReportSendFansOut(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withStateFolder(t)
	f.seedEntry(t, folder)
	writeReports(t, folder, `{"version":2,"subscribers":[{"label":"bob@example.com","recipients":["@bob"]},{"label":"Nobody"},{"label":"alice@example.com","recipients":["#digests"]}]}`)
	// No gateway on the roster: bob and alice by their sessions, Nobody not at all.
	f.rec.on("session list", okAnswer(rosterWith(false, false)))
	f.rec.on("message send", okAnswer(sendResultTo("msg-3", bobSessionID, false)))
	if err := Report(f.inv(f.sessionEnv(), "", "send"), ReportOptions{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(f.out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "sent: Brigade digest payments: 0 decisions open, 0 commits, 1 sessions active to bob@example.com at their session "+bobSessionID[:5]) ||
		lines[1] != "not sent: Nobody: no gateway of their recipients is on the roster, and they have no session that can receive a message" ||
		!strings.HasPrefix(lines[2], "sent: Brigade digest payments: 0 decisions open, 0 commits, 1 sessions active to alice@example.com at their session "+selfSessionID[:5]) {
		t.Fatalf("stdout:\n%s", f.out.String())
	}
	sends := f.rec.specsOf("message send")
	if len(sends) != 2 {
		t.Fatalf("sends = %d", len(sends))
	}
	for i, want := range []string{bobSessionID, selfSessionID} {
		var doc map[string]any
		if err := json.Unmarshal(sends[i].Stdin, &doc); err != nil || doc["recipient_session_id"] != want || strings.HasPrefix(doc["body"].(string), "to:") {
			t.Fatalf("send %d: %v %v", i, doc, err)
		}
	}
	for _, label := range []string{"bob@example.com", "alice@example.com"} {
		if m, err := digest.ReadMarker(folder, label); err != nil || m.Label != label {
			t.Fatalf("%s's marker: %+v %v", label, m, err)
		}
	}
	if _, err := digest.ReadMarker(folder, "Nobody"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Nobody's marker: %v", err)
	}
	// Nobody reachable at all.
	g := newFixture(t)
	gfolder := g.withStateFolder(t)
	g.seedEntry(t, gfolder)
	writeReports(t, gfolder, `{"version":2,"subscribers":[{"label":"Nobody"},{"label":"Noone"}]}`)
	g.rec.on("session list", okAnswer(rosterWith(false, false)))
	inv := g.inv(g.sessionEnv(), "", "send")
	inv.JSON = true
	wantCode(t, Report(inv, ReportOptions{}), protocol.CodeNotFound, ReasonNoRecipient)
	if len(g.rec.specsOf("message send")) != 0 || g.out.Len() != 0 {
		t.Fatalf("a send went out, or stdout %q", g.out.String())
	}
	if _, err := digest.ReadMarker(gfolder, "Nobody"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a marker was written with nothing sent")
	}
}
