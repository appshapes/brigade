// Package notice is the watcher's notice file (plan 3.2, card 34): the
// lines a watcher has for its session, which the next prompt hook prints
// once and removes.
//
// The file used to be ONE line, overwritten by whoever wrote last, so a
// folder-sync summary erased "this session is not receiving" and the other
// way round. It now holds one line per TOPIC. A topic's newer line
// replaces its older one — the two are about one fact, and the later is
// the true one — and lines of different topics stay side by side, oldest
// first, MaxNotices at most.
//
// The file is ${stateDir}/state/<claude_pid>.notice, mode 0600, written
// atomically by the watcher and read through the strict reader. The hook
// takes it by RENAMING it first, so what it prints is exactly what it
// removed: a line the watcher writes meanwhile lands in a new file and
// waits for the next prompt. No lock is held across the two processes. The
// one thing that can happen is a line of another topic printed twice, when
// the watcher read the file a moment before the hook took it.
//
// A file an older watcher wrote is one line of plain text, and is read as
// that.
package notice

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// The topics. One line each.
const (
	// TopicWatcher is whether the session receives: the watcher stopped,
	// gave up, retries slowly, has reconnected.
	TopicWatcher = "watcher"
	// TopicSync is folder sync: the summary of what syncs, or why it is
	// off.
	TopicSync = "sync"
)

const (
	// FileVersion is the document's version.
	FileVersion = 1
	// MaxNotices is how many lines the file holds; the oldest goes first.
	MaxNotices = 5
	// takenSuffix names the file while the hook holds it.
	takenSuffix = ".taken"
)

// A Notice is one line and the topic it belongs to. A line an older
// watcher wrote has no topic.
type Notice struct {
	Topic string    `json:"topic,omitzero"`
	Text  string    `json:"text"`
	At    time.Time `json:"at,omitzero"`
}

type document struct {
	Version int      `json:"version"`
	Notices []Notice `json:"notices"`
}

// mu serialises Put within the process: the watcher's supervisor and its
// sync goroutine both write.
var mu sync.Mutex

// Path is ${stateDir}/state/<claude_pid>.notice.
func Path(stateDir string, claudePID int) string {
	return filepath.Join(stateDir, "state", fmt.Sprintf("%d.notice", claudePID))
}

// Put records text as the topic's line: it replaces the line the topic
// had, goes last, and pushes the oldest line out when the file is full.
// text is folded onto one line. A file that cannot be read as a notice
// file is replaced.
func Put(path, topic, text string, at time.Time) error {
	text = strings.Join(strings.Fields(text), " ")
	if topic == "" || text == "" {
		return errors.New("notice: a notice needs a topic and a text")
	}
	mu.Lock()
	defer mu.Unlock()
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		return err
	}
	old, _ := Read(path)
	kept := make([]Notice, 0, len(old)+1)
	for _, n := range old {
		if n.Topic != topic {
			kept = append(kept, n)
		}
	}
	kept = append(kept, Notice{Topic: topic, Text: text, At: at.UTC()})
	if len(kept) > MaxNotices {
		kept = kept[len(kept)-MaxNotices:]
	}
	data, err := json.Marshal(document{Version: FileVersion, Notices: kept})
	if err != nil {
		return err
	}
	return adapterkit.WriteAtomic(path, append(data, '\n'))
}

// Read returns the file's lines, oldest first, and leaves the file where
// it is. A missing file is no lines and no error.
func Read(path string) ([]Notice, error) {
	data, err := adapterkit.ReadStrict(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parse(data), nil
}

// Take removes the file and returns the lines it held, oldest first. A
// missing file is no lines and no error. A file that is not a private
// regular file is removed unread, and the error says so.
func Take(path string) ([]Notice, error) {
	taken := path + takenSuffix
	if err := os.Rename(path, taken); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	data, err := adapterkit.ReadStrict(taken)
	if rerr := os.Remove(taken); rerr != nil && err == nil {
		err = rerr
	}
	if err != nil {
		return nil, err
	}
	return parse(data), nil
}

// parse reads the document; bytes that are not one are the single line an
// older watcher wrote, of which the first line is the notice.
func parse(data []byte) []Notice {
	var d document
	if err := json.Unmarshal(data, &d); err == nil && d.Version == FileVersion {
		out := make([]Notice, 0, min(len(d.Notices), MaxNotices))
		for _, n := range d.Notices {
			if n.Text != "" && len(out) < MaxNotices {
				out = append(out, n)
			}
		}
		return out
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if strings.TrimSpace(first) == "" {
		return nil
	}
	return []Notice{{Text: first}}
}
