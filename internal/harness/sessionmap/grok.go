package sessionmap

import (
	"path/filepath"
	"strings"

	"github.com/appshapes/brigade/internal/procutil"
)

// GrokReceiver is local state, never a protocol-v1 registration member.
type GrokReceiver struct {
	Cwd           string `json:"cwd"`
	ProcessStart  string `json:"process_start"`
	Incarnation   string `json:"incarnation"`
	MonitorState  string `json:"monitor_state"`
	MonitorTaskID string `json:"monitor_task_id,omitzero"`
}

// ValidGrokReceiver refuses unknown or incomplete receiver bindings.
func (m *ByPID) ValidGrokReceiver() bool {
	if m == nil || m.Harness != "grok" || m.Grok == nil || m.HostProcessPID != 0 && m.HostProcessPID != m.ClaudePID || !filepath.IsAbs(m.Grok.Cwd) || m.Grok.ProcessStart == "" || len(m.Grok.Incarnation) != 32 {
		return false
	}
	return strings.Trim(m.Grok.Incarnation, "0123456789abcdef") == ""
}

// SameReceiver excludes mutable monitor observations, retaining the exact ownership binding.
func SameReceiver(a, b *ByPID) bool {
	if a == nil || b == nil || a.ClaudePID != b.ClaudePID || a.ClaudeSessionID != b.ClaudeSessionID || a.BrigadeSessionID != b.BrigadeSessionID || a.Harness != b.Harness {
		return false
	}
	if a.Harness != "grok" {
		return true
	}
	return a.ValidGrokReceiver() && b.ValidGrokReceiver() && a.Grok.Cwd == b.Grok.Cwd && a.Grok.ProcessStart == b.Grok.ProcessStart && a.Grok.Incarnation == b.Grok.Incarnation
}

// GrokAlive uses the actual TUI process and start identity, never a leader or shell parent.
func (m *ByPID) GrokAlive(lookup func(int) (procutil.Info, error)) bool {
	if lookup == nil || !m.ValidGrokReceiver() {
		return false
	}
	info, err := lookup(m.ClaudePID)
	return err == nil && procutil.IsGrokProcess(info) && info.StartToken == m.Grok.ProcessStart
}

// MonitorCommand is shell text for the native monitor tool, not a subprocess spawn.
// The bootstrap is always one quoted absolute argument, including quotes/newlines in paths.
func (m *ByPID) MonitorCommand() string {
	if !m.ValidGrokReceiver() || !filepath.IsAbs(m.PluginBin) || strings.ContainsAny(m.PluginBin, "\r\n") {
		return ""
	}
	return "GROK_SESSION_ID=" + shellQuote(m.ClaudeSessionID) + " " + shellQuote(m.PluginBin) + " inbox follow --incarnation " + m.Grok.Incarnation
}

// MonitorDescription is an exact correlation token; clipped descriptions cannot prove readiness.
func (m *ByPID) MonitorDescription() string {
	if !m.ValidGrokReceiver() {
		return ""
	}
	return "Brigade inbox " + m.Grok.Incarnation
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
