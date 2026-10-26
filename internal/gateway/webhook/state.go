package webhook

import (
	"encoding/json/v2"
	"errors"
	iofs "io/fs"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// Thread directions in the local state.
const (
	dirIn  = "in"  // the gateway created this message from a webhook POST
	dirOut = "out" // the gateway delivered this message to the webhook
)

const (
	maxThreads   = 10000
	threadMaxAge = 30 * 24 * time.Hour
)

// threadEntry remembers one message the gateway carried, so a reply in
// either direction can be routed and labelled.
type threadEntry struct {
	Direction string    `json:"direction"`
	Peer      string    `json:"peer_session_id"` // in: the recipient; out: the Brigade sender
	Ref       string    `json:"ref,omitzero"`
	At        time.Time `json:"at"`
}

type stateDoc struct {
	SessionID string                  `json:"session_id,omitzero"`
	Threads   map[string]*threadEntry `json:"threads"`
}

// state is the gateway's local, single-machine memory: its own session id
// (for `resume`) and the thread map. It lives in the state directory,
// never in a synced folder: exactly one process writes it.
type state struct {
	mu   sync.Mutex
	path string
	doc  stateDoc
}

func loadState(path string) (*state, error) {
	s := &state{path: path, doc: stateDoc{Threads: map[string]*threadEntry{}}}
	if path == "" {
		return s, nil
	}
	data, err := adapterkit.ReadStrict(path)
	switch {
	case errors.Is(err, iofs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(data, &s.doc); err != nil {
		return nil, errors.New("the gateway state file is not valid JSON; move it aside to start fresh")
	}
	if s.doc.Threads == nil {
		s.doc.Threads = map[string]*threadEntry{}
	}
	return s, nil
}

// saveLocked writes the document atomically, 0600. The caller holds mu.
func (s *state) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(s.path)); err != nil {
		return err
	}
	data, err := json.Marshal(&s.doc)
	if err != nil {
		return err
	}
	return adapterkit.WriteAtomic(s.path, append(data, '\n'))
}

func (s *state) sessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc.SessionID
}

func (s *state) setSessionID(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.SessionID == id {
		return nil
	}
	s.doc.SessionID = id
	return s.saveLocked()
}

func (s *state) thread(id string) (threadEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.doc.Threads[id]
	if !ok {
		return threadEntry{}, false
	}
	return *e, true
}

// remember records one carried message and prunes the map: entries older
// than threadMaxAge go, then the oldest until maxThreads remain.
func (s *state) remember(id string, e threadEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc.Threads[id] = &e
	for k, v := range s.doc.Threads {
		if e.At.Sub(v.At) > threadMaxAge {
			delete(s.doc.Threads, k)
		}
	}
	if over := len(s.doc.Threads) - maxThreads; over > 0 {
		keys := make([]string, 0, len(s.doc.Threads))
		for k := range s.doc.Threads {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return s.doc.Threads[a].At.Compare(s.doc.Threads[b].At) })
		for _, k := range keys[:over] {
			delete(s.doc.Threads, k)
		}
	}
	return s.saveLocked()
}
