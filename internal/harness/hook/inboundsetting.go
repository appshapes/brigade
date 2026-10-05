package hook

import (
	"log/slog"
	"path/filepath"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/policy"
)

// ensureInboundSetting is card 50's write, run by SessionStart and by the
// prompt hook before the native scan: in a session that bypasses permission
// prompts, with the claude_inbound_setting option on, put
// `"crossSessionInbound": "accept"` into the user settings file when that
// file has no crossSessionInbound at all (policy.EnsureUserAccept has the
// bounds). It returns the fixed context line for an added, created or
// skipped outcome, and "" when the member was already there — whatever its
// value — or when nothing applied. Both hooks then scan, so the policy they
// decide sees the file as it now is.
func (r *run) ensureInboundSetting(f facts, opts config.Options, permissionMode string) string {
	if !opts.ClaudeInboundSetting || !policy.BypassesPrompts(permissionMode) {
		return ""
	}
	userFile := ""
	if f.claudeConfigDir != "" {
		userFile = filepath.Join(f.claudeConfigDir, "settings.json")
	}
	res := policy.EnsureUserAccept(userFile, policy.EnsureIO{ReadFile: r.deps.ReadFile, WriteFile: r.deps.WriteFile})
	switch res.Outcome {
	case policy.EnsureAdded, policy.EnsureCreated:
		r.log.Info("user settings: crossSessionInbound accept written", slog.String("outcome", res.Outcome))
	case policy.EnsureSkipped:
		r.log.Warn("user settings: crossSessionInbound accept not written", slog.String("reason", res.Reason))
	}
	return res.Line()
}
