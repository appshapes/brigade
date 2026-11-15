//go:build !windows

package hook

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/testutil"
)

// A start that keeps a running session where it is says what is true of
// that session (work item 327, ADR 0008): the start resolved the checkout
// its own directory is in, and the session took nothing of it.

// otherSettings gives a team file every note a start says of one: members
// this version does not define, a sync member with folders, a polling
// member that sets a heartbeat and a gateway member that cannot be used.
func otherSettings(file map[string]any) map[string]any {
	file["sync"] = map[string]any{"folders": []string{"docs/shared"}}
	file["polling"] = map[string]any{"heartbeat_seconds": 45}
	file["gateway"] = map[string]any{}
	file["not_a_member"] = true
	return file
}

// noProgram is a machine with no program to play a sound or show a
// notification: a start whose option is on says so in a line.
func noProgram(string) ([]string, string) { return nil, "no program here" }

// theOthers are the words of the lines a start says of what it resolved,
// none of which is true of a session that took nothing of it.
var theOthers = []string{"file sync", "message sound", "message notification", "inbound policy is refuse", "team_inbound",
	"not_a_member", "gateway member", "polling member"}

// saysNoneOfTheOthers fails when out carries a line of what a start
// resolved for the checkout it came from.
func saysNoneOfTheOthers(t *testing.T, when, out string) {
	t.Helper()
	for _, words := range theOthers {
		if strings.Contains(out, words) {
			t.Fatalf("%s said %q of a session that runs under none of it:\n%s", when, words, out)
		}
	}
}

// TestAStartFromAnotherCheckoutSaysNothingOfThatCheckoutsSettings: `/clear`
// while the model works in a checkout that is on another backend keeps the
// session where it is. That checkout's team file lists folders to sync,
// sets a heartbeat, carries a gateway that cannot be used and a member this
// version does not define; the member's options at this start turn the
// sound and the notification on, on a machine with no program for either,
// and misspell team_inbound. The session runs under none of it: its map is
// as it was, with no file sync and the policy accept, and its watcher was
// started under that map. So the start says the context line of the
// session and nothing of what it resolved, on Claude Code and on Muse Code,
// where the arming line follows the context line.
func TestAStartFromAnotherCheckoutSaysNothingOfThatCheckoutsSettings(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostClaude, config.HostMuse} {
		t.Run(string(host), func(t *testing.T) {
			t.Parallel()
			f := movingFixture(t, host)
			f.noAdapterOption = true
			f.deps.SoundPlayer, f.deps.Notifier = noProgram, noProgram
			seam := registerSeam(t, f)
			startMember(t, f)
			before, oldPin := f.mustMap(), f.checkoutPin()
			if before.SyncAdapter != "" || before.Inbound != "accept" || before.MessageSound || before.HeartbeatSeconds != 0 {
				t.Fatalf("the session before the start: %+v", before)
			}
			f.memberOf("team-1")
			other, _ := f.addCheckout("on-the-server", otherSettings(serverFile("team-1")), serverPin("team-1", oldPin.URL))
			registers, spawns := len(seam.callsFor("session register")), f.spawner.count()
			f.nativeID, f.cwd = otherConversation, other
			exit, out, errOut := f.run(SubSessionStart, f.sessionDoc("clear"),
				config.OptionMessageSound+"=on", config.OptionMessageNotification+"=on", config.OptionTeamInbound+"=bogus", config.OptionSync+"=on")
			// The name is the one the map has after the start: a host that
			// gives a session no title names it after the start's directory.
			want := []string{startLine(f.mustMap().SessionName, "brigade-sess-1", teamName, "accept")}
			if host == config.HostMuse {
				want = append(want, armLine(f.pluginBin))
			}
			if exit != 0 || !slices.Equal(lines(out), want) {
				t.Fatalf("exit %d, lines\n%q\nwant\n%q\n%s", exit, lines(out), want, errOut)
			}
			saysNoneOfTheOthers(t, "the start", out)
			m := f.mustMap()
			if m.TeamKey != before.TeamKey || m.BrigadeSessionID != "brigade-sess-1" || m.SyncAdapter != "" || m.Inbound != "accept" || m.MessageSound ||
				m.HeartbeatSeconds != 0 || m.ClaudeSessionID != otherConversation || len(seam.callsFor("session register")) != registers || f.spawner.count() != spawns {
				t.Fatalf("the map after the start: %+v", m)
			}
		})
	}
}

// TestAStartThatKeepsASessionSaysTheFileSyncTheSessionHas: the session was
// started in a checkout whose team file lists two folders, and its watcher
// syncs them. A `/clear` from a checkout on another backend whose file
// lists one keeps the session, and the line the conversation that begins
// is given is the session's own: two folders, from its map. The same after
// the session's own checkout followed its team to a file that lists one
// folder and sets a heartbeat while a message is held for the session: the
// start keeps it where it is and says what a prompt says of the move, the
// context line, and the file sync the session has, never the new file's.
func TestAStartThatKeepsASessionSaysTheFileSyncTheSessionHas(t *testing.T) {
	t.Parallel()
	const own = "Brigade: file sync on: 2 folder(s) through syncthing."
	twoFolders := func(t *testing.T, f *fixture) {
		t.Helper()
		doc := fileOf(f.checkoutPin())
		doc["sync"] = map[string]any{"folders": []string{"docs/shared", "notes"}}
		if err := os.WriteFile(filepath.Join(f.cwd, teamfile.FileName), mustJSON(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a start from a checkout on another backend", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		twoFolders(t, f)
		registerSeam(t, f)
		startMember(t, f)
		before, oldPin := f.mustMap(), f.checkoutPin()
		if before.SyncAdapter == "" || len(before.SyncFolders) != 2 {
			t.Fatalf("the session's file sync before the start: %q %q", before.SyncAdapter, before.SyncFolders)
		}
		f.memberOf("team-1")
		other, _ := f.addCheckout("on-the-server", otherSettings(serverFile("team-1")), serverPin("team-1", oldPin.URL))
		spawns := f.spawner.count()
		f.nativeID, f.cwd = otherConversation, other
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		want := []string{startLine("payments-api", "brigade-sess-1", teamName, "accept"), own}
		if exit != 0 || !slices.Equal(lines(out), want) {
			t.Fatalf("exit %d, lines\n%q\nwant\n%q\n%s", exit, lines(out), want, errOut)
		}
		// The watcher that syncs the two folders is the one the session had.
		if m := f.mustMap(); !slices.Equal(m.SyncFolders, before.SyncFolders) || m.SyncRoot != before.SyncRoot || f.spawner.count() != spawns {
			t.Fatalf("the session's file sync after the start: %q under %q, %d watcher(s) started", m.SyncFolders, m.SyncRoot, f.spawner.count()-spawns)
		}
	})

	t.Run("a start in its own checkout, which followed, while a message is held", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		twoFolders(t, f)
		seam := registerSeam(t, f)
		startMember(t, f)
		before, oldPin := f.mustMap(), f.checkoutPin()
		// The checkout is on the server now, by its member's own join, under
		// a file that lists one folder and sets a heartbeat.
		f.memberOf("team-1")
		if err := os.WriteFile(filepath.Join(f.cwd, teamfile.FileName), mustJSON(otherSettings(serverFile("team-1"))), 0o600); err != nil {
			t.Fatal(err)
		}
		tf := f.movedFile()
		f.addCheckoutPin(f.canon(), serverPin("team-1", ""))
		entries := heldEntries(1, "payments-api")
		if err := pendingStoreOf(f).Save(inbound.PendingFile{Entries: entries, UpdatedAt: fixedTime}); err != nil {
			t.Fatal(err)
		}
		registers, spawns := len(seam.callsFor("session register")), f.spawner.count()
		f.nativeID = otherConversation
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		want := []string{rejoinedLineOf(thisCheckout, tf), moveWaitsLine(1), startLine("payments-api", "brigade-sess-1", teamName, "accept"), own}
		if exit != 0 || !slices.Equal(lines(out), want) {
			t.Fatalf("exit %d, lines\n%q\nwant\n%q\n%s", exit, lines(out), want, errOut)
		}
		if strings.Contains(out, "1 folder(s)") || strings.Contains(out, "polling member") || strings.Contains(out, "not_a_member") || strings.Contains(out, "gateway member") {
			t.Fatalf("the start said the notes of a team file the session does not run under:\n%s", out)
		}
		if m := f.mustMap(); m.TeamKey != before.TeamKey || !slices.Equal(m.SyncFolders, before.SyncFolders) || m.HeartbeatSeconds != 0 ||
			len(seam.callsFor("session register")) != registers || f.spawner.count() != spawns || oldPin.URL == "" {
			t.Fatalf("the map after the start: %+v", m)
		}
	})
}

// TestAStartThatKeepsASessionSaysWhatHoldsForItWhateverWasResolved: of what
// the start did and read, a kept session is told what holds for it all the
// same. The start wrote the member's user settings file, because the
// conversation that begins bypasses permission prompts: that is a write,
// and is said. Another `brigade` stands before the plugin's on the
// session's PATH. And two options are read where they are used and not
// frozen in the session's map: follow_team_move at every prompt, and
// team_move_in_session from the start facts this start wrote. Each of their
// lines follows the context line, in the order a start says them.
func TestAStartThatKeepsASessionSaysWhatHoldsForItWhateverWasResolved(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	realSettings(f)
	if err := os.WriteFile(f.userSettingsFile(), []byte("{\n  \"theme\": \"dark\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.deps.SoundPlayer, f.deps.Notifier = noProgram, noProgram
	registerSeam(t, f)
	startMember(t, f)
	oldPin := f.checkoutPin()
	f.memberOf("team-1")
	other, _ := f.addCheckout("on-the-server", otherSettings(serverFile("team-1")), serverPin("team-1", oldPin.URL))
	dir := filepath.Join(f.dirs.Root, "path-bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(dir, "brigade")
	testutil.WriteExecutable(t, decoy, []byte("#!/bin/sh\nexit 0\n"))
	f.nativeID, f.cwd = otherConversation, other
	exit, out, errOut := f.run(SubSessionStart, f.startDocMode("clear", "bypassPermissions"),
		"PATH="+dir+string(os.PathListSeparator)+f.emptyPath(),
		config.OptionFollowTeamMove+"=perhaps", config.OptionTeamMoveInSession+"=perhaps",
		config.OptionMessageSound+"=perhaps", config.OptionSync+"=perhaps", config.OptionMessageInterval+"=never")
	want := []string{
		startLine("payments-api", "brigade-sess-1", teamName, "accept"),
		policy.EnsureResult{Outcome: policy.EnsureAdded, File: f.userSettingsFile()}.Line(),
		shadowLine(decoy),
		config.WarnFollowTeamMoveInvalid,
		config.WarnTeamMoveInSessionInvalid,
	}
	if exit != 0 || !slices.Equal(lines(out), want) {
		t.Fatalf("exit %d, lines\n%q\nwant\n%q\n%s", exit, lines(out), want, errOut)
	}
	if data, err := os.ReadFile(f.userSettingsFile()); err != nil || !strings.Contains(string(data), `"crossSessionInbound": "accept"`) {
		t.Fatalf("the user settings file after the start: %q, %v", data, err)
	}
}

// TestAFollowsLineIsSaidByAStartThatThenKeepsTheSession: a start over a
// running session holds what its resolution says until it is known whether
// the session is kept, and a kept start drops it. One line among them is
// not the resolution's to drop: the line of a follow. A `/clear` in a
// checkout whose team moved follows in its own run, joins the new backend
// with the move's ticket and writes the pin; or it finds the pin a child
// wrote, with the mark the child left, and takes the mark away. When the
// member's settings are then refused (a frame_file that is not there) the
// session is kept, and the follow happened all the same: the start says its
// line before the context line, as a start that follows does, with how the
// machine joined. The conversation has then heard it, and the prompt that
// moves the session does not say that the machine "had joined it there
// already", which is all that is left to say once the mark is gone.
func TestAFollowsLineIsSaidByAStartThatThenKeepsTheSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// asked is true when a prompt started the follow as a child before
		// the start: the start then finds the pin written and the mark left.
		asked bool
	}{
		{"the start follows in its own run", false},
		{"the start finds the mark a child left", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
			before := f.checkoutPin()
			f.moveTeamFile("team-1")
			tf := f.movedFile()
			mark := config.MoveFollowedMark(f.stateDir, f.configDir, f.canon())
			if tc.asked {
				if got := f.prompted(); !slices.Equal(got, []string{movedLine}) || !exists(mark) {
					t.Fatalf("the prompt that started the follow: %q; a mark is left: %v", got, exists(mark))
				}
			}
			missing := config.OptionFrameFile + "=" + filepath.Join(t.TempDir(), "no-such-clause.txt")
			f.nativeID = otherConversation
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"), missing)
			want := []string{
				followedLine(tf, &before, true, "brigade"),
				startLine("payments-api", "brigade-sess-1", teamName, "accept"),
				"Brigade: this session stays connected as it was. The `frame_file` option cannot be used (frame_file_unreadable), so the frame your options name now is not in force here: team messages still reach this session under the frame it had. Fix the `frame` or `frame_file` option, then `/clear` or start a new session.",
			}
			if exit != 0 || !slices.Equal(lines(out), want) {
				t.Fatalf("exit %d, lines\n%q\nwant\n%q\n%s", exit, lines(out), want, errOut)
			}
			// The checkout followed, the machine joined once, the session is
			// where it was, and no mark is left to be said a second time.
			if pin := f.checkoutPin(); !pin.Matches(tf) || len(seam.callsFor("team join")) != 1 || f.mustMap().BrigadeSessionID != "brigade-sess-1" || exists(mark) {
				t.Fatalf("after the start: pin %+v, %d joins, session %q, a mark is left: %v", pin, len(seam.callsFor("team join")), f.mustMap().BrigadeSessionID, exists(mark))
			}
			// The setting is fixed: the next prompt moves the session, and has
			// nothing to say of how the checkout arrived.
			got := f.prompted()
			if !slices.Equal(got, []string{startLine("payments-api", "brigade-sess-2", teamName, "accept")}) {
				t.Fatalf("the prompt after the start:\n%q", got)
			}
			if strings.Contains(strings.Join(got, "\n"), "had joined it there already") {
				t.Fatalf("the prompt said that the machine had joined already: %q", got)
			}
		})
	}
}

// TestAFollowOfAnotherCheckoutIsSaidAndOwesTheSessionsOwnNothing: the
// follow a kept start says can be of another checkout than the session's
// own: a `/clear` while the model works in a second checkout of the team,
// whose team file names the server, follows for that checkout. The line is
// that checkout's and is said. It is not the word of how the session's own
// checkout arrived: when that one follows later, the prompt that moves the
// session still says so.
func TestAFollowOfAnotherCheckoutIsSaidAndOwesTheSessionsOwnNothing(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	home, before := f.cwd, f.checkoutPin()
	other, _ := f.addCheckout("moved-first", serverFile("team-1"), pinDoc(before))
	otherFile, err := teamfile.Parse(filepath.Join(other, teamfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	missing := config.OptionFrameFile + "=" + filepath.Join(t.TempDir(), "no-such-clause.txt")
	f.nativeID, f.cwd = otherConversation, other
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"), missing)
	f.cwd = home
	got := lines(out)
	if exit != 0 || len(got) != 3 || got[0] != followedLine(otherFile, &before, true, "brigade") || got[1] != startLine("payments-api", "brigade-sess-1", teamName, "accept") {
		t.Fatalf("exit %d, lines\n%q\n%s", exit, got, errOut)
	}
	if n := len(seam.callsFor("team join")); n != 1 || f.mustMap().BrigadeSessionID != "brigade-sess-1" {
		t.Fatalf("after the start: %d joins, session %q", n, f.mustMap().BrigadeSessionID)
	}
	// The session's own checkout follows: the machine is a member by now.
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("the first prompt after its own checkout's file changed: %q", got)
	}
	want := []string{followedLine(tf, &before, false, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
	if got := f.prompted(); !slices.Equal(got, want) {
		t.Fatalf("the prompt that moved the session:\n%q\nwant\n%q", got, want)
	}
}
