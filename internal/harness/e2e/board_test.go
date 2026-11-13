//go:build !windows

package e2e

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/foldersync"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// boardItem and boardResult are the members of `brigade work-item --json`
// this file reads.
type boardItem struct {
	ID        string `json:"id"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	List      string `json:"list"`
	ListName  string `json:"list_name"`
	Assignee  string `json:"assignee"`
	CreatedBy string `json:"created_by"`
	Comments  []struct {
		Session string `json:"session"`
		Text    string `json:"text"`
	} `json:"comments"`
	History []struct {
		What string `json:"what"`
	} `json:"history"`
}

type boardResult struct {
	Lists []struct {
		Key    string `json:"key"`
		Name   string `json:"name"`
		Hidden bool   `json:"hidden"`
	} `json:"lists"`
	NotShown []struct {
		List  string `json:"list"`
		Items int    `json:"items"`
	} `json:"not_shown"`
	Items []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		List   string `json:"list"`
	} `json:"items"`
	Item       *boardItem `json:"item"`
	Changed    bool       `json:"changed"`
	Renumbered []struct {
		Title string `json:"title"`
		From  int    `json:"from"`
		To    int    `json:"to"`
	} `json:"renumbered"`
}

// TestABoardSpansTheTeamsRepositories is card 263 with the real binaries:
// ONE team across TWO repositories whose team files turn the board on and
// list no folder, alice with a session in one repository and bob with a
// session in the other, each on a machine of their own (a config dir, a
// state dir and so a Syncthing instance each), the fs adapter as the
// backend, the hooks as real processes and the real detached watchers
// driving the bundled sync adapter.
//
//  1. SessionStart in both checkouts says file sync is on for the team's
//     board, and freezes the board folder under each machine's own state
//     directory, outside both checkouts.
//  2. Both machines hold the one folder id, derived from the team alone,
//     and connect — two sessions that share no repository.
//  3. A work item alice adds in the web repository is on bob's board in
//     the api repository; bob moves it, comments and gives its list
//     another name, and alice sees all three: the work item in the same
//     list, under its new name.
//  4. Each adds a work item before either has the other's: both take the
//     same number. Once the two have crossed, the next command on either
//     machine gives the later one the next free number, and both boards
//     agree.
//  5. Nothing of the board is in either checkout, and the backend's own
//     files carry no work item.
//
// Every duration measured is logged; every wait is a hang catcher.
func TestABoardSpansTheTeamsRepositories(t *testing.T) {
	syncthing := syncthingOnPath(t)
	r := newRig(t)
	r.createTeam()
	r.aBoardSpansTheTeamsRepositories(syncthing, r.fsBackend())
}

// aBoardSpansTheTeamsRepositories is the test above on the team the rig's
// checkout names, whatever backend b it is on.
func (r *rig) aBoardSpansTheTeamsRepositories(syncthing string, b syncBackend) {
	t := r.t
	pathDir := r.bundledSync(syncthing)

	// The team file both repositories commit: the board, and no folder.
	raw, err := os.ReadFile(filepath.Join(r.checkout, teamfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["sync"] = map[string]any{"board": true}
	doc["polling"] = map[string]any{"roster_seconds": 15}
	if raw, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}

	bobCheckout := filepath.Join(r.dirs.Root, "bob", "api-checkout")
	r.writeCheckout(r.checkout, webOrigin, raw)
	r.writeCheckout(bobCheckout, apiOrigin, raw)
	tokenA := "cc-messaging-secret-alice-board-MUST-NOT-LEAK-" + testutil.RunID()
	tokenB := "cc-messaging-secret-bob-board-MUST-NOT-LEAK-" + testutil.RunID()
	alice := r.newSession("alice", "", "alice-web", fakesock.New(t).Path(), tokenA)
	bob := r.newSession("bob", "", "bob-api", fakesock.New(t).Path(), tokenB)
	bob.cwd = bobCheckout
	r.ownStateHome(&bob, "xdg-state-bob")
	for _, s := range []*session{&alice, &bob} {
		s.extraEnv = []string{"PATH=" + r.emptyPath + string(os.PathListSeparator) + pathDir}
	}
	b.join(bob, bobCheckout)
	homes := map[string]string{
		"alice": filepath.Join(r.stateDirOf(alice), "sync", "syncthing"),
		"bob":   filepath.Join(r.stateDirOf(bob), "sync", "syncthing"),
	}
	t.Cleanup(func() {
		r.reapWatchers()
		for _, home := range homes {
			stopDaemon(t, home)
		}
	})

	// --- 1. two sessions start, in two repositories ------------------------------
	start := time.Now()
	const onLine = "Brigade: file sync on: the team's Brigade Board of work items (`brigade work-item list`) through syncthing."
	boards := map[string]string{}
	for name, s := range map[string]session{"alice": alice, "bob": bob} {
		res := b.hook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
		if !strings.Contains(res.stdout, onLine) {
			t.Fatalf("session-start for %s did not turn the board on:\nstdout: %s\nstderr: %s", name, res.stdout, res.stderr)
		}
		m := r.mustMap(s)
		if want := foldersync.BoardPath(r.stateDirOf(s), m.TeamRef); m.BoardFolder != want || len(m.SyncFolders) != 0 {
			t.Fatalf("%s's map: board_folder %q (want %q), sync_folders %v", name, m.BoardFolder, want, m.SyncFolders)
		}
		boards[name] = m.BoardFolder
		r.liveWatcher(s)
	}
	if boards["alice"] == boards["bob"] {
		t.Fatalf("the two machines share one board folder: %s", boards["alice"])
	}
	if a, b := r.mustMap(alice), r.mustMap(bob); a.WorkspaceLabel == b.WorkspaceLabel || a.WorkspaceLabel != webRepo || b.WorkspaceLabel != apiRepo {
		t.Fatalf("the sessions' repositories: %q and %q, want %q and %q", a.WorkspaceLabel, b.WorkspaceLabel, webRepo, apiRepo)
	}

	// --- 2. one folder id, held on both machines, the machines connected ---------
	status := func(s session) syncStatus {
		t.Helper()
		return jsonResult[syncStatus](t, r.mustRun(b.env(s), "", "sync", "status", "--json").stdout)
	}
	// The engine a watcher has just started answers `unavailable` until it
	// has written its configuration: wait for each to answer once.
	for _, s := range []session{alice, bob} {
		testutil.Eventually(t, waitLong, pollSync, func() bool {
			return r.run(b.env(s), "", "sync", "status", "--json").exit == 0
		})
	}
	boardID := foldersync.BoardFolder(r.teamRef, "").ID
	for name, s := range map[string]session{"alice": alice, "bob": bob} {
		// The path is the map's own, compared as written: the folder is
		// made by the watcher's first apply, which may not have run yet.
		c := status(s).Configured
		if len(c) != 1 || c[0].ID != boardID || !c[0].Team || c[0].Label != foldersync.BoardLabel || c[0].Path != boards[name] {
			t.Fatalf("%s configured %+v, want the board %s at %s", name, c, boardID, boards[name])
		}
	}
	device := func(s session) string {
		t.Helper()
		var dev string
		testutil.Eventually(t, waitLong, pollSync, func() bool {
			dev = status(s).Peer
			return dev != ""
		})
		return dev
	}
	devA, devB := device(alice), device(bob)
	if devA == devB {
		t.Fatalf("one device for two machines: %s", devA)
	}
	// What the board asks of the backend: each session's device is on the
	// roster of the whole team, as the other machine reads it.
	for reader, want := range map[*session]map[string]string{
		&alice: {r.mustMap(bob).BrigadeSessionID: devB},
		&bob:   {r.mustMap(alice).BrigadeSessionID: devA},
	} {
		testutil.Eventually(t, waitLong, pollSync, func() bool {
			roster := jsonResult[rosterResult](t, r.mustRun(b.env(*reader), "", "sessions", "--json").stdout)
			for _, rec := range roster.Sessions {
				if dev, listed := want[rec.SessionID]; listed {
					return rec.SyncPeer != nil && *rec.SyncPeer == "syncthing:"+dev
				}
			}
			return false
		})
	}
	holds := func(s session, path string) bool {
		for _, f := range status(s).Folders {
			if f.ID == boardID {
				return samePath(f.Path, path)
			}
		}
		return false
	}
	connected := func(s session, dev string) (listed, up bool) {
		for _, peer := range status(s).Peers {
			if peer.Peer == dev {
				return true, peer.Connected
			}
		}
		return false, false
	}
	reached := false
	testutil.Eventually(t, waitSync, pollSync, func() bool {
		if !holds(alice, boards["alice"]) || !holds(bob, boards["bob"]) {
			return false
		}
		la, ca := connected(alice, devB)
		lb, cb := connected(bob, devA)
		if !reached && la && lb {
			reached = true
			reachOnLoopback(t, homes["alice"], devB, homes["bob"])
			reachOnLoopback(t, homes["bob"], devA, homes["alice"])
		}
		return ca && cb
	})
	t.Logf("the board held on both machines and the two connected %s after the first SessionStart", time.Since(start).Round(time.Millisecond))

	// --- 3. a work item crosses the repositories, and its changes cross back -----
	board := func(s session, stdin string, args ...string) boardResult {
		t.Helper()
		return jsonResult[boardResult](t, r.mustRun(b.env(s), stdin, append([]string{"work-item"}, append(args, "--json")...)...).stdout)
	}
	added := board(alice, "Fix the login redirect\n\nSigning in from the invite link returns to the invite.\n", "add", "--mine")
	if added.Item == nil || added.Item.Number != 1 || added.Item.List != "todo" || added.Item.ListName != "Todo" || added.Item.CreatedBy != aliceLabel || added.Item.Assignee != aliceLabel {
		t.Fatalf("alice's add: %+v", added.Item)
	}
	wrote := time.Now()
	testutil.Eventually(t, waitFile, pollSync, func() bool {
		items := board(bob, "", "list").Items
		return len(items) == 1 && items[0].Number == 1 && items[0].Title == "Fix the login redirect"
	})
	t.Logf("alice's work item, added in the web repository, was on bob's board in the api repository %s later", time.Since(wrote).Round(time.Millisecond))

	if moved := board(bob, "", "move", "1", "Doing"); !moved.Changed || moved.Item.List != "doing" {
		t.Fatalf("bob's move: %+v", moved.Item)
	}
	board(bob, "Reproduced with a fresh invite.\n", "comment", "1")
	// bob renames the list: its key, and the work item in it, stay.
	if renamed := board(bob, "", "rename-list", "doing", "Under way"); !renamed.Changed || len(renamed.Lists) != 3 || renamed.Lists[1].Key != "doing" || renamed.Lists[1].Name != "Under way" {
		t.Fatalf("bob's rename: %+v", renamed.Lists)
	}
	// And hides the list nobody works in from the listing (work item
	// 269): a change of its own, like the rename, that moves nothing.
	if hid := board(bob, "", "hide-list", "Done"); !hid.Changed || len(hid.Lists) != 3 || !hid.Lists[2].Hidden {
		t.Fatalf("bob's hide-list: %+v", hid.Lists)
	}
	wrote = time.Now()
	var seen *boardItem
	testutil.Eventually(t, waitFile, pollSync, func() bool {
		seen = board(alice, "", "show", "1").Item
		if seen == nil || seen.List != "doing" || seen.ListName != "Under way" || len(seen.Comments) != 1 {
			return false
		}
		// alice's listing leaves the hidden list out, and says so.
		listed := board(alice, "", "list")
		return len(listed.Lists) == 3 && listed.Lists[2].Hidden && len(listed.NotShown) == 1 && listed.NotShown[0].List == "done"
	})
	if seen.Comments[0].Text != "Reproduced with a fresh invite." || seen.Comments[0].Session != "bob-api" {
		t.Fatalf("bob's comment as alice reads it: %+v", seen.Comments[0])
	}
	t.Logf("bob's move, comment, rename of the list and hiding of another reached alice %s later; the history reads %v", time.Since(wrote).Round(time.Millisecond), seen.History)

	// --- 4. two machines take one number, and the next command puts it right ----
	clash := time.Now()
	a2 := board(alice, "Alice's second\n", "add")
	b2 := board(bob, "Bob's second\n", "add")
	if gap := time.Since(clash); gap >= 5*time.Second {
		t.Fatalf("the two adds were %s apart: the engine may have carried the first across", gap)
	}
	if a2.Item.Number != 2 || b2.Item.Number != 2 {
		t.Fatalf("the two adds took %d and %d, want 2 and 2: each machine had only its own", a2.Item.Number, b2.Item.Number)
	}
	var settled [2]boardResult
	testutil.Eventually(t, waitFile, pollSync, func() bool {
		settled = [2]boardResult{board(alice, "", "list"), board(bob, "", "list")}
		for _, b := range settled {
			if len(b.Items) != 3 {
				return false
			}
			numbers := map[int]string{}
			for _, it := range b.Items {
				numbers[it.Number] = it.Title
			}
			if len(numbers) != 3 || numbers[1] == "" || numbers[2] == "" || numbers[3] == "" {
				return false
			}
		}
		// Both boards name the same work item by each number.
		title := func(b boardResult, n int) string {
			for _, it := range b.Items {
				if it.Number == n {
					return it.Title
				}
			}
			return ""
		}
		return title(settled[0], 2) == title(settled[1], 2) && title(settled[0], 3) == title(settled[1], 3)
	})
	t.Logf("two work items numbered 2 on two machines were 2 and 3 on both %s after the adds: %+v", time.Since(clash).Round(time.Millisecond), settled[0].Items)
	// The one renumbered says so in its history, on both machines.
	for name, s := range map[string]session{"alice": alice, "bob": bob} {
		var item *boardItem
		testutil.Eventually(t, waitFile, pollSync, func() bool {
			item = board(s, "", "show", "3").Item
			return item != nil && len(item.History) > 1
		})
		if last := item.History[len(item.History)-1].What; last != "renumbered from 2 to 3" {
			t.Errorf("%s: work item 3's history ends %q", name, last)
		}
	}

	// --- 5. the board is in neither checkout, and not at the backend -------------
	for _, dir := range []string{r.checkout, bobCheckout} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() != ".git" && e.Name() != teamfile.FileName {
				t.Errorf("the checkout %s holds %s: the board writes nothing into a checkout", filepath.Base(dir), e.Name())
			}
		}
	}
	if b.files != "" {
		if hits := tokenHits(t, "Fix the login redirect", b.files); len(hits) != 0 {
			t.Errorf("a work item's title is in the backend's files: %v", hits)
		}
	}

	for _, s := range []session{alice, bob} {
		b.hook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "other"}))
	}
	b.tokenNowhere(tokenA)
	b.tokenNowhere(tokenB)
}
