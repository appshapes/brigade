// Package usage is what one session spent, as its harness tells it
// (work item 347, ADR 0036): its model, the context in use and the
// window's size, the tokens of its model calls, its cost when the harness
// gives one, the plan's limits and the paid extra usage. The watcher reads
// it from the harness's own records and writes one file for each session
// in this machine's state directory, mode 0600; `brigade usage` shows it.
// Nothing here leaves the machine: whether a member shares it with the
// team is work item 348, off by default (the owner's ruling of
// 2026-11-24).
//
// Every value is present, or not available with a reason. A value the
// harness does not give is never written as zero: a page that adds the
// usage of several sessions must tell "nothing spent" from "not known".
package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// Version is the shape this package writes and reads.
const Version = 1

// Dir is the directory under Brigade's state directory, and StatusDir the
// one under it where `brigade statusline` keeps what Claude Code's status
// line gave it.
const (
	Dir       = "usage"
	StatusDir = "statusline"
)

// MaxFileBytes caps a usage file a reader decodes.
const MaxFileBytes = 256 << 10

// The reasons a value is not available. A reason is fixed text, so a
// reader can count them.
const (
	// NotGiven: the harness gives this value nowhere Brigade can read.
	NotGiven = "the harness does not give it"
	// NotYet: the harness gives it, and the session has not reached it yet
	// (no model call, or no answer from the limits command so far).
	NotYet = "not seen yet"
	// StatusLineOnly: Claude Code gives it to the member's status line
	// alone, and the status line runs no `brigade statusline`.
	StatusLineOnly = "Claude Code gives it to the status line only: add brigade statusline to it"
	// NotForPlan: the harness gives no such value for the session's plan
	// (no money cost on a subscription, no 5-hour window on a weekly pool).
	NotForPlan = "the plan has none"
	// CommandFailed: the harness's own command for it did not answer.
	CommandFailed = "the harness's command did not answer"
	// BadShape: the harness's record is there, and not in the shape the
	// reader takes; the value is left out rather than guessed.
	BadShape = "the harness's record is not in the shape Brigade reads"
	// NoLimitsYet: Claude Code's status line gave no rate_limits, which it
	// gives Pro, Max and Ultra only, after the session's first answer.
	NoLimitsYet = "the status line gave none: the plan has none, or the session has had no answer yet"
)

// A Value is one value, or the reason it is not available. Exactly one of
// the two is set.
type Value[T any] struct {
	V           *T     `json:"value,omitzero"`
	Unavailable string `json:"unavailable,omitzero"`
}

// Of is a value that is there.
func Of[T any](v T) Value[T] { return Value[T]{V: &v} }

// Missing is a value that is not, for that reason.
func Missing[T any](reason string) Value[T] { return Value[T]{Unavailable: reason} }

// Known reports whether the value is there.
func (v Value[T]) Known() bool { return v.V != nil }

// Or is the value, or def when it is not there.
func (v Value[T]) Or(def T) T {
	if v.V == nil {
		return def
	}
	return *v.V
}

// Tokens are counts of tokens, each its own value: a harness that counts
// cache reads gives them, and one that does not says so.
type Tokens struct {
	Input      Value[int64] `json:"input"`
	Output     Value[int64] `json:"output"`
	CacheRead  Value[int64] `json:"cache_read"`
	CacheWrite Value[int64] `json:"cache_write"`
	Reasoning  Value[int64] `json:"reasoning"`
}

// A Cost is money, in US dollars. Kind says what the harness means by it:
// KindEstimate, at list price, or KindUnknown when it does not say.
type Cost struct {
	USD  float64 `json:"usd"`
	Kind string  `json:"kind"`
	// Partial is true when the harness says some calls had no price.
	Partial bool `json:"partial,omitzero"`
}

// The kinds of a cost.
const (
	KindEstimate = "estimate"
	KindUnknown  = "unknown"
)

// A Limit is one window of the plan's limits: how much of it is used, in
// percent (over 100 is valid), its length when the harness gives it, and
// when it resets.
type Limit struct {
	// Name is the window as Brigade names it: LimitShort (the 5-hour
	// class) or LimitWeekly, else the harness's own name.
	Name          string    `json:"name"`
	UsedPercent   float64   `json:"used_percent"`
	WindowMinutes int       `json:"window_minutes,omitzero"`
	ResetsAt      time.Time `json:"resets_at,omitzero"`
}

// The names of the two windows every plan shows in some form.
const (
	LimitShort  = "short"
	LimitWeekly = "weekly"
)

// Limits are the plan's windows as the harness last gave them, and the
// plan's name when it gives one.
type Limits struct {
	Plan    string  `json:"plan,omitzero"`
	Windows []Limit `json:"windows"`
}

// Extra is the paid usage beyond the plan, as Codex CLI gives it: whether
// credits are there, whether they are unlimited, the balance as Codex
// wrote it, and whether a spend control stopped the session.
type Extra struct {
	HasCredits          *bool  `json:"has_credits,omitzero"`
	Unlimited           *bool  `json:"unlimited,omitzero"`
	Balance             string `json:"balance,omitzero"`
	SpendControlReached *bool  `json:"spend_control_reached,omitzero"`
}

// A Record is one session's usage, as the watcher last read it.
type Record struct {
	Version int `json:"v"`
	// SessionID is the Brigade session id; Session its name and Harness
	// the harness's id (claude-code, codex, grok, muse).
	SessionID string `json:"session_id"`
	Session   string `json:"session,omitzero"`
	Harness   string `json:"harness"`
	// ReadAt is when the watcher read it; the values are as of then.
	ReadAt time.Time `json:"read_at"`

	Model         Value[string] `json:"model"`
	ContextUsed   Value[int64]  `json:"context_used"`
	ContextWindow Value[int64]  `json:"context_window"`
	// Total is the session's tokens so far, each call counted once; Last
	// the latest model call's; Calls how many model calls were counted.
	Total Tokens       `json:"total"`
	Last  Tokens       `json:"last"`
	Calls Value[int64] `json:"calls"`
	Cost  Value[Cost]  `json:"cost"`
	// Limits are the plan's windows. LimitsAt is when the harness last
	// gave them: a window is a fact of the account, as of then.
	Limits   Value[Limits] `json:"limits"`
	LimitsAt time.Time     `json:"limits_at,omitzero"`
	Extra    Value[Extra]  `json:"extra"`
}

// Key is the name of a session's file: the first 32 hex digits of the
// SHA-256 of its id, so an opaque id never names a path.
func Key(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:16])
}

// Path is where the record of the session with that id is, under the state
// directory.
func Path(stateDir, sessionID string) string {
	return filepath.Join(stateDir, Dir, Key(sessionID)+".json")
}

// Write writes the record, whole and atomically, mode 0600, in a directory
// of mode 0700.
func Write(stateDir string, r *Record) error {
	if r.SessionID == "" {
		return errors.New("usage: a record names its session")
	}
	r.Version = Version
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(stateDir, Dir), 0o700); err != nil {
		return err
	}
	return adapterkit.WriteAtomic(Path(stateDir, r.SessionID), append(data, '\n'))
}

// Read is the record of the session with that id: nil when there is none.
func Read(stateDir, sessionID string) (*Record, error) {
	r, err := readFile(Path(stateDir, sessionID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if r.SessionID != sessionID {
		return nil, nil
	}
	return r, nil
}

// List is every record in the state directory, the latest read first. A
// file that is not a record is left out.
func List(stateDir string) ([]*Record, error) {
	entries, err := os.ReadDir(filepath.Join(stateDir, Dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".json") {
			continue
		}
		r, err := readFile(filepath.Join(stateDir, Dir, name))
		if err != nil || Key(r.SessionID)+".json" != name {
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b *Record) int {
		return cmpTime(b.ReadAt, a.ReadAt)
	})
	return out, nil
}

func cmpTime(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	}
	return 0
}

// readFile reads one record: a regular file, no symlink followed, at most
// MaxFileBytes, of this version.
func readFile(path string) (*Record, error) {
	data, err := readCapped(path)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, errors.New("usage: not a usage record")
	}
	if r.Version != Version || r.SessionID == "" {
		return nil, errors.New("usage: a record of another version")
	}
	return &r, nil
}

// readCapped reads a regular file, following no symlink, of at most
// MaxFileBytes.
func readCapped(path string) ([]byte, error) {
	f, err := adapterkit.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxFileBytes {
		return nil, errors.New("usage: not a usage file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, errors.New("usage: the file is larger than a usage file can be")
	}
	return data, nil
}

// Prune removes the records and status files no watcher wrote since
// before, and returns how many. A file it cannot remove is left.
func Prune(stateDir string, before time.Time) int {
	n := 0
	for _, dir := range []string{filepath.Join(stateDir, Dir), filepath.Join(stateDir, Dir, StatusDir)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			fi, err := e.Info()
			if err != nil || !fi.ModTime().Before(before) {
				continue
			}
			if os.Remove(filepath.Join(dir, e.Name())) == nil {
				n++
			}
		}
	}
	return n
}
