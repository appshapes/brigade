// Package watchstate is the one fact a running watcher writes down about
// its connection, for `brigade whoami` to read (card 34): whether the
// watch child is up, and since when. A watcher that retries slowly after
// an outage is a live process that receives nothing, and its pidfile
// cannot say which of the two it is.
//
// The file is ${stateDir}/state/<claude_pid>.watch.json, mode 0600, written
// atomically by the watcher alone and removed when it exits. It carries the
// watcher's pid, a state word and a time — no id, no name, no path,
// nothing from a message. A reader trusts it only beside a live pidfile
// naming the same pid: a watcher that was killed leaves its last word
// behind, and a watcher that was replaced must not speak for its successor.
package watchstate

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// The three states.
const (
	// Connecting: no watch child is ready yet, or the last one ended and
	// the next is on the fast schedule.
	Connecting = "connecting"
	// Connected: the watch child said ready.
	Connected = "connected"
	// Retrying: the watcher is on the slow schedule; Since is when it went
	// there.
	Retrying = "retrying"
)

// FileVersion is the document's version.
const FileVersion = 1

// ErrMalformed reports a file that is not a state document: unparseable,
// another version, no pid, an unknown state word or no time.
var ErrMalformed = errors.New("watchstate: malformed file")

// A State is the document.
type State struct {
	Version int `json:"version"`
	// PID is the watcher that wrote the file.
	PID   int       `json:"pid"`
	State string    `json:"state"`
	Since time.Time `json:"since"`
}

// Path is ${stateDir}/state/<claude_pid>.watch.json.
func Path(stateDir string, claudePID int) string {
	return filepath.Join(stateDir, "state", fmt.Sprintf("%d.watch.json", claudePID))
}

// Write replaces the file with the watcher pid's state since since.
func Write(path string, pid int, state string, since time.Time) error {
	if pid <= 0 || !known(state) || since.IsZero() {
		return ErrMalformed
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(State{Version: FileVersion, PID: pid, State: state, Since: since.UTC()})
	if err != nil {
		return err
	}
	return adapterkit.WriteAtomic(path, append(data, '\n'))
}

// Read reads the file through the strict reader (a private regular file
// of bounded size). A missing file is the underlying fs.ErrNotExist; a
// document that is not a State is ErrMalformed.
func Read(path string) (State, error) {
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, ErrMalformed
	}
	if s.Version != FileVersion || s.PID <= 0 || !known(s.State) || s.Since.IsZero() {
		return State{}, ErrMalformed
	}
	return s, nil
}

// Remove deletes the file when it is the watcher pid's own: a watcher
// that was replaced exits after its successor may have written, and must
// leave that file alone. A missing file, and one it cannot read as its
// own, is no error and is left as it is.
func Remove(path string, pid int) error {
	if s, err := Read(path); err != nil || s.PID != pid {
		return nil //nolint:nilerr // not this watcher's file to remove
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func known(state string) bool {
	switch state {
	case Connecting, Connected, Retrying:
		return true
	}
	return false
}
