// state.go is the facts layer of the manager digest (card 162,
// .context/plans/manager-digest.md §5.3): the three hooks keep the
// session's state entry (internal/harness/state) current when the map
// names a state folder. SessionStart creates the entry — member, name,
// repository, host, the commit HEAD names and its branch — or refreshes
// the one the session already has; the prompt hook stamps the prompt
// and, when HEAD moved, lists the commits the session made since it
// started; SessionEnd records the end. Every git fact is read from
// git's own files, and the one `git log` runs only when HEAD moved,
// under a short budget, so the common prompt starts no process; a
// failure leaves the fields as they were.

package hook

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/gitfacts"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/state"
)

// The budgets of one git call in each hook: SessionStart has 55 s, the
// prompt hook 4.5 s shared with the poll, SessionEnd 1.4 s shared with
// the close.
const (
	stateGitBudgetStart  = 1500 * time.Millisecond
	stateGitBudgetPrompt = time.Second
	stateGitBudgetEnd    = 300 * time.Millisecond
)

// stateStore is the entry store of a session whose map names a state
// folder; false for one that shares no state.
func stateStore(f facts, m *sessionmap.ByPID) (state.Store, bool) {
	if m == nil || m.StateFolder == "" {
		return state.Store{}, false
	}
	return state.Store{StateDir: f.stateDir, Folder: m.StateFolder}, true
}

// stateStart creates or refreshes the entry at SessionStart, on the
// register and the continue path alike, then prunes this machine's
// expired entries (the writer prunes its own, plan §5.1). A session
// resumed after its end lives again: the end is cleared.
func (r *run) stateStart(ctx context.Context, f facts, opts config.Options, m *sessionmap.ByPID, now time.Time) {
	s, ok := stateStore(f, m)
	if !ok {
		return
	}
	e, err := s.Load(m.BrigadeSessionID)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.log.Debug("state: the canonical entry is unusable; a fresh one is written", log.Err(err))
		}
		e = r.newStateEntry(f, opts, m, now)
	} else {
		e.SessionName = m.SessionName
		e.EndedAt, e.EndReason = time.Time{}, ""
	}
	e.Inbound = m.Inbound
	e.Harness = string(f.host)
	r.stateRefreshGit(ctx, m, e, stateGitBudgetStart)
	e.UpdatedAt = now
	r.stateSave(s, e)
	if n := s.PruneOwn(now); n > 0 {
		r.log.Info("state: expired entries of this machine pruned", slog.Int("removed", n))
	}
}

// newStateEntry is a fresh entry for the session the map describes: the
// member's label as the registration sends it (the `label` option's
// resolution, read best effort), the repository's name the folder ids
// carry, else the workspace label, and HEAD as the start head.
func (r *run) newStateEntry(f facts, opts config.Options, m *sessionmap.ByPID, now time.Time) *state.Entry {
	e := state.New(m.BrigadeSessionID, m.SessionName, now)
	e.Member = r.humanLabel(f, opts)
	e.Repository = m.SyncScope
	if e.Repository == "" {
		e.Repository = m.WorkspaceLabel
	}
	hash, branch := gitfacts.Head(m.SyncRoot)
	e.StartHead, e.Head, e.Branch = hash, hash, branch
	return e
}

// statePrompt stamps the prompt and refreshes what may have moved: the
// session's name (the registry's, after a /rename), the inbound policy,
// HEAD and the commits. A session with no canonical entry — one that was
// running when the project gained its state folder, or whose entry was
// pruned — gets a fresh one here, as SessionStart would have written it.
func (r *run) statePrompt(ctx context.Context, f facts, m *sessionmap.ByPID, now time.Time) *state.Entry {
	s, ok := stateStore(f, m)
	if !ok {
		return nil
	}
	e, err := s.Load(m.BrigadeSessionID)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.log.Debug("state: the canonical entry is unusable; a fresh one is written", log.Err(err))
		}
		opts, oerr := r.options()
		if oerr != nil {
			r.log.Debug("state: options unreadable; no entry written", log.Err(oerr))
			return nil
		}
		e = r.newStateEntry(f, opts, m, now)
	}
	if name := registeredName(r.registryEntry(f).Name); name != "" {
		e.SessionName = name
	}
	// The parity ask (stop.go) judges the turn that ENDED at this prompt,
	// so it reads the previous prompt's stamp: the entry is handed over
	// as it was, the new stamp written after.
	judged := *e
	e.LastPromptAt = now
	e.Inbound = m.Inbound
	r.stateRefreshGit(ctx, m, e, stateGitBudgetPrompt)
	e.UpdatedAt = now
	r.stateSave(s, e)
	judged.Commits, judged.Head, judged.Branch = e.Commits, e.Head, e.Branch
	return &judged
}

// stateEnd records the session's end and its final commits. A session
// with no entry has nothing to end.
func (r *run) stateEnd(ctx context.Context, f facts, m *sessionmap.ByPID, reason string, now time.Time) {
	s, ok := stateStore(f, m)
	if !ok {
		return
	}
	e, err := s.Load(m.BrigadeSessionID)
	if err != nil {
		return
	}
	e.EndedAt, e.EndReason = now, reason
	e.Inbound = m.Inbound
	r.stateRefreshGit(ctx, m, e, stateGitBudgetEnd)
	e.UpdatedAt = now
	r.stateSave(s, e)
}

// stateRefreshGit reads HEAD and its branch from git's own files (no
// process) and, when HEAD moved since the entry's last write, lists the
// commits the session's author made since the start head through one
// `git log`, each call under budget. An unreadable HEAD, no git on PATH
// and a call that fails or times out all leave the entry's git facts as
// they were, and the next hook tries again.
func (r *run) stateRefreshGit(ctx context.Context, m *sessionmap.ByPID, e *state.Entry, budget time.Duration) {
	hash, branch := gitfacts.Head(m.SyncRoot)
	if hash == "" {
		return
	}
	e.Branch = branch
	if e.StartHead == "" {
		// The entry was created before the checkout had a commit: the
		// session's range starts here.
		e.StartHead = hash
	}
	if hash == e.Head {
		return
	}
	runner := gitfacts.Runner{Top: m.SyncRoot, Environ: r.environ, Timeout: budget}
	author, err := runner.AuthorEmail(ctx)
	if err != nil {
		r.log.Debug("state: the git author could not be read; commits not refreshed", log.Err(err))
		return
	}
	commits, err := runner.Log(ctx, e.StartHead, author)
	if err != nil {
		r.log.Debug("state: git log failed; commits not refreshed", log.Err(err))
		return
	}
	e.Commits = make([]state.Commit, 0, len(commits))
	for _, c := range commits {
		e.Commits = append(e.Commits, state.Commit{Hash: c.Hash, Subject: c.Subject, Card: c.Card, At: c.At})
	}
	e.Head = hash
}

// stateSave writes both copies; a failure is one log line, never a
// context line: the entry is housekeeping the model need not hear about.
func (r *run) stateSave(s state.Store, e *state.Entry) {
	if err := s.Save(e); err != nil {
		r.log.Warn("state: the session's entry was not written", log.Err(err))
	}
}
