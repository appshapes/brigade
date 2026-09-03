package sessionmap

import (
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// MaxMapBytes caps what the strict reader will read of a map file. A real
// map is well under 2 KiB; anything larger is not one.
const MaxMapBytes = 64 << 10

const (
	sessionsDirName = "sessions"
	byPIDDirName    = "by-pid"
	byNativeDirName = "by-native"
)

// A Store reads and writes the two maps under one state directory. The
// zero Store is not usable: StateDir must be absolute (the harness always
// computes it, 3.2; config.BrigadeStateDir applies the in-session rule).
type Store struct {
	// StateDir is ${BRIGADE_STATE_DIR} as resolved by the caller.
	StateDir string
	// Stat, when non-nil, replaces the fstat of the opened file in the
	// privacy check. It exists for tests: a foreign-uid file cannot be
	// created without privileges, so the foreign-owner refusal is
	// exercised through a FileInfo whose Sys reports another uid. Leave
	// it nil everywhere else — the fstat on the open descriptor is what
	// makes the check free of a check-then-open race.
	Stat func(path string) (fs.FileInfo, error)
}

// ByPIDDir is ${stateDir}/sessions/by-pid.
func (s Store) ByPIDDir() string {
	return filepath.Join(s.StateDir, sessionsDirName, byPIDDirName)
}

// ByNativeDir is ${stateDir}/sessions/by-native.
func (s Store) ByNativeDir() string {
	return filepath.Join(s.StateDir, sessionsDirName, byNativeDirName)
}

// ByPIDPath is the by-pid map file for pid, after checking the Store and
// the pid.
func (s Store) ByPIDPath(pid int) (string, error) {
	if err := s.check(); err != nil {
		return "", err
	}
	if pid <= 0 {
		return "", errInvalid("claude_pid")
	}
	return filepath.Join(s.ByPIDDir(), strconv.Itoa(pid)+".json"), nil
}

// ByNativePath is the by-native map file for id, after validating id as a
// path component (CheckNativeID).
func (s Store) ByNativePath(id string) (string, error) {
	if err := s.check(); err != nil {
		return "", err
	}
	if err := CheckNativeID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.ByNativeDir(), id+".json"), nil
}

// WriteByPID validates m and writes it atomically (0600 in a 0700
// directory chain), overwriting any previous file for the same pid: the
// hook rewrites the map on every SessionStart, including the one `/clear`
// re-fires (E0-8). A nil AdapterCommand is written as [].
func (s Store) WriteByPID(m *ByPID) error {
	if err := m.Validate(); err != nil {
		return err
	}
	path, err := s.ByPIDPath(m.ClaudePID)
	if err != nil {
		return err
	}
	out := *m
	if out.AdapterCommand == nil {
		out.AdapterCommand = []string{}
	}
	return writeAtomic(path, &out)
}

// ReadByPID reads the by-pid map for pid through the strict reader and
// validates it. A missing file is returned as an error for which
// errors.Is(err, fs.ErrNotExist) holds (the caller maps it to
// `not_registered`); every other failure is a *protocol.Error with the
// `config` code and one of this package's Reason* details.
func (s Store) ReadByPID(pid int) (*ByPID, error) {
	path, err := s.ByPIDPath(pid)
	if err != nil {
		return nil, err
	}
	data, err := s.readStrict(path)
	if err != nil {
		return nil, err
	}
	var m ByPID
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errMalformed(path, "not_json")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if m.ClaudePID != pid {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the session map names a different Claude Code process than its file; refusing to trust it",
			Details: map[string]string{"reason": ReasonMapMismatch, "path": path},
		}
	}
	if m.AdapterCommand == nil {
		m.AdapterCommand = []string{}
	}
	return &m, nil
}

// DeleteByPID removes the by-pid map at SessionEnd (3.8). A missing file
// is not an error; the by-native map is deliberately left in place.
func (s Store) DeleteByPID(pid int) error {
	path, err := s.ByPIDPath(pid)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the session map could not be removed",
			Details: map[string]string{"reason": ReasonMapUnreadable, "path": path},
		}
	}
	return nil
}

// WriteByNative validates m and writes the by-native map for id,
// overwriting any previous entry (a native id recurs, E0-5).
func (s Store) WriteByNative(id string, m *ByNative) error {
	if err := m.Validate(); err != nil {
		return err
	}
	path, err := s.ByNativePath(id)
	if err != nil {
		return err
	}
	return writeAtomic(path, m)
}

// ReadByNative reads the by-native map for id through the strict reader.
// A missing file satisfies errors.Is(err, fs.ErrNotExist): no resume
// hint. Other failures are `config` as for ReadByPID.
func (s Store) ReadByNative(id string) (*ByNative, error) {
	path, err := s.ByNativePath(id)
	if err != nil {
		return nil, err
	}
	data, err := s.readStrict(path)
	if err != nil {
		return nil, err
	}
	var m ByNative
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errMalformed(path, "not_json")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// check refuses a Store whose StateDir is not absolute.
func (s Store) check() error {
	if !filepath.IsAbs(s.StateDir) {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the state directory must be an absolute path",
			Details: map[string]string{"reason": ReasonStateDirRelative},
		}
	}
	return nil
}

// writeAtomic marshals v and writes it 0600 through adapterkit.WriteAtomic
// after creating the 0700 directory chain.
func writeAtomic(path string, v any) error {
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the session map directory could not be created",
			Details: map[string]string{"reason": ReasonMapUnreadable, "path": filepath.Dir(path)},
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return adapterkit.WriteAtomic(path, append(data, '\n'))
}

// readStrict opens path without following a symlink and refuses the file
// unless it is regular, grants nothing to group or other, is owned by the
// current uid and is within MaxMapBytes. The privacy check runs on the
// OPEN descriptor (or on Store.Stat's answer in tests), so a file cannot
// be swapped between the check and the read. A missing file is returned
// as the underlying *fs.PathError.
func (s Store) readStrict(path string) ([]byte, error) {
	f, err := openNoFollow(path)
	switch {
	case err == nil:
	case isSymlinkRefusal(err):
		return nil, errNotPrivate(path, "symlink")
	case errors.Is(err, fs.ErrNotExist):
		return nil, err
	default:
		return nil, errUnreadable(path)
	}
	defer func() { _ = f.Close() }()

	var fi fs.FileInfo
	if s.Stat != nil {
		fi, err = s.Stat(path)
	} else {
		fi, err = f.Stat()
	}
	if err != nil {
		return nil, errUnreadable(path)
	}
	switch {
	case !fi.Mode().IsRegular():
		return nil, errNotPrivate(path, "not_regular")
	case fi.Mode().Perm()&0o077 != 0:
		return nil, errNotPrivate(path, "insecure_mode")
	}
	uid, ok := ownerUID(fi)
	if !ok || uid != os.Getuid() {
		return nil, errNotPrivate(path, "foreign_owner")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxMapBytes+1))
	if err != nil {
		return nil, errUnreadable(path)
	}
	if len(data) > MaxMapBytes {
		return nil, errMalformed(path, "too_large")
	}
	return data, nil
}

// errNotPrivate is the refusal of a map that fails a privacy check. check
// names the failed check for the diagnostic; the message is fixed.
func errNotPrivate(path, check string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "the session map is not private (it must be a regular file, mode 0600, owned by you, not a symlink); refusing to trust it",
		Details: map[string]string{"reason": ReasonMapNotPrivate, "check": check, "path": path},
	}
}

// errMalformed is the refusal of a map that is not a JSON document of the
// expected shape or size.
func errMalformed(path, check string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "the session map is not a valid JSON document; restart the session or run /reload-plugins so the hook rewrites it",
		Details: map[string]string{"reason": ReasonMapMalformed, "check": check, "path": path},
	}
}

// errUnreadable is an I/O failure that is not "missing". The underlying
// error text is deliberately not echoed.
func errUnreadable(path string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "the session map could not be read",
		Details: map[string]string{"reason": ReasonMapUnreadable, "path": path},
	}
}
