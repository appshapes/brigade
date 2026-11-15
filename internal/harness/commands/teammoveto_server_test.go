//go:build !windows

package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
)

// `brigade team move --to` from a team that is on a Brigade server, and a
// team that comes back to a server it left (work item 341, ADR 0010). The
// backend a team leaves is whichever keeps the record of where a team
// went, and from 0.57.0 that is a server too. The move through the built
// binary and two servers is the e2e package's
// (TestATeamMovesFromOneServerToAnotherByOneCommandThroughTheRealBinary);
// these tests hold what the command says and asks, and in what order.

// moveOldPin is the certificate pin of the server the team leaves.
const moveOldPin = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

// fromAServer makes the rig's team one that is on a Brigade server: the
// team file names it with its pin, and the machine is joined there.
func (r *moveToRig) fromAServer() {
	r.t.Helper()
	cfg := r.f.dirs.BrigadeConfig
	if err := os.RemoveAll(filepath.Join(cfg, "teams", r.oldKey)); err != nil {
		r.t.Fatal(err)
	}
	r.oldHost = strings.Replace(r.oldHost, ".supabase.co", ".example.test:8743", 1)
	r.oldURL = "https://" + r.oldHost
	r.oldKey = teamstore.Key("rest", r.oldURL, setupTeamRef)
	r.original = "{\n" +
		"  \"version\": 1,\n" +
		"  \"adapter\": \"rest\",\n" +
		"  \"url\": \"" + r.oldURL + "\",\n" +
		"  \"certificate\": \"" + moveOldPin + "\",\n" +
		"  \"team_ref\": \"" + setupTeamRef + "\",\n" +
		"  \"team_name\": \"devs\",\n" +
		"  \"sync\": { \"folders\": [\".brigade\"] },\n" +
		"  \"later\": true\n" +
		"}\n"
	r.writeFile(r.original)
	if err := write.EnsureBinding(cfg, r.oldKey, "rest", r.oldURL, "", moveOldPin, setupTeamRef, "devs", "p_old"); err != nil {
		r.t.Fatal(err)
	}
	r.setLabel(moveToLabel)
	canon, err := teamfile.Canonicalize(r.top)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := write.Pin(cfg, canon, teamstore.Pin{
		Adapter: "rest", URL: r.oldURL, Certificate: moveOldPin, TeamRef: setupTeamRef, ConsentedAt: fixtureNow,
	}); err != nil {
		r.t.Fatal(err)
	}
}

// asksOf lists the commands asked of one end, `old:` or `new:`, in order.
func (r *moveToRig) asksOf(end string) []string {
	var out []string
	for _, asked := range r.order {
		if verb, ok := strings.CutPrefix(asked, end); ok {
			out = append(out, verb)
		}
	}
	return out
}

// TestTeamMoveToMovesATeamFromOneServerToAnother: the same command, the
// same steps, with a server as the backend the team leaves. The team file
// keeps its adapter and gets the new server's address and pin; the record
// is asked of the old server and names the new one; a session on a plugin
// older than 0.57.0 is named as one that will not follow, where from any
// other backend the line is drawn at 0.55.0; and what is left says that
// the old server keeps the team as it is, and what its gateways go on
// doing.
func TestTeamMoveToMovesATeamFromOneServerToAnother(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	r.fromAServer()
	r.original = strings.Replace(r.original, "  \"later\": true\n", "  \"gateway\": {\"email\": \"ops@example.com\"},\n  \"later\": true\n", 1)
	r.writeFile(r.original)
	// The roster of the old server: 0.55.0 and 0.56.0 follow a move from a
	// Supabase project and not one from a server.
	roster := strings.Replace(r.sessions(), `"0.9.1"`, `"0.56.0"`, 1)
	roster = strings.Replace(roster, `"1.0.0"`, `"0.57.0"`, 1)
	r.f.rec.on("session list", okAnswer(roster))
	if err := Team(r.inv(true, r.args()...)); err != nil {
		t.Fatal(err)
	}
	if len(r.prompts) != 1 || !strings.Contains(r.prompts[0], "move team \"devs\"") || !strings.Contains(r.prompts[0], "from "+r.oldHost+" to "+r.newHost) ||
		!strings.Contains(r.prompts[0], "4 sessions that are online run a plugin older than 0.57.0") {
		t.Fatalf("the confirmation: %v", r.prompts)
	}
	for _, named := range []string{"billing (bob@example.com, 0.54.9)", "ci (", "current (dana@example.com, 0.55.0)", "dev-build (erin@example.com, 0.55.0-dev)"} {
		if !strings.Contains(r.prompts[0], named) {
			t.Errorf("the confirmation does not name %q as a session that will not follow: %s", named, r.prompts[0])
		}
	}
	if strings.Contains(r.prompts[0], "future (") {
		t.Errorf("a session on 0.57.0 is named as one that will not follow: %s", r.prompts[0])
	}
	// The order: the old server is asked whether it holds a record and who
	// may write it and for its roster, the new one takes the team in and is
	// asked whether it records that the team left it, opens the move, and
	// only then is the old server asked to record.
	want := []string{
		"old:team moved", "old:team move", "old:session list",
		"new:team take-in", "new:team moved", "new:team open-move", "old:team move",
	}
	if !slices.Equal(r.order, want) {
		t.Fatalf("adapter commands: %v, want %v", r.order, want)
	}
	// The team file keeps the adapter and names the new server and its pin.
	if got := r.fileNow(); got != strings.NewReplacer(r.oldURL, r.newURL, moveOldPin, moveNewPin).Replace(r.original) {
		t.Errorf("the team file after the move:\n%s", got)
	}
	if pin := r.pin(); pin.Adapter != "rest" || pin.URL != r.newURL || pin.Certificate != moveNewPin || pin.MovedFrom != "" {
		t.Errorf("the pin after the move: %+v", pin)
	}
	// The membership on the old server is left where it is: a record there
	// is read and cleared from this machine afterwards.
	if _, err := teamstore.LoadBinding(r.f.dirs.BrigadeConfig, r.oldKey); err != nil {
		t.Errorf("the membership on the server the team left: %v", err)
	}
	out := r.f.out.String()
	for _, need := range []string{
		"recorded on " + r.oldHost + ": team \"devs\"",
		"4 sessions that are online run a plugin older than 0.57.0 and will not follow until their members update it",
		"the mail and Slack gateways are the server operator's to configure on " + r.newHost,
		"the ones on " + r.oldHost + " go on carrying mail and Slack to the team there until its operator takes the team out of that server's gateway configuration",
		r.oldHost + " keeps the team as it is: nothing there was ended, and a member who has not followed still reaches it",
		"to undo: `brigade team move --clear` stops " + r.oldHost + " from saying where the team went",
	} {
		if !strings.Contains(out, need) {
			t.Errorf("the lines lack %q:\n%s", need, out)
		}
	}
	if strings.Contains(out, "cleared the record on") {
		t.Errorf("a server that held no record of the team is said to have had one cleared:\n%s", out)
	}
	r.noSecrets()

	// A team that moved before: the machine holds a membership on the
	// backend the team was on before this one as well. The record's own
	// forms would then ask which backend is meant, so the undo names it.
	again := newMoveToRig(t)
	again.fromAServer()
	earlier := "https://earlier.example.test:8743"
	if err := write.EnsureBinding(again.f.dirs.BrigadeConfig, teamstore.Key("rest", earlier, setupTeamRef), "rest", earlier, "", "", setupTeamRef, "devs", "p_earlier"); err != nil {
		t.Fatal(err)
	}
	if err := Team(again.inv(true, again.args()...)); err != nil {
		t.Fatal(err)
	}
	if need := "to undo: `brigade team move --clear --from " + again.oldHost + "` stops " + again.oldHost + " from saying where the team went"; !strings.Contains(again.f.out.String(), need) {
		t.Errorf("the undo of a team that moved before lacks %q:\n%s", need, again.f.out.String())
	}
}

// TestTeamMoveToFromAServerRefusesBeforeTheNewServerIsAsked: the two
// refusals of the server a team leaves, each before the new server is
// asked anything and before the join secret is sent. A member that does
// not own the team there is told that the owner moves it, in a server's
// words: a team on a server has an owner, which a transfer changes. A
// server that keeps no record of where a team went, one older than 0.8.0,
// is named with the version its operator updates it to.
func TestTeamMoveToFromAServerRefusesBeforeTheNewServerIsAsked(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		refusal answer
		code    protocol.Code
		reason  string
		says    []string
	}{
		"a member that does not own the team": {
			failAnswer(protocol.CodeUnauthorized, "not a member of that team"), protocol.CodeUnauthorized, reasonNotTheCreator,
			[]string{"only the owner of this team on ", "its owner's to record there", "have the owner transfer the team to you first", "Nothing was changed"},
		},
		"a server older than 0.8.0": {
			answer{errObj: &protocol.ErrorObject{
				Code: protocol.CodeConfig, Message: "the server this team is on keeps no record of where a team goes",
				Details: map[string]string{"reason": reasonServerLacksMove},
			}}, protocol.CodeConfig, reasonServerLacksMove,
			[]string{"keeps no record of where a team goes: it is older than 0.8.0", "Its operator updates it to 0.8.0 or later", "joins there with the secret file", "Nothing was changed"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			r.fromAServer()
			// The old server reads as holding no record, and refuses the clear
			// that asks whether this member may write one.
			r.f.rec.on("team move", tc.refusal)
			storeBefore := storeHash(t, r.f.dirs.BrigadeConfig)
			perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), tc.code, tc.reason)
			for _, need := range append(tc.says, r.oldHost) {
				if !strings.Contains(perr.Message, need) {
					t.Errorf("the refusal lacks %q: %s", need, perr.Message)
				}
			}
			if strings.Contains(perr.Message, "who created") {
				t.Errorf("the refusal speaks of a team's creator, which a team on a server need not be owned by: %s", perr.Message)
			}
			if asked := r.asksOf("new:"); len(asked) != 0 {
				t.Errorf("the new server was asked %v before the old one answered who may move the team", asked)
			}
			if len(r.prompts) != 0 {
				t.Errorf("a refused move asked for a confirmation: %v", r.prompts)
			}
			if got := r.fileNow(); got != r.original {
				t.Errorf("a refused move rewrote the team file:\n%s", got)
			}
			if pin := r.pin(); pin.URL != r.oldURL {
				t.Errorf("a refused move rewrote the pin: %+v", pin)
			}
			if got := storeHash(t, r.f.dirs.BrigadeConfig); got != storeBefore {
				t.Errorf("a refused move wrote the store")
			}
			r.noSecrets()
		})
	}
}

// TestTeamMoveToClearsTheRecordOfATeamThatComesBack: a team moved from a
// server to the backend it is on now, and moves back. The server it
// returns to still records that the team left it. That record is cleared
// once the server has answered that this member owns the team there, and
// before the backend the team leaves is asked to record anything: so the
// two never name each other. The question says what will be done, the
// lines and the --json result say that it was, and a server that holds no
// such record has nothing cleared.
func TestTeamMoveToClearsTheRecordOfATeamThatComesBack(t *testing.T) {
	t.Parallel()
	for _, jsonOut := range []bool{false, true} {
		r := newMoveToRig(t)
		r.fromAServer()
		// This machine was a member of the team on the server before, and
		// owns it there: the take-in is a join that still owns.
		if err := write.EnsureBinding(r.f.dirs.BrigadeConfig, r.newKey, "rest", r.newURL, "", moveNewPin, setupTeamRef, "devs", "p_new"); err != nil {
			t.Fatal(err)
		}
		r.f.rec.on("team take-in", okAnswer(r.takenIn(false, true)))
		// What the server records: that the team left it for the old one.
		left := `{"team_ref":"` + setupTeamRef + `","moved":true,"adapter":"rest","url":"` + r.oldURL + `","certificate":"` + moveOldPin + `","recorded_at":"2026-11-10T12:00:00Z"}`
		r.there.on("team moved", okAnswer(left))
		iv := r.inv(true, r.args()...)
		iv.JSON = jsonOut
		if err := Team(iv); err != nil {
			t.Fatal(err)
		}
		if len(r.prompts) != 1 || !strings.Contains(r.prompts[0], "This machine has joined the team on "+r.newHost+" before: if "+r.newHost+
			" records that the team left it, that record is cleared first, because the team is there again.") {
			t.Fatalf("the confirmation does not say what is done about a record on the server: %v", r.prompts)
		}
		want := []string{
			"old:team moved", "old:team move", "old:session list",
			"new:team take-in", "new:team moved", "new:team move", "new:team open-move", "old:team move",
		}
		if !slices.Equal(r.order, want) {
			t.Fatalf("adapter commands: %v, want %v", r.order, want)
		}
		// The clear is a clear, and it carries no ticket.
		cleared := r.there.specsOf("team move")
		if len(cleared) != 1 || !slices.Contains(cleared[0].Argv, "--clear") || slices.Contains(cleared[0].Argv, "--ticket-file") {
			t.Fatalf("what the server was asked about its own record: %v", cleared)
		}
		if read := r.there.specsOf("team moved"); len(read) != 1 || slices.Contains(read[0].Argv, "--ticket-file") {
			t.Fatalf("the server's own record was read with its ticket: %v", read)
		}
		out := r.f.out.String()
		if jsonOut {
			ok, result := envelopeOf(t, out)
			if !ok || result["destination_record_cleared"] != true {
				t.Fatalf("the result does not say that the server's record was cleared: %v", result)
			}
		} else if need := "cleared the record on " + r.newHost + " that the team had left it for " + r.oldHost; !strings.Contains(out, need) || !strings.Contains(out, "the team is on "+r.newHost+" again") {
			t.Errorf("the lines lack %q:\n%s", need, out)
		}
		r.noSecrets()
	}

	// A server that holds no record of the team is asked once, and nothing
	// is cleared: the --json result says so.
	r := newMoveToRig(t)
	iv := r.inv(true, r.args()...)
	iv.JSON = true
	if err := Team(iv); err != nil {
		t.Fatal(err)
	}
	if asked := r.asksOf("new:"); !slices.Equal(asked, []string{"team take-in", "team moved", "team open-move"}) {
		t.Errorf("the server was asked %v", asked)
	}
	if ok, result := envelopeOf(t, r.f.out.String()); !ok || result["destination_record_cleared"] != false {
		t.Errorf("the result of a move to a server that held no record: %v", result)
	}
	if strings.Contains(r.prompts[0], "has joined the team on") {
		t.Errorf("a machine that never joined the server is told of a record there: %s", r.prompts[0])
	}
}

// TestTeamMoveToStopsWhenTheServersOwnRecordIsNotCleared: the clear of
// the server's own record is refused or fails, or the record cannot be
// read. The move stops there: no move is opened, the backend the team
// leaves is asked to record nothing and holds what it held, the checkout
// is unwritten, and the refusal says which server holds what and that the
// same command finishes the move.
func TestTeamMoveToStopsWhenTheServersOwnRecordIsNotCleared(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		read    answer
		clear   answer
		code    protocol.Code
		says    string
		cleared int
	}{
		"the clear is refused": {
			clear: failAnswer(protocol.CodeUnauthorized, "not a member of that team"), code: protocol.CodeUnauthorized,
			says: " still records that the team left it for ", cleared: 1,
		},
		// A clear that failed in a way that may have reached the server: the
		// record may be cleared all the same, and the refusal says so and
		// where it is read (work item 341).
		"the clear fails": {
			clear: failAnswer(protocol.CodeUnavailable, "the backend could not be reached"), code: protocol.CodeUnavailable,
			says: "the answer to the clear of that record did not arrive, so the record may be cleared all the same (`brigade team move --show --from ", cleared: 1,
		},
		"the clear is answered a record": {
			clear: okAnswer(`{"team_ref":"` + setupTeamRef + `","moved":true,"adapter":"rest","url":"https://x.example","recorded_at":"2026-11-10T12:00:00Z"}`),
			code:  protocol.CodeProtocolMismatch, says: " still records that the team left it for ", cleared: 1,
		},
		"the record cannot be read": {
			read: failAnswer(protocol.CodeUnavailable, "the backend could not be reached"), code: protocol.CodeUnavailable,
			says: "whether ", cleared: 0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			r.fromAServer()
			left := `{"team_ref":"` + setupTeamRef + `","moved":true,"adapter":"rest","url":"` + r.oldURL + `","certificate":"` + moveOldPin + `","recorded_at":"2026-11-10T12:00:00Z"}`
			r.there.on("team moved", okAnswer(left))
			if tc.read != (answer{}) {
				r.there.on("team moved", tc.read)
			}
			if tc.clear != (answer{}) {
				r.there.on("team move", tc.clear)
			}
			perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), tc.code, reasonReturnNotCleared)
			for _, need := range []string{
				"the team is taken in on " + r.newHost, tc.says, "Its move was not opened",
				r.oldHost + " was asked to record nothing: it holds what it held, and no session follows this move",
				"Run the same command again to finish the move",
			} {
				if !strings.Contains(perr.Message, need) {
					t.Errorf("the refusal lacks %q: %s", need, perr.Message)
				}
			}
			if got := len(r.there.specsOf("team move")); got != tc.cleared {
				t.Errorf("the server was asked to clear %d times, want %d", got, tc.cleared)
			}
			// The old server was asked the one clear of the checks, which
			// changes nothing, and no record; and no move was opened.
			if asked := r.asksOf("old:"); !slices.Equal(asked, []string{"team moved", "team move", "session list"}) {
				t.Errorf("the backend the team leaves was asked %v", asked)
			}
			for _, spec := range r.f.rec.specsOf("team move") {
				if slices.Contains(spec.Argv, "--ticket-file") {
					t.Errorf("a move whose server could not clear its own record was recorded")
				}
			}
			if slices.Contains(r.order, "new:team open-move") {
				t.Errorf("a move whose server could not clear its own record was opened: %v", r.order)
			}
			if got := r.fileNow(); got != r.original {
				t.Errorf("the team file was rewritten:\n%s", got)
			}
			if pin := r.pin(); pin.URL != r.oldURL {
				t.Errorf("the pin was rewritten: %+v", pin)
			}
			if _, err := os.Lstat(r.ticketFile); err == nil {
				t.Errorf("a ticket file was left")
			}
			r.noSecrets()
		})
	}
}

// TestTeamMoveToSaysThatTheServersRecordIsGoneWhenALaterStepFails (work
// item 341): the server's own record of the team's earlier move is cleared
// before the backend the team leaves records anything, and a step that
// fails after that does not put it back. The stop says so: a member who
// had not followed that earlier move follows nothing until this one is
// finished, and the administrator is the one who can know why.
func TestTeamMoveToSaysThatTheServersRecordIsGoneWhenALaterStepFails(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	r.fromAServer()
	if err := write.EnsureBinding(r.f.dirs.BrigadeConfig, r.newKey, "rest", r.newURL, "", moveNewPin, setupTeamRef, "devs", "p_new"); err != nil {
		t.Fatal(err)
	}
	r.f.rec.on("team take-in", okAnswer(r.takenIn(false, true)))
	left := `{"team_ref":"` + setupTeamRef + `","moved":true,"adapter":"rest","url":"` + r.oldURL + `","certificate":"` + moveOldPin + `","recorded_at":"2026-11-10T12:00:00Z"}`
	r.there.on("team moved", okAnswer(left))
	r.f.rec.on("team open-move", failAnswer(protocol.CodeUnavailable, "the backend could not be reached"))
	perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, "")
	for _, need := range []string{
		"the team is taken in on " + r.newHost + ", and its move was not opened",
		"the record " + r.newHost + " held that the team had left it for " + r.oldHost,
		"is cleared, so a member who had not followed that earlier move does not follow it now",
		"Run the same command again to finish the move",
	} {
		if !strings.Contains(perr.Message, need) {
			t.Errorf("the stop lacks %q: %s", need, perr.Message)
		}
	}
	// A stop that cleared nothing says nothing of a record.
	r = newMoveToRig(t)
	r.f.rec.on("team open-move", failAnswer(protocol.CodeUnavailable, "the backend could not be reached"))
	if perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, ""); strings.Contains(perr.Message, "is cleared") {
		t.Errorf("a stop that cleared no record speaks of one: %s", perr.Message)
	}
}

// TestTeamMoveToKnowsTheServerTheTeamIsOnByAnotherSpelling (work item
// 341): an address that is trivially the one the team file carries (the
// host in another case, the scheme's own port, a slash at the end) is that
// server. Until this it was taken for another: the server was asked to
// take the team in under a second profile, which made the administrator a
// second member of the team on it.
func TestTeamMoveToKnowsTheServerTheTeamIsOnByAnotherSpelling(t *testing.T) {
	t.Parallel()
	for _, spell := range []func(r *moveToRig) string{
		func(r *moveToRig) string { return strings.ToUpper(r.oldURL[:8]) + strings.ToUpper(r.oldHost) + "/" },
		func(r *moveToRig) string { return r.oldURL + "//" },
	} {
		r := newMoveToRig(t)
		r.fromAServer()
		to := spell(r)
		args := []string{"--to", to, "--secret-file", r.secretFile, "--certificate", moveOldPin}
		perr := wantCodeErr(t, Team(r.inv(true, args...)), protocol.CodeConfig, "no_move")
		if !strings.Contains(perr.Message, "names that server already") || len(r.order) != 0 {
			t.Errorf("--to %s: %s; asked %v", to, perr.Message, r.order)
		}
		if typed := strings.TrimRight(to, "/"); typed != r.oldURL {
			if _, err := os.Lstat(filepath.Join(r.f.dirs.BrigadeConfig, "teams", teamstore.Key("rest", typed, setupTeamRef))); err == nil {
				t.Errorf("--to %s left a profile under that spelling", to)
			}
		}
	}
}

// TestALinePrintedToBeRunNamesTheServerWithItsCertificate: a refusal that
// prints a `team move --to` line for its reader to run names the server as
// the command has to be given it: with `--certificate` and the pin where
// the server made its own certificate. Until this the line carried the
// address alone, and run as printed it named a server with no pin: the
// rerun was refused for a `--from` that the team file did not name, and
// the move back to an earlier server did not trust the server.
func TestALinePrintedToBeRunNamesTheServerWithItsCertificate(t *testing.T) {
	t.Parallel()
	if got := toServer("https://brigade.example.test:8743", moveOldPin); got != "https://brigade.example.test:8743 --certificate "+moveOldPin {
		t.Errorf("a server with a pin: %q", got)
	}
	if got := toServer("https://brigade.example.test:8743", ""); got != "https://brigade.example.test:8743" {
		t.Errorf("a server with no pin: %q", got)
	}
}

// TestTeamMoveToRefusesTheServerTheTeamIsOnWhateverTheCertificate (work
// item 341): the team file names a server with a certificate's pin, and
// the one command names the same address with no pin or with another.
// Until this the file did not count as naming the server already, the
// backend the team "leaves" was the very membership on that server, the
// profile was repointed to the pin that was typed, a move was opened, and
// the server recorded that the team had moved to itself. It is refused
// before anything is asked or changed: a server's new certificate is a
// change of the team file and a join, not a move.
func TestTeamMoveToRefusesTheServerTheTeamIsOnWhateverTheCertificate(t *testing.T) {
	t.Parallel()
	const another = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	for name, extra := range map[string][]string{
		"no certificate":      nil,
		"another certificate": {"--certificate", another},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			r.fromAServer()
			before, err := teamstore.LoadBinding(r.f.dirs.BrigadeConfig, r.oldKey)
			if err != nil {
				t.Fatal(err)
			}
			args := append([]string{"--to", r.oldURL, "--secret-file", r.secretFile}, extra...)
			perr := wantCodeErr(t, Team(r.inv(true, args...)), protocol.CodeConfig, "no_move")
			for _, need := range []string{"is the server this team is on", "`certificate`", "brigade team join", "not a move", "Nothing was changed"} {
				if !strings.Contains(perr.Message, need) {
					t.Errorf("the refusal lacks %q: %s", need, perr.Message)
				}
			}
			if len(r.order) != 0 || len(r.prompts) != 0 {
				t.Errorf("a move onto the server the team is on asked %v and confirmed %d times", r.order, len(r.prompts))
			}
			if now, err := teamstore.LoadBinding(r.f.dirs.BrigadeConfig, r.oldKey); err != nil || now.Certificate != before.Certificate || now.URL != before.URL {
				t.Errorf("the membership on the server was repointed: %+v, %v", now, err)
			}
			if got := r.fileNow(); got != r.original {
				t.Errorf("the team file was rewritten:\n%s", got)
			}
		})
	}
}

// TestARerunTakesTheCheckoutsBackendOnlyWhereNoOtherRecordsTheMove (work
// item 341): the choice a command that is run again makes among the
// backends this machine has joined the team on before, from the one the
// checkout names and the ones whose record names the server. A team went
// from A to C, from C to B and from B to C: A and B both record a move to
// C. Until this the checkout's backend was taken whenever its own record
// named the server, so in a checkout left pinned to A the command took A
// for the backend the team is leaving: it opened the move again, which
// replaced the ticket B's record hands out, and recorded on A, and a member
// on B was refused until the owner ran it again with `--from B`. The
// checkout's backend is taken only where it alone records the move, or
// where none does. Where another records it too, nothing on this machine
// tells the checkout the team is leaving from one left behind: the command
// refuses, as it does where the checkout says one thing and only a record
// another.
func TestARerunTakesTheCheckoutsBackendOnlyWhereNoOtherRecordsTheMove(t *testing.T) {
	t.Parallel()
	const a, b, c = 0, 1, 2
	for _, tc := range []struct {
		name     string
		own      int
		recorded []int
		want     rerunStand
	}{
		{"the pin names B and only B records the move", b, []int{b}, rerunOwn},
		{"the pin names B and nothing records the move", b, nil, rerunOwn},
		{"the pin names B and only A records the move", b, []int{a}, rerunAnother},
		{"the pin names A, left behind, and A and B both record the move", a, []int{a, b}, rerunOwnAndAnother},
		{"the pin names B, and A and B both record the move", b, []int{a, b}, rerunOwnAndAnother},
		{"the pin names A, and all three record the move", a, []int{a, b, c}, rerunOwnAndAnother},
		{"the pin names C, and A and B record the move", c, []int{a, b}, rerunAnother},
		{"the checkout names none and one records the move", -1, []int{a}, rerunRecordAlone},
		{"the checkout names none and two record the move", -1, []int{a, b}, rerunRecordAlone},
		{"the checkout names none and nothing records the move", -1, nil, rerunNothing},
	} {
		if got := rerunStandOf(tc.own, tc.recorded); got != tc.want {
			t.Errorf("%s: %d, want %d (%d is the checkout's backend taken)", tc.name, got, tc.want, rerunOwn)
		}
	}
}

// TestTheCommandsAMovePrintsNameOneBackendWhereAHostNamesTwo (work item
// 341): a command the move prints for its reader to run names a backend by
// its host after `--from`. Two servers on one machine, or behind one front
// under two paths, share a host: the host then names both, and the command
// was refused as printed. Where the host names another backend this
// machine has joined the team on, the printed command carries the whole
// address, which names one.
func TestTheCommandsAMovePrintsNameOneBackendWhereAHostNamesTwo(t *testing.T) {
	t.Parallel()
	beside := func(r *moveToRig, url string) {
		t.Helper()
		if err := write.EnsureBinding(r.f.dirs.BrigadeConfig, teamstore.Key("rest", url, setupTeamRef), "rest", url, "", "", setupTeamRef, "devs", "p_beside"); err != nil {
			t.Fatal(err)
		}
	}

	// The undo: the server the team leaves shares its host with another the
	// machine has joined the team on.
	r := newMoveToRig(t)
	r.fromAServer()
	beside(r, r.oldURL+"/second")
	if err := Team(r.inv(true, r.args()...)); err != nil {
		t.Fatal(err)
	}
	if need := "to undo: `brigade team move --clear --from " + r.oldURL + "` stops " + r.oldHost + " from saying where the team went"; !strings.Contains(r.f.out.String(), need) {
		t.Errorf("the undo where the host names two backends of the team lacks %q:\n%s", need, r.f.out.String())
	}

	// The stop that says where the server's own record is read: the server
	// the team returns to shares its host with another.
	r = newMoveToRig(t)
	r.fromAServer()
	beside(r, r.newURL+"/second")
	left := `{"team_ref":"` + setupTeamRef + `","moved":true,"adapter":"rest","url":"` + r.oldURL + `","certificate":"` + moveOldPin + `","recorded_at":"2026-11-10T12:00:00Z"}`
	r.there.on("team moved", okAnswer(left))
	r.there.on("team move", failAnswer(protocol.CodeUnavailable, "the backend could not be reached"))
	perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, reasonReturnNotCleared)
	if need := "(`brigade team move --show --from " + r.newURL + "` says)"; !strings.Contains(perr.Message, need) {
		t.Errorf("the stop where the host names two backends of the team lacks %q: %s", need, perr.Message)
	}

	// The same command run again, which names no backend by itself and
	// prints the command for each: one of the backends the team may be
	// leaving shares its host with the server the team moves to, and with no
	// other. The host then names that backend and the server.
	r = newMoveToRig(t)
	r.fromAServer()
	beside(r, r.newURL+"/second")
	if err := write.EnsureBinding(r.f.dirs.BrigadeConfig, r.newKey, "rest", r.newURL, "", moveNewPin, setupTeamRef, "devs", "p_new"); err != nil {
		t.Fatal(err)
	}
	r.writeFile(strings.NewReplacer(r.oldURL, r.newURL, moveOldPin, moveNewPin).Replace(r.original))
	canon, err := teamfile.Canonicalize(r.top)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Pin(r.f.dirs.BrigadeConfig, canon, teamstore.Pin{Adapter: "rest", URL: r.newURL, Certificate: moveNewPin, TeamRef: setupTeamRef, ConsentedAt: fixtureNow}); err != nil {
		t.Fatal(err)
	}
	perr = wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeConfig, "team_ambiguous")
	for _, need := range []string{"--from " + r.newURL + "/second`", "--from " + r.oldHost + "`"} {
		if !strings.Contains(perr.Message, need) {
			t.Errorf("the refusal of a command that is run again lacks %q: %s", need, perr.Message)
		}
	}
}

// TestARerunsRefusalSpeaksOfSeveralBackendsInThePlural (work item 341): the
// refusal of a command that is run again names the backends that record
// the move, or that did not answer. With several of them it said "A, B
// records a move" and "A, B did not answer what it records": the verb and
// the pronoun follow the number now, and the names are joined as a
// sentence joins them.
func TestARerunsRefusalSpeaksOfSeveralBackendsInThePlural(t *testing.T) {
	t.Parallel()
	const earlier, third = "earlier.example.test:8743", "third.example.test:8743"
	// rerun is a machine that has joined the team on the server the file
	// names already and on the backends named, beside the one the team left;
	// pinned says whether the checkout still names that one.
	rerun := func(pinned bool, others ...string) *moveToRig {
		t.Helper()
		r := newMoveToRig(t)
		r.fromAServer()
		cfg := r.f.dirs.BrigadeConfig
		for _, host := range others {
			if err := write.EnsureBinding(cfg, teamstore.Key("rest", "https://"+host, setupTeamRef), "rest", "https://"+host, "", "", setupTeamRef, "devs", "p_"+host); err != nil {
				t.Fatal(err)
			}
		}
		if err := write.EnsureBinding(cfg, r.newKey, "rest", r.newURL, "", moveNewPin, setupTeamRef, "devs", "p_new"); err != nil {
			t.Fatal(err)
		}
		r.writeFile(strings.NewReplacer(r.oldURL, r.newURL, moveOldPin, moveNewPin).Replace(r.original))
		if !pinned {
			canon, err := teamfile.Canonicalize(r.top)
			if err != nil {
				t.Fatal(err)
			}
			if err := write.Pin(cfg, canon, teamstore.Pin{Adapter: "rest", URL: r.newURL, Certificate: moveNewPin, TeamRef: setupTeamRef, ConsentedAt: fixtureNow}); err != nil {
				t.Fatal(err)
			}
		}
		return r
	}
	refused := func(r *moveToRig, code protocol.Code, needs ...string) {
		t.Helper()
		perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), code, "team_ambiguous")
		for _, need := range needs {
			if !strings.Contains(perr.Message, need) {
				t.Errorf("the refusal lacks %q: %s", need, perr.Message)
			}
		}
		for _, gone := range []string{" records a move to " + r.newHost + ", which says that the team once left it", "that records a move", "what it records"} {
			if strings.Contains(perr.Message, gone) {
				t.Errorf("the refusal speaks of several backends as of one, %q: %s", gone, perr.Message)
			}
		}
	}
	lost := failAnswer(protocol.CodeUnavailable, "the backend could not be reached")

	// The checkout names none, and two record the move.
	r := rerun(false, earlier)
	r.f.rec.on("team moved", okAnswer(r.recorded()))
	r.there.on("team moved", okAnswer(r.recorded()))
	refused(r, protocol.CodeConfig, earlier+" and "+r.oldHost+" each record a move to "+r.newHost+", which says that the team once left each of them for this server")

	// Two do not answer what they record.
	r = rerun(false, earlier)
	r.f.rec.on("team moved", lost)
	r.there.on("team moved", lost)
	refused(r, protocol.CodeUnavailable, earlier+" and "+r.oldHost+" did not answer what they record of this team", "when they answer")

	// The checkout names one that records nothing, and two others record the move.
	r = rerun(true, earlier, third)
	r.there.on("team moved", okAnswer(r.recorded()))
	refused(r, protocol.CodeConfig, "this checkout was joined to "+r.oldHost+", and it is "+earlier+" and "+third+" that record a move to "+r.newHost)

	// The checkout names one that records the move, and so do two others.
	r = rerun(true, earlier, third)
	r.f.rec.on("team moved", okAnswer(r.recorded()))
	r.there.on("team moved", okAnswer(r.recorded()))
	refused(r, protocol.CodeConfig, "this checkout was joined to "+r.oldHost+", which records a move to "+r.newHost+", and so do "+earlier+" and "+third)
}

// longPath is a path that makes a server's address longer than the 64
// code points a display value is cut at.
const longPath = "/the-teams-of-a-company-whose-servers-have-long-addresses/alpha"

// TestTheCommandsAMovePrintsCarryALongAddressWhole (work item 341): the
// address after `--from` and `--to` in a command the move prints went
// through the sanitiser of a short display value, which cuts at 64 code
// points: a server at a long address was printed cut, and the command was
// refused as printed. It is printed whole.
func TestTheCommandsAMovePrintsCarryALongAddressWhole(t *testing.T) {
	t.Parallel()
	// The same command run again: a backend the team may be leaving is at a
	// long address on the host of the server the team moves to.
	r := newMoveToRig(t)
	r.fromAServer()
	cfg, beside := r.f.dirs.BrigadeConfig, r.newURL+longPath
	for key, url := range map[string]string{teamstore.Key("rest", beside, setupTeamRef): beside, r.newKey: r.newURL} {
		if err := write.EnsureBinding(cfg, key, "rest", url, "", moveNewPin, setupTeamRef, "devs", "p_there"); err != nil {
			t.Fatal(err)
		}
	}
	r.writeFile(strings.NewReplacer(r.oldURL, r.newURL, moveOldPin, moveNewPin).Replace(r.original))
	canon, err := teamfile.Canonicalize(r.top)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Pin(cfg, canon, teamstore.Pin{Adapter: "rest", URL: r.newURL, Certificate: moveNewPin, TeamRef: setupTeamRef, ConsentedAt: fixtureNow}); err != nil {
		t.Fatal(err)
	}
	perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeConfig, "team_ambiguous")
	if need := "--secret-file <path> --from " + beside + "`"; len(beside) <= 64 || !strings.Contains(perr.Message, need) {
		t.Errorf("the refusal of a command that is run again lacks %q: %s", need, perr.Message)
	}

	// The stop that says where the server's own record is read: the server
	// the team returns to is at a long address, beside another on its host.
	r = newMoveToRig(t)
	r.fromAServer()
	short := r.newURL
	r.newURL = short + longPath
	if err := write.EnsureBinding(r.f.dirs.BrigadeConfig, r.newKey, "rest", short, "", moveNewPin, setupTeamRef, "devs", "p_beside"); err != nil {
		t.Fatal(err)
	}
	left := `{"team_ref":"` + setupTeamRef + `","moved":true,"adapter":"rest","url":"` + r.oldURL + `","certificate":"` + moveOldPin + `","recorded_at":"2026-11-10T12:00:00Z"}`
	r.there.on("team moved", okAnswer(left))
	r.there.on("team move", failAnswer(protocol.CodeUnavailable, "the backend could not be reached"))
	perr = wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, reasonReturnNotCleared)
	if need := "(`brigade team move --show --from " + r.newURL + "` says)"; !strings.Contains(perr.Message, need) {
		t.Errorf("the stop of a move to a server at a long address lacks %q: %s", need, perr.Message)
	}

	// The result a script reads: the address of the server the team left,
	// which was cut there too.
	r = newMoveToRig(t)
	r.oldHost = "old-" + strings.Repeat("a-long-label-", 5) + r.oldHost
	r.fromAServer()
	iv := r.inv(true, r.args()...)
	iv.JSON = true
	if err := Team(iv); err != nil {
		t.Fatal(err)
	}
	ok, result := envelopeOf(t, r.f.out.String())
	from, _ := result["from"].(map[string]any)
	if !ok || len(r.oldURL) <= 64 || from["url"] != r.oldURL {
		t.Errorf("the result's address of the server the team left: %v, want %s", from["url"], r.oldURL)
	}
}

// TestARerunLeavesOutABackendThatRefusesToSayWhatItRecords (work item 341):
// a command that is run again asks every backend this machine has joined
// the team on what it records. One that answers that the caller may not
// ask, `unauthorized` or `unauthenticated`, was listed with the ones that
// did not answer: "did not answer what it records … Run the same command
// again when it answers", which it never will. Such a backend is not the
// one the team is leaving, whose owner reads its record: it is no
// candidate. With one left the command goes on as it does on a machine
// that has joined the team on one other backend, and with none it refuses
// as it does where there is none. A backend that did not answer, and one
// that answered something that is no record, stay as they were: either may
// be the one.
func TestARerunLeavesOutABackendThatRefusesToSayWhatItRecords(t *testing.T) {
	t.Parallel()
	const earlier = "earlier.example.test:8743"
	// rerun is a machine that has joined the team on the server the file
	// names already, on the server it left and on one before that.
	rerun := func() *moveToRig {
		t.Helper()
		r := newMoveToRig(t)
		r.fromAServer()
		cfg := r.f.dirs.BrigadeConfig
		if err := write.EnsureBinding(cfg, teamstore.Key("rest", "https://"+earlier, setupTeamRef), "rest", "https://"+earlier, "", "", setupTeamRef, "devs", "p_earlier"); err != nil {
			t.Fatal(err)
		}
		if err := write.EnsureBinding(cfg, r.newKey, "rest", r.newURL, "", moveNewPin, setupTeamRef, "devs", "p_new"); err != nil {
			t.Fatal(err)
		}
		r.writeFile(strings.NewReplacer(r.oldURL, r.newURL, moveOldPin, moveNewPin).Replace(r.original))
		canon, err := teamfile.Canonicalize(r.top)
		if err != nil {
			t.Fatal(err)
		}
		if err := write.Pin(cfg, canon, teamstore.Pin{Adapter: "rest", URL: r.newURL, Certificate: moveNewPin, TeamRef: setupTeamRef, ConsentedAt: fixtureNow}); err != nil {
			t.Fatal(err)
		}
		r.f.rec.on("team take-in", okAnswer(r.takenIn(false, true)))
		return r
	}
	notAMember := failAnswer(protocol.CodeUnauthorized, "not an active member of this team")

	// The earlier one refuses the read, and the one the team left answers:
	// the earlier one is left out, and the command goes on with the other.
	for name, refusal := range map[string]answer{
		"no active member":      notAMember,
		"an unknown credential": failAnswer(protocol.CodeUnauthenticated, "the credential is not known to this server"),
	} {
		r := rerun()
		// The server answers the earlier backend's question first, and then its own.
		r.there.on("team moved", refusal, okAnswer(r.notMoved()))
		if err := Team(r.inv(true, r.args()...)); err != nil {
			t.Fatalf("%s: the same command with one backend left: %v", name, err)
		}
		if out := r.f.out.String(); !strings.Contains(out, "recorded on "+r.oldHost+":") || len(r.prompts) != 1 || !strings.Contains(r.prompts[0], "from "+r.oldHost+" to "+r.newHost) {
			t.Errorf("%s: the move did not go on from the one backend left:\n%s\n%v", name, out, r.prompts)
		}
	}

	// Both refuse: there is nothing left to move from here.
	r := rerun()
	r.f.rec.on("team moved", notAMember)
	r.there.on("team moved", notAMember)
	perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeConfig, "no_move")
	for _, need := range []string{"names that server already", earlier, r.oldHost, "no longer count", "nothing left to move from here"} {
		if !strings.Contains(perr.Message, need) {
			t.Errorf("the refusal where every other backend refuses the read lacks %q: %s", need, perr.Message)
		}
	}
	if len(r.prompts) != 0 || strings.Contains(perr.Message, "did not answer") {
		t.Errorf("the refusal where every other backend refuses the read: %s; %d confirmations", perr.Message, len(r.prompts))
	}

	// One that answers what is no record may be the one: nothing is chosen.
	r = rerun()
	r.there.on("team moved", failAnswer(protocol.CodeInternal, "the backend answered with something that is not a result envelope"))
	perr = wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, "team_ambiguous")
	if !strings.Contains(perr.Message, earlier+" did not answer what it records of this team") || !strings.Contains(perr.Message, "--from "+earlier) || !strings.Contains(perr.Message, "--from "+r.oldHost) {
		t.Errorf("the refusal where a backend answers what is no record: %s", perr.Message)
	}
}

// TestTeamMoveToTellsAClearThatIsToldToWaitAsOneThatWasNotAnswered (work
// item 341): the same for the one command, which clears the record a
// server holds of the team's leaving when the team comes back to it. A
// clear answered with a wait was told as a record that stands and could
// not be cleared. It is a clear that was not answered.
func TestTeamMoveToTellsAClearThatIsToldToWaitAsOneThatWasNotAnswered(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	r.fromAServer()
	left := `{"team_ref":"` + setupTeamRef + `","moved":true,"adapter":"rest","url":"` + r.oldURL + `","certificate":"` + moveOldPin + `","recorded_at":"2026-11-10T12:00:00Z"}`
	r.there.on("team moved", okAnswer(left))
	r.there.on("team move", failAnswer(protocol.CodeRateLimited, "slow down"))
	perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeRateLimited, reasonReturnNotCleared)
	for _, need := range []string{"the answer to the clear of that record did not arrive, so the record may be cleared all the same", "The cause: slow down", "Run the same command again to finish the move"} {
		if !strings.Contains(perr.Message, need) {
			t.Errorf("the stop of a clear that was told to wait lacks %q: %s", need, perr.Message)
		}
	}
	if strings.Contains(perr.Message, " still records that the team left it for ") {
		t.Errorf("a clear that was told to wait is told as a record that stands: %s", perr.Message)
	}
}
