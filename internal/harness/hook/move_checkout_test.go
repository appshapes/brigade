//go:build !windows

package hook

import (
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

// A session moves with the checkout it registered in, and no other (work
// item 327, ADR 0008, Decision 8). A prompt's directory follows the model:
// a `cd`, a worktree. While a team's move is under way a member can have
// two checkouts of the team on two backends, one with the changed file and
// one without, and a session that moved on whichever its last prompt came
// from went back and forth between them, a new session each time. These
// tests give the fixture's member more checkouts and send the session's
// prompts from them.

// serverFile is a team file that names the server for the team ref names.
func serverFile(ref string) map[string]any {
	return map[string]any{"version": 1, "adapter": "rest", "url": movedURL, "certificate": movedPin, "team_ref": ref, "team_name": teamName}
}

// serverPin is a checkout's pin on the server for the team ref names; from
// is the backend a follow moved it from, "" for a pin the member's own
// join wrote.
func serverPin(ref, from string) map[string]any {
	pin := map[string]any{"adapter": "rest", "url": movedURL, "certificate": movedPin, "team_ref": ref, "consented_at": "2026-11-14T12:00:00Z"}
	if from != "" {
		pin["moved_from"] = from
	}
	return pin
}

// pinDoc is a pin as the pins file holds it.
func pinDoc(pin teamstore.Pin) map[string]any {
	return map[string]any{"adapter": pin.Adapter, "url": pin.URL, "publishable_key": pin.PublishableKey, "team_ref": pin.TeamRef, "consented_at": "2026-09-06T18:00:00Z"}
}

// fileOf is the team file of a checkout joined as pin says: what the
// fixture's own checkout holds before its team moves.
func fileOf(pin teamstore.Pin) map[string]any {
	return map[string]any{"version": 1, "adapter": pin.Adapter, "url": pin.URL, "publishable_key": pin.PublishableKey, "team_ref": pin.TeamRef, "team_name": teamName}
}

// addCheckout gives the fixture's member another checkout: a directory with
// file as its team file and, when pin is not nil, pin as its pin in the
// member's store, written by hand as the fixture's own is. It returns the
// directory and its canonical path.
func (f *fixture) addCheckout(name string, file, pin map[string]any) (dir, canon string) {
	f.t.Helper()
	dir = filepath.Join(f.t.TempDir(), name)
	//nolint:gosec // G301: a checkout tree is an ordinary directory
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, teamfile.FileName), mustJSON(file), 0o600); err != nil {
		f.t.Fatal(err)
	}
	canon, err := teamfile.Canonicalize(dir)
	if err != nil {
		f.t.Fatal(err)
	}
	if pin != nil {
		f.addCheckoutPin(canon, pin)
	}
	return dir, canon
}

// addCheckoutPin writes pin as the pin of the checkout canon names in the
// member's store, beside the pins that are there.
func (f *fixture) addCheckoutPin(canon string, pin map[string]any) {
	f.t.Helper()
	data, err := os.ReadFile(teamstore.PinsPath(f.configDir))
	if err != nil {
		f.t.Fatal(err)
	}
	var pins struct {
		Version  int                       `json:"version"`
		Projects map[string]map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(data, &pins); err != nil {
		f.t.Fatal(err)
	}
	pins.Projects[canon] = pin
	if err := adapterkit.WriteAtomic(teamstore.PinsPath(f.configDir), mustJSON(&pins)); err != nil {
		f.t.Fatal(err)
	}
}

// memberOf makes this machine a member of the team ref names on the server.
func (f *fixture) memberOf(ref string) {
	f.t.Helper()
	if err := adapterkit.SaveProfile(f.configDir, teamstore.Key("rest", movedURL, ref), &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "rest", URL: movedURL, Certificate: movedPin,
		TeamRef: ref, TeamName: teamName, PrincipalRef: "p-new",
	}); err != nil {
		f.t.Fatal(err)
	}
}

// registerSeam is memberSeam with four registrations to answer,
// brigade-sess-1 to brigade-sess-4: more than any of these tests may make.
func registerSeam(t *testing.T, f *fixture) *adapterSeam {
	t.Helper()
	var answers []fakeadapter.Response
	for _, id := range []string{"brigade-sess-1", "brigade-sess-2", "brigade-sess-3", "brigade-sess-4"} {
		answers = append(answers, okResp(registerDoc(id, "payments-api", false)))
	}
	return memberSeam(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{"session register": answers})
}

// withoutCheckout rewrites the session's map as one written before the
// map named its checkout: what a session that was running when its plugin
// was updated has.
func (f *fixture) withoutCheckout() {
	f.t.Helper()
	m := f.mustMap()
	m.Checkout = ""
	if err := f.store().WriteByPID(m); err != nil {
		f.t.Fatal(err)
	}
}

// untouched asserts that a prompt from dir says nothing, starts no follow,
// registers nothing, asks the old backend nothing, records no line about a
// move and leaves the session's map naming the session, the backend and
// the checkout it named.
func (f *fixture) untouched(seam *adapterSeam, dir, when string) {
	f.t.Helper()
	before, registers, follows, home := *f.mustMap(), len(seam.callsFor("session register")), len(f.spawner.followed()), f.cwd
	asks := len(seam.callsFor("team moved"))
	stamp, _ := os.ReadFile(config.TeamMovedStamp(f.stateDir, f.pid))
	f.cwd = dir
	got := f.prompted()
	f.cwd = home
	if len(got) != 0 {
		f.t.Fatalf("%s: the prompt said %q", when, got)
	}
	if n := len(f.spawner.followed()); n != follows {
		f.t.Fatalf("%s: %d follow children started", when, n-follows)
	}
	if n := len(seam.callsFor("session register")); n != registers || len(seam.callsFor("team moved")) != asks {
		f.t.Fatalf("%s: %d registrations and %d questions to the old backend", when, n-registers, len(seam.callsFor("team moved"))-asks)
	}
	if m := f.mustMap(); m.TeamKey != before.TeamKey || m.BrigadeSessionID != before.BrigadeSessionID || m.Checkout != before.Checkout {
		f.t.Fatalf("%s: the map names %q, %q and the checkout %q; it named %q, %q and %q", when, m.TeamKey, m.BrigadeSessionID, m.Checkout, before.TeamKey, before.BrigadeSessionID, before.Checkout)
	}
	if after, _ := os.ReadFile(config.TeamMovedStamp(f.stateDir, f.pid)); string(after) != string(stamp) {
		f.t.Fatalf("%s: the prompt recorded a line about a move: %q", when, after)
	}
}

// TestASessionMovesWithTheCheckoutItRegisteredInAndNoOther: the session's
// map names the checkout it registered in. Prompts from the member's other
// checkouts of the team — one that has followed to the server, one whose
// file names the server and has not followed, one still on the backend the
// team left — say nothing, start no follow, ask nobody and leave the
// session where it is. When the session's own checkout has the changed
// file it follows in its two steps, and after that a prompt from a
// checkout that is still on the old backend does not take it back: across
// all of it the session is registered twice, once at its start and once
// when its own checkout moved.
func TestASessionMovesWithTheCheckoutItRegisteredInAndNoOther(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := registerSeam(t, f)
	startMember(t, f)
	own, oldPin, newKey := f.canon(), f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
	if m := f.mustMap(); m.Checkout != own {
		t.Fatalf("the registration recorded the checkout %q, want %q", m.Checkout, own)
	}
	// The member's other checkouts of the team.
	f.memberOf("team-1")
	followed, _ := f.addCheckout("followed", serverFile("team-1"), serverPin("team-1", oldPin.URL))
	changed, changedCanon := f.addCheckout("changed", serverFile("team-1"), pinDoc(oldPin))
	behind, _ := f.addCheckout("behind", fileOf(oldPin), pinDoc(oldPin))

	f.untouched(seam, followed, "a prompt from a checkout that followed to the server")
	f.untouched(seam, changed, "a prompt from a checkout whose file names the server and has not followed")
	if pin, ok, err := teamstore.LookupPin(f.configDir, changedCanon); err != nil || !ok || pin.URL != oldPin.URL {
		t.Fatalf("the pin of a checkout the session is not of: %+v, %v", pin, err)
	}
	f.untouched(seam, behind, "a prompt from a checkout still on the old backend")
	f.untouched(seam, f.cwd, "a prompt from the session's own checkout, its file unchanged")

	// The session's own checkout gets the changed file: it follows.
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) || len(f.spawner.followed()) != 1 {
		t.Fatalf("the first prompt of its own checkout after the file changed: %q, %d follows", got, len(f.spawner.followed()))
	}
	// The machine is a member there already, by the checkout that followed.
	got := f.prompted()
	want := []string{followedLine(tf, &oldPin, false, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
	if !slices.Equal(got, want) {
		t.Fatalf("the second prompt:\n%q\nwant\n%q", got, want)
	}
	if m := f.mustMap(); m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" || m.Checkout != own {
		t.Fatalf("the map after the move: %+v", m)
	}

	// It does not go back, wherever its prompts come from.
	for _, dir := range []string{behind, followed, behind, changed, behind} {
		f.untouched(seam, dir, "a prompt from "+filepath.Base(dir)+" after the session moved")
	}
	if n := len(seam.callsFor("session register")); n != 2 {
		t.Fatalf("the session was registered %d times, want twice: at its start and when its own checkout moved", n)
	}
}

// TestASessionFollowsItsOwnCheckoutWhereverItsPromptsComeFrom: a session
// that was started in a main checkout and works in a worktree, or in
// another repository, sends every prompt from somewhere else, and its
// checkout is still its own. Nothing about a move is read from the
// directory a prompt names: the file and the pin are the map's checkout's.
// So with its own checkout's file changed the session follows in its two
// steps from wherever its prompts come: from a checkout of the team that is
// still on the old backend, from one that is not joined, from one of
// another team, from a directory that is no checkout at all. The child is
// started for the session's own checkout, the registration is resolved from
// it, and the map still names it. After that no prompt from any of those
// directories moves the session again.
func TestASessionFollowsItsOwnCheckoutWhereverItsPromptsComeFrom(t *testing.T) {
	t.Parallel()
	elsewhere := func(t *testing.T, f *fixture) map[string]string {
		t.Helper()
		oldPin := f.checkoutPin()
		f.memberOf("team-2")
		behind, _ := f.addCheckout("behind", fileOf(oldPin), pinDoc(oldPin))
		unjoined, _ := f.addCheckout("unjoined", serverFile("team-1"), nil)
		other, _ := f.addCheckout("other-team", serverFile("team-2"), serverPin("team-2", ""))
		bare := filepath.Join(t.TempDir(), "no-checkout")
		if err := os.MkdirAll(bare, 0o700); err != nil {
			t.Fatal(err)
		}
		return map[string]string{
			"a checkout still on the old backend": behind,
			"a checkout that is not joined":       unjoined,
			"a checkout of another team":          other,
			"a directory that is no checkout":     bare,
		}
	}
	// from sends one prompt of the session from dir.
	from := func(f *fixture, dir string) []string {
		home := f.cwd
		f.cwd = dir
		defer func() { f.cwd = home }()
		return f.prompted()
	}
	for _, where := range []string{
		"a checkout still on the old backend", "a checkout that is not joined", "a checkout of another team", "a directory that is no checkout",
	} {
		t.Run(where, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			seam := registerSeam(t, f)
			startMember(t, f)
			own, oldPin, newKey := f.canon(), f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
			dirs := elsewhere(t, f)
			dir := dirs[where]
			// The session's own checkout gets the changed file; the session is
			// working elsewhere.
			f.moveTeamFile("team-1")
			tf := f.movedFile()
			if got := from(f, dir); !slices.Equal(got, []string{movedLine}) {
				t.Fatalf("the first prompt from %s: %q", where, got)
			}
			follows := f.spawner.followed()
			if len(follows) != 1 || !slices.Contains(follows[0].Args, own) {
				t.Fatalf("the follow that was started: %d of them, %v: want one, for the session's own checkout", len(follows), follows)
			}
			if pin := f.checkoutPin(); !pin.Matches(tf) {
				t.Fatalf("the session's own checkout did not follow: %+v", pin)
			}
			// The line names the checkout that followed as the session's own:
			// said of the directory this prompt comes from, "this checkout"
			// would be untrue.
			got := from(f, dir)
			want := []string{followedLineOf(startedCheckout, tf, &oldPin, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
			if !slices.Equal(got, want) || strings.Contains(got[0], thisCheckout) {
				t.Fatalf("the second prompt from %s:\n%q\nwant\n%q", where, got, want)
			}
			m := f.mustMap()
			if m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" || m.Checkout != own {
				t.Fatalf("the map after the move: key %q, session %q, checkout %q", m.TeamKey, m.BrigadeSessionID, m.Checkout)
			}
			// The registration was resolved from the session's own checkout, not
			// from the directory the prompt named.
			regs := seam.callsFor("session register")
			if reg := registration(t, regs[len(regs)-1]); reg.WorkspaceLabel == nil || *reg.WorkspaceLabel != filepath.Base(own) {
				t.Fatalf("the move's registration carries the workspace label %v, want the session's own checkout's, %q", reg.WorkspaceLabel, filepath.Base(own))
			}
			// It does not move again, wherever its prompts come from.
			for name, d := range dirs {
				f.untouched(seam, d, "a prompt from "+name+" after the move")
			}
			f.untouched(seam, f.cwd, "a prompt from its own checkout after the move")
			if n := len(seam.callsFor("session register")); n != 2 {
				t.Fatalf("the session was registered %d times, want twice", n)
			}
		})
	}

	// A prompt from a directory inside the session's own checkout says "this
	// checkout", as it always has.
	t.Run("a directory of its own checkout", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		registerSeam(t, f)
		startMember(t, f)
		oldPin := f.checkoutPin()
		inside := filepath.Join(f.cwd, "cmd", "tool")
		//nolint:gosec // G301: a checkout tree is an ordinary directory
		if err := os.MkdirAll(inside, 0o750); err != nil {
			t.Fatal(err)
		}
		f.moveTeamFile("team-1")
		tf := f.movedFile()
		from(f, inside)
		got := from(f, inside)
		want := []string{followedLine(tf, &oldPin, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
		if !slices.Equal(got, want) {
			t.Fatalf("the second prompt from a directory of its own checkout:\n%q\nwant\n%q", got, want)
		}
	})

	// The same for a member who joined the new backend in the session's
	// checkout with a command of their own: the pin is their consent, and the
	// session moves at its next prompt, sent from another checkout.
	t.Run("the member's own join, and a prompt from another checkout", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		seam := registerSeam(t, f)
		startMember(t, f)
		own, newKey := f.canon(), teamstore.Key("rest", movedURL, "team-1")
		dirs := elsewhere(t, f)
		f.moveTeamFile("team-1")
		tf := f.movedFile()
		f.memberOf("team-1")
		// The pins file as it is, with the session's own checkout joined to
		// the server by a pin that moved from nowhere.
		f.addCheckoutPin(own, serverPin("team-1", ""))
		got := from(f, dirs["a checkout still on the old backend"])
		want := []string{rejoinedLineOf(startedCheckout, tf), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
		if !slices.Equal(got, want) || len(f.spawner.followed()) != 0 {
			t.Fatalf("the prompt after the member's own join:\n%q\nwant\n%q; %d follows", got, want, len(f.spawner.followed()))
		}
		if m := f.mustMap(); m.TeamKey != newKey || m.Checkout != own || len(seam.callsFor("session register")) != 2 {
			t.Fatalf("the map after the move: key %q, checkout %q, %d registrations", m.TeamKey, m.Checkout, len(seam.callsFor("session register")))
		}
	})
}

// TestAMapWithNoCheckoutIsGivenTheFirstOneOfItsTeam: a session that was
// running when its plugin was updated has a map that names no checkout.
// The first prompt that comes from a checkout joined on this machine to
// the session's team gives it that one, and from then on the session is of
// that checkout and no other. So it cannot go back and forth either: at
// most it moves once, with the checkout its first prompt came from.
func TestAMapWithNoCheckoutIsGivenTheFirstOneOfItsTeam(t *testing.T) {
	t.Parallel()

	t.Run("its own checkout, its file unchanged", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		seam := registerSeam(t, f)
		startMember(t, f)
		own, oldPin := f.canon(), f.checkoutPin()
		f.memberOf("team-1")
		followed, _ := f.addCheckout("followed", serverFile("team-1"), serverPin("team-1", oldPin.URL))
		f.withoutCheckout()
		if got := f.prompted(); len(got) != 0 {
			t.Fatalf("the prompt that recorded the checkout said %q", got)
		}
		if m := f.mustMap(); m.Checkout != own || m.BrigadeSessionID != "brigade-sess-1" {
			t.Fatalf("the map after the first prompt: checkout %q, session %q", m.Checkout, m.BrigadeSessionID)
		}
		for range 2 {
			f.untouched(seam, followed, "a prompt from another checkout, once the session's own is on record")
		}
	})

	t.Run("another checkout, which is on the server", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		seam := registerSeam(t, f)
		startMember(t, f)
		home, oldPin, newKey := f.cwd, f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
		f.memberOf("team-1")
		followed, followedCanon := f.addCheckout("followed", serverFile("team-1"), serverPin("team-1", oldPin.URL))
		f.withoutCheckout()
		// The first prompt comes from the checkout that followed: the session
		// is that checkout's from now on, and moves with it.
		f.cwd = followed
		got := f.prompted()
		f.cwd = home
		if len(got) != 2 || got[1] != startLine("payments-api", "brigade-sess-2", teamName, "accept") {
			t.Fatalf("the first prompt: %q", got)
		}
		if m := f.mustMap(); m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" || m.Checkout != followedCanon {
			t.Fatalf("the map after the first prompt: %+v", m)
		}
		// The checkout it started in is still on the old backend, and its
		// prompts come from there and from the other in turn: it stays.
		for _, dir := range []string{home, followed, home, followed, home} {
			f.untouched(seam, dir, "a prompt from "+filepath.Base(dir)+" after the one move")
		}
		if n := len(seam.callsFor("session register")); n != 2 {
			t.Fatalf("the session was registered %d times, want twice", n)
		}
	})

	t.Run("a checkout that is not joined, or is joined to another team, is not it", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		seam := registerSeam(t, f)
		startMember(t, f)
		own := f.canon()
		f.memberOf("team-1")
		f.memberOf("team-2")
		unjoined, _ := f.addCheckout("unjoined", serverFile("team-1"), nil)
		// Its file names the session's team; the member joined another there.
		drifted, _ := f.addCheckout("drifted", serverFile("team-1"), serverPin("team-2", ""))
		f.withoutCheckout()
		for _, dir := range []string{unjoined, drifted} {
			f.untouched(seam, dir, "a prompt from "+filepath.Base(dir))
			if m := f.mustMap(); m.Checkout != "" {
				t.Fatalf("a prompt from %s recorded the checkout %q", filepath.Base(dir), m.Checkout)
			}
		}
		f.prompted()
		if m := f.mustMap(); m.Checkout != own {
			t.Fatalf("the session's own checkout was not recorded: %q", m.Checkout)
		}
	})

	t.Run("a checkout that cannot be recorded moves nobody", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		seam := registerSeam(t, f)
		startMember(t, f)
		oldPin := f.checkoutPin()
		f.memberOf("team-1")
		followed, _ := f.addCheckout("followed", serverFile("team-1"), serverPin("team-1", oldPin.URL))
		f.withoutCheckout()
		mapPath, err := f.store().ByPIDPath(f.key())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(mapPath), 0o500); err != nil { //nolint:gosec // G302: a directory, made unwritable on purpose
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(mapPath), 0o700) }) //nolint:gosec // G302: the directory's own mode again
		f.untouched(seam, followed, "a prompt whose checkout could not be written to the map")
	})
}

// TestAPromptFromACheckoutOfAnotherTeamChangesNothing: the member's other
// checkout is of another team, joined on the server. A prompt of the
// session that comes from it is not a prompt about the session's team at
// all: nothing is said, started or asked, the session is where it was, and
// a map with no checkout on record is not given that one.
func TestAPromptFromACheckoutOfAnotherTeamChangesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		old  bool
	}{
		{"a map that names its checkout", false},
		{"a map that names none", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			seam := registerSeam(t, f)
			startMember(t, f)
			f.memberOf("team-2")
			other, _ := f.addCheckout("other-team", serverFile("team-2"), serverPin("team-2", ""))
			if tc.old {
				f.withoutCheckout()
			}
			for range 2 {
				f.untouched(seam, other, "a prompt from a checkout of another team")
			}
			if exists(config.TeamMovedStamp(f.stateDir, f.pid)) {
				t.Fatalf("a prompt from a checkout of another team recorded a line about a move")
			}
		})
	}
}

// TestAStartFromAnotherCheckoutMovesNobody: `/clear` fires a session's
// start again in the same process, and the document's directory is
// wherever the model is. A running session is its own checkout's at a
// start as at a prompt: a `/clear` that comes from another checkout of the
// team neither moves the session to that checkout's backend nor makes it
// that checkout's. It says the context line of the session where it is.
// Were it otherwise, a member who clears now in one checkout and now in
// another would have a session going back and forth between two backends.
// The session follows when its own checkout does, and is then not taken
// back by a `/clear` from a checkout that is still on the old backend.
func TestAStartFromAnotherCheckoutMovesNobody(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := registerSeam(t, f)
	startMember(t, f)
	home, own, oldPin, newKey := f.cwd, f.canon(), f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
	f.memberOf("team-1")
	followed, _ := f.addCheckout("followed", serverFile("team-1"), serverPin("team-1", oldPin.URL))
	behind, _ := f.addCheckout("behind", fileOf(oldPin), pinDoc(oldPin))
	conversations := []string{
		"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444",
	}
	// cleared is a /clear whose document's directory is dir: the lines of
	// the start it fires, for a conversation of its own.
	cleared := func(dir string) []string {
		t.Helper()
		f.nativeID, conversations = conversations[0], conversations[1:]
		f.cwd = dir
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		f.cwd = home
		if exit != 0 {
			t.Fatalf("/clear from %s: exit %d: %s", filepath.Base(dir), exit, errOut)
		}
		return lines(out)
	}
	is := func(when, key, session string, registrations int) {
		t.Helper()
		m := f.mustMap()
		if m.TeamKey != key || m.BrigadeSessionID != session || m.Checkout != own || m.ClaudeSessionID != f.nativeID {
			t.Fatalf("%s: the map names %q, %q, the checkout %q and the conversation %q", when, m.TeamKey, m.BrigadeSessionID, m.Checkout, m.ClaudeSessionID)
		}
		if n := len(seam.callsFor("session register")); n != registrations {
			t.Fatalf("%s: the session has been registered %d times, want %d", when, n, registrations)
		}
	}
	where := func(session string) []string {
		return []string{startLine("payments-api", session, teamName, "accept")}
	}

	// From the checkout that followed to the server: the session stays.
	if got := cleared(followed); !slices.Equal(got, where("brigade-sess-1")) {
		t.Fatalf("/clear from a checkout that is on the server: %q", got)
	}
	is("after a /clear from a checkout that is on the server", f.teamKey, "brigade-sess-1", 1)
	// From another checkout on the backend the session is on: it continues,
	// and is still its own checkout's.
	if got := cleared(behind); !slices.Equal(got, where("brigade-sess-1")) {
		t.Fatalf("/clear from another checkout on the same backend: %q", got)
	}
	is("after a /clear from another checkout on the same backend", f.teamKey, "brigade-sess-1", 1)

	// Its own checkout gets the changed file: it follows at its prompts.
	f.moveTeamFile("team-1")
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("the first prompt of its own checkout after the file changed: %q", got)
	}
	if got := f.prompted(); len(got) != 2 || got[1] != startLine("payments-api", "brigade-sess-2", teamName, "accept") {
		t.Fatalf("the second prompt: %q", got)
	}
	is("after its own checkout moved", newKey, "brigade-sess-2", 2)
	// A /clear from a checkout that is still on the old backend does not
	// take it back, and one from its own checkout continues it.
	if got := cleared(behind); !slices.Equal(got, where("brigade-sess-2")) {
		t.Fatalf("/clear from a checkout still on the old backend: %q", got)
	}
	is("after a /clear from a checkout still on the old backend", newKey, "brigade-sess-2", 2)
	if got := cleared(home); !slices.Equal(got, where("brigade-sess-2")) {
		t.Fatalf("/clear from its own checkout: %q", got)
	}
	is("after a /clear from its own checkout", newKey, "brigade-sess-2", 2)
}

// TestAStartGivesAMapWithNoCheckoutTheOneItComesFrom: a session that was
// running when its plugin was updated has a map that names no checkout, and
// a `/clear` reaches it before any prompt does. The start gives it the
// checkout it comes from, as a prompt would: on the backend the session is
// on it continues there and is that checkout's; from a checkout that is on
// the server it moves there, once, and is that checkout's from then on.
// Such a start is never taken for one from another checkout, which would
// keep the session where it is for as long as its map named none.
func TestAStartGivesAMapWithNoCheckoutTheOneItComesFrom(t *testing.T) {
	t.Parallel()

	t.Run("a checkout on the backend the session is on", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		seam := registerSeam(t, f)
		startMember(t, f)
		behind, behindCanon := f.addCheckout("behind", fileOf(f.checkoutPin()), pinDoc(f.checkoutPin()))
		f.withoutCheckout()
		f.nativeID, f.cwd = otherConversation, behind
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		if exit != 0 || !slices.Equal(lines(out), []string{startLine("payments-api", "brigade-sess-1", teamName, "accept")}) {
			t.Fatalf("exit %d, lines %q: %s", exit, lines(out), errOut)
		}
		if m := f.mustMap(); m.Checkout != behindCanon || m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || len(seam.callsFor("session register")) != 1 {
			t.Fatalf("the map after the start: checkout %q, want %q; key %q, session %q, %d registrations", m.Checkout, behindCanon, m.TeamKey, m.BrigadeSessionID, len(seam.callsFor("session register")))
		}
	})

	t.Run("a checkout that is on the server", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		seam := registerSeam(t, f)
		startMember(t, f)
		home, oldPin, newKey := f.cwd, f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
		f.memberOf("team-1")
		followed, followedCanon := f.addCheckout("followed", serverFile("team-1"), serverPin("team-1", oldPin.URL))
		f.withoutCheckout()
		f.nativeID, f.cwd = otherConversation, followed
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		f.cwd = home
		if exit != 0 || !slices.Equal(lines(out), []string{startLine("payments-api", "brigade-sess-2", teamName, "accept")}) {
			t.Fatalf("exit %d, lines %q: %s", exit, lines(out), errOut)
		}
		if m := f.mustMap(); m.Checkout != followedCanon || m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" {
			t.Fatalf("the map after the start: checkout %q, want %q; key %q, session %q", m.Checkout, followedCanon, m.TeamKey, m.BrigadeSessionID)
		}
		// It is that checkout's now: the one it started in, still on the old
		// backend, does not take it back.
		for _, dir := range []string{home, followed, home} {
			f.untouched(seam, dir, "a prompt from "+filepath.Base(dir)+" after the one move")
		}
		if n := len(seam.callsFor("session register")); n != 2 {
			t.Fatalf("the session was registered %d times, want twice", n)
		}
	})
}

// TestAStartOverAMapOfAnotherProcessIsOfItsOwnCheckout: the map under this
// pid may be one a dead process left: its watcher is gone and it names
// another conversation. It names a checkout too, and that one is the dead
// session's. A start from another checkout is then no start "from another
// checkout than the session's own": there is no running session to keep,
// so it registers where its own checkout is, and its map names its own
// checkout and not the one it found.
func TestAStartOverAMapOfAnotherProcessIsOfItsOwnCheckout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		server bool
	}{
		{"its checkout is on the backend the map names", false},
		{"its checkout is on the server", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			seam := registerSeam(t, f)
			startMember(t, f)
			left, oldPin := f.canon(), f.checkoutPin()
			other, otherCanon := f.addCheckout("other", fileOf(oldPin), pinDoc(oldPin))
			wantKey := f.teamKey
			if tc.server {
				f.memberOf("team-1")
				other, otherCanon = f.addCheckout("on-the-server", serverFile("team-1"), serverPin("team-1", oldPin.URL))
				wantKey = teamstore.Key("rest", movedURL, "team-1")
			}
			if m := f.mustMap(); m.Checkout != left {
				t.Fatalf("the map that was left names the checkout %q", m.Checkout)
			}
			if err := os.Remove(f.pidfilePath()); err != nil { // its watcher is gone
				t.Fatal(err)
			}
			f.nativeID, f.cwd = otherConversation, other
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
			if exit != 0 || strings.Contains(out, "(brigade-sess-1)") {
				t.Fatalf("exit %d, stdout %q: %s", exit, out, errOut)
			}
			if m := f.mustMap(); m.Checkout != otherCanon || m.TeamKey != wantKey || len(seam.callsFor("session register")) != 2 {
				t.Fatalf("the map after the start: checkout %q, want %q; key %q, want %q; %d registrations, want 2", m.Checkout, otherCanon, m.TeamKey, wantKey, len(seam.callsFor("session register")))
			}
		})
	}
}

// TestTheHooksThatRewriteAMapKeepItsCheckout: the map is rewritten by more
// than a session's start: a `/compact`, a prompt that brings a new
// permission mode or a new name, a prompt that decides the inbound policy
// again. Each of them reads the map and writes it back, from wherever the
// model is, and the checkout the map names is the session's for as long as
// it is registered: a writer that dropped it would turn the session into
// one that takes the next checkout a prompt comes from.
func TestTheHooksThatRewriteAMapKeepItsCheckout(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	registerSeam(t, f)
	startMember(t, f)
	own, oldPin := f.canon(), f.checkoutPin()
	// Every hook below comes from another checkout of the team.
	other, _ := f.addCheckout("behind", fileOf(oldPin), pinDoc(oldPin))
	f.cwd = other
	kept := func(when string, rewritten func(before, after *sessionmap.ByPID) bool) {
		t.Helper()
		before := *f.mustMap()
		f.now = f.now.Add(time.Second)
		switch when {
		case "a /compact":
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("compact")); exit != 0 {
				t.Fatalf("%s: exit %d: %s", when, exit, errOut)
			}
		default:
			if exit, _, errOut := f.run(SubPrompt, f.promptDoc("plan")); exit != 0 {
				t.Fatalf("%s: exit %d: %s", when, exit, errOut)
			}
		}
		after := f.mustMap()
		if !rewritten(&before, after) {
			t.Fatalf("%s did not rewrite the map: the test holds nothing", when)
		}
		if after.Checkout != own || after.BrigadeSessionID != before.BrigadeSessionID || after.TeamKey != before.TeamKey {
			t.Fatalf("%s left a map that names the checkout %q, the session %q and the key %q", when, after.Checkout, after.BrigadeSessionID, after.TeamKey)
		}
	}
	kept("a /compact", func(before, after *sessionmap.ByPID) bool { return after.UpdatedAt.After(before.UpdatedAt) })
	kept("a prompt in another permission mode", func(before, after *sessionmap.ByPID) bool {
		return after.PermissionMode == "plan" && before.PermissionMode != "plan"
	})
	f.deps.Registry = fakeregistry.New(t, map[int]string{f.pid: fakeregistry.Observed(f.pid, "renamed", "busy", f.socket)})
	kept("a prompt after a rename", func(before, after *sessionmap.ByPID) bool {
		return after.SessionName == "renamed" && before.SessionName != "renamed"
	})
}

// TestASessionRegisteredAgainKeepsItsCheckout: a running session, its
// watcher alive, whose backend no longer has it is registered again by its
// next start, on the backend it is on. That is the same session of the same
// process, so it is still the checkout's it registered in, though this
// start's directory is in another checkout of the team. A map that names no
// checkout takes the start's.
func TestASessionRegisteredAgainKeepsItsCheckout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		old  bool
	}{
		{"a map that names its checkout", false},
		{"a map that names none", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			// The backend answers the start's heartbeat that it has no such
			// session.
			seam := memberSeam(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
				"session heartbeat": {errResp(protocol.CodeNotFound, "session_not_found")},
			})
			startMember(t, f)
			own, oldPin := f.canon(), f.checkoutPin()
			behind, behindCanon := f.addCheckout("behind", fileOf(oldPin), pinDoc(oldPin))
			if tc.old {
				f.withoutCheckout()
			}
			// Another conversation, from the other checkout.
			f.nativeID, f.cwd = otherConversation, behind
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("clear")); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			want := own
			if tc.old {
				want = behindCanon
			}
			if m := f.mustMap(); m.Checkout != want || m.TeamKey != f.teamKey || len(seam.callsFor("session register")) != 2 {
				t.Fatalf("the map after the session was registered again: checkout %q, want %q; key %q; %d registrations", m.Checkout, want, m.TeamKey, len(seam.callsFor("session register")))
			}
		})
	}
}

// TestAStartInAnotherCheckoutWithNoWatcherAliveRegistersThere: a start is
// from another checkout than a running session's own only when a session
// is running, which its live watcher shows. With no watcher alive the map
// can still name the conversation the start names, and on Codex CLI that
// shows nothing: a thread is keyed by its id, its map outlives its process,
// and a thread that is resumed, in a new process, from another checkout
// finds the map of the process before. Such a start registers where it is,
// as it did before a map named a checkout, and its map names the checkout
// it starts in: on the server when that checkout is there, with no resume
// hint for an id the server never issued, and on the backend the map named
// when the checkout is still on it. Messages that waited for the session of
// the process before keep nobody back. A watcher is started in each case.
func TestAStartInAnotherCheckoutWithNoWatcherAliveRegistersThere(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostCodex, config.HostClaude} {
		for _, tc := range []struct {
			name   string
			server bool
			held   bool
		}{
			{"the checkout is on the server", true, false},
			{"the checkout is on the server, and messages waited", true, true},
			{"the checkout is on the backend the map names", false, false},
		} {
			t.Run(string(host)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				f := movingFixture(t, host)
				f.noAdapterOption = true
				seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
				oldPin := f.checkoutPin()
				other, otherCanon := f.addCheckout("behind", fileOf(oldPin), pinDoc(oldPin))
				wantKey := f.teamKey
				if tc.server {
					f.memberOf("team-1")
					other, otherCanon = f.addCheckout("on-the-server", serverFile("team-1"), serverPin("team-1", oldPin.URL))
					wantKey = teamstore.Key("rest", movedURL, "team-1")
				}
				if tc.held {
					if err := pendingStoreOf(f).Save(inbound.PendingFile{Entries: heldEntries(1, "payments-api"), UpdatedAt: fixedTime}); err != nil {
						t.Fatal(err)
					}
				}
				// The process ends: its watcher goes, and the map stays.
				if err := os.Remove(f.pidfilePath()); err != nil {
					t.Fatal(err)
				}
				spawns := f.spawner.count()
				// The same conversation, started again from the other checkout.
				f.cwd = other
				exit, out, errOut := f.run(SubSessionStart, f.sessionDoc("resume"))
				if exit != 0 || strings.Contains(out, "not connected") || strings.Contains(out, "(brigade-sess-1)") {
					t.Fatalf("exit %d, stdout %q: %s", exit, out, errOut)
				}
				m := f.mustMap()
				if m.Checkout != otherCanon || m.TeamKey != wantKey || m.BrigadeSessionID != "brigade-sess-2" {
					t.Fatalf("the map after the start: checkout %q, want %q; key %q, want %q; session %q", m.Checkout, otherCanon, m.TeamKey, wantKey, m.BrigadeSessionID)
				}
				regs := seam.callsFor("session register")
				if len(regs) != 2 || f.spawner.count() != spawns+1 {
					t.Fatalf("%d registrations and %d watchers started, want one of each", len(regs)-1, f.spawner.count()-spawns)
				}
				// An id is a hint only to the backend that issued it.
				if reg := registration(t, regs[1]); (reg.Resume != nil) == tc.server {
					t.Fatalf("the registration's resume hint: %v; the checkout is on the server: %v", reg.Resume, tc.server)
				}
			})
		}
	}
}
