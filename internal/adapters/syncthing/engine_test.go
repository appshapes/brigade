package syncthing

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	engine "github.com/appshapes/brigade/internal/syncthing"
	"github.com/appshapes/brigade/internal/syncthing/syncthingtest"
)

const (
	peerOne = "AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA"
	peerTwo = "BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB"
	seedOne = "CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC"
)

// TestCreateOverTheEngineSharesTheTeamFolder: `team create` in engine
// mode records this machine's device, seeds the profile with it, and
// posts the team folder — its path, its id shape, the one-second watcher
// delay, no versioning.
func TestCreateOverTheEngineSharesTheTeamFolder(t *testing.T) {
	t.Parallel()
	r := newRig(t).withEngine()
	teamRef, _ := r.team("alice", "ops")
	status := r.ok("", "--profile", "alice", "profile", "status")
	if status["device_id"] != syncthingtest.ID || status["engine"] != engineRunning || status["team_folder"] != folderPresent {
		t.Fatalf("profile status = %v", status)
	}
	if seeds, _ := status["seeds"].([]any); len(seeds) != 1 || seeds[0] != syncthingtest.ID {
		t.Fatalf("seeds = %v", status["seeds"])
	}
	if m := r.readStore(teamRef, membersDirName, r.principalOf("alice")+jsonExt); m["device_id"] != syncthingtest.ID {
		t.Fatalf("alice's member file = %v", m)
	}
	r.fake.Mu.Lock()
	defer r.fake.Mu.Unlock()
	if len(r.fake.Folders) != 1 {
		t.Fatalf("folders = %+v", r.fake.Folders)
	}
	f := r.fake.Folders[0]
	if f.ID != folderID(teamRef) || strings.Count(f.ID, "-") != 3 || f.Path != filepath.Join(r.root, teamRef) ||
		f.FSWatcherDelayS != 1 || !f.FSWatcherEnabled || f.Type != "sendreceive" || f.Versioning.Type != "" || len(f.Devices) != 0 {
		t.Fatalf("team folder = %+v", f)
	}
}

// TestReconcilePostsSeedsMembersAndAcceptances: the folder's device list
// is the union of the seeds, the active members' devices and the
// devices accepted this round, never this machine's own; a pending
// device is accepted only on a proof keyed by the current digest, and a
// banned device never.
func TestReconcilePostsSeedsMembersAndAcceptances(t *testing.T) {
	t.Parallel()
	r := newRig(t).withEngine()
	teamRef, secret := r.team("alice", "ops")
	bob := writeMember(t, r, teamRef, "bob0000000000000000000000000001", peerOne)
	writeMember(t, r, teamRef, "eve0000000000000000000000000001", "") // a member whose join never completed: no device yet
	digest := sha256hex(secret)
	const joiner = "DDDDDDD-DDDDDDD-DDDDDDD-DDDDDDD-DDDDDDD-DDDDDDD-DDDDDDD-DDDDDDD"
	const forger = "EEEEEEE-EEEEEEE-EEEEEEE-EEEEEEE-EEEEEEE-EEEEEEE-EEEEEEE-EEEEEEE"
	const bannedDev = "FFFFFFF-FFFFFFF-FFFFFFF-FFFFFFF-FFFFFFF-FFFFFFF-FFFFFFF-FFFFFFF"
	good, err := proofName(digest, joiner, r.now)
	if err != nil {
		t.Fatal(err)
	}
	bad, _ := proofName(sha256hex("wrong"), forger, r.now)
	bannedProof, _ := proofName(digest, bannedDev, r.now)
	r.fake.Mu.Lock()
	r.fake.Pending = map[string]engine.PendingDevice{
		joiner:           {Name: good, Time: r.now},
		forger:           {Name: bad, Time: r.now},
		bannedDev:        {Name: bannedProof, Time: r.now},
		syncthingtest.ID: {Name: good, Time: r.now},
	}
	r.fake.Mu.Unlock()
	writeRevoked(t, r, teamRef, "mallory000000000000000000000001", bannedDev, r.now.Add(-time.Hour), true)

	c := r.command("alice")
	a, err := c.attach("test")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.reconcile(a, teamRef, []string{seedOne, syncthingtest.ID}); err != nil {
		t.Fatal(err)
	}
	got := r.fake.FolderDevices(folderID(teamRef))
	slices.Sort(got)
	want := []string{peerOne, seedOne, joiner}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("folder devices = %v, want %v (bob, the seed, the accepted joiner; not self, not the forger, not the banned, and eve has no device)", got, want)
	}
	ids := r.fake.DeviceIDs()
	if !slices.Contains(ids, joiner) || slices.Contains(ids, forger) || slices.Contains(ids, bannedDev) {
		t.Fatalf("configured devices = %v", ids)
	}
	_ = bob
	_ = peerTwo
}

// TestReconcileDropsARevokedDeviceAfterTheGrace: a marker younger than
// the grace leaves the device in place; an older one removes it from the
// engine; a rejoin — a proof newer than the revocation — re-accepts it
// and deletes the marker.
func TestReconcileDropsARevokedDeviceAfterTheGrace(t *testing.T) {
	t.Parallel()
	r := newRig(t).withEngine()
	teamRef, secret := r.team("alice", "ops")
	bob := writeMember(t, r, teamRef, "bob0000000000000000000000000001", peerOne)
	c := r.command("alice")
	a, err := c.attach("test")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.reconcile(a, teamRef, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.fake.FolderDevices(folderID(teamRef)), peerOne) {
		t.Fatal("bob's device is not on the folder before the revoke")
	}
	writeRevoked(t, r, teamRef, bob, peerOne, r.now, false)
	if err := c.reconcile(a, teamRef, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.fake.DeviceIDs(), peerOne) || !slices.Contains(r.fake.FolderDevices(folderID(teamRef)), peerOne) {
		t.Fatal("the device left the folder or the engine within the grace, before the marker could reach it")
	}
	r.now = r.now.Add(revokeGrace + time.Second)
	if err := c.reconcile(a, teamRef, nil); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.fake.DeviceIDs(), peerOne) || slices.Contains(r.fake.FolderDevices(folderID(teamRef)), peerOne) {
		t.Fatal("the device survived the grace")
	}
	// The rejoin: bob's engine reconnects with a proof made after the
	// revocation.
	digest := sha256hex(secret)
	stale, _ := proofName(digest, peerOne, r.now.Add(-2*revokeGrace))
	r.fake.Mu.Lock()
	r.fake.Pending = map[string]engine.PendingDevice{peerOne: {Name: stale, Time: r.now}}
	r.fake.Mu.Unlock()
	if err := c.reconcile(a, teamRef, nil); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.fake.DeviceIDs(), peerOne) {
		t.Fatal("a proof older than the revocation was accepted")
	}
	fresh, _ := proofName(digest, peerOne, r.now)
	r.fake.Mu.Lock()
	r.fake.Pending = map[string]engine.PendingDevice{peerOne: {Name: fresh, Time: r.now}}
	r.fake.Mu.Unlock()
	if err := c.reconcile(a, teamRef, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.fake.FolderDevices(folderID(teamRef)), peerOne) {
		t.Fatal("the rejoin was not accepted")
	}
	if _, err := os.Lstat(filepath.Join(r.root, teamRef, revokedDirName, bob+jsonExt)); err == nil {
		t.Fatal("the marker survived the rejoin")
	}
}

// TestJoinPendingThenCompletes: a join before the team folder has arrived
// seeds the engine, names this device with the proof, binds the profile
// with the file's team name and keeps the digest; when the folder
// arrives, the next command completes the membership.
func TestJoinPendingThenCompletes(t *testing.T) {
	t.Parallel()
	creator := newRig(t)
	teamRef, secret := creator.team("alice", "ops")
	r := newRig(t).withEngine()
	// No seed: refused; no engine (local mode): the uniform refusal.
	r.fails("config", 11, `{"join_secret":"`+secret+`","backend":{"team_name":"ops"}}`, "--profile", "bob", "team", "join")
	got := r.ok(`{"join_secret":"`+secret+`","human_label":"bob@example.com","backend":{"devices":["`+seedOne+`"],"team_name":"ops"}}`,
		"--profile", "bob", "team", "join")
	if got["team_ref"] != teamRef || got["team_name"] != "ops" || got["rejoined"] != false {
		t.Fatalf("pending join = %v", got)
	}
	status := r.ok("", "--profile", "bob", "profile", "status")
	if status["team_folder"] != folderPending || status["state"] != "joined" {
		t.Fatalf("status after a pending join = %v", status)
	}
	// The device announces the proof; the folder is shared with the seed.
	r.fake.Mu.Lock()
	name := r.fake.Names[syncthingtest.ID]
	r.fake.Mu.Unlock()
	digest := sha256hex(secret)
	if _, ok := verifyProof(digest, syncthingtest.ID, name); !ok {
		t.Fatalf("own device name = %q, want a proof", name)
	}
	if devices := r.fake.FolderDevices(folderID(teamRef)); !slices.Equal(devices, []string{seedOne}) {
		t.Fatalf("folder devices = %v, want the seed", devices)
	}
	bob := r.principalOf("bob")
	if m := r.readStore(teamRef, membersDirName, bob+jsonExt); m["secret_version"] != float64(0) || m["device_id"] != syncthingtest.ID {
		t.Fatalf("pending member file = %v", m)
	}
	// Before the folder arrives bob sees only himself.
	b := r.register("bob", "b")
	if s := mustSessions(t, r.ok("", "--profile", "bob", "session", "list")); len(s) != 1 || s[0]["session_id"] != b {
		t.Fatalf("roster before the folder = %v", s)
	}
	// The folder arrives: alice's team.json and member file.
	copyFile(t, filepath.Join(creator.root, teamRef, teamFileName), filepath.Join(r.root, teamRef, teamFileName))
	alice := creator.principalOf("alice")
	copyFile(t, filepath.Join(creator.root, teamRef, membersDirName, alice+jsonExt), filepath.Join(r.root, teamRef, membersDirName, alice+jsonExt))
	r.ok("", "--profile", "bob", "session", "list")
	if m := r.readStore(teamRef, membersDirName, bob+jsonExt); m["secret_version"] != float64(1) || m["status"] != memberActive {
		t.Fatalf("member file after completion = %v", m)
	}
	if status := r.ok("", "--profile", "bob", "profile", "status"); status["team_folder"] != folderPresent {
		t.Fatalf("status after completion = %v", status)
	}
	// A pending join whose secret was rotated away before the folder
	// arrived ends as left.
	r2 := newRig(t).withEngine()
	r2.ok(`{"join_secret":"`+secret+`","backend":{"devices":["`+seedOne+`"],"team_name":"ops"}}`, "--profile", "carol", "team", "join")
	creator.ok("", "--profile", "alice", "team", "rotate-secret", "--secret-file", filepath.Join(creator.base, "n.secret"))
	copyFile(t, filepath.Join(creator.root, teamRef, teamFileName), filepath.Join(r2.root, teamRef, teamFileName))
	r2.fails("unauthorized", 5, "", "--profile", "carol", "session", "list")
	if m := r2.readStore(teamRef, membersDirName, r2.principalOf("carol")+jsonExt); m["status"] != memberLeft {
		t.Fatalf("carol's file after a stale digest = %v", m)
	}
}

// command builds an in-process command for a profile, as run would, for
// the engine tests that call reconcile directly.
func (r *rig) command(profile string) *command {
	r.t.Helper()
	c := &command{environ: r.env, now: r.clock(), deps: r.deps, profileName: profile}
	c.log = discardLogger()
	var err error
	if c.cfgDir, err = configDir(r.env); err != nil {
		r.t.Fatal(err)
	}
	if err := c.authenticate(true); err != nil {
		r.t.Fatal(err)
	}
	return c
}

// writeMember plants an active member with a device id, as a replicated
// member file would arrive.
func writeMember(t *testing.T, r *rig, teamRef, principal, device string) string {
	t.Helper()
	s := &store{root: r.root, now: r.clock()}
	if err := writeJSON(s.memberPath(teamRef, principal), &memberFile{
		PrincipalRef: principal, HumanLabel: principal[:3] + "@example.com", Status: memberActive,
		JoinedAt: r.now, DeviceID: device, SecretVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	return principal
}

// writeRevoked plants the creator's marker.
func writeRevoked(t *testing.T, r *rig, teamRef, principal, device string, at time.Time, ban bool) {
	t.Helper()
	s := &store{root: r.root, now: r.clock()}
	if err := writeJSON(s.revokedPath(teamRef, principal), &revokedFile{
		PrincipalRef: principal, DeviceID: device, RevokedAt: at, Ban: ban, By: "creator",
	}); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, to, string(data))
}
