package inbound

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// This file is the on-disk state of the `hold` policy (6.8 step 5, 3.6;
// P5-9): the pending file, which records what was held and what the human
// has released, and the release file, through which `brigade inbox
// release` in a terminal tells the watcher (or the prompt-hook poll) what
// to deliver. Both are keyed by the Brigade session id exactly as P5-14
// keys the seen file, through the one encoder StateName, so a crash and a
// `claude --resume` onto the same session find the same review queue and
// the human's release file and the watcher's path are the same string.

// The bounds of the two files.
const (
	// HoldCapacity is how many held messages the pending file and the
	// pipeline's memory keep (6.8 step 5: 100). Beyond it the OLDEST entry
	// is dropped from the file and from memory and is NOT acknowledged:
	// the server keeps it and redelivers it on the next watch-child
	// restart. A conforming backend refuses the 61st unacknowledged
	// message to one recipient (protocol.MaxUnackedPerRecipient), so this
	// bound is a backstop reachable only through the pipeline directly.
	HoldCapacity = 100
	// PendingFileVersion is the schema version of the pending file.
	PendingFileVersion = 1
	// MaxPendingFileBytes bounds what Load will read; a real pending file
	// is under 60 KiB (100 entries of a 200-byte id, a 200-code-point
	// summary and the rest).
	MaxPendingFileBytes = 1 << 20
	// ReleaseFileVersion is the schema version of the release file.
	ReleaseFileVersion = 1
	// MaxReleaseFileBytes bounds what ReadRelease will read.
	MaxReleaseFileBytes = 1 << 20
	// MaxReleaseIDs caps the ids one release file names; more than the
	// pipeline can hold could never be released at once.
	MaxReleaseIDs = HoldCapacity
)

// StateName is the file-name stem for a per-session state file: the
// Brigade session id itself when it is a safe path component (1-64 bytes
// of [A-Za-z0-9_-], the rule sessionmap.CheckNativeID applies to the
// by-native map's file name and the fs adapter applies to its own opaque
// refs, fs/store.go safeRef), and otherwise the lowercase hex SHA-256 of
// the id with a ".sha256" suffix. Session ids are opaque and may be any
// non-empty string (protocol-v1.md, convention 8), so the mapping must be
// total; a checked id contains no ".", so the two branches cannot collide.
// It is the ONE encoding behind SeenPath, PendingPath and ReleasePath
// (P5-14 wrote it for the seen file; P5-9 exports it and keys the two hold
// files the same way), and a total function returning a string like
// pidfile.Path: the encoding has no failure mode.
func StateName(brigadeSessionID string) string {
	return seenStem(brigadeSessionID)
}

// PendingPath is ${stateDir}/state/pending/<StateName>.json: the file
// that records the messages held under the `hold` policy for one Brigade
// session (6.8 step 5), keyed by the session so it survives a crash and a
// `claude --resume` and so a human's `brigade inbox release --session
// <id>` and the watcher name the same file.
func PendingPath(stateDir, brigadeSessionID string) string {
	return filepath.Join(stateDir, "state", "pending", StateName(brigadeSessionID)+".json")
}

// ReleasePath is ${stateDir}/state/release/<StateName>.json: the file
// `brigade inbox release` writes in a terminal and the watcher consumes on
// its liveness tick (3.6).
func ReleasePath(stateDir, brigadeSessionID string) string {
	return filepath.Join(stateDir, "state", "release", StateName(brigadeSessionID)+".json")
}

// A PendingEntry is one held message as the pending file records it:
// NEVER the body. The body is re-read through `message receive` when a
// human lists it in a terminal and is never written under the state
// directory; the summary IS stored (at most protocol.MaxSummaryChars,
// sanitised on the way in) so the listing has something to show for a
// message the backend has since deleted. SenderName and Summary are
// sanitised at write time (protocol.SanitizeName, protocol.SanitizeSummary),
// so the file can never carry a raw control character, a bidi override or
// a forgeable tag; the three ids are opaque wire values kept verbatim,
// because a message id must round-trip to its acknowledgement byte for
// byte, and every printer sanitises them on the way out.
type PendingEntry struct {
	MessageID       string    `json:"message_id"`
	SenderSessionID string    `json:"sender_session_id"`
	SenderName      string    `json:"sender_name"`
	SenderPrincipal string    `json:"sender_principal"`
	Summary         string    `json:"summary,omitzero"`
	ReceivedAt      time.Time `json:"received_at"`
	// ReleasedAt, when set, is the durable "cleared for delivery" flag: the
	// human released this id and the next drain (or the next restart,
	// prompt or tick) injects it through the accept path (3.5).
	ReleasedAt time.Time `json:"released_at,omitzero"`
}

// Released reports whether the human has released the entry.
func (e PendingEntry) Released() bool { return !e.ReleasedAt.IsZero() }

// A PendingFile is the pending file's document: the held entries oldest
// first, the count of entries the HoldCapacity bound has dropped from the
// file (for `brigade inbox`'s terminal output only, never the notice) and
// when it was last written. Version and SessionID are the store's: a
// FilePendingStore writes its own and refuses a file naming another
// session on load.
type PendingFile struct {
	Version      int            `json:"version"`
	SessionID    string         `json:"session_id"`
	Entries      []PendingEntry `json:"entries"`
	DroppedTotal int            `json:"dropped_total"`
	UpdatedAt    time.Time      `json:"updated_at,omitzero"`
}

// A PendingStore persists the held entries across restarts. Load runs once
// at construction; Save runs after every change to the held set — a new
// hold, a release stamp, a delivery — with the full bounded list.
type PendingStore interface {
	Load() (PendingFile, error)
	Save(f PendingFile) error
}

// A FilePendingStore keeps the pending file as one 0600 document written
// atomically (adapterkit.WriteAtomic) inside a 0700 directory chain —
// the same two calls FileSeenStore.Save makes, so state/pending/ is
// created by the existing primitives. Load reads it through
// adapterkit.ReadStrict, so a planted, group-readable or symlinked
// pending file is refused loudly rather than trusted: a pending file that
// could be written by another user could add released_at to an id and
// cause an injection the human never asked for, which is why the strict
// reader is not optional here. A refused, malformed, wrong-version or
// foreign-session file is a fixed-text error; the pipeline then starts
// empty and the next Save replaces the file.
type FilePendingStore struct {
	// Path is normally PendingPath(stateDir, brigadeSessionID).
	Path string
	// SessionID is the Brigade session the file must name.
	SessionID string
}

// Load returns the persisted document, entries oldest first, at most
// HoldCapacity of them. A missing file is an empty document and a nil
// error. Entries whose message id is empty, over MaxMessageIDBytes or not
// UTF-8, or whose received_at is missing, are skipped.
func (s FilePendingStore) Load() (PendingFile, error) {
	data, err := adapterkit.ReadStrict(s.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return PendingFile{Version: PendingFileVersion, SessionID: s.SessionID}, nil
	case err != nil:
		return PendingFile{}, err
	case len(data) > MaxPendingFileBytes:
		return PendingFile{}, errPendingFileTooLarge
	}
	var f PendingFile
	if err := json.Unmarshal(data, &f); err != nil {
		return PendingFile{}, errPendingFileMalformed
	}
	if f.Version != PendingFileVersion {
		return PendingFile{}, errPendingFileVersion
	}
	if f.SessionID != s.SessionID {
		return PendingFile{}, errPendingFileForeign
	}
	entries := make([]PendingEntry, 0, min(len(f.Entries), HoldCapacity))
	for _, e := range f.Entries {
		if !validID(e.MessageID) || e.ReceivedAt.IsZero() {
			continue
		}
		entries = append(entries, e)
	}
	if len(entries) > HoldCapacity {
		entries = entries[len(entries)-HoldCapacity:]
	}
	f.Entries = entries
	return f, nil
}

// Save writes f (entries oldest first; only the last HoldCapacity are
// kept) atomically, 0600, creating the 0700 directory chain as needed.
// The version and the session id are the store's, whatever f carries.
func (s FilePendingStore) Save(f PendingFile) error {
	f.Version = PendingFileVersion
	f.SessionID = s.SessionID
	if len(f.Entries) > HoldCapacity {
		f.Entries = f.Entries[len(f.Entries)-HoldCapacity:]
	}
	if f.Entries == nil {
		f.Entries = []PendingEntry{}
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(s.Path)); err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return adapterkit.WriteAtomic(s.Path, append(data, '\n'))
}

// validID is the id rule the seen and pending files share on load.
func validID(id string) bool {
	return id != "" && len(id) <= MaxMessageIDBytes && utf8.ValidString(id)
}

// The fixed-text failures of the two loaders; a file's content is never
// echoed.
var (
	errPendingFileTooLarge  = errors.New("inbound: pending file exceeds the size cap")
	errPendingFileMalformed = errors.New("inbound: pending file is not a valid JSON document")
	errPendingFileVersion   = errors.New("inbound: pending file has an unknown version")
	errPendingFileForeign   = errors.New("inbound: pending file names another session")
	errReleaseFileTooLarge  = errors.New("inbound: release file exceeds the size cap")
	errReleaseFileMalformed = errors.New("inbound: release file is not a valid JSON document")
	errReleaseFileVersion   = errors.New("inbound: release file has an unknown version")
	// ErrReleaseForeignSession: the release file names another session.
	// The watcher leaves such a file in place (it may belong to a session
	// whose watcher is not running yet) and `brigade inbox release`
	// replaces it, because the file sits at the path that session's id
	// computes and can only be a planted or corrupt one.
	ErrReleaseForeignSession = errors.New("inbound: release file names another session")
)

// A MemoryPendingStore is a PendingStore in memory, for tests inside a
// synctest bubble and for callers that want no persistence. It records
// how many times Save ran. The zero value is ready.
type MemoryPendingStore struct {
	mu    sync.Mutex
	file  PendingFile
	saves int
}

// Load returns a copy of the last saved document.
func (m *MemoryPendingStore) Load() (PendingFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.copyLocked(), nil
}

// Save keeps a copy of f.
func (m *MemoryPendingStore) Save(f PendingFile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f.Entries = slices.Clone(f.Entries)
	m.file = f
	m.saves++
	return nil
}

// File returns a copy of the last saved document.
func (m *MemoryPendingStore) File() PendingFile {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.copyLocked()
}

// Saves is how many times Save has run.
func (m *MemoryPendingStore) Saves() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saves
}

func (m *MemoryPendingStore) copyLocked() PendingFile {
	f := m.file
	f.Entries = slices.Clone(f.Entries)
	return f
}

// A ReleaseFile is the release file's document: the ids the human
// released for one session, and when. It is transient — the watcher
// deletes it once the stamps are in the pending file — while the pending
// file is the durable record (3.5).
type ReleaseFile struct {
	Version    int       `json:"version"`
	SessionID  string    `json:"session_id"`
	MessageIDs []string  `json:"message_ids"`
	WrittenAt  time.Time `json:"written_at,omitzero"`
}

// ReadRelease reads the release file at path through adapterkit.ReadStrict
// and returns the decoded document AND the raw bytes, which ConsumeRelease
// needs for its compare-then-delete. A missing file is an error for which
// errors.Is(err, fs.ErrNotExist) holds; a refused, over-large, malformed
// or wrong-version file is a fixed-text error. The ids are deduplicated,
// invalid ones skipped, and at most MaxReleaseIDs kept. The session id is
// returned as written: the caller compares it with its own and treats a
// mismatch as ErrReleaseForeignSession.
func ReadRelease(path string) (ReleaseFile, []byte, error) {
	data, err := adapterkit.ReadStrict(path)
	switch {
	case err != nil:
		return ReleaseFile{}, nil, err
	case len(data) > MaxReleaseFileBytes:
		return ReleaseFile{}, nil, errReleaseFileTooLarge
	}
	var f ReleaseFile
	if err := json.Unmarshal(data, &f); err != nil {
		return ReleaseFile{}, nil, errReleaseFileMalformed
	}
	if f.Version != ReleaseFileVersion {
		return ReleaseFile{}, nil, errReleaseFileVersion
	}
	f.MessageIDs = dedupeIDs(f.MessageIDs)
	return f, data, nil
}

// WriteRelease writes f atomically, 0600, creating the 0700 directory
// chain as needed, with the ids deduplicated and capped at MaxReleaseIDs.
func WriteRelease(path string, f ReleaseFile) error {
	f.Version = ReleaseFileVersion
	f.MessageIDs = dedupeIDs(f.MessageIDs)
	if f.MessageIDs == nil {
		f.MessageIDs = []string{}
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return adapterkit.WriteAtomic(path, append(data, '\n'))
}

// MergeRelease is what `brigade inbox release` does: it unions ids with
// whatever release file already exists at path for sessionID (missing →
// empty; a file naming another session, or one that cannot be read, is
// replaced with a warning the caller logs, because the path is computed
// from sessionID and such a file could only be a planted or corrupt one),
// caps the union at MaxReleaseIDs and writes atomically, so two releases
// seconds apart cannot lose the first. It returns the document written
// and the reason the existing file was disregarded, "" when it was merged
// or absent.
func MergeRelease(path, sessionID string, ids []string, now time.Time) (ReleaseFile, string, error) {
	existing, _, err := ReadRelease(path)
	disregarded := ""
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		disregarded = "unreadable"
	case existing.SessionID != sessionID:
		disregarded = "foreign_session"
	default:
		ids = append(slices.Clone(existing.MessageIDs), ids...)
	}
	f := ReleaseFile{Version: ReleaseFileVersion, SessionID: sessionID, MessageIDs: dedupeIDs(ids), WrittenAt: now}
	if err := WriteRelease(path, f); err != nil {
		return ReleaseFile{}, disregarded, err
	}
	return f, disregarded, nil
}

// ConsumeRelease unlinks the release file at path ONLY when its content is
// still byte-for-byte the raw bytes ReadRelease returned, and reports
// whether it did: adapterkit.RemovePidfile's compare-then-delete, reused
// verbatim. A `brigade inbox release` that wrote a second batch between
// the read and this call leaves different bytes, the delete is a no-op,
// and the next tick applies the union (3.5). It runs AFTER the pending
// file has been saved with the stamps, so a crash between the two
// re-applies the same stamps on the next start, which is idempotent.
func ConsumeRelease(path string, raw []byte) (bool, error) {
	return adapterkit.RemovePidfile(path, raw)
}

// dedupeIDs keeps the first occurrence of each valid id, in order, at most
// MaxReleaseIDs of them.
func dedupeIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, min(len(ids), MaxReleaseIDs))
	for _, id := range ids {
		if !validID(id) || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) == MaxReleaseIDs {
			break
		}
	}
	return out
}
