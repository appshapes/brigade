//go:build !windows

package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// `brigade team move` (work item 273, ADR 0003): the fixture's team t_9
// was joined on a Supabase project and its administrator has pointed the
// checkout's team file at a Brigade server and joined it there.
const moveNewPin = "sha256:4444444444444444444444444444444444444444444444444444444444444444"

// moveTicket is the ticket the server's operator wrote: a join secret of
// the team, put together here.
var moveTicket = strings.Join([]string{"brg1", setupTeamRef, strings.Repeat("ab", 16)}, ".")

// moveRig is the administrator's machine. The three backends have hosts of
// the test's own: these commands name each adapter by its name, the bundled
// adapters' argv is the same in every test, and the adapter client keeps
// one describe per argv and profile for the life of the process, so a
// profile of the test's own is what keeps one test's describe out of
// another.
type moveRig struct {
	t       *testing.T
	f       *fixture
	top     string
	oldURL  string // the Supabase project the team is leaving
	oldHost string
	newURL  string // the server it goes to
	newHost string
	other   string // a second Supabase project's host, for --from
	oldKey  string
	newKey  string
	ticket  string
	prompts []string
}

// newMoveRig joins the team on both backends and writes the team file and
// the ticket file. joinedNew false leaves the server's membership out.
func newMoveRig(t *testing.T, joinedNew bool) *moveRig {
	t.Helper()
	f := newFixture(t)
	sum := sha256.Sum256([]byte(t.Name()))
	id := hex.EncodeToString(sum[:6])
	r := &moveRig{
		t: t, f: f, top: mkCheckout(t, f.dirs.Root),
		oldHost: "old" + id + ".supabase.co", newHost: "new" + id + ".example.test", other: "def" + id + ".supabase.co",
		ticket: filepath.Join(f.dirs.Root, "ops.ticket"),
	}
	r.oldURL, r.newURL = "https://"+r.oldHost, "https://"+r.newHost
	r.oldKey, r.newKey = teamstore.Key("supabase", r.oldURL, setupTeamRef), teamstore.Key("rest", r.newURL, setupTeamRef)
	doc := `{"version":1,"adapter":"rest","url":"` + r.newURL + `","certificate":"` + moveNewPin + `","team_ref":"` + setupTeamRef + `","team_name":"devs"}`
	//nolint:gosec // G306: a committed team file IS 0644
	if err := os.WriteFile(filepath.Join(r.top, teamfile.FileName), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := write.EnsureBinding(f.dirs.BrigadeConfig, r.oldKey, "supabase", r.oldURL, "sb_publishable_x", "", setupTeamRef, "devs", "p_old"); err != nil {
		t.Fatal(err)
	}
	if joinedNew {
		if err := write.EnsureBinding(f.dirs.BrigadeConfig, r.newKey, "rest", r.newURL, "", moveNewPin, setupTeamRef, "devs", "p_new"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(r.ticket, []byte(moveTicket+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The backend the team is leaving keeps a record of moves.
	f.rec.on("describe", okAnswer(string(fakeadapter.DescribeJSON(protocol.ProtocolVersion, "team.roster", "team.move"))))
	return r
}

// asked lists the adapter commands the rig's recorder saw, in order, but
// for describe, which an adapter answers from local files.
func (r *moveRig) asked() []string {
	var out []string
	for _, verb := range r.f.rec.verbs() {
		if verb != "describe" {
			out = append(out, verb)
		}
	}
	return out
}

// inv is the administrator's terminal running `brigade team move …`, with
// the confirmation answered as told.
func (r *moveRig) inv(confirm bool, args ...string) Invocation {
	iv := r.f.inv(r.f.terminalEnv(), "", append([]string{"move"}, args...)...)
	iv.Deps.Getwd = func() (string, error) { return r.top, nil }
	iv.Deps.IsTerminal = func(io.Reader) bool { return true }
	iv.Deps.Confirm = func(prompt string) (bool, error) {
		r.prompts = append(r.prompts, prompt)
		return confirm, nil
	}
	return iv
}

// recorded is the old backend's answer to a record of the rig's move.
func (r *moveRig) recorded() string {
	return `{"team_ref":"` + setupTeamRef + `","moved":true,"adapter":"rest","url":"` + r.newURL + `","certificate":"` + moveNewPin + `","recorded_at":"2026-11-14T12:00:00Z"}`
}

// joinOther joins the team on a second Supabase project, so that two
// backends could be the one it is leaving, and returns that team key.
func (r *moveRig) joinOther() string {
	r.t.Helper()
	key := teamstore.Key("supabase", "https://"+r.other, setupTeamRef)
	if err := write.EnsureBinding(r.f.dirs.BrigadeConfig, key, "supabase", "https://"+r.other, "sb_publishable_y", "", setupTeamRef, "devs", "p_other"); err != nil {
		r.t.Fatal(err)
	}
	return key
}

// pointBack rewrites the team file to name the Supabase project again, as
// an administrator who undoes a move does.
func (r *moveRig) pointBack() {
	r.t.Helper()
	doc := `{"version":1,"adapter":"supabase","url":"` + r.oldURL + `","publishable_key":"sb_publishable_x","team_ref":"` + setupTeamRef + `","team_name":"devs"}`
	//nolint:gosec // G306: a committed team file IS 0644
	if err := os.WriteFile(filepath.Join(r.top, teamfile.FileName), []byte(doc), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// TestTeamMoveRecordsWhatTheTeamFileNames: the record is made on the
// backend the team is leaving, under that membership's profile, with the
// destination the team file names as the stdin document and the ticket's
// path, never the ticket, on argv; the administrator is shown the team,
// both hosts and the certificate first; and the lines say what to do next.
func TestTeamMoveRecordsWhatTheTeamFileNames(t *testing.T) {
	t.Parallel()
	r := newMoveRig(t, true)
	r.f.rec.on("team move", okAnswer(r.recorded()))
	if err := Team(r.inv(true, "--ticket-file", r.ticket)); err != nil {
		t.Fatal(err)
	}
	if len(r.prompts) != 1 {
		t.Fatalf("%d confirmations, want one", len(r.prompts))
	}
	for _, want := range []string{`"devs"`, setupTeamRef, r.oldHost, r.newHost, moveNewPin, "no command of theirs"} {
		if !strings.Contains(r.prompts[0], want) {
			t.Errorf("the confirmation lacks %q: %s", want, r.prompts[0])
		}
	}
	specs := r.f.rec.specsOf("team move")
	if got := r.f.rec.verbs(); !slices.Equal(got, []string{"describe", "team move"}) {
		t.Fatalf("adapter calls: %v, want its describe and the one record", got)
	}
	argv := specs[0].Argv
	if i := slices.Index(argv, "--profile"); i < 0 || argv[i+1] != r.oldKey {
		t.Fatalf("the record was not made under the profile of the backend the team is leaving")
	}
	if i := slices.Index(argv, "--ticket-file"); i < 0 || argv[i+1] != r.ticket || strings.Contains(strings.Join(argv, " "), moveTicket) {
		t.Fatalf("argv does not name the ticket's file, or carries the ticket")
	}
	if got := string(specs[0].Stdin); got != `{"adapter":"rest","url":"`+r.newURL+`","certificate":"`+moveNewPin+`"}` {
		t.Fatalf("the destination sent: %s", got)
	}
	out := r.f.out.String()
	for _, want := range []string{"recorded on " + r.oldHost, r.newHost, "commit and push " + teamfile.FileName, "team move --clear"} {
		if !strings.Contains(out, want) {
			t.Errorf("the lines lack %q: %s", want, out)
		}
	}
	if strings.Contains(out+r.f.errb.String()+strings.Join(r.prompts, ""), moveTicket) {
		t.Fatalf("the ticket reached a stream or the confirmation")
	}
}

// TestTeamMoveRefusesBeforeAnythingIsAsked: each refusal the command makes
// on its own spawns no adapter and asks for no confirmation.
func TestTeamMoveRefusesBeforeAnythingIsAsked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		joined bool
		setup  func(r *moveRig) []string
		code   protocol.Code
		reason string
	}{
		{"no form", true, func(*moveRig) []string { return nil }, protocol.CodeUsage, ""},
		{"two forms", true, func(r *moveRig) []string { return []string{"--clear", "--ticket-file", r.ticket} }, protocol.CodeUsage, ""},
		{"an argument that is no flag", true, func(*moveRig) []string { return []string{"--show", "extra"} }, protocol.CodeUsage, ""},
		{"a ticket on the command line", true, func(*moveRig) []string { return []string{"--ticket-file", moveTicket} }, protocol.CodeUsage, ""},
		{"a relative ticket file", true, func(*moveRig) []string { return []string{"--ticket-file", "ops.ticket"} }, protocol.CodeUsage, ""},
		{"a ticket file inside the checkout", true, func(r *moveRig) []string {
			inside := filepath.Join(r.top, "ops.ticket")
			if err := os.WriteFile(inside, []byte(moveTicket+"\n"), 0o600); err != nil {
				r.t.Fatal(err)
			}
			return []string{"--ticket-file", inside}
		}, protocol.CodeUsage, ""},
		{"a ticket file that is not there", true, func(r *moveRig) []string {
			return []string{"--ticket-file", filepath.Join(r.f.dirs.Root, "no-such.ticket")}
		}, protocol.CodeConfig, "ticket_file_missing"},
		{"a file that holds no ticket", true, func(r *moveRig) []string {
			if err := os.WriteFile(r.ticket, []byte("not a ticket\n"), 0o600); err != nil {
				r.t.Fatal(err)
			}
			return []string{"--ticket-file", r.ticket}
		}, protocol.CodeInvalidInput, "invalid_value"},
		{"a ticket of another team", true, func(r *moveRig) []string {
			if err := os.WriteFile(r.ticket, []byte(strings.Replace(moveTicket, setupTeamRef, "t_8", 1)+"\n"), 0o600); err != nil {
				r.t.Fatal(err)
			}
			return []string{"--ticket-file", r.ticket}
		}, protocol.CodeInvalidInput, "invalid_value"},
		{"the administrator has not joined the new backend", false, func(r *moveRig) []string { return []string{"--ticket-file", r.ticket} },
			protocol.CodeConfig, "not_joined_at_destination"},
		{"no team file", true, func(r *moveRig) []string {
			if err := os.Remove(filepath.Join(r.top, teamfile.FileName)); err != nil {
				r.t.Fatal(err)
			}
			return []string{"--ticket-file", r.ticket}
		}, protocol.CodeConfig, "team_file_missing"},
		{"the file names the only backend this machine joined the team on", true, func(r *moveRig) []string {
			if err := os.RemoveAll(filepath.Join(r.f.dirs.BrigadeConfig, "teams", r.oldKey)); err != nil {
				r.t.Fatal(err)
			}
			return []string{"--ticket-file", r.ticket}
		}, protocol.CodeConfig, "no_move"},
		{"the machine never joined this team", true, func(r *moveRig) []string {
			if err := os.RemoveAll(filepath.Join(r.f.dirs.BrigadeConfig, "teams")); err != nil {
				r.t.Fatal(err)
			}
			return []string{"--show"}
		}, protocol.CodeConfig, "team_unknown"},
		{"two other backends and no --from", true, func(r *moveRig) []string {
			r.joinOther()
			return []string{"--show"}
		}, protocol.CodeConfig, "team_ambiguous"},
		{"a --from that names no backend this machine joined the team on", true, func(*moveRig) []string {
			return []string{"--show", "--from", "nowhere.example.test"}
		}, protocol.CodeConfig, "team_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newMoveRig(t, tc.joined)
			args := tc.setup(r)
			err := Team(r.inv(true, args...))
			perr := wantCodeErr(t, err, tc.code, tc.reason)
			if strings.Contains(perr.Message, moveTicket) || strings.Contains(perr.Message, strings.Repeat("ab", 16)) {
				t.Fatalf("the refusal carries the ticket")
			}
			if r.f.rec.count() != 0 || len(r.prompts) != 0 {
				t.Fatalf("a refusal spawned %v and asked %d times", r.f.rec.verbs(), len(r.prompts))
			}
		})
	}
}

// TestTeamMoveDeclinedAndRefusedInASession: a declined confirmation records
// nothing; inside a session the two writing forms are refused with the one
// in-session shape, before the team file is even read.
func TestTeamMoveDeclinedAndRefusedInASession(t *testing.T) {
	t.Parallel()
	r := newMoveRig(t, true)
	wantCode(t, Team(r.inv(false, "--ticket-file", r.ticket)), protocol.CodeUsage, "declined")
	if len(r.asked()) != 0 || len(r.prompts) != 1 {
		t.Fatalf("a declined record asked %v after %d confirmations", r.asked(), len(r.prompts))
	}
	calls := r.f.rec.count()
	for _, args := range [][]string{{"--ticket-file", r.ticket}, {"--clear"}} {
		iv := r.inv(true, args...)
		iv.Environ = r.f.sessionEnv()
		perr := wantCodeErr(t, Team(iv), protocol.CodeUsage, "in_session")
		if perr.Message != RefusalMoveInSession {
			t.Fatalf("%v in a session: %q", args, perr.Message)
		}
	}
	if r.f.rec.count() != calls {
		t.Fatalf("a refusal in a session spawned %v", r.f.rec.verbs())
	}
}

// TestTeamMoveAsksNothingOfABackendThatKeepsNoRecord: an adapter that does
// not advertise team.move is asked nothing new. Each form is refused with
// one `config` answer that says what its members do instead, after the
// adapter's describe and before any command of the capability.
func TestTeamMoveAsksNothingOfABackendThatKeepsNoRecord(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{"record": {"--ticket-file", ""}, "clear": {"--clear"}, "show": {"--show"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveRig(t, true)
			r.f.rec.on("describe", okAnswer(string(fakeadapter.DescribeJSON(protocol.ProtocolVersion, "team.roster", "team.admin"))))
			if args[0] == "--ticket-file" {
				args = []string{"--ticket-file", r.ticket}
			}
			perr := wantCodeErr(t, Team(r.inv(true, args...)), protocol.CodeConfig, "no_team_move")
			if !strings.Contains(perr.Message, "team.move") || len(r.asked()) != 0 || len(r.prompts) != 0 {
				t.Fatalf("%q, asked %v, %d confirmations", perr.Message, r.asked(), len(r.prompts))
			}
		})
	}
}

// TestTeamMoveFromReachesTheBackendTheFileNames: once the team file has
// been pointed back at the backend that holds the record, that backend is
// the file's own, and --from still names it: the record there can be read
// and cleared. A record is never made on the backend the file names.
func TestTeamMoveFromReachesTheBackendTheFileNames(t *testing.T) {
	t.Parallel()
	r := newMoveRig(t, true)
	// The administrator undoes the move: the file names the Supabase project
	// again. With no --from the other backend of the team is the server, whose
	// adapter keeps no record of moves and is asked for none.
	r.pointBack()
	r.f.rec.on("describe", okAnswer(string(fakeadapter.DescribeJSON(protocol.ProtocolVersion, "team.roster", "team.admin"))))
	wantCode(t, Team(r.inv(true, "--clear")), protocol.CodeConfig, "no_team_move")
	if len(r.asked()) != 0 {
		t.Fatalf("a clear with no --from asked %v", r.asked())
	}
	r.f.rec.on("describe", okAnswer(string(fakeadapter.DescribeJSON(protocol.ProtocolVersion, "team.roster", "team.move"))))
	r.f.rec.on("team move", okAnswer(`{"team_ref":"`+setupTeamRef+`","moved":false}`))
	if err := Team(r.inv(true, "--clear", "--from", r.oldHost)); err != nil {
		t.Fatal(err)
	}
	cleared := r.f.rec.specsOf("team move")
	if len(cleared) != 1 || !slices.Contains(cleared[0].Argv, "--clear") {
		t.Fatalf("the clear with --from: %v", r.f.rec.verbs())
	}
	if i := slices.Index(cleared[0].Argv, "--profile"); i < 0 || cleared[0].Argv[i+1] != r.oldKey {
		t.Fatalf("the clear with --from did not run under the profile of the backend it names")
	}
	r.f.rec.on("team moved", okAnswer(`{"team_ref":"`+setupTeamRef+`","moved":false}`))
	if err := Team(r.inv(true, "--show", "--from", r.oldURL)); err != nil || !strings.Contains(r.f.out.String(), "no move recorded on "+r.oldHost) {
		t.Fatalf("the show with --from: %v\n%s", err, r.f.out.String())
	}
	before := len(r.asked())
	wantCode(t, Team(r.inv(true, "--ticket-file", r.ticket, "--from", r.oldHost)), protocol.CodeConfig, "no_move")
	if len(r.asked()) != before || len(r.prompts) != 0 {
		t.Fatalf("a record on the backend the file names asked %v", r.asked()[before:])
	}
}

// TestTeamMoveClearShowAndFrom: --clear and --show run under the profile of
// the backend the team left; --clear asks for no confirmation and sends
// --clear alone; --show never asks for the ticket and says whether the
// record names what the team file names; and --from picks one of two
// backends by its host.
func TestTeamMoveClearShowAndFrom(t *testing.T) {
	t.Parallel()
	r := newMoveRig(t, true)
	r.f.rec.on("team move", okAnswer(`{"team_ref":"`+setupTeamRef+`","moved":false}`))
	if err := Team(r.inv(true, "--clear")); err != nil {
		t.Fatal(err)
	}
	cleared := r.f.rec.specsOf("team move")
	if len(cleared) != 1 || len(r.prompts) != 0 || !slices.Contains(cleared[0].Argv, "--clear") || slices.Contains(cleared[0].Argv, "--ticket-file") || len(cleared[0].Stdin) != 0 {
		t.Fatalf("the clear: %d calls, %d confirmations, argv %v", len(cleared), len(r.prompts), cleared)
	}
	if out := r.f.out.String(); !strings.Contains(out, "cleared: "+r.oldHost+" no longer says") {
		t.Fatalf("the clear's line: %s", out)
	}

	show := func(answer string, args ...string) string {
		t.Helper()
		r.f.out.Reset()
		r.f.rec.on("team moved", okAnswer(answer))
		if err := Team(r.inv(true, append([]string{"--show"}, args...)...)); err != nil {
			t.Fatal(err)
		}
		return r.f.out.String()
	}
	if out := show(r.recorded()); !strings.Contains(out, "recorded on "+r.oldHost+" at 2026-11-14 12:00 UTC") || !strings.Contains(out, "names the same backend") {
		t.Fatalf("a record of the file's backend: %s", out)
	}
	if out := show(strings.Replace(r.recorded(), r.newURL, "https://elsewhere.example.test", 1)); !strings.Contains(out, "elsewhere.example.test") || !strings.Contains(out, "names ANOTHER backend") {
		t.Fatalf("a record of another backend: %s", out)
	}
	if out := show(`{"team_ref":"` + setupTeamRef + `","moved":false}`); !strings.Contains(out, "no move recorded on "+r.oldHost) {
		t.Fatalf("no record: %s", out)
	}
	for _, spec := range r.f.rec.specsOf("team moved") {
		if slices.Contains(spec.Argv, "--ticket-file") {
			t.Fatalf("--show asked the backend for the ticket")
		}
		if i := slices.Index(spec.Argv, "--profile"); i < 0 || spec.Argv[i+1] != r.oldKey {
			t.Fatalf("--show did not run under the profile of the backend the team left")
		}
	}

	// A second earlier backend: --from names one by its host, or its url.
	otherKey := r.joinOther()
	for _, from := range []string{r.other, "https://" + r.other} {
		before := len(r.f.rec.specsOf("team moved"))
		if out := show(`{"team_ref":"`+setupTeamRef+`","moved":false}`, "--from", from); !strings.Contains(out, "no move recorded on "+r.other) {
			t.Fatalf("--from %s: %s", from, out)
		}
		spec := r.f.rec.specsOf("team moved")[before]
		if i := slices.Index(spec.Argv, "--profile"); i < 0 || spec.Argv[i+1] != otherKey {
			t.Fatalf("--from %s did not pick the backend it names", from)
		}
	}
}

// TestTeamMoveRefusesAnAnswerThatIsNotItsRecord: a backend that answers a
// record other than the one it was given, or a shape that is not the
// protocol's, is a protocol mismatch and prints no success line.
func TestTeamMoveRefusesAnAnswerThatIsNotItsRecord(t *testing.T) {
	t.Parallel()
	for name, spoil := range map[string]func(r *moveRig) string{
		"another url": func(r *moveRig) string {
			return strings.Replace(r.recorded(), r.newURL, "https://elsewhere.example.test", 1)
		},
		"no certificate": func(r *moveRig) string {
			return strings.Replace(r.recorded(), `,"certificate":"`+moveNewPin+`"`, "", 1)
		},
		"moved false": func(*moveRig) string { return `{"team_ref":"` + setupTeamRef + `","moved":false}` },
		"no team":     func(*moveRig) string { return `{"moved":true}` },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveRig(t, true)
			r.f.rec.on("team move", okAnswer(spoil(r)))
			wantCode(t, Team(r.inv(true, "--ticket-file", r.ticket)), protocol.CodeProtocolMismatch, "bad_result")
			if strings.Contains(r.f.out.String(), "recorded") {
				t.Fatalf("a success line was printed: %s", r.f.out.String())
			}
		})
	}
}
