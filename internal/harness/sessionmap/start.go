package sessionmap

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// StartFacts is what the SessionStart hook knows about a Claude Code
// process BEFORE it knows whether the session attaches to a team (P7-11):
// the resolved Brigade config directory (the config_dir option applied —
// plugin options never reach the Bash tool, so a command run inside the
// session has no other way to learn it), the bootstrap's realpath and the
// native session id. It is written on every SessionStart, joined or not,
// and removed at SessionEnd, so an in-session `team create`/`team join` in
// a not-yet-attached session writes its credential and pin into the SAME
// store the hooks read. It carries no identity: the by-pid map stays the
// only thing that says which Brigade session a process is, and its
// Validate is untouched.
type StartFacts struct {
	// ClaudePID is the Claude Code process id; it is also the file name.
	ClaudePID int `json:"claude_pid"`
	// ClaudeSessionID is the native session id from the hook's stdin.
	ClaudeSessionID string `json:"claude_session_id"`
	// ConfigDir is the resolved absolute Brigade config directory.
	ConfigDir string `json:"config_dir"`
	// PluginBin is the bootstrap's resolved realpath when the hook knows
	// it, else "".
	PluginBin string `json:"plugin_bin"`
	// WrittenAt is the hook's clock at the write.
	WrittenAt time.Time `json:"written_at"`
}

// Validate checks the members a reader relies on.
func (f *StartFacts) Validate() error {
	switch {
	case f.ClaudePID <= 0:
		return errInvalid("claude_pid")
	case !filepath.IsAbs(f.ConfigDir):
		return errInvalid("config_dir")
	case f.PluginBin != "" && !filepath.IsAbs(f.PluginBin):
		return errInvalid("plugin_bin")
	}
	return nil
}

// StartPath is the start-facts file for pid: ${stateDir}/sessions/by-pid/
// <pid>.start.json, beside the map it precedes.
func (s Store) StartPath(pid int) (string, error) {
	if err := s.check(); err != nil {
		return "", err
	}
	if pid <= 0 {
		return "", errInvalid("claude_pid")
	}
	return filepath.Join(s.ByPIDDir(), strconv.Itoa(pid)+".start.json"), nil
}

// WriteStart validates f and writes it atomically (0600 in a 0700 chain),
// overwriting any previous file for the same pid.
func (s Store) WriteStart(f *StartFacts) error {
	if err := f.Validate(); err != nil {
		return err
	}
	path, err := s.StartPath(f.ClaudePID)
	if err != nil {
		return err
	}
	return writeAtomic(path, f)
}

// ReadStart reads the start facts for pid through the strict reader (the
// map's own privacy rule: regular, 0600, owned by the caller, no symlink)
// and validates them. A missing file satisfies errors.Is(err,
// fs.ErrNotExist).
func (s Store) ReadStart(pid int) (*StartFacts, error) {
	path, err := s.StartPath(pid)
	if err != nil {
		return nil, err
	}
	data, err := s.readStrict(path)
	if err != nil {
		return nil, err
	}
	var f StartFacts
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errMalformed(path, "not_json")
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if f.ClaudePID != pid {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the session's start facts name a different Claude Code process than their file; refusing to trust them",
			Details: map[string]string{"reason": ReasonMapMismatch, "path": path},
		}
	}
	return &f, nil
}

// DeleteStart removes the start facts at SessionEnd. A missing file is not
// an error.
func (s Store) DeleteStart(pid int) error {
	path, err := s.StartPath(pid)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the session's start facts could not be removed",
			Details: map[string]string{"reason": ReasonMapUnreadable, "path": path},
		}
	}
	return nil
}
