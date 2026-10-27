//go:build !windows

package commands

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestStartFactsOnPointedHosts pins card 116: an in-session command on Muse
// Code or Codex CLI, whose shell carries the native id and not the key,
// reads the SessionStart facts through the start pointer the hook left —
// the road `team join` and `team create` take before the session has a
// map. Such a session resolves Brigade's directories from HOME alone, so
// the facts live under HOME/.local/state/brigade whatever XDG_* says.
func TestStartFactsOnPointedHosts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	stateDir := filepath.Join(f.dirs.Home, ".local", "state", "brigade")
	store := sessionmap.Store{StateDir: stateDir}
	for _, tc := range []struct{ host, variable, id string }{
		{"codex", "CODEX_THREAD_ID", "01a14aa0-8a90-7712-a2b8-79405a4acbca"},
		{"muse", "MUSE_SESSION_ID", "01a14785-947f-7d93-a4f4-6ee1f6b2be94"},
	} {
		env := []string{"HOME=" + f.dirs.Home, "XDG_STATE_HOME=/nowhere", tc.variable + "=" + tc.id}
		inv := f.inv(env, "")
		// No pointer yet: not_registered, with the remedy, never a Claude-only refusal.
		_, err := inv.startFacts(stateDir)
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Details["reason"] != config.ReasonNotRegistered || strings.Contains(perr.Message, "Claude Code") {
			t.Fatalf("%s without a pointer: %v", tc.host, err)
		}
		key := 1<<30 | 12345
		if err := store.WriteStart(&sessionmap.StartFacts{ClaudePID: key, ClaudeSessionID: tc.id, ConfigDir: f.dirs.BrigadeConfig, LabelOption: "none", WrittenAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := store.WriteStartPointer(tc.host, tc.id, key, time.Now()); err != nil {
			t.Fatal(err)
		}
		facts, err := inv.startFacts(stateDir)
		if err != nil || facts.ConfigDir != f.dirs.BrigadeConfig || facts.ClaudePID != key {
			t.Fatalf("%s: facts %+v %v", tc.host, facts, err)
		}
		// The label option rides the same road (card 24, part B).
		if got := inv.labelOption(); got != config.LabelNone {
			t.Fatalf("%s: labelOption %q, want none from the facts", tc.host, got)
		}
		// The config directory a writer would use is the facts' (dialectTarget's rule).
		if dir, err := inv.storeDir(); err != nil || dir != f.dirs.BrigadeConfig {
			t.Fatalf("%s: storeDir %q %v", tc.host, dir, err)
		}
	}
	// A Claude session is untouched: the facts are read by CLAUDE_PID, in the XDG state directory.
	claudeStore := sessionmap.Store{StateDir: f.stateDir}
	if err := claudeStore.WriteStart(&sessionmap.StartFacts{ClaudePID: fixturePID, ConfigDir: f.dirs.BrigadeConfig, WrittenAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if facts, err := f.inv(f.sessionEnv(), "").startFacts(f.stateDir); err != nil || facts.ClaudePID != fixturePID {
		t.Fatalf("claude: %+v %v", facts, err)
	}
}
