package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/watchstate"
	"github.com/appshapes/brigade/internal/procutil"
)

// The `delivery:` line of `brigade whoami` (card 34): whether this session
// is receiving, from this machine's own files.

const watcherPID = 777

// deliveryFixture is a session WITH an inbox socket, which is the session
// a watcher serves.
func deliveryFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	m := f.byPID()
	m.SocketPath = "/tmp/cc-socks/inbox.sock"
	f.writeMap(t, m)
	return f
}

// liveWatcher writes a pidfile for a watcher of the fixture's session and
// returns a lookup under which that process is alive.
func (f *fixture) liveWatcher(t *testing.T, sessionID, version string) func(int) (procutil.Info, error) {
	t.Helper()
	entry := pidfile.Entry{PID: watcherPID, StartToken: "tok-777", BrigadeSessionID: sessionID, Version: version}
	if err := pidfile.Create(pidfile.Path(f.stateDir, fixturePID), entry); err != nil {
		t.Fatal(err)
	}
	return func(pid int) (procutil.Info, error) {
		return procutil.Info{PID: pid, Exists: true, StartToken: "tok-777"}, nil
	}
}

func deadProcess(pid int) (procutil.Info, error) { return procutil.Info{PID: pid}, nil }

// watcherSays writes the state file as the watcher pid would.
func (f *fixture) watcherSays(t *testing.T, pid int, state string, ago time.Duration) {
	t.Helper()
	if err := watchstate.Write(watchstate.Path(f.stateDir, fixturePID), pid, state, fixtureNow.Add(-ago)); err != nil {
		t.Fatal(err)
	}
}

// deliveredAgo makes the seen file as old as a delivery that long ago.
func (f *fixture) deliveredAgo(t *testing.T, ago time.Duration) {
	t.Helper()
	path := inbound.SeenPath(f.stateDir, selfSessionID)
	if err := (inbound.FileSeenStore{Path: path}).Save([]string{"m-1"}); err != nil {
		t.Fatal(err)
	}
	at := fixtureNow.Add(-ago)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// holds writes a pending file with held unreleased entries and one that
// was released.
func (f *fixture) holds(t *testing.T, held int) {
	t.Helper()
	entries := []inbound.PendingEntry{{
		MessageID: "released", SenderSessionID: "s", SenderName: "n", SenderPrincipal: "p",
		ReceivedAt: fixtureNow.Add(-time.Hour), ReleasedAt: fixtureNow.Add(-time.Minute),
	}}
	for i := range held {
		entries = append(entries, inbound.PendingEntry{
			MessageID: "held-" + string(rune('a'+i)), SenderSessionID: "s", SenderName: "n", SenderPrincipal: "p",
			ReceivedAt: fixtureNow.Add(-time.Hour),
		})
	}
	store := inbound.FilePendingStore{Path: inbound.PendingPath(f.stateDir, selfSessionID), SessionID: selfSessionID}
	if err := store.Save(inbound.PendingFile{Entries: entries, UpdatedAt: fixtureNow}); err != nil {
		t.Fatal(err)
	}
}

// deliveryLine runs whoami and returns its last line.
func deliveryLine(t *testing.T, f *fixture, lookup func(int) (procutil.Info, error)) string {
	t.Helper()
	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "")
	inv.Deps.Lookup = lookup
	if err := Whoami(inv); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(f.out.String(), "\n"), "\n")
	return lines[len(lines)-1]
}

// TestWhoamiDeliveryLine: the line for each thing a session can be.
func TestWhoamiDeliveryLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *fixture) func(int) (procutil.Info, error)
		want  string
	}{
		{"connected, delivering, nothing held", func(t *testing.T, f *fixture) func(int) (procutil.Info, error) {
			t.Helper()
			f.watcherSays(t, watcherPID, watchstate.Connected, 12*time.Minute)
			f.deliveredAgo(t, 3*time.Minute+20*time.Second)
			return f.liveWatcher(t, selfSessionID, "0.15.0")
		}, "delivery: watcher running (0.15.0), connected for 12m; last delivery recorded 3m ago; 0 held"},
		{"an outage: alive, and not receiving", func(t *testing.T, f *fixture) func(int) (procutil.Info, error) {
			t.Helper()
			f.watcherSays(t, watcherPID, watchstate.Retrying, 17*time.Minute)
			f.deliveredAgo(t, 3*time.Hour)
			f.holds(t, 2)
			return f.liveWatcher(t, selfSessionID, "0.15.0")
		}, "delivery: watcher running (0.15.0), not connected for 17m and retrying; last delivery recorded 3h ago; 2 held"},
		{"between two watch children", func(t *testing.T, f *fixture) func(int) (procutil.Info, error) {
			t.Helper()
			f.watcherSays(t, watcherPID, watchstate.Connecting, 4*time.Second)
			return f.liveWatcher(t, selfSessionID, "0.15.0")
		}, "delivery: watcher running (0.15.0), connecting for 4s; no delivery recorded; 0 held"},
		{"a watcher that keeps no state file", func(t *testing.T, f *fixture) func(int) (procutil.Info, error) {
			t.Helper()
			f.deliveredAgo(t, 50*time.Hour)
			return f.liveWatcher(t, selfSessionID, "0.14.0")
		}, "delivery: watcher running (0.14.0); last delivery recorded 2d ago; 0 held"},
		// A replaced watcher's last word is not its successor's.
		{"a state file another watcher wrote", func(t *testing.T, f *fixture) func(int) (procutil.Info, error) {
			t.Helper()
			f.watcherSays(t, watcherPID+1, watchstate.Retrying, time.Hour)
			return f.liveWatcher(t, selfSessionID, "0.15.0")
		}, "delivery: watcher running (0.15.0); no delivery recorded; 0 held"},
		{"no watcher process", func(t *testing.T, f *fixture) func(int) (procutil.Info, error) {
			t.Helper()
			f.deliveredAgo(t, 45*time.Second)
			return deadProcess
		}, "delivery: watcher not running; the next prompt starts it again; last delivery recorded 45s ago; 0 held"},
		// A pidfile whose process died, and a state file it left behind.
		{"a watcher that was killed", func(t *testing.T, f *fixture) func(int) (procutil.Info, error) {
			t.Helper()
			f.watcherSays(t, watcherPID, watchstate.Connected, time.Hour)
			_ = f.liveWatcher(t, selfSessionID, "0.15.0")
			return deadProcess
		}, "delivery: watcher not running; the next prompt starts it again; no delivery recorded; 0 held"},
		{"a live watcher of another session", func(t *testing.T, f *fixture) func(int) (procutil.Info, error) {
			t.Helper()
			f.watcherSays(t, watcherPID, watchstate.Connected, time.Hour)
			return f.liveWatcher(t, "another-session", "0.15.0")
		}, "delivery: watcher not running; the next prompt starts it again; no delivery recorded; 0 held"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := deliveryFixture(t)
			if got := deliveryLine(t, f, tc.setup(t, f)); got != tc.want {
				t.Errorf("line:\n got %q\nwant %q", got, tc.want)
			}
			if n := f.rec.count(); n != 1 {
				t.Errorf("spawned %d, want the describe alone: delivery is read from local files", n)
			}
		})
	}
}

// TestWhoamiDeliveryWhenAFileCannotBeRead: a pending file that is not the
// session's own leaves the count out instead of saying zero.
func TestWhoamiDeliveryWhenAFileCannotBeRead(t *testing.T) {
	t.Parallel()
	f := deliveryFixture(t)
	path := inbound.PendingPath(f.stateDir, selfSessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a pending file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := deliveryLine(t, f, deadProcess), "delivery: watcher not running; the next prompt starts it again; no delivery recorded; held count not readable"; got != want {
		t.Errorf("line:\n got %q\nwant %q", got, want)
	}
	if f.errb.Len() != 0 {
		t.Errorf("stderr = %q", f.errb.String())
	}
}

// TestWhoamiDeliveryJSON: the same facts as members, with times instead of
// ages and with no path and no id among them.
func TestWhoamiDeliveryJSON(t *testing.T) {
	t.Parallel()
	f := deliveryFixture(t)
	f.watcherSays(t, watcherPID, watchstate.Retrying, 17*time.Minute)
	f.deliveredAgo(t, 3*time.Hour)
	f.holds(t, 2)
	inv := f.inv(f.sessionEnv(), "")
	inv.JSON = true
	inv.Deps.Lookup = f.liveWatcher(t, selfSessionID, "0.15.0")
	if err := Whoami(inv); err != nil {
		t.Fatal(err)
	}
	_, result := envelopeOf(t, f.out.String())
	d, _ := result["delivery"].(map[string]any)
	if d["watcher"] != WatcherRunning || d["watcher_version"] != "0.15.0" || d["connection"] != watchstate.Retrying ||
		d["connection_since"] != "2026-09-02T11:43:30Z" || d["last_delivery_at"] != "2026-09-02T09:00:30Z" || d["held"] != float64(2) {
		t.Errorf("delivery = %v", d)
	}
	if len(d) != 6 {
		t.Errorf("delivery has %d members, want 6: %v", len(d), d)
	}
	for _, leak := range []string{f.stateDir, "watch.json", "pending", "held-a", "tok-777"} {
		if strings.Contains(f.out.String(), leak) {
			t.Errorf("the envelope carries %q", leak)
		}
	}

	// No watcher, nothing delivered, nothing held: three members.
	g := newFixture(t)
	inv = g.inv(g.sessionEnv(), "")
	inv.JSON = true
	inv.Deps.Lookup = deadProcess
	if err := Whoami(inv); err != nil {
		t.Fatal(err)
	}
	_, result = envelopeOf(t, g.out.String())
	d, _ = result["delivery"].(map[string]any)
	if d["watcher"] != WatcherNone || d["held"] != float64(0) || len(d) != 2 {
		t.Errorf("delivery = %v", d)
	}
}

// TestAgoText: an age in its largest whole unit.
func TestAgoText(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		-time.Minute:                    "0s",
		0:                               "0s",
		999 * time.Millisecond:          "0s",
		59 * time.Second:                "59s",
		time.Minute:                     "1m",
		59*time.Minute + 59*time.Second: "59m",
		time.Hour:                       "1h",
		47*time.Hour + 59*time.Minute:   "47h",
		48 * time.Hour:                  "2d",
		9*24*time.Hour + 23*time.Hour:   "9d",
	} {
		if got := agoText(d); got != want {
			t.Errorf("agoText(%v) = %q, want %q", d, got, want)
		}
	}
}
