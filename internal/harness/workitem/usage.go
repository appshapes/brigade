// usage.go is the usage a member shares with the team (work item 348, ADR
// 0038): with the `share_usage` option on (off by default, the owner's
// ruling of 2026-11-24), a session's watcher keeps one file of the board,
// `usage/<key>.json`, key being SessionKey of its session id: its model,
// its context, its plan's limits and extra usage, and its tokens and
// estimated cost on each work item. Unlike a change, the file is written
// again in place: one writer, the session's own watcher, so file sync
// never makes a conflict copy of it, and a session's share never grows.
// Like every file of the board it is a teammate's word (docs/security.md,
// section 19): any machine can write one under any name.

package workitem

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/usage"
	"github.com/appshapes/brigade/internal/protocol"
)

// UsageDir is the directory of the shared usages, beside WorkDir.
const UsageDir = "usage"

// The caps a shared usage keeps to, whoever wrote it.
const (
	MaxUsageItems   = 500
	MaxUsageWindows = 8
	maxUsageText    = 128
)

// A Usage is what one session shared of its usage, as of ReadAt.
type Usage struct {
	Version       int           `json:"v"`
	SessionID     string        `json:"session_id"`
	By            string        `json:"by,omitzero"`
	Session       string        `json:"session,omitzero"`
	Harness       string        `json:"harness,omitzero"`
	Model         string        `json:"model,omitzero"`
	ContextUsed   *int64        `json:"context_used,omitzero"`
	ContextWindow *int64        `json:"context_window,omitzero"`
	Limits        *usage.Limits `json:"limits,omitzero"`
	LimitsAt      time.Time     `json:"limits_at,omitzero"`
	Extra         *usage.Extra  `json:"extra,omitzero"`
	Items         []UsageItem   `json:"items,omitzero"`
	ReadAt        time.Time     `json:"read_at"`
}

// A UsageItem is what the session spent on one work item, by its id.
type UsageItem struct {
	Item string `json:"item"`
	usage.ItemUsage
}

// ShareUsage writes the session's shared usage, whole and atomically, in
// place of the one it wrote before. who is its writer and session name.
func (s Store) ShareUsage(u *Usage, who Author) error {
	if u == nil || !validSessionID(u.SessionID) {
		return errNoSession
	}
	if !filepath.IsAbs(s.Folder) {
		return errors.New("workitem: the board folder must be an absolute path")
	}
	dir := filepath.Join(s.Folder, UsageDir)
	if fi, err := os.Lstat(dir); err == nil && !fi.IsDir() {
		return errNotADirectory
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	c := *u
	c.Version, c.By, c.Session = Version, who.Member, who.Session
	data, err := json.Marshal(&c)
	if err != nil {
		return err
	}
	if len(data) > MaxChangeBytes {
		return errors.New("workitem: the shared usage is larger than a file of the board can be")
	}
	return adapterkit.WriteAtomic(filepath.Join(dir, SessionKey(u.SessionID)+".json"), append(data, '\n'))
}

// ReadSharedUsage is every shared usage the folder holds, by session id,
// and how many files it left out: a name that is not a key, a file that
// is not one of the expected shape, or one whose session id is not its
// name's. A folder with none is no error.
func ReadSharedUsage(folder string) (map[string]*Usage, int, error) {
	out := map[string]*Usage{}
	entries, skipped, err := readDir(filepath.Join(folder, UsageDir))
	if err != nil {
		return nil, 0, err
	}
	for i, d := range entries {
		if i >= 2*MaxSessions || len(out) >= MaxSessions {
			skipped += len(entries) - i
			break
		}
		key, ok := strings.CutSuffix(d.Name(), ".json")
		if !ok || d.IsDir() || !ValidID32(key) {
			skipped++
			continue
		}
		u, err := readUsage(filepath.Join(folder, UsageDir, d.Name()))
		if err != nil || SessionKey(u.SessionID) != key {
			skipped++
			continue
		}
		out[u.SessionID] = u
	}
	return out, skipped, nil
}

// readUsage reads one shared usage within the rules of every read of a
// synced folder: a regular file, no symlink followed, at most
// MaxChangeBytes, of this version; then cleaned.
func readUsage(path string) (*Usage, error) {
	f, err := adapterkit.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxChangeBytes {
		return nil, errors.New("workitem: not a shared usage")
	}
	data := make([]byte, fi.Size())
	if _, err := f.ReadAt(data, 0); err != nil {
		return nil, err
	}
	var u Usage
	if err := json.Unmarshal(data, &u); err != nil || u.Version != Version || !validSessionID(u.SessionID) || u.ReadAt.IsZero() {
		return nil, errors.New("workitem: not a shared usage of the expected shape")
	}
	u.clean()
	return &u, nil
}

// clean caps and cleans a shared usage read from the folder.
func (u *Usage) clean() {
	text := func(s string) string { return protocol.TruncateRunes(Line(s), maxUsageText) }
	u.By, u.Session = protocol.SanitizeLabel(Line(u.By)), protocol.OneLineName(u.Session)
	u.Harness, u.Model = text(u.Harness), text(u.Model)
	for _, p := range []**int64{&u.ContextUsed, &u.ContextWindow} {
		if *p != nil && **p < 0 {
			*p = nil
		}
	}
	if l := u.Limits; l != nil {
		l.Plan = text(l.Plan)
		l.Windows = slices.DeleteFunc(l.Windows, func(w usage.Limit) bool { return !finite(w.UsedPercent) || w.UsedPercent < 0 || w.WindowMinutes < 0 })
		if len(l.Windows) > MaxUsageWindows {
			l.Windows = l.Windows[:MaxUsageWindows]
		}
		for i := range l.Windows {
			l.Windows[i].Name = text(l.Windows[i].Name)
		}
	}
	if x := u.Extra; x != nil {
		x.Balance = text(x.Balance)
	}
	u.Items = slices.DeleteFunc(u.Items, func(it UsageItem) bool {
		t := it.Tokens
		return !ValidID(it.Item) || it.Number < 1 || it.Number > MaxNumber || !finite(it.CostUSD) || it.CostUSD < 0 ||
			t.Input < 0 || t.Output < 0 || t.CacheRead < 0 || t.CacheWrite < 0 || it.Priced < 0 || it.Unpriced < 0
	})
	if len(u.Items) > MaxUsageItems {
		u.Items = u.Items[:MaxUsageItems]
	}
	for i := range u.Items {
		u.Items[i].Model = text(u.Items[i].Model)
	}
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
