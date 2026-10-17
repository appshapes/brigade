package sessionmap

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// ByHost is the by-host pointer (card 88): keyed by a host's name and the
// session's native id, it names the host pid the by-pid map is keyed by,
// for a host whose shell tool knows the native id but not the pid. Muse
// Code is the one such host today: its hooks, its shell tool and its
// monitors carry MUSE_SESSION_ID (a UUID), and the hook's parent is the
// Muse process. The pointer is written on every SessionStart and removed
// at SessionEnd; it carries no identity of its own — the by-pid map stays
// the trust boundary, and config.Session refuses a pointer whose map does
// not name the same native id, harness and Brigade session.
type ByHost struct {
	HostPID          int       `json:"host_pid"`
	BrigadeSessionID string    `json:"brigade_session_id"`
	UpdatedAt        time.Time `json:"updated_at,omitzero"`
}

// Validate requires the two members a lookup cannot do without.
func (m *ByHost) Validate() error {
	switch {
	case m.HostPID <= 0:
		return errInvalid("host_pid")
	case m.BrigadeSessionID == "":
		return errInvalid("brigade_session_id")
	}
	return nil
}

// byHostDirName is the directory under sessions/ that holds one
// subdirectory per host.
const byHostDirName = "by-host"

// CheckHost validates a host name before it becomes a path component: a
// short lower-case token. The two hosts Brigade knows are claude-code and
// muse; the check is on shape, not on a list, so the config package (which
// imports this one) owns the list.
func CheckHost(host string) error {
	if host == "" || len(host) > 32 {
		return errInvalid("host")
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return errInvalid("host")
		}
	}
	return nil
}

// ByHostDir is ${stateDir}/sessions/by-host/<host>.
func (s Store) ByHostDir(host string) string {
	return filepath.Join(s.StateDir, sessionsDirName, byHostDirName, host)
}

// ByHostPath is the pointer file for (host, id), after validating both as
// path components.
func (s Store) ByHostPath(host, id string) (string, error) {
	if err := s.check(); err != nil {
		return "", err
	}
	if err := CheckHost(host); err != nil {
		return "", err
	}
	if err := CheckNativeID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.ByHostDir(host), id+".json"), nil
}

// WriteByHost validates m and writes the pointer atomically (0600 in a 0700
// chain), overwriting any previous one for the same (host, id).
func (s Store) WriteByHost(host, id string, m *ByHost) error {
	if err := m.Validate(); err != nil {
		return err
	}
	path, err := s.ByHostPath(host, id)
	if err != nil {
		return err
	}
	return writeAtomic(path, m)
}

// ReadByHost reads the pointer through the strict reader and validates
// it. A missing file satisfies errors.Is(err, fs.ErrNotExist).
func (s Store) ReadByHost(host, id string) (*ByHost, error) {
	path, err := s.ByHostPath(host, id)
	if err != nil {
		return nil, err
	}
	data, err := s.readStrict(path)
	if err != nil {
		return nil, err
	}
	var m ByHost
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errMalformed(path, "not_json")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteByHost removes the pointer at SessionEnd. A missing file is not an
// error.
func (s Store) DeleteByHost(host, id string) error {
	path, err := s.ByHostPath(host, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the session pointer could not be removed",
			Details: map[string]string{"reason": ReasonMapUnreadable, "path": path},
		}
	}
	return nil
}
