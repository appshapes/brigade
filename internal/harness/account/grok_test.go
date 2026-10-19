package account

import (
	"errors"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

func TestGrokNeverReadsAClaudeAccount(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	env := []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=/never-read", config.GrokSessionIDVar + "=synthetic"}
	if got := Paths(env); len(got) != 0 {
		t.Fatalf("Claude account paths for Grok: %v", got)
	}
	if email, err := Email(env); email != "" || !errors.Is(err, ErrNoConfigDir) {
		t.Fatalf("email %q error %v", email, err)
	}
	// A Claude session that merely inherited a Grok terminal tool's
	// GROK_SESSION_ID is still a Claude session (config.HostOf keeps Grok
	// last): it reads its own account, as every Claude session does.
	nested := append(env, "CLAUDE_PID=42")
	if got := Paths(nested); len(got) != 2 {
		t.Fatalf("a nested Claude session lost its account paths: %v", got)
	}
}
