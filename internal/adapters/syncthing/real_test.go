package syncthing

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	engine "github.com/appshapes/brigade/internal/syncthing"
	"github.com/appshapes/brigade/internal/testutil"
)

// realSyncthingVar opts in to the one test that runs the real `syncthing`
// on PATH, twice. `make test` stays hermetic without it.
const realSyncthingVar = "BRIGADE_TEST_SYNCTHING"

const realWait = 90 * time.Second

// TestRealTwoEnginesJoinDeliverAndRevoke is the cross-machine proof of
// card 141 on one machine: two state directories, two engines.
//
//   - A creates the team: its engine starts, its device is the seed.
//   - A registers a session and watches: the watch's engine rounds are
//     what accepts a joiner.
//   - B joins with the secret and A's device as the seed, before the
//     folder exists on B: a pending join. B's engine connects to A's
//     (A's address is given, so the test does not wait on discovery), A
//     lists B as pending with the proof name, accepts it, and the team
//     folder reaches B. B's next command completes the membership.
//   - B registers a session, which reaches A's roster; A sends B a
//     message, which B's watch delivers.
//   - A revokes B: B's watch ends with `unauthorized` once the marker
//     arrives, and A's engine drops B's device after the grace.
func TestRealTwoEnginesJoinDeliverAndRevoke(t *testing.T) {
	if os.Getenv(realSyncthingVar) != "1" {
		t.Skip("set " + realSyncthingVar + "=1 to run against the syncthing on PATH")
	}
	a := newRig(t).withRealEngine()
	b := newRig(t).withRealEngine()
	logs := func() {
		if !t.Failed() {
			return
		}
		for name, r := range map[string]*rig{"a": a, "b": b} {
			raw, err := os.ReadFile(filepath.Join(engine.Home(r.state), "syncthing.log")) //nolint:gosec // G304: the rig's own log
			if err != nil {
				continue
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) > 40 {
				lines = lines[len(lines)-40:]
			}
			t.Logf("%s, the end of syncthing.log:\n%s", name, strings.Join(lines, "\n"))
		}
	}
	t.Cleanup(logs)

	start := time.Now()
	teamRef, secret := a.team("alice", "ops")
	statusA := a.ok("", "--profile", "alice", "profile", "status")
	deviceA := str(t, statusA, "device_id")
	if len(deviceA) < 50 || statusA["engine"] != engineRunning {
		t.Fatalf("A's status = %v", statusA)
	}
	t.Logf("A created the team and started its engine in %s", time.Since(start).Round(time.Millisecond))
	sa := a.register("alice", "a")
	wa := startWatch(t, a, "--profile", "alice", "message", "watch", "--session", sa)
	if ready := wa.next(); ready["event"] != "ready" {
		t.Fatalf("A's watch: %v", ready)
	}

	joined := time.Now()
	got := b.ok(`{"join_secret":"`+secret+`","human_label":"bob@example.com","backend":{"devices":["`+deviceA+`"],"team_name":"ops"}}`,
		"--profile", "bob", "team", "join")
	if got["team_ref"] != teamRef || got["team_name"] != "ops" {
		t.Fatalf("B's pending join = %v", got)
	}
	if status := b.ok("", "--profile", "bob", "profile", "status"); status["team_folder"] != folderPending || status["engine"] != engineRunning {
		t.Fatalf("B's status after the join = %v", status)
	}
	// A's address, so B connects now rather than after discovery.
	eA := engine.New(a.state, engine.RealDeps(), nil, nil)
	eB := engine.New(b.state, engine.RealDeps(), nil, nil)
	apiB, err := eB.Running()
	if err != nil {
		t.Fatal(err)
	}
	if err := apiB.SetDevice(engine.DeviceConfig{DeviceID: deviceA, Name: "brigade seed", Addresses: []string{"tcp://127.0.0.1:" + strconv.Itoa(eA.ListenPort())}}); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, realWait, 500*time.Millisecond, func() bool {
		_, err := os.Lstat(filepath.Join(b.root, teamRef, teamFileName))
		return err == nil
	})
	t.Logf("the team folder reached B %s after the join", time.Since(joined).Round(time.Millisecond))
	b.ok("", "--profile", "bob", "session", "list")
	if m := b.readStore(teamRef, membersDirName, b.principalOf("bob")+jsonExt); m["secret_version"] != float64(1) {
		t.Fatalf("B's member file after completion = %v", m)
	}
	sb := b.register("bob", "b")
	testutil.Eventually(t, realWait, 500*time.Millisecond, func() bool {
		for _, s := range mustSessions(t, a.ok("", "--profile", "alice", "session", "list")) {
			if s["session_id"] == sb {
				return true
			}
		}
		return false
	})
	t.Logf("B's session is on A's roster %s after the join", time.Since(joined).Round(time.Millisecond))

	wb := startWatch(t, b, "--profile", "bob", "message", "watch", "--session", sb)
	if ready := wb.next(); ready["event"] != "ready" {
		t.Fatalf("B's watch: %v", ready)
	}
	sentAt := time.Now()
	sent := a.send("alice", sa, sb, "across two engines")
	event := wb.next()
	message, _ := event["message"].(map[string]any)
	if event["event"] != "message" || message["message_id"] != sent["message_id"] || message["body"] != "across two engines" {
		t.Fatalf("B's watch event = %v", event)
	}
	t.Logf("A's message reached B's watch in %s", time.Since(sentAt).Round(time.Millisecond))

	revokedAt := time.Now()
	bob := b.principalOf("bob")
	if got := a.ok("", "--profile", "alice", "team", "revoke-member", "--principal", bob); got["changed"] != true {
		t.Fatalf("revoke = %v", got)
	}
	if ev := wb.next(); ev["event"] != "error" {
		t.Fatalf("B's watch after the revoke: %v", ev)
	} else if object, _ := ev["error"].(map[string]any); object["code"] != "unauthorized" {
		t.Fatalf("B's watch error = %v", object)
	}
	if code := wb.exit(); code != 5 {
		t.Fatalf("B's watch exit = %d", code)
	}
	t.Logf("the revocation ended B's watch %s after it was written", time.Since(revokedAt).Round(time.Millisecond))
	apiA, err := eA.Running()
	if err != nil {
		t.Fatal(err)
	}
	deviceB := str(t, b.ok("", "--profile", "bob", "profile", "status"), "device_id")
	testutil.Eventually(t, realWait, time.Second, func() bool {
		devices, err := apiA.Devices()
		if err != nil {
			return false
		}
		return !slices.ContainsFunc(devices, func(d engine.ConfiguredDevice) bool { return d.DeviceID == deviceB })
	})
	t.Logf("A's engine dropped B's device %s after the revoke", time.Since(revokedAt).Round(time.Millisecond))
	_ = wa.stdin.Close()
	if code := wa.exit(); code != 0 {
		t.Errorf("A's watch exit = %d", code)
	}
}
