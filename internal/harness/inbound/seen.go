package inbound

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// SeenCapacity is how many injected message ids the pipeline remembers,
// in memory and in the seen file (3.2: "last 2,000 injected message_ids").
const SeenCapacity = 2000

// SeenFileVersion is the schema version of the seen file.
const SeenFileVersion = 1

// MaxSeenFileBytes bounds what Load will read; a real seen file is under
// 200 KiB, and anything larger is not one.
const MaxSeenFileBytes = 1 << 20

// A SeenStore persists the ids the pipeline has injected, so a restart
// between an injection and its acknowledgement cannot inject the same
// message twice (U-13). Load runs once at construction; Save runs after
// every successful injection with the full bounded list, oldest first.
type SeenStore interface {
	Load() ([]string, error)
	Save(ids []string) error
}

// SeenPath is ${stateDir}/state/<claude_pid>.seen.json (3.2).
func SeenPath(stateDir string, claudePID int) string {
	return filepath.Join(stateDir, "state", strconv.Itoa(claudePID)+".seen.json")
}

// seenFile is the on-disk shape.
type seenFile struct {
	Version    int      `json:"version"`
	MessageIDs []string `json:"message_ids"`
}

// A FileSeenStore keeps the seen ids in one 0600 file written atomically
// (adapterkit.WriteAtomic) inside a 0700 directory chain. Load reads it
// through adapterkit.ReadStrict: a group- or world-readable file is refused
// — a planted seen file would make the pipeline acknowledge messages
// without injecting them, so the file is trusted only on the same terms as
// the by-pid map — and the pipeline then starts empty and, on the next
// Save, replaces the file with a 0600 one.
type FileSeenStore struct {
	// Path is normally SeenPath(stateDir, claudePID).
	Path string
}

// Load returns the persisted ids, oldest first, at most SeenCapacity. A
// missing file is (nil, nil). A refused (not 0600), unreadable, over-large,
// malformed or wrong-version file is an error; ids that are empty, over
// MaxMessageIDBytes or not UTF-8 are skipped.
func (s FileSeenStore) Load() ([]string, error) {
	data, err := adapterkit.ReadStrict(s.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case len(data) > MaxSeenFileBytes:
		return nil, errSeenFileTooLarge
	}
	var f seenFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errSeenFileMalformed
	}
	if f.Version != SeenFileVersion {
		return nil, errSeenFileVersion
	}
	ids := make([]string, 0, min(len(f.MessageIDs), SeenCapacity))
	for _, id := range f.MessageIDs {
		if id == "" || len(id) > MaxMessageIDBytes || !utf8.ValidString(id) {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) > SeenCapacity {
		ids = ids[len(ids)-SeenCapacity:]
	}
	return ids, nil
}

// Save writes ids (oldest first; only the last SeenCapacity are kept)
// atomically, 0600, creating the 0700 directory chain as needed.
func (s FileSeenStore) Save(ids []string) error {
	if len(ids) > SeenCapacity {
		ids = ids[len(ids)-SeenCapacity:]
	}
	if ids == nil {
		ids = []string{}
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(s.Path)); err != nil {
		return err
	}
	data, err := json.Marshal(seenFile{Version: SeenFileVersion, MessageIDs: ids})
	if err != nil {
		return err
	}
	return adapterkit.WriteAtomic(s.Path, append(data, '\n'))
}

// The fixed-text failures of Load; the file's content is never echoed.
var (
	errSeenFileTooLarge  = errors.New("inbound: seen file exceeds the size cap")
	errSeenFileMalformed = errors.New("inbound: seen file is not a valid JSON document")
	errSeenFileVersion   = errors.New("inbound: seen file has an unknown version")
)

// A MemorySeenStore is a SeenStore in memory, for tests inside a synctest
// bubble (where real files are out of bounds) and for callers that want no
// persistence. It records how many times Save ran. The zero value is ready.
type MemorySeenStore struct {
	mu    sync.Mutex
	ids   []string
	saves int
}

// Load returns a copy of the last saved ids.
func (m *MemorySeenStore) Load() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ids...), nil
}

// Save keeps a copy of ids.
func (m *MemorySeenStore) Save(ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ids = append([]string(nil), ids...)
	m.saves++
	return nil
}

// IDs returns a copy of the last saved ids.
func (m *MemorySeenStore) IDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ids...)
}

// Saves is how many times Save has run.
func (m *MemorySeenStore) Saves() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saves
}
