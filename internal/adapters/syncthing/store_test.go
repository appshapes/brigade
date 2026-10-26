package syncthing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCreateJoinSendReceive is the whole message path in local mode: two
// profiles on one store, a message written by one and read by the other.
func TestCreateJoinSendReceive(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")
	if got := r.join("bob", secret); got["rejoined"] != false || got["team_ref"] != teamRef || got["team_name"] != "ops" {
		t.Fatalf("join = %v", got)
	}
	a := r.register("alice", "a")
	b := r.register("bob", "b")
	sent := r.send("alice", a, b, "hello")
	if sent["status"] != "accepted" {
		t.Fatalf("send = %v", sent)
	}
	got := mustMessages(t, r.ok("", "--profile", "bob", "message", "receive", "--session", b))
	if len(got) != 1 || got[0]["body"] != "hello" || got[0]["message_id"] != sent["message_id"] {
		t.Fatalf("receive = %v", got)
	}
	acked := r.ok(`{"message_ids":["`+str(t, sent, "message_id")+`"]}`, "--profile", "bob", "message", "ack", "--session", b)
	if ids, _ := acked["acked"].([]any); len(ids) != 1 {
		t.Fatalf("ack = %v", acked)
	}
	// The team folder's housekeeping: .stignore inside, the lock outside.
	if _, err := os.Lstat(filepath.Join(r.root, teamRef, ignoreFileName)); err != nil {
		t.Errorf("no .stignore in the team folder: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(r.root, lockName)); err != nil {
		t.Errorf("the lock is not beside the team folders: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(r.root, teamRef, lockName)); err == nil {
		t.Error("a lock file inside the team folder would be replicated")
	}
	// The member file carries the secret version; team.json starts at 1.
	if m := r.readStore(teamRef, membersDirName, r.principalOf("bob")+jsonExt); m["secret_version"] != float64(1) || m["status"] != memberActive {
		t.Errorf("bob's member file = %v", m)
	}
}

// TestRevokedMarkerFiltersEverywhere: the creator's marker hides the
// member's sessions, refuses the member's own verbs with the uniform
// unauthorized, and a rejoin with the secret deletes it; a ban does not.
func TestRevokedMarkerFiltersEverywhere(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")
	r.join("bob", secret)
	a := r.register("alice", "a")
	b := r.register("bob", "b")
	bob := r.principalOf("bob")

	// Bob cannot revoke: not the creator.
	r.fails("unauthorized", 5, "", "--profile", "bob", "team", "revoke-member", "--principal", r.principalOf("alice"))
	// Alice cannot revoke herself.
	r.fails("invalid_input", 3, "", "--profile", "alice", "team", "revoke-member", "--principal", r.principalOf("alice"))

	got := r.ok("", "--profile", "alice", "team", "revoke-member", "--principal", bob)
	if got["status"] != statusRevoked || got["changed"] != true || got["sessions_closed"] != float64(1) {
		t.Fatalf("revoke = %v", got)
	}
	if _, err := os.Lstat(filepath.Join(r.root, teamRef, revokedDirName, bob+jsonExt)); err != nil {
		t.Fatalf("no marker: %v", err)
	}
	// Bob's own file is untouched: the creator never writes it.
	if m := r.readStore(teamRef, membersDirName, bob+jsonExt); m["status"] != memberActive {
		t.Fatalf("the revoke rewrote bob's own file: %v", m)
	}
	// Alice's roster hides bob; bob's verbs are unauthorized; a send to
	// bob's session is not_found.
	if sessions := mustSessions(t, r.ok("", "--profile", "alice", "session", "list")); len(sessions) != 1 || sessions[0]["session_id"] != a {
		t.Fatalf("roster after revoke = %v", sessions)
	}
	refused := r.fails("unauthorized", 5, "", "--profile", "bob", "session", "list")
	r.fails("not_found", 6, `{"sender_session_id":"`+a+`","recipient_session_id":"`+b+`","body":"x"}`, "--profile", "alice", "message", "send")
	// Idempotent: a second revoke changes nothing.
	if again := r.ok("", "--profile", "alice", "team", "revoke-member", "--principal", bob); again["changed"] != false {
		t.Fatalf("second revoke = %v", again)
	}
	// A rejoin with the secret deletes the marker.
	if got := r.join("bob", secret); got["rejoined"] != true {
		t.Fatalf("rejoin = %v", got)
	}
	if _, err := os.Lstat(filepath.Join(r.root, teamRef, revokedDirName, bob+jsonExt)); err == nil {
		t.Fatal("the marker survived the rejoin")
	}
	r.ok("", "--profile", "bob", "session", "list")
	// A ban: the rejoin answers byte for byte what a wrong secret answers.
	if got := r.ok("", "--profile", "alice", "team", "revoke-member", "--principal", bob, "--ban"); got["status"] != statusBanned {
		t.Fatalf("ban = %v", got)
	}
	banned := r.fails("unauthorized", 5, `{"join_secret":"`+secret+`"}`, "--profile", "bob", "team", "join")
	wrong := r.fails("unauthorized", 5, `{"join_secret":"`+secret+`x"}`, "--profile", "carol", "team", "join")
	if errorJSON(t, banned.stdout) != errorJSON(t, wrong.stdout) {
		t.Fatalf("a ban is distinguishable from a wrong secret:\n%s\n%s", banned.stdout, wrong.stdout)
	}
	// The uniform unauthorized of a revoked member is the not-a-member text.
	if !strings.Contains(refused.stdout, "not an active member") {
		t.Fatalf("revoked member's refusal = %s", refused.stdout)
	}
	// Un-ban: the marker rewritten without ban lets the rejoin through.
	r.ok("", "--profile", "alice", "team", "revoke-member", "--principal", bob)
	if got := r.join("bob", secret); got["rejoined"] != true {
		t.Fatalf("rejoin after un-ban = %v", got)
	}
}

// TestLeaveWritesOwnFileOnly: a leave marks the member's own file left
// and closes its sessions; the roster hides it; a rejoin keeps the
// principal.
func TestLeaveWritesOwnFileOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")
	r.join("bob", secret)
	r.register("bob", "b")
	bob := r.principalOf("bob")
	if got := r.ok("", "--profile", "bob", "team", "leave"); got["left"] != true {
		t.Fatalf("leave = %v", got)
	}
	if m := r.readStore(teamRef, membersDirName, bob+jsonExt); m["status"] != memberLeft {
		t.Fatalf("bob's file after leave = %v", m)
	}
	if sessions := mustSessions(t, r.ok("", "--profile", "alice", "session", "list", "--include-offline")); len(sessions) != 0 {
		t.Fatalf("roster shows a left member's sessions: %v", sessions)
	}
	if got := r.join("bob", secret); got["rejoined"] != true || got["principal_ref"] != bob {
		t.Fatalf("rejoin = %v", got)
	}
}

// TestRotateAndMaxVersionRevoke: rotation revokes nobody and the old
// secret dies; a version revoke evicts the members who joined at or
// below the version, never the creator.
func TestRotateAndMaxVersionRevoke(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")
	r.join("bob", secret)
	r.fails("usage", 2, "", "--profile", "alice", "team", "rotate-secret")
	r.fails("unauthorized", 5, "", "--profile", "bob", "team", "rotate-secret", "--secret-file", filepath.Join(r.base, "new.secret"))
	file := filepath.Join(r.base, "new.secret")
	got := r.ok("", "--profile", "alice", "team", "rotate-secret", "--secret-file", file)
	if got["secret_version"] != float64(2) || got["secret_file"] != file {
		t.Fatalf("rotate = %v", got)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	fresh := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(fresh, "brg1."+teamRef+".") {
		t.Fatalf("the new secret has the wrong shape: %q", fresh)
	}
	// Bob stays; the old secret is dead; the new one joins carol at version 2.
	r.ok("", "--profile", "bob", "session", "list")
	r.fails("unauthorized", 5, `{"join_secret":"`+secret+`"}`, "--profile", "dave", "team", "join")
	r.join("carol", fresh)
	if m := r.readStore(teamRef, membersDirName, r.principalOf("carol")+jsonExt); m["secret_version"] != float64(2) {
		t.Fatalf("carol's version = %v", m)
	}
	evicted := r.ok("", "--profile", "alice", "team", "revoke-member", "--max-version", "1")
	if evicted["revoked"] != float64(1) || evicted["max_version"] != float64(1) {
		t.Fatalf("eviction = %v", evicted)
	}
	r.fails("unauthorized", 5, "", "--profile", "bob", "session", "list")
	r.ok("", "--profile", "carol", "session", "list")
	r.ok("", "--profile", "alice", "session", "list")
}

// TestTransferMovesTheCreatorRole.
func TestTransferMovesTheCreatorRole(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	bob := r.principalOf("bob")
	r.fails("not_found", 6, `{"principal_ref":"nobody0000"}`, "--profile", "alice", "team", "transfer")
	if got := r.ok("", "--profile", "alice", "team", "transfer", "--principal", bob); got["transferred"] != true {
		t.Fatalf("transfer = %v", got)
	}
	file := filepath.Join(r.base, "x.secret")
	r.fails("unauthorized", 5, "", "--profile", "alice", "team", "rotate-secret", "--secret-file", file)
	r.ok("", "--profile", "bob", "team", "rotate-secret", "--secret-file", file)
}

// TestSyncConflictCopiesAreIgnored: a conflict copy Syncthing writes beside
// a session or message file is never read as one.
func TestSyncConflictCopiesAreIgnored(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, _ := r.team("alice", "ops")
	a := r.register("alice", "a")
	sessions := filepath.Join(r.root, teamRef, sessionsDirName)
	data, err := os.ReadFile(filepath.Join(sessions, a+jsonExt))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessions, a+".sync-conflict-20261026-120000-ABCDEFG"+jsonExt), string(data))
	inbox := filepath.Join(r.root, teamRef, inboxDirName, a)
	sent := r.send("alice", a, a, "to myself")
	names, _ := os.ReadDir(inbox)
	writeFile(t, filepath.Join(inbox, strings.TrimSuffix(names[0].Name(), jsonExt)+".sync-conflict-20261026-120000-ABCDEFG"+jsonExt), "{}")
	if got := mustSessions(t, r.ok("", "--profile", "alice", "session", "list")); len(got) != 1 {
		t.Fatalf("roster = %v, want the one real session", got)
	}
	if got := mustMessages(t, r.ok("", "--profile", "alice", "message", "receive", "--session", a)); len(got) != 1 || got[0]["message_id"] != sent["message_id"] {
		t.Fatalf("receive = %v, want the one real message", got)
	}
}

// TestRootResolution: --root and the store variable must be absolute;
// the default is under the state directory; describe never creates it.
func TestRootResolution(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.env = r.env[:3]
	if _, err := os.Lstat(filepath.Join(r.state, rootDirName)); err == nil {
		t.Fatal("the root exists before any command")
	}
	r.ok("", "--profile", "x", "describe")
	if _, err := os.Lstat(filepath.Join(r.state, rootDirName)); err == nil {
		t.Fatal("describe created the root")
	}
	r.fails("config", 11, "", "--root", "relative/dir", "--profile", "x", "team", "members")
	r.env = append(r.env, storeVar+"=relative/dir")
	r.fails("config", 11, "", "--profile", "x", "team", "members")
	r.env = r.env[:3]
	// Local mode without the folder: a join of an unknown team is the
	// uniform secret refusal, never a pending join.
	r.fails("unauthorized", 5, `{"join_secret":"brg1.nosuchteam.abcdef0123456789abcdef0123456789"}`, "--root", r.root, "--profile", "x", "team", "join")
}

// TestSweepDeletesOnThisReplica: an acknowledged message older than the
// retention goes, on this machine, through the ordinary sweep.
func TestSweepDeletesOnThisReplica(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, _ := r.team("alice", "ops")
	a := r.register("alice", "a")
	sent := r.send("alice", a, a, "x")
	r.ok(`{"message_ids":["`+str(t, sent, "message_id")+`"]}`, "--profile", "alice", "message", "ack", "--session", a)
	acked := filepath.Join(r.root, teamRef, ackedDirName, a)
	if names, _ := os.ReadDir(acked); len(names) != 1 {
		t.Fatalf("acked = %v", names)
	}
	r.now = r.now.Add(25 * time.Hour)
	r.ok("", "--profile", "alice", "session", "list", "--include-offline")
	if names, _ := os.ReadDir(acked); len(names) != 0 {
		t.Fatalf("acked after the sweep = %v", names)
	}
}
