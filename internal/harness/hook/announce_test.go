package hook

import (
	"errors"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// The two announcement options of cards 35 and 36 through SessionStart:
// what it freezes into the map and the one line it prints. A table row
// names the option's environment variable, the fixture seam it probes,
// the line's subject and the map member, so one body covers both.
var announceKinds = map[string]struct {
	env  string
	what string
	warn string
	seam func(f *fixture, probe func(string) ([]string, string))
}{
	"sound": {config.OptionMessageSound, "message sound", config.WarnMessageSoundInvalid,
		func(f *fixture, p func(string) ([]string, string)) { f.deps.SoundPlayer = p }},
	"notification": {config.OptionMessageNotification, "message notification", config.WarnMessageNotificationInvalid,
		func(f *fixture, p func(string) ([]string, string)) { f.deps.Notifier = p }},
}

func frozen(f *fixture, kind string) bool {
	m := f.mustMap()
	if kind == "sound" {
		return m.MessageSound
	}
	return m.MessageNotification
}

// TestAnnounceLines pins what SessionStart freezes for each option and
// the one line it prints: unset or off freezes off and says nothing; on
// with a program freezes on and says nothing — the sound or the banner
// speaks for itself; on without one freezes off and names why; a value
// that is neither word freezes off with the fixed warning. In every case
// the session connects, and no line carries a path.
func TestAnnounceLines(t *testing.T) {
	t.Parallel()
	found := func(string) ([]string, string) { return []string{"/opt/bin/program", "-x"}, "" }
	const why = "no program here" // stands for any of notify's fixed reasons; the line carries it verbatim
	missing := func(string) ([]string, string) { return nil, why }
	for kind, k := range announceKinds {
		cases := map[string]struct {
			option string
			probe  func(string) ([]string, string)
			want   []string
			on     bool
		}{
			"unset":                {},
			"off":                  {option: "off", probe: found},
			"on with a program":    {option: "on", probe: found, on: true},
			"on without a program": {option: "on", probe: missing, want: []string{"Brigade: " + k.what + " off (" + why + ")."}},
			"neither word":         {option: "loud", probe: found, want: []string{k.warn}},
		}
		for name, tc := range cases {
			t.Run(kind+", "+name, func(t *testing.T) {
				t.Parallel()
				f := newFixture(t)
				seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
				if tc.probe != nil {
					k.seam(f, tc.probe)
				}
				var extra []string
				if tc.option != "" {
					extra = append(extra, k.env+"="+tc.option)
				}
				exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"), extra...)
				if exit != 0 {
					t.Fatalf("exit %d: %s", exit, errOut)
				}
				if got := strings.Join(seam.verbs(), ","); got != "describe,session register" {
					t.Fatalf("calls %q: the session must connect whatever the option says", got)
				}
				got := lines(out)
				if len(got) != 1+len(tc.want) || !strings.HasPrefix(got[0], "Brigade: this session is ") {
					t.Fatalf("lines = %q\nwant the context line then %q", got, tc.want)
				}
				for i, w := range tc.want {
					if got[1+i] != w {
						t.Fatalf("line %d = %q\nwant      %q", 1+i, got[1+i], w)
					}
				}
				if strings.Contains(out, "/opt/bin") {
					t.Fatalf("a line carries the program's path: %q", out)
				}
				if got := frozen(f, kind); got != tc.on {
					t.Fatalf("frozen %s = %v, want %v", kind, got, tc.on)
				}
				other := "notification"
				if kind == "notification" {
					other = "sound"
				}
				if frozen(f, other) {
					t.Fatalf("the %s option froze the %s member too", kind, other)
				}
			})
		}
	}
}

// TestAnnounceLinesNeedAWatcher: both run in the watcher, so a session
// that runs none says each is off with the reason file sync gives — no
// inbox socket, or a spawn that failed — even with the options on and
// the programs at hand. The map still freezes what was resolved, and
// with both on the two lines come in a fixed order: sound, notification.
func TestAnnounceLinesNeedAWatcher(t *testing.T) {
	t.Parallel()
	found := func(string) ([]string, string) { return []string{"/opt/bin/program"}, "" }
	for name, tc := range map[string]struct {
		noSocket bool
		spawnErr error
		why      string
	}{
		"no inbox socket":  {noSocket: true, why: "no watcher runs without an inbox socket"},
		"the spawn failed": {spawnErr: errors.New("fork failed"), why: "the watcher has not started yet"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.noSocket = tc.noSocket
			f.spawner.err = tc.spawnErr
			f.deps.SoundPlayer = found
			f.deps.Notifier = found
			f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"),
				config.OptionMessageSound+"=on", config.OptionMessageNotification+"=on")
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			want := []string{"Brigade: message sound off (" + tc.why + ").", "Brigade: message notification off (" + tc.why + ")."}
			got := lines(out)
			if len(got) != 3 || !strings.HasPrefix(got[0], "Brigade: this session is ") || got[1] != want[0] || got[2] != want[1] {
				t.Fatalf("lines = %q\nwant the context line then %q", got, want)
			}
			if m := f.mustMap(); !m.MessageSound || !m.MessageNotification {
				t.Fatalf("the map does not carry both members for on sessions: %v %v", m.MessageSound, m.MessageNotification)
			}
		})
	}
}

// TestAnnounceFlipOnTheContinuePath: the watcher re-reads the map on
// every liveness tick, so a flipped option on the continue path (/clear)
// rewrites the map and keeps the live watcher — no respawn, unlike a
// changed sync member — and the same options keep it too.
func TestAnnounceFlipOnTheContinuePath(t *testing.T) {
	t.Parallel()
	found := func(string) ([]string, string) { return []string{"/opt/bin/program"}, "" }
	f := newFixture(t)
	old := testutil.NewSleeper(t)
	f.spawner.watcherPID = old
	f.deps.SoundPlayer = found
	f.deps.Notifier = found
	f.useSeam(map[string][]fakeadapter.Response{
		"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
		"session heartbeat": {okResp(heartbeatDoc())},
	})
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if m := f.mustMap(); m.MessageSound || m.MessageNotification {
		t.Fatal("the map carries an announcement member with the options unset")
	}
	for i, tc := range []struct {
		sound, notification string
		wantSound, wantNote bool
	}{
		{"on", "", true, false},
		{"on", "on", true, true},
		{"off", "on", false, true},
		{"", "", false, false},
	} {
		var extra []string
		if tc.sound != "" {
			extra = append(extra, config.OptionMessageSound+"="+tc.sound)
		}
		if tc.notification != "" {
			extra = append(extra, config.OptionMessageNotification+"="+tc.notification)
		}
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"), extra...)
		if exit != 0 {
			t.Fatalf("run %d: exit %d: %s", i, exit, errOut)
		}
		if got := lines(out); len(got) != 1 {
			t.Fatalf("run %d: lines = %q, want the context line alone", i, got)
		}
		if f.spawner.count() != 1 || !alive(old) {
			t.Fatalf("run %d: the watcher was respawned (%d) or killed (%v) for an option flip", i, f.spawner.count(), alive(old))
		}
		if m := f.mustMap(); m.MessageSound != tc.wantSound || m.MessageNotification != tc.wantNote {
			t.Fatalf("run %d: map sound %v notification %v, want %v %v", i, m.MessageSound, m.MessageNotification, tc.wantSound, tc.wantNote)
		}
	}
}

// TestMessageIntervalFreezesAndWarns (card 38): a good value is frozen
// into the map in seconds with no line; unset freezes the default; a bad
// value freezes the default with the one fixed warning, after any
// announcement line, and the session connects.
func TestMessageIntervalFreezesAndWarns(t *testing.T) {
	t.Parallel()
	found := func(string) ([]string, string) { return []string{"/opt/bin/program"}, "" }
	for name, tc := range map[string]struct {
		interval string
		seconds  int
		warn     bool
	}{
		"unset":       {"", 30, false},
		"two minutes": {"120", 120, false},
		"too short":   {"1", 30, true},
		"not seconds": {"30s", 30, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.deps.SoundPlayer = found
			f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
			extra := []string{config.OptionMessageSound + "=on"}
			if tc.interval != "" {
				extra = append(extra, config.OptionMessageInterval+"="+tc.interval)
			}
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"), extra...)
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			got := lines(out)
			want := 1
			if tc.warn {
				want = 2
			}
			if len(got) != want || !strings.HasPrefix(got[0], "Brigade: this session is ") {
				t.Fatalf("lines = %q, want %d", got, want)
			}
			if tc.warn && got[1] != config.WarnMessageIntervalInvalid {
				t.Fatalf("line 1 = %q, want the interval warning", got[1])
			}
			if m := f.mustMap(); m.MessageIntervalSeconds != tc.seconds || !m.MessageSound {
				t.Fatalf("map interval %d sound %v, want %d and on", m.MessageIntervalSeconds, m.MessageSound, tc.seconds)
			}
		})
	}
}
