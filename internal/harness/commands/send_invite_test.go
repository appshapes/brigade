package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/gateway"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestSendInvite is card 242's sender half: --invite writes the summary the
// recipient's Brigade recognises an invitation by, and the confirmation
// says what the sender may expect back.
func TestSendInvite(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.rec.on("session list", okAnswer(listResult()))
	f.rec.on("message send", okAnswer(sendResultJSON("m-i", false)), okAnswer(sendResultJSON("m-i", true)))
	body := "Plan: shared/plans/checkout-test/plan.md\nYou would take P1 and P2.\n"
	if err := Send(f.inv(f.sessionEnv(), body, recipientID), SendOptions{Invite: "  checkout-test\n"}); err != nil {
		t.Fatalf("send --invite: %v", err)
	}
	req := sentRequest(t, f)
	if req.Summary != "Plan invitation: checkout-test" || req.ReplyTo != "" || req.Body != body {
		t.Errorf("request = %+v", req)
	}
	if !strings.HasPrefix(req.Summary, frame.InvitationSummaryPrefix) {
		t.Errorf("summary %q does not begin with the frame's prefix", req.Summary)
	}
	if err := req.Validate(); err != nil {
		t.Errorf("the request does not validate: %v", err)
	}
	want := "accepted: message m-i to " + recipientID + ". " + AcceptedNote + "\n" +
		"recipient: bbbbb, idle, inbound accept, name \"billing\" (unverified)\n" +
		"invitation: plan \"checkout-test\"; " + InvitationNote + "\n"
	if f.out.String() != want {
		t.Errorf("stdout:\n got %q\nwant %q", f.out.String(), want)
	}

	f.out.Reset()
	inv := f.inv(f.sessionEnv(), body, recipientID)
	inv.JSON = true
	if err := Send(inv, SendOptions{Invite: "checkout-test"}); err != nil {
		t.Fatalf("send --invite --json: %v", err)
	}
	_, result := envelopeOf(t, f.out.String())
	got, _ := result["invitation"].(map[string]any)
	if got["plan"] != "checkout-test" || got["note"] != InvitationNote {
		t.Errorf("invitation = %v", result["invitation"])
	}
	if _, has := got["older_plugin"]; has {
		t.Errorf("a recipient with no reported version carries older_plugin: %v", got)
	}

	// An ordinary send says nothing about an invitation.
	f2 := newFixture(t)
	f2.rec.on("session list", okAnswer(listResult())).on("message send", okAnswer(sendResultJSON("m-o", false)))
	inv = f2.inv(f2.sessionEnv(), body, recipientID)
	inv.JSON = true
	if err := Send(inv, SendOptions{Summary: "status"}); err != nil {
		t.Fatal(err)
	}
	if _, result := envelopeOf(t, f2.out.String()); result["invitation"] != nil {
		t.Errorf("an ordinary send carries invitation: %v", result["invitation"])
	}
}

// TestSendInviteUsage: the flag writes the summary itself and an invitation
// answers nothing, so neither --summary nor --reply-to goes with it; a plan
// name of nothing but space is no name; and the cap is the summary's own.
// Each is refused before anything is spawned.
func TestSendInviteUsage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, opts := range []SendOptions{
		{Invite: "p", Summary: "s"},
		{Invite: "p", ReplyTo: "m-0"},
		{Invite: " \n\t"},
	} {
		wantCode(t, Send(f.inv(f.sessionEnv(), "body", recipientID), opts), protocol.CodeUsage, "")
	}
	// A name that is nothing once sanitised is no name: the recipient's
	// frame reads the sanitised summary, and would find no plan after the
	// prefix.
	wantCode(t, Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{Invite: "\u200b\u202e"}), protocol.CodeUsage, "")
	long := strings.Repeat("p", protocol.MaxSummaryChars-len(frame.InvitationSummaryPrefix)+1)
	perr := wantCodeErr(t, Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{Invite: long}), protocol.CodeInvalidInput, "too_long")
	if perr.Details["field"] != "invite" || perr.Details["limit"] != "183" || perr.Details["actual"] != "184" {
		t.Errorf("details = %v", perr.Details)
	}
	// A body that opens the way a gateway opens what it relays would be
	// confirmed here and be no invitation there: refused, the opening named.
	for _, body := range []string{
		"Email from the coordinator: you would take P1.",
		"Slack message from the plan channel, forwarded.",
		"Message from \"coordinator\": you would take P1.",
		"Message from a webhook caller (not really).",
	} {
		perr := wantCodeErr(t, Send(f.inv(f.sessionEnv(), body, recipientID), SendOptions{Invite: "p"}), protocol.CodeInvalidInput, "relayed_opening")
		if perr.Details["field"] != "body" {
			t.Errorf("details = %v", perr.Details)
		}
	}
	if f.rec.count() != 0 {
		t.Errorf("%d children spawned for a refused invitation", f.rec.count())
	}
	f.rec.on("session list", okAnswer(listResult())).on("message send", okAnswer(sendResultJSON("m1", false)))
	exact := long[1:]
	if err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{Invite: exact}); err != nil {
		t.Fatalf("control: a plan name that fills the summary exactly must pass: %v", err)
	}
}

// TestSendInviteNeverToAGateway: a gateway forwards to people or to an
// outside service, and only a coding session answers an invitation. The
// refusal comes from the roster's own fact, before the send.
func TestSendInviteNeverToAGateway(t *testing.T) {
	t.Parallel()
	const gw = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	rec := rosterRec(gw, "mail-gateway", "active", "accept")
	rec["harness"] = gateway.HarnessEmail
	f := newFixture(t)
	f.rec.on("session list", okAnswer(rosterJSON(false, rec)))
	perr := wantCodeErr(t, Send(f.inv(f.sessionEnv(), "body", "eeeee"), SendOptions{Invite: "p"}), protocol.CodeInvalidInput, "gateway")
	if perr.Details["field"] != "invite" {
		t.Errorf("details = %v", perr.Details)
	}
	if n := len(f.rec.specsOf("message send")); n != 0 {
		t.Errorf("%d sends spawned for an invitation to a gateway", n)
	}
	// Control: the same session takes an ordinary message.
	f.rec.on("session list", okAnswer(rosterJSON(false, rec))).on("message send", okAnswer(sendResultTo("m-g", gw, false)))
	if err := Send(f.inv(f.sessionEnv(), "to: qa@example.com\nhello", "eeeee"), SendOptions{}); err != nil {
		t.Fatalf("an ordinary message to a gateway: %v", err)
	}
}

// TestSendInviteSaysWhenThePluginIsOlder: a recipient the roster shows on a
// release older than InvitationSince reads the invitation as an ordinary
// message, and the sender is told. Nothing is said for a recipient on that
// release or a later one, for one that reports no version, or for a build
// that is not a release.
func TestSendInviteSaysWhenThePluginIsOlder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		version string
		older   bool
	}{
		{"0.48.0", true},
		{"0.9.12", true},
		{InvitationSince, false},
		{"0.49.1", false},
		{"0.100.0", false},
		{"1.0.0", false},
		{"", false},
		{"0.48.0-dev", false},
		{"0.48", false},
		{"v0.48.0", false},
		{"00.48.0", false},
	} {
		t.Run("version "+tc.version, func(t *testing.T) {
			t.Parallel()
			rec := rosterRec(recipientID, "billing", "idle", "accept")
			if tc.version != "" {
				rec["brigade_version"] = tc.version
			}
			f := newFixture(t)
			f.rec.on("session list", okAnswer(rosterJSON(false, rec)))
			f.rec.on("message send", okAnswer(sendResultJSON("m-v", false)), okAnswer(sendResultJSON("m-v", true)))
			if err := Send(f.inv(f.sessionEnv(), "body", "bbbbb"), SendOptions{Invite: "p"}); err != nil {
				t.Fatalf("send: %v", err)
			}
			line := "invitation: the recipient's plugin reports " + tc.version + ", older than " + InvitationSince
			if got := strings.Contains(f.out.String(), line); got != tc.older {
				t.Errorf("the older-plugin line is present = %v, want %v:\n%s", got, tc.older, f.out.String())
			}
			f.out.Reset()
			inv := f.inv(f.sessionEnv(), "body", "bbbbb")
			inv.JSON = true
			if err := Send(inv, SendOptions{Invite: "p"}); err != nil {
				t.Fatalf("send --json: %v", err)
			}
			_, result := envelopeOf(t, f.out.String())
			got, _ := result["invitation"].(map[string]any)
			if v, has := got["older_plugin"]; has != tc.older || (tc.older && v != tc.version) {
				t.Errorf("older_plugin = %v (present %v), want present %v", v, has, tc.older)
			}
		})
	}
	if _, ok := releaseNumbers(InvitationSince); !ok {
		t.Fatalf("InvitationSince %q is not a release version", InvitationSince)
	}
}

// TestSendInviteSaysWhenTheRosterWasNotRead: the gateway refusal and the
// older-plugin line both come from the roster. When it could not be read, or
// does not list the recipient, the confirmation says the two went unchecked
// instead of promising an answer.
func TestSendInviteSaysWhenTheRosterWasNotRead(t *testing.T) {
	t.Parallel()
	for name, roster := range map[string]answer{
		"the roster read failed":         failAnswer(protocol.CodeUnavailable, "down"),
		"the roster lacks the recipient": okAnswer(rosterJSON(true, rosterRec("ffffffffffffffffffffffffffffffff", "other", "idle", "accept"))),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.rec.on("session list", roster, roster)
			f.rec.on("message send", okAnswer(sendResultJSON("m-u", false)), okAnswer(sendResultJSON("m-u", true)))
			if err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{Invite: "p"}); err != nil {
				t.Fatalf("send: %v", err)
			}
			want := "accepted: message m-u to " + recipientID + ". " + AcceptedNote + "\n" +
				"invitation: plan \"p\"; " + InvitationNote + "\n" +
				"invitation: " + InvitationUnchecked + "\n"
			if f.out.String() != want {
				t.Errorf("stdout:\n got %q\nwant %q", f.out.String(), want)
			}
			f.out.Reset()
			inv := f.inv(f.sessionEnv(), "body", recipientID)
			inv.JSON = true
			if err := Send(inv, SendOptions{Invite: "p"}); err != nil {
				t.Fatalf("send --json: %v", err)
			}
			_, result := envelopeOf(t, f.out.String())
			if got, _ := result["invitation"].(map[string]any); got["unchecked"] != true {
				t.Errorf("invitation = %v, want unchecked", result["invitation"])
			}
		})
	}
}

// TestInvitationSinceIsTheReleaseThatCarriesIt holds InvitationSince to the
// changelog: while the entry is under [Unreleased] the constant must be
// newer than the release the plugin pins, and once the entry's section is
// named, the constant is that section's version. A release under another
// number fails here until the constant follows.
func TestInvitationSinceIsTheReleaseThatCarriesIt(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "..")
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	const entry = "- **A session answers a plan invitation for itself** (card 242)."
	section := ""
	found := false
	for _, line := range strings.Split(string(changelog), "\n") {
		if rest, ok := strings.CutPrefix(line, "## ["); ok {
			section, _, _ = strings.Cut(rest, "]")
		}
		if strings.HasPrefix(line, entry) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("CHANGELOG.md has no entry starting %q", entry)
	}
	since, _ := releaseNumbers(InvitationSince)
	if section != "Unreleased" {
		if section != InvitationSince {
			t.Errorf("the changelog carries plan invitations in [%s], and InvitationSince is %s", section, InvitationSince)
		}
		return
	}
	pinned, err := os.ReadFile(filepath.Join(root, "plugin", "bin", "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	have, ok := releaseNumbers(strings.TrimSpace(string(pinned)))
	if !ok {
		t.Fatalf("plugin/bin/VERSION %q is not a release version", pinned)
	}
	if slices.Compare(have[:], since[:]) >= 0 {
		t.Errorf("plan invitations are unreleased, the plugin pins %s, and InvitationSince is %s: it must name the release still to come",
			strings.TrimSpace(string(pinned)), InvitationSince)
	}
}
