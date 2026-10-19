package account

import (
	"errors"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

func TestGrokNeverReadsAClaudeAccount(t *testing.T) {
	t.Parallel()
	env := []string{"HOME=" + t.TempDir(), "CLAUDE_CONFIG_DIR=/never-read", config.GrokSessionIDVar + "=synthetic", "CLAUDE_PID=42"}
	if got := Paths(env); len(got) != 0 {
		t.Fatalf("Claude account paths for Grok: %v", got)
	}
	if email, err := Email(env); email != "" || !errors.Is(err, ErrNoConfigDir) {
		t.Fatalf("email %q error %v", email, err)
	}
}
