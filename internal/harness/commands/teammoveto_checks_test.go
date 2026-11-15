//go:build !windows

package commands

import (
	"encoding/json/v2"
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
	var sent struct {
		Backend map[string]string `json:"backend"`
	}
	if err := json.Unmarshal(take.Stdin, &sent); err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(take.Argv, "--profile"); take.Argv[i+1] != key ||
		len(sent.Backend) != 2 || sent.Backend["url"] != typed || sent.Backend["certificate"] != moveNewPin {
		t.Fatalf("the take-in: %s, backend %v", argvOf(take), sent.Backend)
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
	other := strings.Replace(r.recorded(), moveNewPin, otherPin, 1)
	r.f.rec.on("team moved", okAnswer(other))
	r.f.rec.on("team move", okAnswer(other), okAnswer(r.recorded()))
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
		// The check records the record that is there again; the move's own record is what fails.
		r.f.rec.on("team move", okAnswer(r.recorded()), unavailable)
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
		elsewhere := strings.Replace(r.recorded(), r.newURL, "https://elsewhere.example.test", 1)
		r.f.rec.on("team moved", okAnswer(elsewhere))
		r.f.rec.on("team open-move", okAnswer(opened(r, "true")))
		r.f.rec.on("team move", okAnswer(elsewhere), unavailable)
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

// recordedOn is the old backend's answer for a record of a move to url
// under pin, "" for no pin.
func recordedOn(url, pin string) string {
	certificate := ""
	if pin != "" {
		certificate = `,"certificate":"` + pin + `"`
	}
	return `{"team_ref":"` + setupTeamRef + `","moved":true,"adapter":"rest","url":"` + url + `"` + certificate + `,"recorded_at":"2026-11-14T12:00:00Z"}`
}

// TestTeamMoveToRecordsAgainARecordThatIsThere: where the backend the team
// leaves holds a record already, the question whether this member may write
// one is asked by writing that record again, and it is asked before the
// administrator is, and before the server is asked anything. The record's
// ticket is read into the private file the move's own ticket is kept in
// later, by the command every member is answered, and handed back with the
// destination the record names, in the document and never on argv: for a
// record of this move, and for one of a move somewhere else, which keeps
// its own destination and its own certificate until the move's record
// replaces it. Nothing is cleared, and the file is gone at the end.
func TestTeamMoveToRecordsAgainARecordThatIsThere(t *testing.T) {
	t.Parallel()
	const elsewhere = "https://elsewhere.example.test/team"
	for name, tc := range map[string]struct {
		record func(r *moveToRig) string
		names  func(r *moveToRig) string
	}{
		"a record of this move": {
			record: func(r *moveToRig) string { return r.recorded() },
			names: func(r *moveToRig) string {
				return `{"adapter":"rest","url":"` + r.newURL + `","certificate":"` + moveNewPin + `"}`
			},
		},
		"a record of a move somewhere else": {
			record: func(*moveToRig) string { return recordedOn(elsewhere, otherPin) },
			names: func(*moveToRig) string {
				return `{"adapter":"rest","url":"` + elsewhere + `","certificate":"` + otherPin + `"}`
			},
		},
		"a record of a move to a server with no pin": {
			record: func(*moveToRig) string { return recordedOn(elsewhere, "") },
			names:  func(*moveToRig) string { return `{"adapter":"rest","url":"` + elsewhere + `"}` },
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			r.f.rec.on("team moved", okAnswer(tc.record(r)))
			r.f.rec.on("team move", okAnswer(tc.record(r)), okAnswer(r.recorded()))
			r.f.rec.on("team take-in", okAnswer(r.takenIn(false, true)))
			if err := Team(r.inv(true, r.args()...)); err != nil {
				t.Fatal(err)
			}
			want := []string{"team moved", "team moved", "team move", "session list", "team take-in", "team open-move", "team move"}
			if got := r.asked(); !slices.Equal(got, want) {
				t.Fatalf("adapter commands: %v, want %v", got, want)
			}
			// The two reads: the first asks whether there is a record, the second for its ticket, in the file.
			reads := r.f.rec.specsOf("team moved")
			if slices.Contains(reads[0].Argv, "--ticket-file") || !strings.HasSuffix(argvOf(reads[1]), "--ticket-file "+r.ticketFile) {
				t.Fatalf("the reads: %s; %s", argvOf(reads[0]), argvOf(reads[1]))
			}
			// The record written again: under the old backend's profile, with the ticket by its file's path
			// and the destination the record named, to the byte; and it is written before anyone is asked.
			again := r.f.rec.specsOf("team move")[0]
			if i := slices.Index(again.Argv, "--profile"); again.Argv[i+1] != r.oldKey || slices.Contains(again.Argv, "--clear") ||
				!strings.HasSuffix(argvOf(again), "--ticket-file "+r.ticketFile) {
				t.Fatalf("the record written again: %s", argvOf(again))
			}
			if got := string(again.Stdin); got != tc.names(r) {
				t.Fatalf("the record written again names %s, want what the record named: %s", got, tc.names(r))
			}
			// What each `team move` was handed in the file: the record's own ticket, then the move's.
			if !slices.Equal(r.ticketsRead, []string{moveToRecordTicket, moveToTicket}) {
				t.Fatalf("the tickets the old backend was handed are not the record's and then the move's")
			}
			for _, spec := range r.f.rec.specsOf("team move") {
				if slices.Contains(spec.Argv, "--clear") {
					t.Fatalf("a record that was there was cleared")
				}
			}
			if _, err := os.Lstat(r.ticketFile); err == nil {
				t.Fatalf("the ticket's file is still there")
			}
			if len(r.prompts) != 1 {
				t.Fatalf("%d confirmations, want one", len(r.prompts))
			}
			r.noSecrets()
		})
	}
}

// TestTeamMoveToRefusesAMemberWhoDidNotCreateTheTeamWhereARecordIsThere:
// the old backend holds a record and refuses this member the write of it,
// as it refuses anyone who did not create the team. The move stops there,
// in the words of a member who met no record: before the administrator's
// question, with the server's adapter asked nothing but what it offers,
// which it answers from local files, and with the team file, the pin and
// the store as they were. The record's ticket was in its file for the
// check, and the file is gone.
func TestTeamMoveToRefusesAMemberWhoDidNotCreateTheTeamWhereARecordIsThere(t *testing.T) {
	t.Parallel()
	for name, record := range map[string]func(r *moveToRig) string{
		"a record of this move":             func(r *moveToRig) string { return r.recorded() },
		"a record of a move somewhere else": func(*moveToRig) string { return recordedOn("https://elsewhere.example.test", otherPin) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			r.f.rec.on("team moved", okAnswer(record(r)))
			r.f.rec.on("team move", failAnswer(protocol.CodeUnauthorized, "not the team's owner"))
			storeBefore := storeHash(t, r.f.dirs.BrigadeConfig)
			perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnauthorized, reasonNotTheCreator)
			if !strings.Contains(perr.Message, "only the member who created this team on "+r.oldHost+" can move it") || !strings.Contains(perr.Message, "Nothing was changed") {
				t.Fatalf("the refusal: %s", perr.Message)
			}
			if got, want := r.asked(), []string{"team moved", "team moved", "team move"}; !slices.Equal(got, want) {
				t.Fatalf("adapter commands: %v, want %v", got, want)
			}
			for i := range r.f.rec.count() {
				spec := r.f.rec.spec(t, i)
				if slices.Contains(spec.Argv, "rest") && verbKey(spec.Argv) != "describe" {
					t.Fatalf("the server's adapter was asked %s", argvOf(spec))
				}
			}
			if len(r.prompts) != 0 {
				t.Fatalf("a member who may not move the team was asked: %v", r.prompts)
			}
			if got := r.fileNow(); got != r.original {
				t.Fatalf("the refusal rewrote the team file:\n%s", got)
			}
			if pin := r.pin(); pin.Adapter != "supabase" || pin.URL != r.oldURL {
				t.Fatalf("the refusal rewrote the pin: %+v", pin)
			}
			if storeHash(t, r.f.dirs.BrigadeConfig) != storeBefore {
				t.Fatalf("the refusal wrote the store")
			}
			if _, err := os.Lstat(r.ticketFile); err == nil {
				t.Fatalf("the check left the record's ticket in its file")
			}
			r.noSecrets()
		})
	}
}

// TestTeamMoveToStopsWhenARecordCannotBeChecked: each thing that keeps the
// command from recording again a record that is there stops it in its
// checks, saying what to do, with the server's adapter asked nothing, the
// administrator not asked and no ticket file left: a state directory that
// takes no ticket file, by the command's own finding and by the adapter's;
// a record that was cleared between the read that found it and the read of
// its ticket, which is not put back; an answer that names another file, or
// whose file holds no ticket of this team, with the record not touched; a
// write that answers another record; and a backend that cannot be reached.
func TestTeamMoveToStopsWhenARecordCannotBeChecked(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		spoil  func(r *moveToRig)
		code   protocol.Code
		reason string
		says   []string
		asked  []string
	}{
		"the ticket's directory is not a directory": {
			spoil: func(r *moveToRig) {
				dir := filepath.Dir(r.ticketFile)
				if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
					r.t.Fatal(err)
				}
				if err := os.WriteFile(dir, []byte("not a directory\n"), 0o600); err != nil {
					r.t.Fatal(err)
				}
			},
			code: protocol.CodeConfig, reason: reasonTicketFileUnwritable,
			says:  []string{"holds a record of a move already", "a private file for its ticket, which could not be written", "team-move", "and run this again. Nothing was changed"},
			asked: []string{"team moved"},
		},
		"the adapter cannot write the ticket's file": {
			spoil: func(r *moveToRig) {
				r.f.rec.on("team moved", okAnswer(r.recorded()), answer{errObj: &protocol.ErrorObject{
					Code: protocol.CodeConfig, Message: "the file named by --ticket-file cannot be written", Details: map[string]string{"reason": "ticket_file_unwritable"},
				}})
			},
			code: protocol.CodeConfig, reason: reasonTicketFileUnwritable,
			says:  []string{"holds a record of a move already", "a private file for its ticket, which could not be written", "and run this again. Nothing was changed"},
			asked: []string{"team moved", "team moved"},
		},
		"the record is cleared before its ticket is read": {
			spoil: func(r *moveToRig) { r.f.rec.on("team moved", okAnswer(r.recorded()), okAnswer(r.notMoved())) },
			code:  protocol.CodeConflict, reason: reasonRecordChanged,
			says:  []string{"was cleared while this command was checking it", "Run the same command again", "Nothing was changed"},
			asked: []string{"team moved", "team moved"},
		},
		"the read of the ticket names another file": {
			spoil: func(r *moveToRig) { r.recordTicketFile = "/elsewhere/ticket" },
			code:  protocol.CodeProtocolMismatch, asked: []string{"team moved", "team moved"},
		},
		"the read of the ticket wrote no ticket": {
			spoil: func(r *moveToRig) { r.recordTicket = "" },
			code:  protocol.CodeConfig, reason: "ticket_file_missing",
			says:  []string{"its adapter did not hand over that record's ticket", "Nothing was changed"},
			asked: []string{"team moved", "team moved"},
		},
		"the read of the ticket wrote the ticket of another team": {
			spoil: func(r *moveToRig) { r.recordTicket = strings.Replace(moveToRecordTicket, setupTeamRef, "t_8", 1) },
			code:  protocol.CodeInvalidInput,
			says:  []string{"its adapter did not hand over that record's ticket", "Nothing was changed"},
			asked: []string{"team moved", "team moved"},
		},
		"the write answers another record": {
			spoil: func(r *moveToRig) {
				r.f.rec.on("team move", okAnswer(recordedOn("https://elsewhere.example.test", moveNewPin)))
			},
			code: protocol.CodeProtocolMismatch, asked: []string{"team moved", "team moved", "team move"},
		},
		"the write answers that the team has not moved": {
			spoil: func(r *moveToRig) { r.f.rec.on("team move", okAnswer(r.notMoved())) },
			code:  protocol.CodeProtocolMismatch, asked: []string{"team moved", "team moved", "team move"},
		},
		"the backend cannot be reached for the write": {
			spoil: func(r *moveToRig) {
				r.f.rec.on("team move", failAnswer(protocol.CodeUnavailable, "the backend could not be reached"))
			},
			code: protocol.CodeUnavailable, asked: []string{"team moved", "team moved", "team move"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			r.f.rec.on("team moved", okAnswer(r.recorded()))
			r.f.rec.on("team move", okAnswer(r.recorded()))
			tc.spoil(r)
			storeBefore := storeHash(t, r.f.dirs.BrigadeConfig)
			perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), tc.code, tc.reason)
			for _, need := range tc.says {
				if !strings.Contains(perr.Message, need) {
					t.Fatalf("the refusal lacks %q: %s", need, perr.Message)
				}
			}
			if got := r.asked(); !slices.Equal(got, tc.asked) {
				t.Fatalf("adapter commands: %v, want %v", got, tc.asked)
			}
			if len(r.prompts) != 0 {
				t.Fatalf("the administrator was asked: %v", r.prompts)
			}
			if got := r.fileNow(); got != r.original {
				t.Fatalf("the refusal rewrote the team file:\n%s", got)
			}
			if storeHash(t, r.f.dirs.BrigadeConfig) != storeBefore {
				t.Fatalf("the refusal wrote the store")
			}
			if info, err := os.Lstat(r.ticketFile); err == nil && info.Mode().IsRegular() {
				t.Fatalf("the check left a ticket file")
			}
			r.noSecrets()
		})
	}
}

// TestTeamMoveToRecordsAgainTheRecordItReadTheTicketOf: the record that is
// written again is the one whose ticket was read. A record the
// administrator replaced, on another machine, between the read that found
// one and the read of its ticket is written again as that second read
// answered it, and is the record the question names as the one this move
// replaces.
func TestTeamMoveToRecordsAgainTheRecordItReadTheTicketOf(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	const elsewhere = "https://elsewhere.example.test"
	r.f.rec.on("team moved", okAnswer(r.recorded()), okAnswer(recordedOn(elsewhere, "")))
	r.f.rec.on("team move", okAnswer(recordedOn(elsewhere, "")), okAnswer(r.recorded()))
	if err := Team(r.inv(true, r.args()...)); err != nil {
		t.Fatal(err)
	}
	if got := string(r.f.rec.specsOf("team move")[0].Stdin); got != `{"adapter":"rest","url":"`+elsewhere+`"}` {
		t.Fatalf("the record written again names %s", got)
	}
	if len(r.prompts) != 1 || !strings.Contains(r.prompts[0], "This replaces the record "+r.oldHost+" holds of a move to elsewhere.example.test") {
		t.Fatalf("the confirmation: %v", r.prompts)
	}
}

// TestTeamMoveToReadsTheDaysInDecimal: --ticket-days is a number of days
// in decimal digits. A zero before it does not make it octal, as the flag
// package would read it, and a number in another base or with anything but
// digits in it is refused before anything is asked.
func TestTeamMoveToReadsTheDaysInDecimal(t *testing.T) {
	t.Parallel()
	for typed, days := range map[string]string{"010": "10", "0365": "365", "7": "7", "+7": "7"} {
		t.Run(typed, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			if err := Team(r.inv(true, r.args("--ticket-days", typed)...)); err != nil {
				t.Fatal(err)
			}
			if open := r.f.rec.specsOf("team open-move"); len(open) != 1 || !strings.HasSuffix(argvOf(open[0]), "--ticket-days "+days) {
				t.Fatalf("the open: %s", argvOf(open[0]))
			}
		})
	}
	for _, typed := range []string{"0x10", "0b11", "0o17", "1_0", "7.5", "seven", "", " 7", "7 ", "1e1"} {
		t.Run("refused "+typed, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			perr := wantCodeErr(t, Team(r.inv(true, r.args("--ticket-days", typed)...)), protocol.CodeUsage, "")
			if !strings.Contains(perr.Message, "--ticket-days must be a whole number of days, in decimal digits, between 1 and 365") {
				t.Fatalf("the refusal: %s", perr.Message)
			}
			if r.f.rec.count() != 0 || len(r.prompts) != 0 {
				t.Fatalf("a refusal spawned %v and asked %d times", r.f.rec.verbs(), len(r.prompts))
			}
		})
	}
	// A number of days with another form of the command is still no form of it.
	r := newMoveToRig(t)
	wantCode(t, Team(r.inv(true, "--show", "--ticket-days", "010")), protocol.CodeUsage, "")
}

// TestTeamMoveToSaysThatTheRosterWasCutShort: the sessions on an older
// plugin are read from the old backend's roster, and a roster the backend
// cut short may lack some. The question and the lines then say that the
// list is not complete, where sessions are named and where none is, and
// the --json result says it; a whole roster says nothing of the kind.
func TestTeamMoveToSaysThatTheRosterWasCutShort(t *testing.T) {
	t.Parallel()
	const notAll = "the roster it was read from was cut short, so this list is not complete"
	cut := func(doc string) string {
		if !strings.Contains(doc, `"truncated":false`) {
			t.Fatalf("the rig's roster has no truncated member to turn: %s", doc)
		}
		return strings.Replace(doc, `"truncated":false`, `"truncated":true`, 1)
	}
	t.Run("sessions are named", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		r.f.rec.on("session list", okAnswer(cut(r.sessions())))
		if err := Team(r.inv(true, r.args()...)); err != nil {
			t.Fatal(err)
		}
		named := "billing (bob@example.com, 0.54.9), ci ([cccccccc], 0.9.1); " + notAll
		if len(r.prompts) != 1 || !strings.Contains(r.prompts[0], named+".") || !strings.Contains(r.f.out.String(), "  2 sessions that are online run a plugin older than 0.55.0") ||
			!strings.Contains(r.f.out.String(), named) {
			t.Fatalf("the confirmation: %v\nthe lines:\n%s", r.prompts, r.f.out.String())
		}
	})
	t.Run("no session is named", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		none := `{"team_ref":"` + setupTeamRef + `","team_name":"devs","server_time":"2026-11-14T12:00:00Z","truncated":true,"sessions":[]}`
		r.f.rec.on("session list", okAnswer(none))
		if err := Team(r.inv(true, r.args()...)); err != nil {
			t.Fatal(err)
		}
		said := "no session that is online was seen on a plugin older than 0.55.0, but " + notAll
		if len(r.prompts) != 1 || !strings.Contains(r.prompts[0], said+".") || !strings.Contains(r.f.out.String(), "  "+said) {
			t.Fatalf("the confirmation: %v\nthe lines:\n%s", r.prompts, r.f.out.String())
		}
	})
	t.Run("with --json", func(t *testing.T) {
		t.Parallel()
		for _, truncated := range []bool{true, false} {
			r := newMoveToRig(t)
			if truncated {
				r.f.rec.on("session list", okAnswer(cut(r.sessions())))
			}
			iv := r.inv(true, r.args()...)
			iv.JSON = true
			if err := Team(iv); err != nil {
				t.Fatal(err)
			}
			if ok, result := envelopeOf(t, r.f.out.String()); !ok || result["roster_truncated"] != truncated {
				t.Fatalf("the result of a roster cut short=%v: %v", truncated, result)
			}
		}
	})
	t.Run("a whole roster", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		if err := Team(r.inv(true, r.args()...)); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(r.prompts[0]+r.f.out.String(), "cut short") {
			t.Fatalf("a whole roster is said to be cut short:\n%s\n%s", r.prompts[0], r.f.out.String())
		}
	})
}

// TestTeamMoveToShowsThePathPrefixOfTheAddress: an address with a path
// after its host names a server at that path, and the path is part of
// where every member's session is sent. The question shows it, as the
// lines after it and the record's own reading do; an address that is a
// host alone is shown as a host alone, with no slash after it.
func TestTeamMoveToShowsThePathPrefixOfTheAddress(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	typed := r.newURL + "/brigade/ops"
	r.f.rec.on("team move", okAnswer(r.notMoved()), okAnswer(recordedOn(typed, moveNewPin)))
	if err := Team(r.inv(true, "--to", typed+"/", "--secret-file", r.secretFile, "--certificate", moveNewPin)); err != nil {
		t.Fatal(err)
	}
	where := r.newHost + `/brigade/ops via adapter "rest"`
	if len(r.prompts) != 1 || !strings.Contains(r.prompts[0], `) from `+r.oldHost+` to `+where) {
		t.Fatalf("the confirmation does not show the path: %v", r.prompts)
	}
	if out := r.f.out.String(); !strings.Contains(out, "moved to "+where) {
		t.Fatalf("the lines do not show the path:\n%s", out)
	}
	if got := string(r.f.rec.specsOf("team move")[1].Stdin); got != `{"adapter":"rest","url":"`+typed+`","certificate":"`+moveNewPin+`"}` {
		t.Fatalf("the destination recorded: %s", got)
	}
	// A host alone, with the slash a person types after it.
	plain := newMoveToRig(t)
	if err := Team(plain.inv(true, "--to", plain.newURL+"/", "--secret-file", plain.secretFile, "--certificate", moveNewPin)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain.prompts[0], ` to `+plain.newHost+` via adapter "rest"`) || strings.Contains(plain.prompts[0], plain.newHost+"/") {
		t.Fatalf("the confirmation of a host alone: %v", plain.prompts)
	}
	for raw, want := range map[string]string{
		"https://h.example": "", "https://h.example/": "", "https://h.example//": "", "https://h.example/a": "/a", "https://h.example/a/b/": "/a/b",
		"h.example/a": "", "": "",
	} {
		if got := pathPrefix(raw); got != want {
			t.Errorf("pathPrefix(%q) = %q, want %q", raw, got, want)
		}
	}
}
