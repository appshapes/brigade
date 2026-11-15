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
