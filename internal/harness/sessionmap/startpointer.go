package sessionmap

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// A StartPointer is the start-time pointer of a pointed host (card 116):
// keyed by the host's name and the session's native id, it names the key
// the session's START FACTS are filed under, before the session attaches.
// The by-host pointer (ByHost) exists only once a map does, and a session
// that has not joined its team has no map; but the model's shell on Muse
// Code and Codex CLI knows the native id and not the key, and an in-session
// `team join` must find the start facts — above all the resolved config
// directory — to write the credential and the pin where the hooks read
// them (P7-11). SessionStart writes it beside the start facts, joined or
// not; SessionEnd removes it with them. It carries no identity: the start
// facts it points at carry none either.
type StartPointer struct {
	HostPID   int       `json:"host_pid"`
	WrittenAt time.Time `json:"written_at,omitzero"`
}

// Validate requires the one member a lookup cannot do without.
func (p *StartPointer) Validate() error {
	if p.HostPID <= 0 {
		return errInvalid("host_pid")
	}
	return nil
}

// startPointerSuffix follows the native id in the file's name, beside the
// attached pointer's `<id>.json`.
const startPointerSuffix = ".start.json"

// StartPointerPath is the start pointer for (host, id), after validating
// both as path components.
func (s Store) StartPointerPath(host, id string) (string, error) {
	path, err := s.ByHostPath(host, id)
	if err != nil {
		return "", err
	}
	return path[:len(path)-len(".json")] + startPointerSuffix, nil
}

// WriteStartPointer writes the pointer atomically (0600 in a 0700 chain),
// overwriting any previous one for the same (host, id).
func (s Store) WriteStartPointer(host, id string, pid int, now time.Time) error {
	p := &StartPointer{HostPID: pid, WrittenAt: now}
	if err := p.Validate(); err != nil {
		return err
	}
	path, err := s.StartPointerPath(host, id)
	if err != nil {
		return err
	}
	return writeAtomic(path, p)
}

// ReadStartPointer reads the pointer through the strict reader and returns
// the key it names. A missing file satisfies errors.Is(err, fs.ErrNotExist).
func (s Store) ReadStartPointer(host, id string) (int, error) {
	path, err := s.StartPointerPath(host, id)
	if err != nil {
		return 0, err
	}
	data, err := s.readStrict(path)
	if err != nil {
		return 0, err
	}
	var p StartPointer
	if err := json.Unmarshal(data, &p); err != nil {
		return 0, errMalformed(path, "not_json")
	}
	if err := p.Validate(); err != nil {
		return 0, err
	}
	return p.HostPID, nil
}

// DeleteStartPointer removes the pointer at SessionEnd. A missing file is
// not an error.
func (s Store) DeleteStartPointer(host, id string) error {
	path, err := s.StartPointerPath(host, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the session's start pointer could not be removed",
			Details: map[string]string{"reason": ReasonMapUnreadable, "path": path},
		}
	}
	return nil
}
