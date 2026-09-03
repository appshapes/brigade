// Package registry reads Claude Code's own session registry, best effort
// and read-only (plan 6.5, A.3): $CLAUDE_CONFIG_DIR/sessions/<pid>.json,
// an undocumented file that carries the session's display name (set with
// --name or /rename), its busy/idle status, the inbox socket path and the
// Claude Code version. The hook feeds the name and status to `session
// register` and the heartbeats.
//
// The reader opens exactly ONE name, <pid>.json, through the fs.FS it is
// handed. It never lists the directory and never opens the 0600
// <pid>.<sha>.key peer auth key that lives beside every entry (6.5; the
// testutil/fakeregistry recorder fails any test in which a key name is
// opened). Nothing is ever written.
//
// The format is Claude Code's to change: a missing or malformed file, or
// a member of an unexpected type, yields Found=false or an empty member,
// never a failure the hook has to act on — the plan's fallbacks (hook
// stdin session_title, then basename(cwd); activity idle) are the hook's.
package registry

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// The two `status` values the registry uses. Anything else reads as idle.
const (
	StatusBusy = "busy"
	StatusIdle = "idle"
)

// MaxEntryBytes caps what Read will read of one entry. The observed entry
// is under 1 KiB; the cap keeps a corrupt or hostile file harmless.
const MaxEntryBytes = 256 << 10

// sessionsDirName is the registry directory under CLAUDE_CONFIG_DIR.
const sessionsDirName = "sessions"

// An Entry is what the harness uses of a registry record. Every member
// is optional in the file; Found reports whether the file existed and
// parsed as a JSON object at all. Strings are as written by Claude Code
// (user-chosen, unsanitised): the caller sanitises before printing or
// registering them.
type Entry struct {
	// Found is true when <pid>.json existed and was a JSON object.
	Found bool
	// Name is the display name (`name`); NameSource says who set it
	// (`nameSource`, e.g. "user").
	Name       string
	NameSource string
	// Status is StatusBusy or StatusIdle when Found — any other value in
	// the file, including none, is normalised to idle — and "" otherwise.
	Status string
	// MessagingSocketPath is `messagingSocketPath`, the inbox socket.
	MessagingSocketPath string
	// Entrypoint is `entrypoint` ("cli" or "sdk-cli"); Kind is `kind`,
	// which is informational only (a -p session reads kind interactive,
	// entrypoint sdk-cli — 6.5).
	Entrypoint string
	Kind       string
	// Version is Claude Code's version (`version`), used as
	// harness_version when present.
	Version string
}

// Activity is the heartbeat activity the entry implies: busy only when
// the entry was found and says so, otherwise idle (6.5).
func (e Entry) Activity() string {
	if e.Found && e.Status == StatusBusy {
		return StatusBusy
	}
	return StatusIdle
}

// Dir returns the fs.FS rooted at the registry directory of
// claudeConfigDir — the value Read expects. The caller resolves
// claudeConfigDir (config.ClaudeConfigDir); it is never assumed here.
func Dir(claudeConfigDir string) fs.FS {
	return os.DirFS(filepath.Join(claudeConfigDir, sessionsDirName))
}

// FileName is the one name Read opens for pid.
func FileName(pid int) string {
	return strconv.Itoa(pid) + ".json"
}

// Read reads the registry entry for pid from fsys, best effort. The
// returned error is DIAGNOSTIC ONLY — worth a debug log line, never an
// action: a missing file satisfies errors.Is(err, fs.ErrNotExist), a
// malformed one carries fixed text, and in both cases Found is false and
// every member is zero. A member of the wrong type is skipped, not fatal.
func Read(fsys fs.FS, pid int) (Entry, error) {
	if pid <= 0 {
		return Entry{}, errors.New("registry: pid must be positive")
	}
	f, err := fsys.Open(FileName(pid))
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxEntryBytes+1))
	if err != nil {
		return Entry{}, errors.New("registry: entry could not be read")
	}
	if len(data) > MaxEntryBytes {
		return Entry{}, errors.New("registry: entry exceeds the size cap")
	}
	var raw map[string]jsontext.Value
	if err := json.Unmarshal(data, &raw); err != nil {
		return Entry{}, errors.New("registry: entry is not a JSON object")
	}
	e := Entry{
		Found:               true,
		Name:                member(raw, "name"),
		NameSource:          member(raw, "nameSource"),
		MessagingSocketPath: member(raw, "messagingSocketPath"),
		Entrypoint:          member(raw, "entrypoint"),
		Kind:                member(raw, "kind"),
		Version:             member(raw, "version"),
	}
	if member(raw, "status") == StatusBusy {
		e.Status = StatusBusy
	} else {
		e.Status = StatusIdle
	}
	return e, nil
}

// member returns the string member name of raw, or "" when it is absent
// or not a JSON string.
func member(raw map[string]jsontext.Value, name string) string {
	v, ok := raw[name]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}
