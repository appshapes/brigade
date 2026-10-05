package policy

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// The outcomes of EnsureUserAccept.
const (
	// EnsureAdded: the member was inserted into an existing file.
	EnsureAdded = "added"
	// EnsureCreated: the file did not exist and was created with the one
	// member.
	EnsureCreated = "created"
	// EnsurePresent: the file already has a top-level crossSessionInbound,
	// whatever its value; nothing was written.
	EnsurePresent = "present"
	// EnsureSkipped: nothing was written, for the Reason given.
	EnsureSkipped = "skipped"
)

// The fixed reasons of an EnsureSkipped outcome.
const (
	ReasonNoConfigDir  = "no_config_dir"
	ReasonSymlink      = "symlink"
	ReasonUnreadable   = "unreadable"
	ReasonTooLarge     = "too_large"
	ReasonNotAnObject  = "not_a_json_object"
	ReasonUnwritable   = "unwritable"
	ReasonVerifyFailed = "verify_failed"
)

// userAcceptMember is the one member Brigade writes, as it appears in the
// file: Claude Code's own two-space style.
const userAcceptMember = `"` + SettingKey + `": "` + NativeAccept + `"`

// An EnsureResult says what EnsureUserAccept did.
type EnsureResult struct {
	// Outcome is one of EnsureAdded, EnsureCreated, EnsurePresent and
	// EnsureSkipped.
	Outcome string
	// Reason is the fixed skip reason, "" for the other outcomes.
	Reason string
	// File is the user settings file.
	File string
}

// EnsureIO are the injectable file operations of EnsureUserAccept; the zero
// value is the real filesystem. ReadFile is the same seam the scan uses, so
// a test can keep the two in one world.
type EnsureIO struct {
	ReadFile  func(string) ([]byte, error)
	WriteFile func(path string, data []byte, mode os.FileMode) error
	Lstat     func(string) (fs.FileInfo, error)
}

func (io EnsureIO) resolved() EnsureIO {
	if io.ReadFile == nil {
		io.ReadFile = os.ReadFile
	}
	if io.WriteFile == nil {
		io.WriteFile = adapterkit.WriteAtomicMode
	}
	if io.Lstat == nil {
		io.Lstat = os.Lstat
	}
	return io
}

// EnsureUserAccept puts `"crossSessionInbound": "accept"` into the user
// settings file when it has no crossSessionInbound at all (card 50): the
// one line Claude Code's own dialog tells a bypass-permissions user to add,
// in the one file where Claude Code reads it as explicit. It is the ONE
// write Brigade ever makes under the Claude Code configuration directory,
// and it is bounded so: the file must be a regular file (a symlink is
// refused unread), no larger than MaxSettingsBytes, and a JSON object; a
// file that already has the member, with any value, is left exactly as it
// is — hold and refuse are the user's choices; a missing file is created
// with the one member, 0600, but a missing directory is not created (no
// Claude Code configuration lives there). The member is inserted as the
// object's first, by text, so every other byte of the file — order,
// indentation, the rest — stays as the user had it, and the result is
// parsed back and compared member by member with the original before it is
// written, atomically, under the file's own mode. Nothing from the file is
// logged or returned.
func EnsureUserAccept(userFile string, io EnsureIO) EnsureResult {
	io = io.resolved()
	res := EnsureResult{File: userFile}
	if userFile == "" {
		res.Outcome, res.Reason = EnsureSkipped, ReasonNoConfigDir
		return res
	}
	mode := os.FileMode(0o600)
	if fi, err := io.Lstat(userFile); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			res.Outcome, res.Reason = EnsureSkipped, ReasonSymlink
			return res
		}
		mode = fi.Mode().Perm()
	}
	data, err := io.ReadFile(userFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if _, derr := io.Lstat(filepath.Dir(userFile)); derr != nil {
			res.Outcome, res.Reason = EnsureSkipped, ReasonNoConfigDir
			return res
		}
		content := []byte("{\n  " + userAcceptMember + "\n}\n")
		if werr := io.WriteFile(userFile, content, mode); werr != nil {
			res.Outcome, res.Reason = EnsureSkipped, ReasonUnwritable
			return res
		}
		res.Outcome = EnsureCreated
		return res
	case err != nil:
		res.Outcome, res.Reason = EnsureSkipped, ReasonUnreadable
		return res
	case len(data) > MaxSettingsBytes:
		res.Outcome, res.Reason = EnsureSkipped, ReasonTooLarge
		return res
	}
	before, ok := members(data)
	if !ok {
		res.Outcome, res.Reason = EnsureSkipped, ReasonNotAnObject
		return res
	}
	if _, has := before[SettingKey]; has {
		res.Outcome = EnsurePresent
		return res
	}
	after := insertFirst(data, len(before) > 0)
	got, ok := members(after)
	if !ok || string(got[SettingKey]) != `"`+NativeAccept+`"` || !sameMembers(before, got) {
		res.Outcome, res.Reason = EnsureSkipped, ReasonVerifyFailed
		return res
	}
	if werr := io.WriteFile(userFile, after, mode); werr != nil {
		res.Outcome, res.Reason = EnsureSkipped, ReasonUnwritable
		return res
	}
	res.Outcome = EnsureAdded
	return res
}

// members parses data as a JSON object into its top-level members, raw.
// Anything that is not one object — an array, a scalar, malformed text, a
// duplicate member name — is not ok.
func members(data []byte) (map[string]jsontext.Value, bool) {
	var m map[string]jsontext.Value
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

// insertFirst puts the member right after the object's opening brace. With
// other members present it is followed by a comma, so the user's first
// member keeps its own line and indentation; in an empty object it stands
// alone on its line.
func insertFirst(data []byte, hasMembers bool) []byte {
	i := bytes.IndexByte(data, '{')
	var out bytes.Buffer
	out.Grow(len(data) + len(userAcceptMember) + 8)
	out.Write(data[:i+1])
	out.WriteString("\n  " + userAcceptMember)
	if hasMembers {
		out.WriteString(",")
	} else {
		out.WriteString("\n")
	}
	out.Write(data[i+1:])
	return out.Bytes()
}

// sameMembers reports whether got is before plus the one member, every
// other member byte for byte the same.
func sameMembers(before, got map[string]jsontext.Value) bool {
	if len(got) != len(before)+1 {
		return false
	}
	for k, v := range before {
		if g, ok := got[k]; !ok || !bytes.Equal(g, v) {
			return false
		}
	}
	return true
}

// The fixed clauses of Line.
const (
	ensureWhy  = `, so Claude Code delivers team messages to this session, which bypasses permission prompts (it would hold each one for a dialog otherwise); `
	ensureUndo = ` Remove the line, or set the plugin option claude_inbound_setting to false, to undo.`
)

// Line is the one context line the hook prints for the result: what was
// written and where, why, when Claude Code reads it, and how to undo it;
// for a skip, that nothing was written and the fixed reason. "" for
// EnsurePresent — nothing happened, nothing to say. Fixed text: the only
// variable parts are the file path this harness computed and the fixed
// reason word.
func (r EnsureResult) Line() string {
	file := r.File
	if file == "" {
		file = "your Claude Code user settings.json"
	}
	switch r.Outcome {
	case EnsureAdded:
		return `Brigade: added ` + userAcceptMember + ` to ` + file + ensureWhy +
			`Claude Code picks the change up within the session.` + ensureUndo
	case EnsureCreated:
		return `Brigade: wrote ` + file + ` with ` + userAcceptMember + ensureWhy +
			`a new settings file is read at your next session start.` + ensureUndo
	case EnsureSkipped:
		return `Brigade: could not add ` + userAcceptMember + ` to ` + file + ` (` + r.Reason + `); add it yourself to receive team messages in this session.`
	}
	return ""
}
