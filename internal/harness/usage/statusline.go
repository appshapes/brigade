package usage

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// MaxStatusBytes caps the status line document `brigade statusline` reads.
const MaxStatusBytes = 1 << 20

// A Status is what Claude Code gave the member's status line, as
// `brigade statusline` keeps it for the watcher: the one machine-readable
// source of a Claude Code session's cost, context window and plan limits
// (https://code.claude.com/docs/en/statusline.md). A plugin cannot set the
// status line, so only a member who adds the step has one.
type Status struct {
	Version int `json:"v"`
	// SessionID is Claude Code's own session id, the status line's
	// session_id; the file's name is Key of it.
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
	// CostUSD is cost.total_cost_usd, an estimate at list price.
	CostUSD *float64 `json:"cost_usd,omitzero"`
	// ContextWindow is context_window.context_window_size; ContextUsed the
	// input, cache-write and cache-read tokens of current_usage.
	ContextWindow *int64 `json:"context_window,omitzero"`
	ContextUsed   *int64 `json:"context_used,omitzero"`
	// Limits is the rate_limits windows the status line had: none for a
	// plan that has none, or before the session's first answer.
	Limits []Limit `json:"limits"`
	// HasLimits says rate_limits was in the document at all.
	HasLimits bool `json:"has_limits"`
}

// statusDoc is the part of the status line document Brigade reads. Every
// other member is skipped by the decoder.
type statusDoc struct {
	SessionID string `json:"session_id"`
	Cost      *struct {
		Total jsontext.Value `json:"total_cost_usd"`
	} `json:"cost"`
	Context *struct {
		Size    jsontext.Value `json:"context_window_size"`
		Current *struct {
			Input      jsontext.Value `json:"input_tokens"`
			CacheWrite jsontext.Value `json:"cache_creation_input_tokens"`
			CacheRead  jsontext.Value `json:"cache_read_input_tokens"`
		} `json:"current_usage"`
	} `json:"context_window"`
	Limits *map[string]*struct {
		Used     jsontext.Value `json:"used_percentage"`
		ResetsAt jsontext.Value `json:"resets_at"`
	} `json:"rate_limits"`
}

// ErrNoSession is a status line document with no session id.
var ErrNoSession = errors.New("usage: the status line document names no session")

// ParseStatus reads a status line document. A member of the wrong type is
// left out, never an error: the status line is Claude Code's to change.
func ParseStatus(data []byte, now time.Time) (*Status, error) {
	var d statusDoc
	if err := json.Unmarshal(data, &d, json.RejectUnknownMembers(false), jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true)); err != nil {
		return nil, errors.New("usage: the status line document is not JSON of the expected shape")
	}
	if d.SessionID == "" || len(d.SessionID) > 256 {
		return nil, ErrNoSession
	}
	s := &Status{Version: Version, SessionID: d.SessionID, At: now.UTC(), Limits: []Limit{}}
	if d.Cost != nil {
		if v, ok := number(d.Cost.Total); ok && v >= 0 {
			s.CostUSD = &v
		}
	}
	if c := d.Context; c != nil {
		if v, ok := count(c.Size); ok && v > 0 {
			s.ContextWindow = &v
		}
		if u := c.Current; u != nil {
			in, ok1 := count(u.Input)
			cw, ok2 := count(u.CacheWrite)
			cr, ok3 := count(u.CacheRead)
			// All zeros after /compact until the next call: empty, as the
			// page says.
			if ok1 && ok2 && ok3 && in+cw+cr > 0 {
				used := in + cw + cr
				s.ContextUsed = &used
			}
		}
	}
	if d.Limits != nil {
		s.HasLimits = true
		for _, w := range []struct{ key, name string }{{"five_hour", LimitShort}, {"seven_day", LimitWeekly}, {"spend_limit", "spend"}} {
			l, ok := (*d.Limits)[w.key]
			if !ok || l == nil {
				continue
			}
			used, ok := number(l.Used)
			if !ok || used < 0 {
				continue
			}
			lim := Limit{Name: w.name, UsedPercent: used}
			if at, ok := count(l.ResetsAt); ok && at > 0 {
				lim.ResetsAt = time.Unix(at, 0).UTC()
			}
			switch w.key {
			case "five_hour":
				lim.WindowMinutes = 300
			case "seven_day":
				lim.WindowMinutes = 10080
			}
			s.Limits = append(s.Limits, lim)
		}
	}
	return s, nil
}

// number reads a JSON number that is finite.
func number(raw jsontext.Value) (float64, bool) {
	if len(raw) == 0 || raw.Kind() != '0' {
		return 0, false
	}
	var v float64
	if json.Unmarshal(raw, &v) != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// count reads a JSON number that is a non-negative whole number a page can
// add without loss: at most 2^53.
func count(raw jsontext.Value) (int64, bool) {
	v, ok := number(raw)
	if !ok || v < 0 || v > 1<<53 || v != math.Trunc(v) {
		return 0, false
	}
	return int64(v), true
}

// StatusPath is where the status of the Claude Code session with that id
// is, under the state directory.
func StatusPath(stateDir, nativeID string) string {
	return filepath.Join(stateDir, Dir, StatusDir, Key(nativeID)+".json")
}

// WriteStatus writes it, whole and atomically, mode 0600.
func WriteStatus(stateDir string, s *Status) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(stateDir, Dir, StatusDir), 0o700); err != nil {
		return err
	}
	return adapterkit.WriteAtomic(StatusPath(stateDir, s.SessionID), append(data, '\n'))
}

// ReadStatus is the status of the Claude Code session with that id: nil
// when its status line runs no `brigade statusline`.
func ReadStatus(stateDir, nativeID string) (*Status, error) {
	data, err := readCapped(StatusPath(stateDir, nativeID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil || s.Version != Version || s.SessionID != nativeID {
		return nil, nil //nolint:nilerr // a file that is not this session's status is no status, as none is
	}
	return &s, nil
}
