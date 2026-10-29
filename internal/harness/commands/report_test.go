package commands

import (
	"encoding/json/v2"
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

// TestReportConfigureWritesTheFile: configure writes reports.json with
// the recipients in the order given, the period normalised; an address
// needs the folder ignored (the address rule), a Slack handle does not;
// the flag refusals spawn nothing.
func TestReportConfigureWritesTheFile(t *testing.T) {
	t.Parallel()
	t.Run("ignored folder, an address", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		if err := os.MkdirAll(folder, 0o750); err != nil { //nolint:gosec // G301: a checkout tree
			t.Fatal(err)
		}
		path := gitInit(t, filepath.Dir(filepath.Dir(folder)), true)
		if err := Report(f.inv(f.sessionEnv(path), "", "configure"), ReportOptions{Manager: " Rjae ", Recipients: "@rjae | rjae@example.com", Every: "4h"}); err != nil {
			t.Fatalf("configure: %v", err)
		}
		// Only the manager, an address: it is the recipient.
		g := newFixture(t)
		gfolder := g.withStateFolder(t)
		if err := os.MkdirAll(gfolder, 0o750); err != nil { //nolint:gosec // G301: a checkout tree
			t.Fatal(err)
		}
		gpath := gitInit(t, filepath.Dir(filepath.Dir(gfolder)), true)
		if err := Report(g.inv(g.sessionEnv(gpath), "", "configure"), ReportOptions{Manager: "rjae@example.com"}); err != nil {
			t.Fatalf("configure with the manager alone: %v", err)
		}
		if r, err := state.ReadReports(gfolder); err != nil || strings.Join(r.Recipients, "|") != "rjae@example.com" {
			t.Fatalf("reports with the manager alone: %+v %v", r, err)
		}
		if f.out.String() != "configured: manager Rjae, recipients @rjae | rjae@example.com, every 4h0m0s\n" || f.rec.count() != 0 {
			t.Fatalf("stdout %q spawns %d", f.out.String(), f.rec.count())
		}
		r, err := state.ReadReports(folder)
		if err != nil || r.Manager != "Rjae" || strings.Join(r.Recipients, "|") != "@rjae|rjae@example.com" || r.Every != "4h0m0s" || r.At != "" {
			t.Fatalf("reports %+v %v", r, err)
		}
	})
	t.Run("not ignored, an address refused, a handle written", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		folder := f.withStateFolder(t)
		if err := os.MkdirAll(folder, 0o750); err != nil { //nolint:gosec // G301: a checkout tree
			t.Fatal(err)
		}
		path := gitInit(t, filepath.Dir(filepath.Dir(folder)), false)
		wantCode(t, Report(f.inv(f.sessionEnv(path), "", "configure"), ReportOptions{Manager: "Rjae", Recipients: "rjae@example.com"}), protocol.CodeConfig, ReasonNotIgnored)
		if _, err := state.ReadReports(folder); err == nil {
			t.Fatal("reports.json was written despite the address rule")
		}
		// The manager's label as the one recipient: an address, so the
		// address rule applies and refuses here too.
		wantCode(t, Report(f.inv(f.sessionEnv(path), "", "configure"), ReportOptions{Manager: "rjae@example.com"}), protocol.CodeConfig, ReasonNotIgnored)
		inv := f.inv(f.sessionEnv(path), "", "configure")
		inv.JSON = true
		if err := Report(inv, ReportOptions{Manager: "Rjae", Recipients: "#digests", At: "08:00", TZ: "America/Los_Angeles"}); err != nil {
			t.Fatal(err)
		}
		ok, result := envelopeOf(t, f.out.String())
		reports, _ := result["reports"].(map[string]any)
		if !ok || reports["manager"] != "Rjae" || reports["at"] != "08:00" || reports["tz"] != "America/Los_Angeles" || result["note"] != ReportConfiguredNote {
			t.Fatalf("result %v", result)
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
			"no verb":    {nil, ReportOptions{}, protocol.CodeUsage, ""},
			"a bad verb": {[]string{"wipe"}, ReportOptions{}, protocol.CodeUsage, ""},
			"no manager": {[]string{"configure"}, ReportOptions{Recipients: "@x"}, protocol.CodeInvalidInput, ReasonManagerNeeded},
			"no recipients, a manager that is no address": {[]string{"configure"}, ReportOptions{Manager: "Rjae"}, protocol.CodeInvalidInput, ReasonNoRecipient},
			"a recipient shape":                           {[]string{"configure"}, ReportOptions{Manager: "m", Recipients: "@x | nobody"}, protocol.CodeInvalidInput, "recipient_shape"},
			"a bad every":                                 {[]string{"configure"}, ReportOptions{Manager: "m", Recipients: "@x", Every: "daily"}, protocol.CodeInvalidInput, ReasonBadEvery},
			"every too short":                             {[]string{"configure"}, ReportOptions{Manager: "m", Recipients: "@x", Every: "10s"}, protocol.CodeInvalidInput, ReasonBadEvery},
			"a bad at":                                    {[]string{"configure"}, ReportOptions{Manager: "m", Recipients: "@x", At: "8am"}, protocol.CodeInvalidInput, ReasonBadAt},
			"a bad tz":                                    {[]string{"configure"}, ReportOptions{Manager: "m", Recipients: "@x", TZ: "Mars/Olympus"}, protocol.CodeInvalidInput, ReasonBadTZ},
			"no state folder":                             {[]string{"configure"}, ReportOptions{Manager: "m", Recipients: "@x"}, protocol.CodeConfig, ReasonNoStateFolder},
			"show, no folder":                             {[]string{"show"}, ReportOptions{}, protocol.CodeConfig, ReasonNoStateFolder},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := newFixture(t)
				perr := wantCodeErr(t, Report(f.inv(f.sessionEnv(), "", tc.args...), tc.opts), tc.code, tc.reason)
				if strings.Contains(perr.Message, "Mars") || strings.Contains(perr.Message, "nobody") {
					t.Errorf("the message echoes a value: %q", perr.Message)
				}
			})
		}
		f := newFixture(t)
		wantCode(t, Report(f.inv(f.terminalEnv(), "", "show"), ReportOptions{}), protocol.CodeConfig, "not_in_session")
	})
}

// TestReportShowAndSend: show prints the digest the producer would send
// now, built from the folder and the roster, and writes no marker; send
// sends it through the Slack gateway and writes the marker; without a
// manager both refuse.
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
	wantCode(t, Report(f.inv(f.sessionEnv(), "", "show"), ReportOptions{}), protocol.CodeConfig, ReasonNoManager)
	writeReports(t, folder, reportsDoc)
	// Both gateways on the roster: the address goes through Slack.
	f.rec.on("session list", okAnswer(rosterWith(true, true)))
	f.rec.on("message send", okAnswer(sendResultTo("msg-9", slackGatewayID, false)))
	if err := Report(f.inv(f.sessionEnv(), "", "show"), ReportOptions{}); err != nil {
		t.Fatalf("show: %v", err)
	}
	body := f.out.String()
	if !strings.HasPrefix(body, "Brigade digest: team ops, repository payments, since the start, produced 2026-09-02 12:00 UTC by session payments-api\n") ||
		!strings.Contains(body, "card 162 | aaaaaaa | 162: Add the report command | alice@example.com (session payments-api)") || !strings.Contains(body, "Active now (1):") {
		t.Fatalf("show:\n%s", body)
	}
	if len(f.rec.specsOf("message send")) != 0 {
		t.Fatal("show sent a message")
	}
	if _, err := digest.ReadMarker(folder); err == nil {
		t.Fatal("show wrote the marker")
	}
	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "", "send")
	inv.JSON = true
	if err := Report(inv, ReportOptions{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	ok, result := envelopeOf(t, f.out.String())
	sent, _ := result["sent"].(map[string]any)
	dg, _ := result["digest"].(map[string]any)
	if !ok || sent["How"] != "slack" || sent["To"] != "@bob" || sent["MessageID"] != "msg-9" || dg["commits"] != float64(1) || result["note"] != ReportSentNote {
		t.Fatalf("result %v", result)
	}
	doc := sendDoc(t, f)
	if doc["recipient_session_id"] != slackGatewayID || !strings.HasPrefix(doc["body"].(string), "to: @bob\n\nBrigade digest: team ops") || doc["summary"] != "Brigade digest payments: 0 decisions open, 1 commits, 1 sessions active" {
		t.Fatalf("send %v", doc)
	}
	marker, err := digest.ReadMarker(folder)
	if err != nil || marker.SessionID != selfSessionID || !marker.ProducedAt.Equal(fixtureNow) || marker.Commits != 1 {
		t.Fatalf("marker %+v %v", marker, err)
	}
	var m map[string]any
	data, _ := os.ReadFile(digest.MarkerPath(folder))
	if err := json.Unmarshal(data, &m); err != nil || m["version"] != float64(1) {
		t.Fatalf("marker file %s %v", data, err)
	}
}
