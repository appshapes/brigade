// Package pidfile is the watcher's single-instance guard (plan 6.6, the
// `${BRIGADE_STATE_DIR}/watchers/<claude_pid>.json` row of 3.2, corrected
// by E0-5 items 1 and 3; package tree 7.1).
//
// One pidfile per Claude Code process names the watcher serving it: the
// watcher's pid, the start token that identifies THAT incarnation of the
// pid (internal/procutil), the Brigade session it serves, the socket it
// posts to, and the SHA-256 of the messaging token it was spawned with —
// never the token itself (D9: the hash is enough for the next SessionStart
// hook to notice a rotated token and respawn; the token is never written
// to any file, 3.2).
//
// The file is created O_EXCL so two hooks racing to spawn cannot both win,
// and it is removed by CONTENT: [Remove] unlinks only a file that still
// holds exactly the entry the caller wrote (E0-5 item 3 — the replace flow
// leaves the superseded watcher running, and its cleanup must not delete
// the file that by then belongs to its replacement). Liveness is judged by
// [Alive] from a procutil.Info, which reads the process STATE: a zombie
// reads as dead even though kill(pid, 0) still succeeds on it (E0-5 item
// 1), a foreign pid reads as reused, and a start token that differs byte
// for byte reads as reused.
//
// The package has no policy about WHEN to spawn, replace or exit — that is
// the hook's (P3-4) and the watcher's (P3-5). It reads and writes one file
// and judges one entry.
package pidfile

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// Entry is the pidfile's content (3.2). Field order is the wire order;
// every member is always present so the encoding is one fixed shape and
// [Remove]'s byte comparison has nothing to be surprised by.
type Entry struct {
	// PID is the watcher's own pid.
	PID int `json:"pid"`
	// StartToken is procutil.Info.StartToken for PID at the time the file
	// was written; compared byte for byte, never parsed.
	StartToken string `json:"start_token"`
	// BrigadeSessionID is the session the watcher serves.
	BrigadeSessionID string `json:"brigade_session_id"`
	// SocketPath is the inbox socket the watcher posts to.
	SocketPath string `json:"socket_path"`
	// TokenSHA256 is [TokenSHA256] of the messaging token the watcher was
	// spawned with, or empty in sink mode where there is no token.
	TokenSHA256 string `json:"token_sha256"`
	// Version is buildinfo.String() of the binary the watcher runs, from
	// 0.5.1; the hooks replace a live watcher whose version is not their
	// own, so a plugin update reaches a running session at its next prompt
	// instead of when the session ends. A pidfile without it (0.5.0 and
	// older) reads as "", which is never the current version.
	Version string `json:"version,omitzero"`
}

// maxBytes bounds what [Read] will load: a pidfile is a few hundred bytes,
// and anything larger is not one.
const maxBytes = 4096

var (
	// ErrMalformed reports a file (or an Entry handed to [Create]) that is
	// not a pidfile: unparseable, oversized, a non-positive pid, or an
	// empty start token.
	ErrMalformed = errors.New("pidfile: malformed entry")
	// ErrSuperseded is returned by [Replace] when the file no longer holds
	// the entry the caller expected to retire: another process replaced it
	// first, and this caller's new entry must not be written over it.
	ErrSuperseded = errors.New("pidfile: the file holds a different entry")
)

// Path is the pidfile location for a Claude Code pid:
// `${stateDir}/watchers/<claudePID>.json` (3.2). The pid is an integer the
// caller has already parsed, so no hostile value can reach the path.
func Path(stateDir string, claudePID int) string {
	return filepath.Join(stateDir, "watchers", strconv.Itoa(claudePID)+".json")
}

// Encode renders e as the exact bytes [Create] writes: one JSON object
// with every member, followed by a newline. [Remove] compares against these
// bytes, so an entry re-encoded from the same field values always matches
// the file it wrote.
func Encode(e Entry) []byte {
	// A struct of ints and strings cannot fail to marshal.
	data, _ := json.Marshal(e)
	return append(data, '\n')
}

// validate is the shape check shared by Create and Read.
func validate(e Entry) error {
	if e.PID <= 0 {
		return fmt.Errorf("%w: pid %d", ErrMalformed, e.PID)
	}
	if e.StartToken == "" {
		return fmt.Errorf("%w: empty start token", ErrMalformed)
	}
	return nil
}

// Create writes e to path with O_CREATE|O_EXCL and mode 0600, creating the
// 0700 parent directory when it is missing. An existing file — a live or
// stale predecessor — fails with an error for which errors.Is(err,
// fs.ErrExist) holds; the caller then runs its liveness check ([Check])
// and, when the holder is dead, [Replace]. Create never deletes anything.
func Create(path string, e Entry) error {
	if err := validate(e); err != nil {
		return err
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		return fmt.Errorf("pidfile: %w", err)
	}
	if err := adapterkit.WritePidfile(path, Encode(e)); err != nil {
		return fmt.Errorf("pidfile: %w", err)
	}
	return nil
}

// Read loads the entry at path. A missing file is reported with an error
// for which errors.Is(err, fs.ErrNotExist) holds. The file must be a
// regular file (not a symlink, not a FIFO that would block the reader),
// at most maxBytes, mode 0600 — adapterkit.ReadStrict refuses anything
// group- or world-accessible with `config` — and a JSON object with a
// positive pid and a non-empty start token ([ErrMalformed] otherwise).
func Read(path string) (Entry, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	if !fi.Mode().IsRegular() {
		return Entry{}, fmt.Errorf("%w: %s is not a regular file", ErrMalformed, path)
	}
	if fi.Size() > maxBytes {
		return Entry{}, fmt.Errorf("%w: %s is %d bytes", ErrMalformed, path, fi.Size())
	}
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return Entry{}, fmt.Errorf("%w: %s: not a pidfile", ErrMalformed, path)
	}
	if err := validate(e); err != nil {
		return Entry{}, fmt.Errorf("%w (%s)", err, path)
	}
	return e, nil
}

// Remove unlinks path ONLY when the file still holds exactly [Encode](e),
// and reports whether it did. A missing file and a file with other content
// both return (false, nil): in either case the file is not this entry's to
// remove. Compare-then-delete is required, not a courtesy (E0-5 item 3).
func Remove(path string, e Entry) (bool, error) {
	removed, err := adapterkit.RemovePidfile(path, Encode(e))
	if err != nil {
		return false, fmt.Errorf("pidfile: %w", err)
	}
	return removed, nil
}

// Replace retires old and writes latest in its place: [Remove](path, old),
// then [Create](path, latest). When the file exists but no longer holds
// old, nothing is written and [ErrSuperseded] is returned — someone else
// replaced it first and the caller must re-run its liveness check on the
// new holder. A file that vanished between the caller's read and this
// call is not an error: the holder cleaned up, and latest is created.
// O_EXCL in Create remains the final arbiter of the race.
func Replace(path string, old, latest Entry) error {
	removed, err := Remove(path, old)
	if err != nil {
		return err
	}
	if !removed {
		if _, err := os.Lstat(path); err == nil {
			return ErrSuperseded
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("pidfile: %w", err)
		}
	}
	return Create(path, latest)
}
