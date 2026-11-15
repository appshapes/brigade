//go:build !windows

package commands

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
)

// More of `brigade team move --to` (work item 328), from its second review:
// the one spelling of the destination, a record under another certificate,
// and the option read in the session of every harness.

// TestTeamMoveToWritesOneSpellingOfTheDestination: the address is kept as
// it was typed but for the slashes at its end, and no letter of it is
// folded. That one spelling is the backend the server's adapter is given,
// the destination the old backend records, the team file's url, the pin's
// and the binding's, so the record names what the file names byte for
// byte. A certificate goes with an https address however its scheme was
// typed.
func TestTeamMoveToWritesOneSpellingOfTheDestination(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	const typed = "HTTPS://Brigade.Example.TEST:8743"
	key := teamstore.Key("rest", typed, setupTeamRef)
	r.f.rec.on("team move", okAnswer(r.notMoved()),
		okAnswer(`{"team_ref":"`+setupTeamRef+`","moved":true,"adapter":"rest","url":"`+typed+`","certificate":"`+moveNewPin+`","recorded_at":"2026-11-14T12:00:00Z"}`))
	if err := Team(r.inv(true, "--to", typed+"//", "--secret-file", r.secretFile, "--certificate", opensslSpelling(moveNewPin))); err != nil {
		t.Fatal(err)
	}
	take := r.f.rec.specsOf("team take-in")[0]
	if i := slices.Index(take.Argv, "--profile"); take.Argv[i+1] != key ||
		!strings.Contains(string(take.Stdin), `"backend":{"url":"`+typed+`","certificate":"`+moveNewPin+`"}`) {
		t.Fatalf("the take-in: %s", argvOf(take))
	}
	if got := string(r.f.rec.specsOf("team move")[1].Stdin); got != `{"adapter":"rest","url":"`+typed+`","certificate":"`+moveNewPin+`"}` {
		t.Fatalf("the destination recorded: %s", got)
	}
	f, err := teamfile.Parse(r.file)
	if err != nil || f.Adapter != "rest" || f.URL != typed || f.Certificate != moveNewPin {
		t.Fatalf("the team file: %+v, %v\n%s", f, err, r.fileNow())
	}
	if pin := r.pin(); !pin.Matches(f) || pin.URL != typed {
		t.Fatalf("the pin: %+v", pin)
	}
	if b, err := teamstore.LoadBinding(r.f.dirs.BrigadeConfig, key); err != nil || teamstore.VerifyBinding(b, f) != nil || b.URL != typed {
		t.Fatalf("the binding at the server: %+v, %v", b, err)
	}
	r.noSecrets()
}

// TestTeamMoveToNamesARecordUnderAnotherCertificate: a record of a move to
// the same address under another certificate is another destination: it
// is named before the administrator answers as the record this move
// replaces, and it is not cleared.
func TestTeamMoveToNamesARecordUnderAnotherCertificate(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	r.f.rec.on("team moved", okAnswer(strings.Replace(r.recorded(), moveNewPin, otherPin, 1)))
	r.f.rec.on("team move", okAnswer(r.recorded()))
	if err := Team(r.inv(true, r.args()...)); err != nil {
		t.Fatal(err)
	}
	if len(r.prompts) != 1 || !strings.Contains(r.prompts[0], "This replaces the record "+r.oldHost+" holds of a move to "+r.newHost) ||
		!strings.Contains(r.prompts[0], otherPin) {
		t.Fatalf("the confirmation: %v", r.prompts)
	}
	for _, spec := range r.f.rec.specsOf("team move") {
		if slices.Contains(spec.Argv, "--clear") {
			t.Fatalf("a record that was there was cleared")
		}
	}
}

// TestTeamMoveReadsTheOptionInEveryHostsSession: a command in the shell of
// a Muse Code, Codex CLI or Grok Build session reads team_move_in_session
// as one in a Claude Code session does: from the start facts of that
// session, which such a shell reaches through the start pointer from the
// host's native id, under HOME whatever XDG_* says. Off refuses the three
// writing forms with nothing asked of anyone, --show reads all the same,
// and on lets a writing form run.
func TestTeamMoveReadsTheOptionInEveryHostsSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ host, variable, id string }{
		{"muse", "MUSE_SESSION_ID", "01a14785-947f-7d93-a4f4-6ee1f6b2be94"},
		{"codex", "CODEX_THREAD_ID", "01a14aa0-8a90-7712-a2b8-79405a4acbca"},
		{"grok", "GROK_SESSION_ID", "0199c1c5-5f0f-7a53-9f6e-0d5a3a4b9c11"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			r := newMoveRig(t, true)
			store := sessionmap.Store{StateDir: filepath.Join(r.f.dirs.Home, ".local", "state", "brigade")}
			const key = 1<<30 | 4242
			facts := func(option string) {
				t.Helper()
				if err := store.WriteStart(&sessionmap.StartFacts{
					ClaudePID: key, ClaudeSessionID: tc.id, ConfigDir: r.f.dirs.BrigadeConfig, TeamMoveInSession: option, WrittenAt: fixtureNow,
				}); err != nil {
					t.Fatal(err)
				}
				if err := store.WriteStartPointer(tc.host, tc.id, key, fixtureNow); err != nil {
					t.Fatal(err)
				}
			}
			inv := func(args ...string) Invocation {
				iv := r.f.inv([]string{"HOME=" + r.f.dirs.Home, "XDG_STATE_HOME=/nowhere", "XDG_CONFIG_HOME=/nowhere", tc.variable + "=" + tc.id},
					"", append([]string{"move"}, args...)...)
				iv.Deps.Getwd = func() (string, error) { return r.top, nil }
				iv.Deps.IsTerminal = func(io.Reader) bool { return false }
				iv.Deps.Confirm = func(string) (bool, error) {
					t.Error("a command in a session asked a y/N question")
					return false, nil
				}
				return iv
			}
			facts(config.SyncOff)
			for _, form := range [][]string{{"--clear"}, {"--ticket-file", r.ticket}, {"--to", r.newURL, "--secret-file", r.ticket}} {
				perr := wantCodeErr(t, Team(inv(form...)), protocol.CodeUsage, "in_session")
				if perr.Message != RefusalMoveInSession {
					t.Fatalf("%v with the option off: %q", form, perr.Message)
				}
			}
			if r.f.rec.count() != 0 {
				t.Fatalf("a refusal in a session spawned %v", r.f.rec.verbs())
			}
			r.f.rec.on("team moved", okAnswer(`{"team_ref":"`+setupTeamRef+`","moved":false}`))
			if err := Team(inv("--show")); err != nil || !strings.Contains(r.f.out.String(), "no move recorded on "+r.oldHost) {
				t.Fatalf("--show with the option off: %v\n%s", err, r.f.out.String())
			}
			facts("")
			r.f.rec.on("team move", okAnswer(`{"team_ref":"`+setupTeamRef+`","moved":false}`))
			if err := Team(inv("--clear")); err != nil || !strings.Contains(r.f.out.String(), "cleared: "+r.oldHost) {
				t.Fatalf("a clear with the option on: %v\n%s", err, r.f.out.String())
			}
		})
	}
}

// TestTeamMoveToSaysThatTheRecordHoldsATicketThatWasReplaced: a second open
// replaces the move's ticket on the server. When a move that was recorded
// is run again and its record fails, the old backend still hands out the
// ticket of the run before, which admits nobody now: the refusal says so,
// beside what it always says. A first run whose record fails left no record
// behind and says no such thing.
func TestTeamMoveToSaysThatTheRecordHoldsATicketThatWasReplaced(t *testing.T) {
	t.Parallel()
	const stale = "hands out a ticket that this run replaced, so no member follows until the record is made again"
	unavailable := failAnswer(protocol.CodeUnavailable, "the backend could not be reached")
	opened := func(r *moveToRig, replaced string) string {
		return `{"team_ref":"` + setupTeamRef + `","team_name":"devs","ends_at":"2026-12-14T12:00:00Z","replaced":` + replaced + `,"ticket_file":"` + r.ticketFile + `"}`
	}
	t.Run("a move that was recorded, run again", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		r.f.rec.on("team take-in", okAnswer(r.takenIn(false, true)))
		r.f.rec.on("team moved", okAnswer(r.recorded()))
		r.f.rec.on("team open-move", okAnswer(opened(r, "true")))
		r.f.rec.on("team move", unavailable)
		perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, "")
		if !strings.Contains(perr.Message, "its move is open, and "+r.oldHost+" did not record it; the record it holds from a run before this one "+stale) ||
			!strings.Contains(perr.Message, "Run the same command again") {
			t.Fatalf("the refusal: %s", perr.Message)
		}
		r.noSecrets()
	})
	t.Run("a first run", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		// A take-in before this run opened a move, and nothing was recorded.
		r.f.rec.on("team open-move", okAnswer(opened(r, "true")))
		r.f.rec.on("team move", okAnswer(r.notMoved()), unavailable)
		perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, "")
		if !strings.Contains(perr.Message, "did not record it") || strings.Contains(perr.Message, stale) {
			t.Fatalf("the refusal: %s", perr.Message)
		}
	})
	t.Run("a record of a move somewhere else", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		r.f.rec.on("team moved", okAnswer(strings.Replace(r.recorded(), r.newURL, "https://elsewhere.example.test", 1)))
		r.f.rec.on("team open-move", okAnswer(opened(r, "true")))
		r.f.rec.on("team move", unavailable)
		perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, "")
		if !strings.Contains(perr.Message, "did not record it") || strings.Contains(perr.Message, stale) {
			t.Fatalf("the refusal: %s", perr.Message)
		}
	})
}

// TestTeamMoveToAndATeamFileThatIsNotAPlainFile: a team file that is a
// symbolic link is no team file, to a session and to this command alike:
// the move stops before anything is asked, and the link and the file it
// points at are as they were. A team file its owner made read-only is a
// team file: a rewrite replaces the file and does not write into it, so the
// move goes on, and what replaces it has a committed team file's mode.
func TestTeamMoveToAndATeamFileThatIsNotAPlainFile(t *testing.T) {
	t.Parallel()
	t.Run("a symbolic link", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		elsewhere := filepath.Join(r.f.dirs.Root, "the-team-file-elsewhere.json")
		if err := os.Rename(r.file, elsewhere); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, r.file); err != nil {
			t.Fatal(err)
		}
		storeBefore := storeHash(t, r.f.dirs.BrigadeConfig)
		wantCode(t, Team(r.inv(true, r.args()...)), protocol.CodeConfig, teamfile.ReasonNotRegularFile)
		if r.f.rec.count() != 0 || len(r.prompts) != 0 {
			t.Fatalf("the refusal spawned %v and asked %d times", r.f.rec.verbs(), len(r.prompts))
		}
		if info, err := os.Lstat(r.file); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link after the refusal: %v, %v", info, err)
		}
		if text, err := os.ReadFile(elsewhere); err != nil || string(text) != r.original {
			t.Fatalf("the file the link points at was rewritten (%v):\n%s", err, text)
		}
		if storeHash(t, r.f.dirs.BrigadeConfig) != storeBefore {
			t.Fatalf("the refusal wrote the store")
		}
	})
	t.Run("a read-only file", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		if err := os.Chmod(r.file, 0o444); err != nil { //nolint:gosec // G302: a team file its owner made read-only
			t.Fatal(err)
		}
		if err := Team(r.inv(true, r.args()...)); err != nil {
			t.Fatal(err)
		}
		if got := r.fileNow(); got != r.moved() {
			t.Fatalf("the team file:\n%s\nwant:\n%s", got, r.moved())
		}
		if info, err := os.Lstat(r.file); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
			t.Fatalf("the team file after the move: %v, %v", info, err)
		}
		// Nothing of the rewrite is left beside it.
		entries, err := os.ReadDir(r.top)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp-") {
				t.Fatalf("the rewrite left %s in the checkout", e.Name())
			}
		}
	})
}
