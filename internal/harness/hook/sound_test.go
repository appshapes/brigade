package hook

import (
	"errors"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sound"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestMessageSoundLines pins what SessionStart freezes for the message
// sound and the one line it prints (card 35): the option unset or off
// freezes off and says nothing; on with a player freezes on and says
// nothing — the sound speaks for itself; on without a player freezes off
// and names why; a value that is neither word freezes off with the fixed
// warning. In every case the session connects, and no line carries a
// path.
func TestMessageSoundLines(t *testing.T) {
	t.Parallel()
	playerFound := func(string) ([]string, string) { return []string{"/opt/bin/afplay", "-v", "0.25", "/x.aiff"}, "" }
	playerMissing := func(string) ([]string, string) { return nil, sound.WhyNoPlayerDarwin }
	cases := map[string]struct {
		option string // CLAUDE_PLUGIN_OPTION_MESSAGE_SOUND; "" leaves it unset
		player func(string) ([]string, string)
		want   []string
		on     bool
	}{
		"unset":               {},
		"off":                 {option: "off", player: playerFound},
		"on with a player":    {option: "on", player: playerFound, on: true},
		"on without a player": {option: "on", player: playerMissing, want: []string{"Brigade: message sound off (" + sound.WhyNoPlayerDarwin + ")."}},
		"neither word":        {option: "loud", player: playerFound, want: []string{config.WarnMessageSoundInvalid}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
			if tc.player != nil {
				f.deps.SoundPlayer = tc.player
			}
			var extra []string
			if tc.option != "" {
				extra = append(extra, config.OptionMessageSound+"="+tc.option)
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
				t.Fatalf("a line carries the player's path: %q", out)
			}
			if m := f.mustMap(); m.MessageSound != tc.on {
				t.Fatalf("frozen message_sound = %v, want %v", m.MessageSound, tc.on)
			}
		})
	}
}

// TestMessageSoundLineNeedsAWatcher: the sound plays in the watcher, so a
// session that runs none says the sound is off with the reason file sync
// gives — no inbox socket, or a spawn that failed — even with the option
// on and a player at hand. The map still freezes what was resolved.
func TestMessageSoundLineNeedsAWatcher(t *testing.T) {
	t.Parallel()
	playerFound := func(string) ([]string, string) { return []string{"/opt/bin/afplay"}, "" }
	for name, tc := range map[string]struct {
		noSocket bool
		spawnErr error
		want     string
	}{
		"no inbox socket":  {noSocket: true, want: "Brigade: message sound off (no watcher runs without an inbox socket)."},
		"the spawn failed": {spawnErr: errors.New("fork failed"), want: "Brigade: message sound off (the watcher has not started yet)."},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.noSocket = tc.noSocket
			f.spawner.err = tc.spawnErr
			f.deps.SoundPlayer = playerFound
			f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"), config.OptionMessageSound+"=on")
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			got := lines(out)
			if len(got) != 2 || !strings.HasPrefix(got[0], "Brigade: this session is ") || got[1] != tc.want {
				t.Fatalf("lines = %q\nwant the context line then %q", got, tc.want)
			}
			if m := f.mustMap(); !m.MessageSound {
				t.Fatal("the map does not carry message_sound for an on session")
			}
		})
	}
}

// TestMessageSoundFlipOnTheContinuePath: the watcher re-reads the map on
// every liveness tick, so a flipped option on the continue path (/clear)
// rewrites the map and keeps the live watcher — no respawn, unlike a
// changed sync member — and the same option keeps it too.
func TestMessageSoundFlipOnTheContinuePath(t *testing.T) {
	t.Parallel()
	playerFound := func(string) ([]string, string) { return []string{"/opt/bin/afplay"}, "" }
	f := newFixture(t)
	old := testutil.NewSleeper(t)
	f.spawner.watcherPID = old
	f.deps.SoundPlayer = playerFound
	f.useSeam(map[string][]fakeadapter.Response{
		"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
		"session heartbeat": {okResp(heartbeatDoc())},
	})
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if m := f.mustMap(); m.MessageSound {
		t.Fatal("the map carries message_sound with the option unset")
	}
	for i, tc := range []struct {
		option string
		on     bool
	}{
		{"on", true},
		{"on", true},
		{"off", false},
	} {
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"), config.OptionMessageSound+"="+tc.option)
		if exit != 0 {
			t.Fatalf("run %d: exit %d: %s", i, exit, errOut)
		}
		if got := lines(out); len(got) != 1 {
			t.Fatalf("run %d: lines = %q, want the context line alone", i, got)
		}
		if f.spawner.count() != 1 || !alive(old) {
			t.Fatalf("run %d: the watcher was respawned (%d) or killed (%v) for a sound flip", i, f.spawner.count(), alive(old))
		}
		if m := f.mustMap(); m.MessageSound != tc.on {
			t.Fatalf("run %d: map message_sound = %v, want %v", i, m.MessageSound, tc.on)
		}
	}
}
