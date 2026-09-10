package hook

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
)

// sessionStart is `brigade hook session-start` (6.3, 6.5, 6.10, D36, D9).
func (r *run) sessionStart() int {
	in, err := r.readInput()
	if err != nil {
		r.fail("session-start: bad stdin", err, notConnected(protocol.CodeConfig))
		return 0
	}
	f, err := r.facts()
	if err != nil {
		code, _ := codeOf(err)
		r.fail("session-start: session facts", err, notConnected(code))
		return 0
	}
	if in.Source == sourceCompact {
		// /compact re-fires SessionStart in the same process with the
		// same native id: refresh the map, no network.
		r.refreshMap(f, in)
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), startBudget)
	defer cancel()
	r.connect(ctx, f, in, r.deps.PidfileWait)
	return 0
}

// refreshMap is the `source = compact` path: the session name, the
// permission mode, the transcript path and updated_at are refreshed in the
// by-pid map, nothing else happens — the frame members in particular are
// left as SessionStart froze them, so a mid-session edit of a frame_file
// cannot take effect on /compact (P5-12).
func (r *run) refreshMap(f facts, in input) {
	store := sessionmap.Store{StateDir: f.stateDir}
	m, err := store.ReadByPID(f.pid)
	if err != nil {
		r.log.Debug("compact: no usable session map to refresh", log.Err(err))
		return
	}
	id := r.identity(f, in)
	m.SessionName = id.name
	if in.PermissionMode != "" {
		m.PermissionMode = in.PermissionMode
	}
	if p := transcriptPath(in); p != "" {
		m.TranscriptPath = p
	}
	m.UpdatedAt = r.deps.Now()
	if err := store.WriteByPID(m); err != nil {
		r.log.Warn("compact: session map not refreshed", log.Err(err))
	}
}

// resolved is everything the register/heartbeat paths share.
type resolved struct {
	opts        config.Options
	teamKey     string
	teamFile    *teamfile.File
	adapter     config.Adapter
	argv        []string
	id          identity
	dec         policy.Decision
	instruction frame.Instruction
	client      *adapterclient.Client
	store       sessionmap.Store
}

// connect registers (or re-attaches to) the Brigade session and prints
// the context line. pidfileWait bounds the wait for the watcher's pidfile;
// the prompt hook's retry passes a shorter one. Every failure prints a
// `not connected` line and returns; the exit status is the caller's 0.
func (r *run) connect(ctx context.Context, f facts, in input, pidfileWait time.Duration) {
	res, ok := r.resolve(f, in)
	if !ok {
		return
	}
	existing := r.existingMap(res.store, f.pid)
	verdict := r.checkPidfile(f)
	now := r.deps.Now()

	// Idempotent per CLAUDE_PID (E0-8: SessionStart re-fires on /clear;
	// D9): a live watcher for this pid that serves the session the map
	// names means the session continues.
	if existing != nil && verdict.Found && verdict.Alive && verdict.Entry.BrigadeSessionID == existing.BrigadeSessionID {
		if verdict.Entry.SocketPath != f.socket || verdict.Entry.TokenSHA256 != pidfile.TokenSHA256(f.token) {
			// The socket path or the token rotated: the watcher holds
			// stale coordinates and every later post would fail
			// own-child verification (D9). Cold in practice (E0-5 (c)).
			r.log.Info("watcher coordinates changed; respawning", slog.Int("watcher_pid", verdict.Entry.PID))
			r.stopWatcher(verdict.Entry, f)
			m := r.buildMap(f, in, res, existing.BrigadeSessionID, existing.TeamRef, existing.TeamName, existing.RegisteredAt, now)
			r.spawnWatcher(ctx, f, m, pidfileWait)
		}
		hbErr := r.heartbeat(ctx, res, existing.BrigadeSessionID)
		if hbErr == nil || !isCode(hbErr, protocol.CodeConflict, protocol.CodeNotFound) {
			if hbErr != nil {
				r.log.Warn("heartbeat failed; the watcher keeps trying", log.Err(hbErr))
			}
			m := r.buildMap(f, in, res, existing.BrigadeSessionID, existing.TeamRef, existing.TeamName, existing.RegisteredAt, now)
			r.finish(f, in, res, m, now)
			return
		}
		// The server no longer has the session (closed or expired and
		// gone): retire the watcher and register afresh.
		r.log.Info("session is gone server-side; registering again", log.Err(hbErr))
		r.stopWatcher(verdict.Entry, f)
		verdict = pidfile.Verdict{}
	}
	if verdict.Found && verdict.Alive {
		// A live watcher serving another session for this pid (the map
		// was replaced): it must not keep draining the old inbox.
		r.log.Info("retiring a watcher of another session", slog.Int("watcher_pid", verdict.Entry.PID))
		r.stopWatcher(verdict.Entry, f)
	}

	dctx, dcancel := context.WithTimeout(ctx, adapterclient.DescribeTimeout)
	desc, err := res.client.Describe(dctx)
	dcancel()
	if err != nil {
		r.fail("session-start: describe", err, notConnectedFor(err))
		return
	}
	reg := &protocol.SessionRegistration{
		Harness:        harnessName,
		HarnessVersion: res.id.harnessVersion,
		SessionName:    res.id.name,
		Activity:       res.id.activity,
		Inbound:        res.dec.Policy.String(),
		Resume:         r.resumeHint(f, in, res.store, existing),
	}
	if res.opts.ShareWorkspaceLabel && res.opts.WorkspaceLabel != "" {
		label := res.opts.WorkspaceLabel
		reg.WorkspaceLabel = &label
	}
	result, err := r.register(ctx, res.client, reg)
	if err != nil {
		r.fail("session-start: register", err, notConnectedFor(err))
		return
	}
	m := r.buildMap(f, in, res, result.SessionID, desc.Profile.TeamRef, desc.Profile.TeamName, now, now)
	if existing != nil && existing.BrigadeSessionID == m.BrigadeSessionID && !existing.RegisteredAt.IsZero() {
		m.RegisteredAt = existing.RegisteredAt
	}
	if err := res.store.WriteByPID(m); err != nil {
		r.fail("session-start: session map not written", err, notConnected(protocol.CodeConfig))
		return
	}
	r.spawnWatcher(ctx, f, m, pidfileWait)
	r.finish(f, in, res, m, now)
}

// resolve is the attach-only resolution of P7-6 (brief §4): (1) the
// project's team file is discovered — none, or no repository at all,
// means Brigade stays OFF, silently (DEBUG only; a repo without Brigade
// must not nag); (2) the per-checkout pin must exist and match the file
// (a human consented HERE, and the file has not been re-pointed since);
// (3) the binding must exist and match the file field-by-field (the key
// is never trusted alone); (4) only then is the adapter resolved — by
// the file's NAME, strictly user-side — and the frozen chain runs with
// the team key as the profile. The hook can not join: no code path here
// transmits a secret or writes the store, and the refusal lines echo
// nothing from a mismatched file.
func (r *run) resolve(f facts, in input) (resolved, bool) {
	opts, err := config.ParseOptions(r.environ)
	if err != nil {
		r.fail("session-start: options", err, optionsLine(err))
		return resolved{}, false
	}
	// The start facts go down BEFORE the team gates (P7-11): joined or not,
	// a command run inside this session can then find the store the hooks
	// use. Best effort — a session that cannot write its state directory
	// fails at the map write below with its own line.
	r.writeStartFacts(f, in, opts)
	tf, key, ok := r.resolveTeam(opts, in)
	if !ok {
		return resolved{}, false
	}
	adapter, err := config.ResolveAdapter(opts, opts.ConfigDir, tf.Adapter)
	if err != nil {
		r.fail("session-start: adapter resolution", err, adapterLine(tf.Adapter, err))
		return resolved{}, false
	}
	argv, err := adapterArgv(adapter)
	if err != nil {
		r.fail("session-start: adapter command", err, adapterLine(tf.Adapter, err))
		return resolved{}, false
	}
	id := r.identity(f, in)
	scan := policy.ScanNative(f.claudeConfigDir, in.Cwd, r.deps.ReadFile)
	dec := policy.Decide(policy.Inputs{
		Option:         opts.TeamInbound,
		OptionWarning:  opts.TeamInboundWarning,
		Native:         scan,
		PermissionMode: in.PermissionMode,
		NonInteractive: id.nonInteractive,
		Entrypoint:     id.entrypoint,
	})
	instruction, err := r.frameInstruction(opts)
	if err != nil {
		r.fail("session-start: frame text", err, frameLine(err))
		return resolved{}, false
	}
	return resolved{
		opts:        opts,
		teamKey:     key,
		teamFile:    tf,
		adapter:     adapter,
		argv:        argv,
		id:          id,
		dec:         dec,
		instruction: instruction,
		client:      r.client(adapter, key, opts.ConfigDir, f.stateDir),
		store:       sessionmap.Store{StateDir: f.stateDir},
	}, true
}

// writeStartFacts records what the hook knows before the team gates —
// the resolved config dir above all — so an in-session `team create` or
// `team join` in a not-yet-attached session writes to the same store the
// hooks read (P7-11). Through sessionmap (the state directory), never the
// team store: the hook still cannot join.
func (r *run) writeStartFacts(f facts, in input, opts config.Options) {
	store := sessionmap.Store{StateDir: f.stateDir}
	err := store.WriteStart(&sessionmap.StartFacts{
		ClaudePID:       f.pid,
		ClaudeSessionID: in.SessionID,
		ConfigDir:       opts.ConfigDir,
		PluginBin:       f.pluginBin,
		WrittenAt:       r.deps.Now(),
	})
	if err != nil {
		r.log.Warn("session-start: start facts not written", log.Err(err))
	}
}

// resolveTeam runs steps 1-3: discovery, the pin gate, the binding gate.
// A false return means the session does not attach; whether a line was
// printed depends on which gate said no (no file at all says nothing).
func (r *run) resolveTeam(opts config.Options, in input) (*teamfile.File, string, bool) {
	path, found := teamfile.Discover(in.Cwd)
	if !found {
		r.log.Debug("no team file discovered; Brigade stays off")
		return nil, "", false
	}
	tf, err := teamfile.Parse(path)
	if err != nil {
		r.fail("session-start: team file", err, teamFileLine(err))
		return nil, "", false
	}
	canon, err := teamfile.Canonicalize(filepath.Dir(path))
	if err != nil {
		r.fail("session-start: canonicalize checkout", err, notConnected(protocol.CodeConfig))
		return nil, "", false
	}
	pin, pinned, err := teamstore.LookupPin(opts.ConfigDir, canon)
	if err != nil {
		r.fail("session-start: pin store", err, notConnectedFor(err))
		return nil, "", false
	}
	if !pinned {
		r.log.Debug("no pin for this checkout", slog.String("canonical", canon))
		r.say(notJoinedLine(tf.TeamName))
		return nil, "", false
	}
	if !pin.Matches(tf) {
		// The drift line deliberately echoes NOTHING from the file: the
		// file in front of us is unconsented input until a human reviews
		// the change in a terminal.
		r.say(driftLine)
		return nil, "", false
	}
	key := teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef)
	binding, err := teamstore.LoadBinding(opts.ConfigDir, key)
	if err != nil {
		r.log.Debug("no binding for the pinned team", log.Err(err))
		r.say(notJoinedLine(tf.TeamName))
		return nil, "", false
	}
	if err := teamstore.VerifyBinding(binding, tf); err != nil {
		r.fail("session-start: binding mismatch", err, notJoinedLine(tf.TeamName))
		return nil, "", false
	}
	return tf, key, true
}

// frameInstruction resolves the frame's instruction paragraph (P5-12): the
// named level of the frame option, or — when frame_file is set, which wins
// — the user's own clause read ONCE, here, through r.deps.ReadFile, folded
// to one line and checked. The result is frozen into the by-pid map; the
// watcher and the prompt-hook poll never re-read the file, so a file
// swapped after SessionStart changes nothing until the next SessionStart
// (a new session, /clear, /reload-plugins). Every failure is `config` with
// its own details.reason; the path is never part of any error.
func (r *run) frameInstruction(opts config.Options) (frame.Instruction, error) {
	if opts.FrameFile == "" {
		return frame.Instruction{Level: opts.Frame}, nil
	}
	folded, err := readClause(opts.FrameFile, r.deps.ReadFile)
	if err != nil {
		return frame.Instruction{}, err
	}
	return frame.Instruction{Level: frame.LevelCustom, Custom: folded}, nil
}

// readClause applies rules 3-7 of the P5-12 brief to a frame_file: an
// existing regular file (a directory, a FIFO or a device is refused before
// any open, so nothing can block the hook), within frame.MaxCustomBytes
// (checked on the size before the read and on the bytes after it), valid
// UTF-8, then frame.FoldClause and frame.CheckClause. A symlink is
// followed: the path is the user's own setting.
func readClause(path string, readFile func(string) ([]byte, error)) (string, error) {
	fi, err := os.Stat(path)
	switch {
	case err != nil:
		return "", frameFileErr(frame.ReasonFileUnreadable, "frame_file does not exist or cannot be read")
	case !fi.Mode().IsRegular():
		return "", frameFileErr(frame.ReasonFileUnreadable, "frame_file is not a regular file")
	case fi.Size() > frame.MaxCustomBytes:
		return "", frameFileErr(frame.ReasonFileTooLarge, "frame_file must be at most 4096 bytes")
	}
	data, err := readFile(path)
	if err != nil {
		return "", frameFileErr(frame.ReasonFileUnreadable, "frame_file does not exist or cannot be read")
	}
	if len(data) > frame.MaxCustomBytes {
		return "", frameFileErr(frame.ReasonFileTooLarge, "frame_file must be at most 4096 bytes")
	}
	if !utf8.Valid(data) {
		return "", frameFileErr(frame.ReasonFileNotUTF8, "frame_file must be UTF-8 text")
	}
	folded := frame.FoldClause(string(data))
	if err := frame.CheckClause(folded); err != nil {
		return "", err
	}
	return folded, nil
}

// frameFileErr is the `config` failure for a frame_file the hook could not
// use; fixed text, never the path.
func frameFileErr(reason, message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: message,
		Details: map[string]string{"option": "frame_file", "reason": reason},
	}
}

// adapterArgv is the map's adapter_command: the resolved argv prefix, or
// [] for the bundled adapter (Adapter.Encode's array, kept as a slice).
func adapterArgv(a config.Adapter) ([]string, error) {
	if a.Bundled {
		return []string{}, nil
	}
	if err := sessionmap.CheckAdapterCommand(a.Argv); err != nil {
		return nil, err
	}
	return append([]string{}, a.Argv...), nil
}

// existingMap reads this pid's by-pid map; a missing or refused map is
// nil (a refused one is overwritten by the registration that follows, so
// a planted map never survives a SessionStart).
func (r *run) existingMap(store sessionmap.Store, pid int) *sessionmap.ByPID {
	m, err := store.ReadByPID(pid)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.log.Warn("existing session map refused; it will be replaced", log.Err(err))
		}
		return nil
	}
	return m
}

// checkPidfile judges this pid's watcher pidfile; an unreadable file is
// logged and treated as none (the watcher's own guard decides what to do
// with it).
func (r *run) checkPidfile(f facts) pidfile.Verdict {
	v, err := pidfile.Check(pidfile.Path(f.stateDir, f.pid), r.deps.Lookup)
	if err != nil {
		r.log.Warn("watcher pidfile unreadable", log.Err(err))
		return pidfile.Verdict{}
	}
	return v
}

// heartbeat sends the current name, activity and inbound policy for a
// session that continues (WatchRequestTimeout).
func (r *run) heartbeat(ctx context.Context, res resolved, sessionID string) error {
	hctx, cancel := context.WithTimeout(ctx, adapterclient.WatchRequestTimeout)
	defer cancel()
	name, activity, inbound := res.id.name, res.id.activity, res.dec.Policy.String()
	_, err := res.client.Heartbeat(hctx, sessionID, &protocol.HeartbeatRequest{
		Activity:    &activity,
		SessionName: &name,
		Inbound:     &inbound,
	})
	return err
}

// register runs `session register` (RegisterTimeout) and, when the resume
// hint is refused with not_found or conflict (session_live, 4.5.8),
// registers again without it.
func (r *run) register(ctx context.Context, c *adapterclient.Client, reg *protocol.SessionRegistration) (*adapterclient.RegisterResult, error) {
	rctx, cancel := context.WithTimeout(ctx, adapterclient.RegisterTimeout)
	result, err := c.Register(rctx, reg)
	cancel()
	if err == nil || reg.Resume == nil || !isCode(err, protocol.CodeNotFound, protocol.CodeConflict) {
		return result, err
	}
	r.log.Info("resume hint refused; registering a fresh session", log.Err(err))
	reg.Resume = nil
	rctx, cancel = context.WithTimeout(ctx, adapterclient.RegisterTimeout)
	defer cancel()
	return c.Register(rctx, reg)
}

// resumeHint is the `resume.session_id` of the registration (3.7, E0-5
// (f)): the by-native entry for this native id (any source), else the
// session this pid's own map named (its watcher died, or the server said
// the session was gone); skipped when a live pidfile of ANOTHER pid names
// that session, because the original process is still running.
func (r *run) resumeHint(f facts, in input, store sessionmap.Store, existing *sessionmap.ByPID) *protocol.ResumeRef {
	hint := ""
	if bn, err := store.ReadByNative(in.SessionID); err == nil && bn.BrigadeSessionID != "" {
		hint = bn.BrigadeSessionID
	} else if existing != nil {
		hint = existing.BrigadeSessionID
	}
	if hint == "" {
		return nil
	}
	if other, ok := r.otherLiveWatcher(f, hint); ok {
		r.log.Info("resume hint skipped: another live watcher serves that session", slog.Int("watcher_pid", other))
		return nil
	}
	return &protocol.ResumeRef{SessionID: hint}
}

// otherLiveWatcher reports whether a live pidfile of a pid other than f.pid
// names sessionID, and which watcher pid holds it.
func (r *run) otherLiveWatcher(f facts, sessionID string) (int, bool) {
	dir := filepath.Dir(pidfile.Path(f.stateDir, f.pid))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, false
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		pid, perr := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if perr != nil || pid <= 0 || pid == f.pid {
			continue
		}
		v, cerr := pidfile.Check(filepath.Join(dir, name), r.deps.Lookup)
		if cerr == nil && v.Found && v.Alive && v.Entry.BrigadeSessionID == sessionID {
			return v.Entry.PID, true
		}
	}
	return 0, false
}

// buildMap assembles the by-pid map from the resolved values (3.2). It
// carries the RESOLVED profile, config dir, adapter command and frame
// instruction (the level, and the folded text only under custom), never a
// raw option, never the frame_file path, and never the token. The
// transcript path is the document's own (absolute, else none): /clear
// re-fires SessionStart with a new native session and a new transcript,
// and this rewrite is how the watcher learns of it.
func (r *run) buildMap(f facts, in input, res resolved, sessionID, teamRef, teamName string, registeredAt, now time.Time) *sessionmap.ByPID {
	return &sessionmap.ByPID{
		ClaudePID:        f.pid,
		ClaudeSessionID:  in.SessionID,
		BrigadeSessionID: sessionID,
		TeamRef:          teamRef,
		TeamName:         teamName,
		SessionName:      res.id.name,
		PermissionMode:   in.PermissionMode,
		NonInteractive:   res.id.nonInteractive,
		Inbound:          res.dec.Policy.String(),
		FrameLevel:       string(res.instruction.Level),
		FrameText:        res.instruction.Custom,
		SocketPath:       f.socket,
		TranscriptPath:   transcriptPath(in),
		TeamKey:          res.teamKey,
		ConfigDir:        res.opts.ConfigDir,
		AdapterCommand:   res.argv,
		PluginBin:        f.pluginBin,
		HarnessVersion:   res.id.harnessVersion,
		RegisteredAt:     registeredAt,
		UpdatedAt:        now,
	}
}

// finish writes the maps (the by-pid map is rewritten on every
// SessionStart), runs the shadowing check and the weekly prune, and prints
// the context line with the policy warnings.
func (r *run) finish(f facts, in input, res resolved, m *sessionmap.ByPID, now time.Time) {
	if err := res.store.WriteByPID(m); err != nil {
		r.fail("session-start: session map not written", err, notConnected(protocol.CodeConfig))
		return
	}
	r.writeByNative(res.store, in.SessionID, m, now)
	warnings := append([]string{}, res.dec.Warnings...)
	if res.opts.FrameWarning != "" {
		warnings = append(warnings, res.opts.FrameWarning)
	}
	if path, ok := r.shadowing(f); ok {
		warnings = append(warnings, shadowLine(path))
	}
	r.pruneCache(f, now)
	r.say(startLine(m.SessionName, m.BrigadeSessionID, m.TeamName, m.Inbound))
	for _, w := range warnings {
		r.say(w)
	}
}

// writeByNative records the resume hint for this native id, overwriting
// any previous entry (a native id recurs, E0-5 item 6). An id that is not
// a safe path component is skipped with a log line.
func (r *run) writeByNative(store sessionmap.Store, nativeID string, m *sessionmap.ByPID, now time.Time) {
	if nativeID == "" {
		return
	}
	err := store.WriteByNative(nativeID, &sessionmap.ByNative{
		BrigadeSessionID: m.BrigadeSessionID,
		TeamRef:          m.TeamRef,
		SessionName:      m.SessionName,
		UpdatedAt:        now,
	})
	if err != nil {
		r.log.Warn("by-native map not written", log.Err(err))
	}
}

// shadowing reports a `brigade` on the hook's own PATH that is not the
// plugin's bootstrap (E0-8 (e): the plugin's bin/ is appended LAST to the
// Bash tool's PATH, so any other one wins there).
func (r *run) shadowing(f facts) (string, bool) {
	pathVar := ""
	for i := len(r.environ) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(r.environ[i], "PATH="); ok {
			pathVar = v
			break
		}
	}
	found, ok := r.deps.LookPath(pathVar, "brigade")
	if !ok {
		return "", false
	}
	// Fail closed when the plugin's own path is unknown (CLAUDE_PLUGIN_ROOT
	// unset or relative): without it the guard below cannot tell the
	// plugin's bootstrap from a shadow, and a warning that names the
	// plugin's bin/brigade would put the absolute path back in front of the
	// model — the form P5-13 removed from the context line (F1).
	if f.pluginBin == "" || samePath(found, f.pluginBin) {
		return "", false
	}
	return found, true
}

// samePath reports whether two paths name the same file after symlinks.
func samePath(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = filepath.Clean(a)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = filepath.Clean(b)
	}
	return ra == rb
}

// lookPath searches pathVar (a PATH value) for an executable regular file
// named name, exactly as a shell would, and returns the first hit.
func lookPath(pathVar, name string) (string, bool) {
	for _, dir := range filepath.SplitList(pathVar) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		if abs, aerr := filepath.Abs(p); aerr == nil {
			p = abs
		}
		return p, true
	}
	return "", false
}
