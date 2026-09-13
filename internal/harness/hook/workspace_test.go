package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// registerWithLabel runs SessionStart against a seeded checkout and returns
// the registration the adapter saw and the by-pid map the hook wrote.
func registerWithLabel(t *testing.T, gitConfig string, extraEnv ...string) (protocol.SessionRegistration, string) {
	t.Helper()
	f := newFixture(t)
	f.seedTeam(t)
	if gitConfig != "" {
		if err := os.WriteFile(filepath.Join(f.cwd, ".git", "config"), []byte(gitConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
	exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup"), extraEnv...)
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	var reg protocol.SessionRegistration
	if err := json.Unmarshal(seam.callsFor("session register")[0].Stdin, &reg); err != nil {
		t.Fatal(err)
	}
	return reg, f.mustMap().WorkspaceLabel
}

// TestSessionStartRegistersTheRepositoryName (P11-5): with nothing
// configured the session carries its repository's name as the workspace
// label — the `origin` remote's, else the checkout directory's — in the
// registration and in the by-pid map the watcher re-opens from.
func TestSessionStartRegistersTheRepositoryName(t *testing.T) {
	t.Parallel()
	t.Run("from the origin remote", func(t *testing.T) {
		t.Parallel()
		reg, mapped := registerWithLabel(t, "[remote \"origin\"]\n\turl = git@github.com:appshapes/payments-api.git\n")
		if reg.WorkspaceLabel == nil || *reg.WorkspaceLabel != "payments-api" || mapped != "payments-api" {
			t.Fatalf("registered %v, map %q; want payments-api in both", reg.WorkspaceLabel, mapped)
		}
	})
	t.Run("from the directory when there is no remote", func(t *testing.T) {
		t.Parallel()
		reg, mapped := registerWithLabel(t, "")
		if reg.WorkspaceLabel == nil || *reg.WorkspaceLabel != "checkout" || mapped != "checkout" {
			t.Fatalf("registered %v, map %q; want the checkout's directory name", reg.WorkspaceLabel, mapped)
		}
	})
	t.Run("the user's own label replaces it", func(t *testing.T) {
		t.Parallel()
		reg, mapped := registerWithLabel(t, "[remote \"origin\"]\n\turl = git@github.com:appshapes/payments-api.git\n",
			config.OptionWorkspaceLabel+"=laptop")
		if reg.WorkspaceLabel == nil || *reg.WorkspaceLabel != "laptop" || mapped != "laptop" {
			t.Fatalf("registered %v, map %q; want laptop", reg.WorkspaceLabel, mapped)
		}
	})
	t.Run("share_workspace_label off sends nothing", func(t *testing.T) {
		t.Parallel()
		reg, mapped := registerWithLabel(t, "[remote \"origin\"]\n\turl = git@github.com:appshapes/payments-api.git\n",
			config.OptionShareWorkspaceLabel+"=false", config.OptionWorkspaceLabel+"=laptop")
		if reg.WorkspaceLabel != nil || mapped != "" {
			t.Fatalf("registered %v, map %q; want neither", reg.WorkspaceLabel, mapped)
		}
	})
}
