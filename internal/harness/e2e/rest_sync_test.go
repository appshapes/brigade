//go:build !windows

package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/teamstore"
)

// File sync and the Brigade Board on a team on a Brigade server (work item
// 89; the first check of work item 272, before the `appshapes` team moves):
// the two real-Syncthing tests of sync_test.go and board_test.go, each run
// again with the team created by `team create --adapter rest` and both
// sessions attached through the bundled `rest` adapter. Nothing a member
// syncs travels through the server. What the server carries is each
// session's `sync_peer` and `workspace_label`, which the other member's
// watcher reads from the roster to introduce the two machines; these tests
// are that, end to end, with the real binaries.
//
// Like every test of the real daemon they are skipped unless
// BRIGADE_TEST_SYNCTHING=1. The backend is the stand-in server, or the
// built `brigade-server` that BRIGADE_E2E_SERVER names, over TLS with its
// own certificate pinned in the team file.

// serverBackend is a team on the server at backend: the admin's `team
// create` in the rig's checkout, which makes alice the first member, and
// bob's own join. The rig's fs store is never used.
func (r *rig) serverBackend(backend restBackend) syncBackend {
	t := r.t
	t.Helper()
	r.secretFile = filepath.Join(r.dirs.Root, "team.secret")
	r.createTeamOnServer(backend)
	return syncBackend{
		env:  r.restEnv,
		hook: r.restHook,
		// bob joins as a member does: inside his session, in his own
		// checkout, with the file that holds the secret. Until then his
		// session is attached to nothing and is told to join.
		join: func(bob session, checkout string) {
			t.Helper()
			res := r.restHook(bob, "session-start", bob.hookDoc("SessionStart", map[string]any{"source": "startup"}))
			if r.mapExists(bob) || !strings.Contains(res.stdout, "team join") {
				t.Fatalf("a session of a member who has not joined: stdout %q, map %v", res.stdout, r.mapExists(bob))
			}
			res = r.runIn(checkout, r.restEnv(bob), "", "team", "join", "--secret-file", r.secretFile, "--label", bobLabel)
			if res.exit != 0 {
				t.Fatalf("team join: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
			}
			binding, err := teamstore.LoadBinding(r.configDirB, r.teamKey)
			if err != nil || binding.Adapter != "rest" || binding.URL != backend.url || binding.TeamRef != r.teamRef {
				t.Fatalf("bob's binding after the join: %+v %v", binding, err)
			}
		},
		files: backend.dir,
		tokenNowhere: func(token string) {
			t.Helper()
			if hits := tokenHits(t, token, r.dirs.Root); len(hits) != 0 {
				t.Errorf("the messaging token is in %v", hits)
			}
			if backend.dir == "" {
				return
			}
			if hits := tokenHits(t, token, backend.dir); len(hits) != 0 {
				t.Errorf("the messaging token is in the server's files: %v", hits)
			}
		},
	}
}

// TestTwoSessionsOfATeamOnAServerSyncAFolder is
// TestTwoSessionsSyncAFolderThroughSyncthing on a team on a server: two
// members, two machines, two checkouts of one repository whose team file
// lists a folder. The two instances find each other from the roster the
// server answers, a file crosses each way, an edit on both sides leaves a
// conflict copy, and each instance stops with its machine's last session.
func TestTwoSessionsOfATeamOnAServerSyncAFolder(t *testing.T) {
	syncthing := syncthingOnPath(t)
	backend := startRestBackend(t)
	r := newRig(t)
	r.twoSessionsSyncAFolder(syncthing, r.serverBackend(backend))
}

// TestABoardSpansTheRepositoriesOfATeamOnAServer is
// TestABoardSpansTheTeamsRepositories on a team on a server: one team, a
// member with a session in one repository and a member with a session in
// another, the board turned on in the team file both commit. A work item
// added in one repository is on the other member's board, its changes
// cross back, two work items that took one number are put right, and
// nothing of the board is in a checkout or in the server's files.
func TestABoardSpansTheRepositoriesOfATeamOnAServer(t *testing.T) {
	syncthing := syncthingOnPath(t)
	backend := startRestBackend(t)
	r := newRig(t)
	r.aBoardSpansTheTeamsRepositories(syncthing, r.serverBackend(backend))
}
