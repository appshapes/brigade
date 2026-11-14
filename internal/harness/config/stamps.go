package config

import (
	"crypto/sha256"
	"encoding/hex"
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

// TeamMovedStamp records what the prompt hook has told a running session
// about its team's move to another backend: ${stateDir}/state/<pid>.team-moved
// (work items 273 and 327, ADR 0007). Each line about a move is said once
// in a conversation, so the file holds the native id of the conversation
// that was told and one word for each line it has heard. A stamp that a
// dead session left under the same process id names another conversation
// and silences nobody; SessionEnd removes it, and so does the prompt that
// moves the session.
func TeamMovedStamp(stateDir string, pid int) string {
	return filepath.Join(stateDir, "state", strconv.Itoa(pid)+".team-moved")
}

// MoveAttemptStamp is when a prompt hook last started the follow of a
// team's move for a checkout: ${stateDir}/state/move-<digest>.attempt
// (work item 327, ADR 0007). A follow asks the backend the checkout joined
// one question, and this file is what keeps that question from being asked
// at every prompt: at most one follow a minute is started for a checkout,
// whichever of its sessions is prompted. The digest is of the member's
// store and the checkout, so two members on one machine do not share it.
func MoveAttemptStamp(stateDir, configDir, checkout string) string {
	return filepath.Join(stateDir, "state", "move-"+moveDigest(configDir, checkout)+".attempt")
}

// MoveFollowedMark is what a follow that ran outside a session's start
// leaves for the checkout it followed in: ${stateDir}/state/
// move-<digest>.followed (work item 327, ADR 0007). The follow has nobody
// to tell, so the next hook of a session in that checkout says the
// follow's line and removes the file: the session that asked, or, when it
// ended first, the next one to start there.
func MoveFollowedMark(stateDir, configDir, checkout string) string {
	return filepath.Join(stateDir, "state", "move-"+moveDigest(configDir, checkout)+".followed")
}

// moveDigest names a checkout of one member's store in a file name: 16
// bytes of the SHA-256 of the two paths, a zero byte between them.
func moveDigest(configDir, checkout string) string {
	sum := sha256.Sum256([]byte(configDir + "\x00" + checkout))
	return hex.EncodeToString(sum[:16])
}

// MaxSessionNameChars caps the name `brigade rename` records and the
// prompt hook applies (card 174): Claude Code's own cap on a /rename name.
const MaxSessionNameChars = 200

// RenameStamp is the pending session name `brigade rename` leaves for the
// prompt hook (card 174): one line, `<claude_session_id> <name>`, written
// by the command and taken — read and removed — by the next prompt hook,
// which answers Claude Code with the name as its output's sessionTitle.
// Here for the same reason as the stamps above: both packages need the
// path and neither imports the other.
func RenameStamp(stateDir string, pid int) string {
	return filepath.Join(stateDir, "state", strconv.Itoa(pid)+".rename")
}
