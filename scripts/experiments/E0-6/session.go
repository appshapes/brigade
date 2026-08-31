package main

// session.go — the design under test (plan 5.1, lines 538-549):
//
//	profiles/<name>/session.json    the GoTrue response verbatim, 0600, in a 0700 directory,
//	                               written by an atomic write (CreateTemp, write, fsync, rename, fsync dir)
//	profiles/<name>/session.json.lock  the SIDECAR advisory lock: flock(LOCK_EX) is taken on this file and
//	                               never on session.json, because the rename swaps the inode out from
//	                               under a lock held on the old one.
//
// Every read-refresh-write runs under the sidecar lock, bounded at 10 s (LOCK_NB polled every 100 ms,
// then `unavailable`); after acquiring, the file is RE-READ and the refresh is skipped when another
// process already stored a token with more than 90 s left.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	refreshMarginDefault = 90 * time.Second // plan 5.1 (auth-js's margin)
	lockBound            = 10 * time.Second // plan 5.1
	lockPoll             = 100 * time.Millisecond
)

var errLockUnavailable = errors.New("unavailable")

// ---------------------------------------------------------------- profile paths

type profile struct {
	dir string
}

func (p profile) sessionPath() string { return filepath.Join(p.dir, "session.json") }
func (p profile) lockPath() string    { return filepath.Join(p.dir, "session.json.lock") }

// ---------------------------------------------------------------- read

// readResult is one observation of session.json, used as evidence for check (c).
type readResult struct {
	S           Session
	Raw         []byte
	Mode        os.FileMode
	OK          bool
	ParseErr    string // non-empty => the bytes on disk did not parse as JSON (a torn read)
	MissingList []string
	StatErr     string
	ReadErr     string
}

// requiredFields is what the adapter needs out of the file; a partial write would drop some of them.
func (r *readResult) validate() {
	if r.S.AccessToken == "" {
		r.MissingList = append(r.MissingList, "access_token")
	}
	if r.S.RefreshToken == "" {
		r.MissingList = append(r.MissingList, "refresh_token")
	}
	if r.S.ExpiresAt == 0 {
		r.MissingList = append(r.MissingList, "expires_at")
	}
	if r.S.ExpiresIn == 0 {
		r.MissingList = append(r.MissingList, "expires_in")
	}
	if r.S.TokenType == "" {
		r.MissingList = append(r.MissingList, "token_type")
	}
	if r.S.User.ID == "" {
		r.MissingList = append(r.MissingList, "user.id")
	}
	r.OK = r.ParseErr == "" && r.ReadErr == "" && len(r.MissingList) == 0
}

// readSession reads session.json WITHOUT the lock. The atomic write is supposed to make that safe;
// the tearing prober calls this in a tight loop for the whole soak to find out whether it is.
func readSession(path string) readResult {
	var r readResult
	fi, err := os.Stat(path)
	if err != nil {
		r.StatErr = err.Error()
	} else {
		r.Mode = fi.Mode().Perm()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		r.ReadErr = err.Error()
		r.validate()
		return r
	}
	r.Raw = b
	if err := json.Unmarshal(b, &r.S); err != nil {
		r.ParseErr = err.Error()
		r.validate()
		return r
	}
	r.validate()
	return r
}

// ---------------------------------------------------------------- atomic write

// writeAtomic is adapterkit.WriteAtomic: CreateTemp 0600 in the 0700 profile directory, write,
// fsync, rename, fsync the directory.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("createtemp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	tmpName = "" // renamed away; nothing to clean up
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync dir: %w", err)
	}
	return nil
}

func writeSession(path string, s Session, raw []byte) error {
	// The file holds the response verbatim when we have it, so nothing the server sent is dropped.
	if len(raw) > 0 {
		return writeAtomic(path, raw)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

// ---------------------------------------------------------------- the sidecar lock

type lockHandle struct {
	f        *os.File
	Waited   time.Duration
	Attempts int
}

func (l *lockHandle) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// acquireLock takes flock(LOCK_EX) on the SIDECAR with the plan's bound: LOCK_NB polled every 100 ms
// for 10 s, then `unavailable`. readOnly opens the sidecar O_RDONLY, which is what a process whose
// profile directory it cannot write to has to do.
func acquireLock(lockPath string, readOnly bool) (*lockHandle, error) {
	var f *os.File
	var err error
	if readOnly {
		f, err = os.OpenFile(lockPath, os.O_RDONLY, 0o600)
	} else {
		f, err = os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("open sidecar: %w", err)
	}
	start := time.Now()
	deadline := start.Add(lockBound)
	attempts := 0
	for {
		attempts++
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &lockHandle{f: f, Waited: time.Since(start), Attempts: attempts}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("flock: %w (after %v, %d attempts)", err, time.Since(start), attempts)
		}
		if time.Now().After(deadline) {
			f.Close()
			return &lockHandle{Waited: time.Since(start), Attempts: attempts}, errLockUnavailable
		}
		time.Sleep(lockPoll)
	}
}

// tryLockNB is the (d) probe: one non-blocking attempt, so "is the lock held right now?" is a fact
// rather than an assumption.
func tryLockNB(lockPath string) (held bool, h *lockHandle, err error) {
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false, nil, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return false, &lockHandle{f: f, Attempts: 1}, nil
	}
	f.Close()
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil, nil
	}
	return false, nil, err
}

// ---------------------------------------------------------------- token identity for the transcript

// tokID is a stable, non-secret handle on a token so two processes can be shown to be holding the
// same or different refresh tokens without any token reaching the transcript.
func tokID(t string) string {
	if t == "" {
		return "-"
	}
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:4])
}

func remaining(s Session) time.Duration {
	return time.Until(time.Unix(s.ExpiresAt, 0))
}

// ---------------------------------------------------------------- the refresh decision

type refreshOutcome struct {
	Did        bool
	Skipped    bool   // another process had already stored a token with > margin left
	Persisted  bool   // the new session reached session.json
	PersistErr string // non-empty => read-only fallback: refreshed in memory, not persisted
	Status     int
	ErrorCode  string // GoTrue error_code, e.g. refresh_token_already_used
	SentRT     string // tokID of the refresh token presented
	GotRT      string // tokID of the refresh token returned
	GotAT      string // tokID of the access token returned
	OneBehind  bool   // the answer carried a refresh token we did not derive from ours in this call
	LockWait   time.Duration
	LockTries  int
	LockErr    string
	Elapsed    time.Duration
	Session    Session
	Raw        []byte
}

// authErrorCode decodes GoTrue's error body in either shape (with or without X-Supabase-Api-Version).
func authErrorCode(b []byte) string {
	var a AuthError
	if err := json.Unmarshal(b, &a); err != nil {
		return ""
	}
	if a.ErrorCode != "" {
		return a.ErrorCode
	}
	if a.Error != "" {
		return a.Error
	}
	return ""
}

type freshOpts struct {
	Margin  time.Duration
	Persist bool // false = the read-only / sandboxed fallback
	Force   bool
	// Mem, when non-nil, is the sandboxed process's in-memory session. Its expiry drives the
	// margin decision, but the credential PRESENTED is always the one in the file — which is what
	// keeps a non-persisting process from ever getting two steps ahead of the file and revoking
	// the family. Without this the sandboxed process would refresh every tick, because the file's
	// token stays inside the margin until some writer advances it.
	Mem *Session
}

// ensureFresh is the whole of the design under test for one process.
//
//	Persist=false is the read-only / sandboxed fallback: refresh in memory, use the new access token,
//	do not persist, leave the older refresh token in the file for the next writer to redeem one-behind.
func ensureFresh(ctx context.Context, env Env, p profile, cur Session, o freshOpts) (Session, refreshOutcome) {
	var out refreshOutcome

	lk, err := acquireLock(p.lockPath(), !o.Persist)
	if lk != nil {
		out.LockWait, out.LockTries = lk.Waited, lk.Attempts
	}
	if err != nil {
		out.LockErr = err.Error()
		return cur, out
	}
	defer lk.release()

	// Re-read under the lock: another process may have stored a fresh token while we waited.
	rr := readSession(p.sessionPath())
	if rr.OK {
		cur = rr.S
	}
	decide := cur
	if o.Mem != nil && o.Mem.AccessToken != "" {
		decide = *o.Mem
	}
	if !o.Force && remaining(decide) > o.Margin {
		out.Skipped = true
		out.Session = decide
		return decide, out
	}

	out.Did = true
	out.SentRT = tokID(cur.RefreshToken)
	t0 := time.Now()
	ns, r := refreshQuiet(ctx, env, env.PublishableKey, cur.RefreshToken)
	out.Elapsed = time.Since(t0)
	out.Status = r.Status
	if r.Status != 200 {
		out.ErrorCode = authErrorCode(r.Body)
		if out.ErrorCode == "" {
			out.ErrorCode = fmt.Sprintf("http_%d", r.Status)
		}
		return cur, out
	}
	out.GotRT, out.GotAT = tokID(ns.RefreshToken), tokID(ns.AccessToken)
	out.Session, out.Raw = ns, r.Body

	if !o.Persist {
		out.PersistErr = "not attempted (read-only profile)"
		return ns, out
	}
	if err := writeSession(p.sessionPath(), ns, r.Body); err != nil {
		out.PersistErr = err.Error()
		return ns, out // in-memory fallback: the command still runs with the new access token
	}
	out.Persisted = true
	return ns, out
}
