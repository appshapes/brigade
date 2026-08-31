package main

// events.go — the shared, append-only observation log. All three processes plus the orchestrator
// write it; the orchestrator reads it back at the end and every number in the report comes from it.
// It lives OUTSIDE the profile directory, because the in-memory process's sandbox denies writes there.
//
// One event is one line under 4 KiB written with a single O_APPEND write, which POSIX makes atomic
// against other appenders, so interleaving cannot tear a line.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type event struct {
	T    string         `json:"t"`
	Proc string         `json:"proc"`
	PID  int            `json:"pid"`
	Kind string         `json:"kind"`
	D    map[string]any `json:"d,omitempty"`
}

type eventLog struct {
	mu   sync.Mutex
	f    *os.File
	proc string
	pid  int
	echo bool
}

func openEventLog(path, proc string, echo bool) *eventLog {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fatal("open event log: %v", err)
	}
	return &eventLog{f: f, proc: proc, pid: os.Getpid(), echo: echo}
}

func (e *eventLog) emit(kind string, d map[string]any) {
	ev := event{T: time.Now().UTC().Format(time.RFC3339Nano), Proc: e.proc, PID: e.pid, Kind: kind, D: d}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b = append(b, '\n')
	if len(b) > 4000 {
		b = b[:0]
		trimmed, _ := json.Marshal(event{T: ev.T, Proc: ev.Proc, PID: ev.PID, Kind: kind,
			D: map[string]any{"truncated": true}})
		b = append(trimmed, '\n')
	}
	e.mu.Lock()
	_, _ = e.f.Write(b)
	e.mu.Unlock()
	if e.echo {
		fmt.Printf("%s %s[%d] %s %v\n", ev.T, ev.Proc, ev.PID, kind, d)
	}
}

func (e *eventLog) close() { e.mu.Lock(); _ = e.f.Close(); e.mu.Unlock() }

// emitRefresh records one pass through ensureFresh, which is the whole evidence base for (a), (c) and (f).
func (e *eventLog) emitRefresh(note string, out refreshOutcome) {
	d := map[string]any{
		"note":         note,
		"did":          out.Did,
		"skipped":      out.Skipped,
		"persisted":    out.Persisted,
		"status":       out.Status,
		"error_code":   out.ErrorCode,
		"sent_rt":      out.SentRT,
		"got_rt":       out.GotRT,
		"got_at":       out.GotAT,
		"lock_wait_us": out.LockWait.Microseconds(),
		"lock_tries":   out.LockTries,
		"elapsed_ms":   out.Elapsed.Milliseconds(),
	}
	if out.LockErr != "" {
		d["lock_err"] = out.LockErr
	}
	if out.PersistErr != "" {
		d["persist_err"] = out.PersistErr
	}
	if out.Did && out.Status == 200 {
		d["exp_in"] = out.Session.ExpiresIn
		d["expires_at"] = out.Session.ExpiresAt
	}
	e.emit("refresh", d)
}

func readEvents(path string) ([]event, []string) {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal("read event log: %v", err)
	}
	var evs []event
	var bad []string
	start := 0
	for i := 0; i <= len(b); i++ {
		if i == len(b) || b[i] == '\n' {
			line := b[start:i]
			start = i + 1
			if len(line) == 0 {
				continue
			}
			var ev event
			if err := json.Unmarshal(line, &ev); err != nil {
				bad = append(bad, string(line))
				continue
			}
			evs = append(evs, ev)
		}
	}
	return evs, bad
}

func (ev event) str(k string) string {
	if ev.D == nil {
		return ""
	}
	s, _ := ev.D[k].(string)
	return s
}

func (ev event) num(k string) float64 {
	if ev.D == nil {
		return 0
	}
	f, _ := ev.D[k].(float64)
	return f
}

func (ev event) boolean(k string) bool {
	if ev.D == nil {
		return false
	}
	b, _ := ev.D[k].(bool)
	return b
}

func (ev event) when() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, ev.T)
	return t
}

// randHex is E0-2's fixture helper.
func randHex(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }
