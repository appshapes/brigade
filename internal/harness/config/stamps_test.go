package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

// TestMoveStampsAreKeyedByStoreAndCheckout: the two files a team's move
// keeps for a checkout (work item 327) are under the state directory's
// `state`, are the same for the same store and checkout, and differ for
// another store, another checkout, and for two pairs of paths that only
// read alike when put end to end. Neither path is in a name: a file name
// under the state directory tells nobody where a member's checkouts are.
func TestMoveStampsAreKeyedByStoreAndCheckout(t *testing.T) {
	t.Parallel()
	state := filepath.Join(t.TempDir(), "brigade")
	store, checkout := filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "checkout")
	attempt, followed := config.MoveAttemptStamp(state, store, checkout), config.MoveFollowedMark(state, store, checkout)
	if attempt == followed || attempt != config.MoveAttemptStamp(state, store, checkout) || followed != config.MoveFollowedMark(state, store, checkout) {
		t.Fatalf("the attempt's stamp %q and the follow's mark %q", attempt, followed)
	}
	for _, path := range []string{attempt, followed} {
		if filepath.Dir(path) != filepath.Join(state, "state") {
			t.Errorf("%q is not in the state directory's state", path)
		}
		if name := filepath.Base(path); !strings.HasPrefix(name, "move-") || strings.Contains(name, "config") || strings.Contains(name, "checkout") {
			t.Errorf("the name %q", name)
		}
	}
	for what, other := range map[string]string{
		"another store":    config.MoveAttemptStamp(state, store+"-b", checkout),
		"another checkout": config.MoveAttemptStamp(state, store, checkout+"-b"),
	} {
		if other == attempt {
			t.Errorf("%s has the same stamp", what)
		}
	}
	if config.MoveAttemptStamp(state, "/a/b", "/c") == config.MoveAttemptStamp(state, "/a", "/b/c") ||
		config.MoveAttemptStamp(state, "/ab", "c") == config.MoveAttemptStamp(state, "/a", "bc") {
		t.Errorf("two pairs of paths that read alike end to end share a stamp")
	}
}
