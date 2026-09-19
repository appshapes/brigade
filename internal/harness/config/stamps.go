package config

import (
	"path/filepath"
	"strconv"
)

// RegisterRetryStamp is the prompt hook's registration-retry stamp for a
// Claude Code process: ${stateDir}/state/<pid>.register-retry. The hook
// writes it after each failed re-registration so a backend that is down is
// asked once a minute, not once a prompt (P5-18); an in-session `team join`
// removes it so the very next prompt registers the session it just joined
// (P7-11). It lives here because both packages need the path and neither
// imports the other.
func RegisterRetryStamp(stateDir string, pid int) string {
	return filepath.Join(stateDir, "state", strconv.Itoa(pid)+".register-retry")
}

// DoingNudgeStamp is the prompt hook's doing-line reminder stamp for a
// Claude Code process: ${stateDir}/state/<pid>.doing-nudge (card 25, plan
// 5.4). The prompt hook owns it — it records when Brigade last reminded
// the model of its doing line and for which conversation — and the
// watcher removes it when a re-open could not carry the line forward
// (plan 5.3), so the next eligible prompt tells the model, truthfully,
// that its line is blank. Here for the same reason as the retry stamp:
// both packages need the path and neither imports the other.
func DoingNudgeStamp(stateDir string, pid int) string {
	return filepath.Join(stateDir, "state", strconv.Itoa(pid)+".doing-nudge")
}
