package hook

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/harness/account"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/sound"
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
		r.zeroDoingStamp(f)
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

// zeroDoingStamp is the `source = compact` half of the doing reminder
// (card 25, plan 5.3): the summary may have dropped the full text, so a
// reminder stamp this pid holds is zeroed — its time, never its
// conversation id — and the next eligible prompt re-issues FULL. Every
// other SessionStart source leaves the stamp alone: the conversation id
// inside it is what makes a new conversation's first prompt say BLANK,
// and a SessionStart runs in the background, so removing the file here
// could race that first prompt (plan 5.4). A stamp that is not there, or
// not readable as one, stays as it is: the prompt hook reads either as
// missing.
func (r *run) zeroDoingStamp(f facts) {
	path := doingStampPath(f.stateDir, f.pid)
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.log.Debug("compact: doing stamp unreadable; left as it is", log.Err(err))
		}
		return
	}
	_, id, ok := strings.Cut(strings.TrimSpace(string(data)), " ")
	if !ok || id == "" {
		return
	}
	r.writeDoingStamp(path, time.Time{}, id)
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
	// workspaceLabel is the label the session registers (P11-5): the
	// user's own, else the repository name derived from the checkout;
	// "" sends nothing.
	workspaceLabel string
	// doingRules is what the permission rules Brigade can read say about
	// `brigade doing` (card 25, plan 5.2), scanned in resolve beside the
	// native scan; doingMode is the mode the map carries, set by connect —
	// from the adapter's capabilities, the option and doingRules on the
	// register path; on the continue path from the existing map's
	// capability verdict with the option and doingRules resolved again.
	doingRules policy.Verdict
	doingMode  string
	// sync is file sync as this SessionStart resolved it and the map
	// freezes it (folder-sync plan §4.3), zero when the session syncs
	// nothing; syncLine is the one line that says so after the context
	// line, "" when there is nothing to say.
	sync     frozenSync
	syncLine string
	// messageSound is the `message_sound` option as the map carries it
	// (card 35): on, and a player this machine has. soundLine is the one
	// line SessionStart prints when the option is on and no player is.
	messageSound bool
	soundLine    string
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
	res.workspaceLabel = workspaceLabel(res.opts, in.Cwd)
	existing := r.existingMap(res.store, f.pid)
	verdict := r.checkPidfile(f)
	now := r.deps.Now()

	// Idempotent per CLAUDE_PID (E0-8: SessionStart re-fires on /clear;
	// D9): a live watcher for this pid that serves the session the map
	// names means the session continues.
	if existing != nil && verdict.Found && verdict.Alive && verdict.Entry.BrigadeSessionID == existing.BrigadeSessionID {
		// The continue path has no describe, so the capability half of
		// the doing mode is the existing map's — unsupported stays
		// unsupported, absent stays absent until the prompt hook's
		// one-time resolution of P16-5 — and the option and the rules are
		// resolved again (plan 5.3): an opt-out takes effect at this
		// SessionStart, not the next session. Whether the heartbeat blanks
		// the doing line is decided HERE, where the document and the
		// existing map are both in view, and handed to heartbeat.
		res.doingMode = continuedDoingMode(existing.DoingMode, res.opts, res.doingRules)
		blank := blankDoingLine(in, existing, res.doingMode)
		reason := respawnReason(verdict.Entry, f)
		if reason == "" && !res.sync.frozenIn(existing) {
			// The watcher reads the sync members once, when it starts its
			// sync adapter (folder-sync plan §4.3): an edited `sync` member or
			// a flipped `sync` option takes effect through a new watcher.
			reason = "sync changed"
		}
		watcherRuns := true // the live watcher stays unless replaced
		if reason != "" {
			r.log.Info("watcher "+reason+"; respawning", slog.Int("watcher_pid", verdict.Entry.PID),
				slog.String("watcher_version", verdict.Entry.Version))
			r.stopWatcher(verdict.Entry, f)
			m := r.buildMap(f, in, res, existing.BrigadeSessionID, existing.TeamRef, existing.TeamName, existing.RegisteredAt, now)
			// The replacement reads the map once, when it starts: this
			// SessionStart's map is written first, as the register path
			// does, or a "sync changed" respawn would run the OLD sync
			// members while the line below announces the new ones.
			// finish() writes it again, harmlessly.
			if err := res.store.WriteByPID(m); err != nil {
				r.log.Warn("session map not written before the respawn", log.Err(err))
			}
			watcherRuns = r.spawnWatcher(ctx, f, m, pidfileWait)
		}
		hbErr := r.heartbeat(ctx, res, existing.BrigadeSessionID, blank)
		if hbErr == nil || !isCode(hbErr, protocol.CodeConflict, protocol.CodeNotFound) {
			if hbErr != nil {
				r.log.Warn("heartbeat failed; the watcher keeps trying", log.Err(hbErr))
			}
			m := r.buildMap(f, in, res, existing.BrigadeSessionID, existing.TeamRef, existing.TeamName, existing.RegisteredAt, now)
			r.finish(f, in, res, m, now, watcherRuns)
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
	// The doing mode is resolved from THIS describe (plan 5.2): no extra
	// spawn, because the register path also runs under the prompt hook's
	// 4.5 s retry budget (the P5-18 trap).
	res.doingMode = doingMode(res.opts, desc.Capabilities, res.doingRules)
	reg := &protocol.SessionRegistration{
		Harness:        harnessName,
		HarnessVersion: res.id.harnessVersion,
		SessionName:    res.id.name,
		Activity:       res.id.activity,
		Inbound:        res.dec.Policy.String(),
		BrigadeVersion: r.deps.BrigadeVersion(),
		SyncPeer:       r.deps.SyncPeer(),
		Resume:         r.resumeHint(f, in, res.store, existing),
	}
	if res.workspaceLabel != "" {
		label := res.workspaceLabel
		reg.WorkspaceLabel = &label
	}
	// Card 24, part C: the member's default label, for an adapter that
	// announces session.human_label to adopt into a membership that has
	// none. Absent when the `label` option is `none` or the account email
	// cannot be read — the fill is best effort and never a refusal.
	if label := r.humanLabel(res.opts); label != "" {
		reg.HumanLabel = &label
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
	spawned := r.spawnWatcher(ctx, f, m, pidfileWait)
	r.finish(f, in, res, m, now, spawned)
}

// respawnReason says why a live watcher serving this session must be
// replaced, or "" when it stays. "coordinates changed": the socket path or
// the token rotated, so the watcher holds stale coordinates and every
// later post would fail own-child verification (D9; cold in practice,
// E0-5 (c)). "version changed": the watcher was spawned by another Brigade
// version — the pidfile carries buildinfo.String() from 0.5.1, and one
// without it is a 0.5.0 or older watcher — and an updated plugin must
// replace it, or the session keeps running the old watcher until it ends
// (found on the 0.5.0 update: every running watcher stayed 0.4.1 and no
// session ever reported a model).
func respawnReason(e pidfile.Entry, f facts) string {
	if e.SocketPath != f.socket || e.TokenSHA256 != pidfile.TokenSHA256(f.token) {
		return "coordinates changed"
	}
	if e.Version != buildinfo.String() {
		return "version changed"
	}
	return ""
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
	doingRules := policy.ScanDoingRules(f.claudeConfigDir, doingScanDirs(r.environ, in), r.deps.ReadFile)
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
	sync, syncLine := r.resolveSync(opts, tf, in.Cwd)
	messageSound, soundLine := r.resolveSound(opts)
	return resolved{
		sync:         sync,
		syncLine:     syncLine,
		messageSound: messageSound,
		soundLine:    soundLine,
		opts:         opts,
		teamKey:      key,
		teamFile:     tf,
		adapter:      adapter,
		argv:         argv,
		id:           id,
		dec:          dec,
		instruction:  instruction,
		client:       r.client(adapter, key, opts.ConfigDir, f.stateDir),
		store:        sessionmap.Store{StateDir: f.stateDir},
		doingRules:   doingRules,
	}, true
}

// doingScanDirs names the directories whose project settings the doing
// scan reads (plan 5.2): CLAUDE_PROJECT_DIR from the hook's environment
// when it is absolute — the project root Claude Code itself reads its
// project settings from — and the document's cwd. The scan adds the
// repository toplevel of each.
func doingScanDirs(environ []string, in input) []string {
	var dirs []string
	if p := adapterkit.Getenv(environ, envProjectDir); filepath.IsAbs(p) {
		dirs = append(dirs, p)
	}
	return append(dirs, in.Cwd)
}

// doingMode resolves the by-pid map's `doing_mode` (plan 5.2, first
// match wins): unsupported when the adapter does not advertise
// `session.description`; off when the `share_doing` option is false;
// unasked when an ask or deny in the settings Brigade can read matches
// the verb, or a candidate file could not be read or parsed, or the
// Claude config directory is unresolved (Blocked); allowed when an allow
// entry exactly covers it; quiet otherwise. The verb publishes in every
// mode but the first two; the words decide only which lines the prompt
// hook may print (P16-5).
func doingMode(opts config.Options, capabilities []string, rules policy.Verdict) string {
	if !slices.Contains(capabilities, doingCapability) {
		return doing.ModeUnsupported
	}
	return doingModeWithin(opts, rules)
}

// doingModeWithin is the option-and-rules half of doingMode, for an
// adapter known to announce the capability.
func doingModeWithin(opts config.Options, rules policy.Verdict) string {
	switch {
	case !opts.ShareDoing:
		return doing.ModeOff
	case rules == policy.VerdictBlocked:
		return doing.ModeUnasked
	case rules == policy.VerdictAllowed:
		return doing.ModeAllowed
	default:
		return doing.ModeQuiet
	}
}

// continuedDoingMode re-resolves the map's doing_mode on the continue
// path, where no describe runs (plan 5.3): the capability verdict is the
// existing map's — unsupported stays unsupported, and an absent member (a
// map written before the mode existed) stays absent for the prompt hook's
// one-time resolution — while the option and the rules go through the
// same arms as a fresh resolution, so `share_doing` turned off, or a rule
// added since, lands at the next SessionStart of any kind but compact
// rather than at the next session. Without this the verb would keep
// publishing after the opt-out until the session ended.
func continuedDoingMode(existing string, opts config.Options, rules policy.Verdict) string {
	switch existing {
	case "", doing.ModeUnsupported:
		return existing
	}
	return doingModeWithin(opts, rules)
}

// blankDoingLine decides whether the continue path's heartbeat carries
// session_description "" — the wire form of "none" (plan 5.3). It does
// when the native session id changed, a real conversation switch (/clear,
// or an in-process /resume; a same-id re-fire such as /reload-plugins is
// not one), whatever the option says: a doing line lives inside one
// conversation (ruling 5). And it does, on a same-id re-fire too, when the
// existing map's mode published and the new resolution is off: opting out
// retracts at the next SessionStart of any kind but compact. Only when the
// mode is known and not unsupported: an adapter without the capability
// would refuse the member, and a map without the mode has not been
// resolved yet.
func blankDoingLine(in input, existing *sessionmap.ByPID, mode string) bool {
	if mode == "" || mode == doing.ModeUnsupported {
		return false
	}
	if in.SessionID != existing.ClaudeSessionID {
		return true
	}
	return mode == doing.ModeOff && existing.DoingMode != doing.ModeOff
}

// doingCapability is the 4.7 capability an adapter advertises when it
// stores and lists `session_description` (C-13, C-19).
const doingCapability = "session.description"

// writeStartFacts records what the hook knows before the team gates —
// the resolved config dir above all — so an in-session `team create` or
// `team join` in a not-yet-attached session writes to the same store the
// hooks read (P7-11). The `label` option rides along for the same reason
// and no other: plugin options never reach the Bash tool, so this is
// where an in-session join reads it (card 24, part B). The OPTION is
// written, never a label — the account email is read at the point of use,
// so this file never carries a member's email. Through sessionmap (the
// state directory), never the team store: the hook still cannot join.
func (r *run) writeStartFacts(f facts, in input, opts config.Options) {
	store := sessionmap.Store{StateDir: f.stateDir}
	err := store.WriteStart(&sessionmap.StartFacts{
		ClaudePID:       f.pid,
		ClaudeSessionID: in.SessionID,
		ConfigDir:       opts.ConfigDir,
		PluginBin:       f.pluginBin,
		LabelOption:     opts.Label,
		WrittenAt:       r.deps.Now(),
	})
	if err != nil {
		r.log.Warn("session-start: start facts not written", log.Err(err))
	}
}

// humanLabel is the member's DEFAULT display label, sent on every
// registration for an adapter that announces session.human_label to adopt
// into a membership that has none (card 24, part C). The `label` option
// decides, exactly as it does for a `team create` or `team join`: `none`
// sends nothing, a literal is that text, `account` — the default — is the
// Claude account email. Reading that email is best effort: a failure is a
// debug line without the value and an empty label, never a refusal, so a
// session whose account cannot be read registers as every session did
// before this version.
func (r *run) humanLabel(opts config.Options) string {
	return config.LabelFor(opts.Label, func() string {
		email, err := account.Email(r.environ)
		if err != nil {
			r.log.Debug("claude account email unavailable", log.Err(err))
		}
		return email
	})
}

// workspaceLabel is the label a session registers (P11-5): the user's
// own when the option names one, else the repository name derived from
// the checkout — the value that tells a session in one of a team's
// repositories from a session in the next, and that survives a /rename
// where the session name does not. "" when share_workspace_label is off
// or the cwd is in no repository: then nothing is sent.
func workspaceLabel(opts config.Options, cwd string) string {
	if !opts.ShareWorkspaceLabel {
		return ""
	}
	if opts.WorkspaceLabel != "" {
		return opts.WorkspaceLabel
	}
	top, ok := teamfile.Toplevel(cwd)
	if !ok {
		return ""
	}
	return teamfile.RepoName(top)
}

// frozenSync is file sync as SessionStart freezes it into the by-pid map
// (folder-sync plan §4.3): the adapter NAME, the folders exactly as the
// team file lists them, and the canonical repository toplevel they are
// relative to. The zero value syncs nothing.
type frozenSync struct {
	adapter string
	folders []string
	root    string
}

// frozenIn reports whether m already carries exactly this configuration:
// on the continue path a difference is a respawn reason, because the
// watcher reads these members once, when it starts its sync goroutine.
func (s frozenSync) frozenIn(m *sessionmap.ByPID) bool {
	return m.SyncAdapter == s.adapter && m.SyncRoot == s.root && slices.Equal(m.SyncFolders, s.folders)
}

// The reasons the SessionStart sync line gives for a session that syncs
// nothing although its team file declares a usable `sync` member.
const (
	syncOffOption     = "the sync option is off"
	syncOffNoFolders  = "no folders listed"
	syncOffUnresolved = "the checkout's toplevel could not be resolved"
	// File sync runs in the watcher: a session with none syncs nothing.
	syncOffNoSocket  = "no watcher runs without an inbox socket"
	syncOffNoWatcher = "the watcher has not started yet"
)

// resolveSync decides what file sync this session runs (folder-sync plan
// §4.3) and the one line that says so after the context line. A team file
// without a `sync` member says nothing — a project that syncs nothing
// must not be told so at every start — and one whose member is unusable
// has already earned card 32's line (syncUnusableLine), so it gets no
// second. Otherwise: the option off, or no folders, is a `file sync off`
// line naming why; a usable member with folders and the option on freezes
// the adapter's name, the folders and the canonical toplevel (the folders
// are relative to it, teamfile.SyncConfig) and says `file sync on`. Nothing
// here looks for the adapter on PATH: the watcher discovers it, and says
// so through its notice when it cannot (watch/sync.go).
func (r *run) resolveSync(opts config.Options, tf *teamfile.File, cwd string) (frozenSync, string) {
	switch {
	case tf == nil || tf.Sync == nil:
		return frozenSync{}, ""
	case !opts.Sync:
		return frozenSync{}, syncOffLine(syncOffOption)
	case len(tf.Sync.Folders) == 0:
		return frozenSync{}, syncOffLine(syncOffNoFolders)
	}
	root, ok := syncRoot(cwd)
	if !ok {
		r.log.Warn("session-start: sync root unresolved; file sync is off")
		return frozenSync{}, syncOffLine(syncOffUnresolved)
	}
	return frozenSync{adapter: tf.Sync.Adapter, folders: slices.Clone(tf.Sync.Folders), root: root},
		syncOnLine(len(tf.Sync.Folders), tf.Sync.Adapter)
}

// syncRoot is the canonical (symlink-free, clean, absolute) toplevel of
// the repository cwd is in — the root the team file's folders are
// relative to — or false. The map refuses anything else, and a refused
// map would cost the whole session its connection, not just its sync.
func syncRoot(cwd string) (string, bool) {
	top, ok := teamfile.Toplevel(cwd)
	if !ok {
		return "", false
	}
	root, err := teamfile.Canonicalize(top)
	if err != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", false
	}
	return root, true
}

// syncOnLine and syncOffLine are the one SessionStart sync line (§4.3).
// The adapter is a validated name and why is one of this file's fixed
// reasons; neither carries a folder, a path or a value from the option.
func syncOnLine(folders int, adapter string) string {
	return "Brigade: file sync on: " + strconv.Itoa(folders) + " folder(s) through " + attr(adapter) + "."
}

func syncOffLine(why string) string {
	return "Brigade: file sync off (" + why + ")."
}

// resolveSound decides whether this session's watcher plays a sound as a
// message arrives (card 35): the option on, and a player this machine
// has. Off says nothing — the default must not be announced at every
// start — and on with a player says nothing either: the sound speaks for
// itself. On without a player is one line naming why, so a member who
// set the option and hears nothing knows what to install. Nothing here
// runs a program; the reason is one of sound's fixed texts, never a path.
func (r *run) resolveSound(opts config.Options) (bool, string) {
	if !opts.MessageSound {
		return false, ""
	}
	if _, why := r.deps.SoundPlayer(pathValue(r.environ)); why != "" {
		return false, soundOffLine(why)
	}
	return true, ""
}

// soundOffLine is the one SessionStart line for a session whose
// `message_sound` option is on and whose machine cannot play (card 35).
func soundOffLine(why string) string {
	return "Brigade: message sound off (" + why + ")."
}

// soundPlayer is the production SoundPlayer: sound.Resolve over sound's
// own PATH search — the watcher's, which skips a relative entry — so the
// hook, probing from the project directory, answers as the watcher will.
func soundPlayer(pathVar string) ([]string, string) {
	return sound.Resolve(runtime.GOOS, func(name string) (string, bool) { return sound.LookPath(pathVar, name) }, sound.Exists)
}

// pathValue is the PATH value of environ, last occurrence winning.
func pathValue(environ []string) string {
	for i := len(environ) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(environ[i], "PATH="); ok {
			return v
		}
	}
	return ""
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
// session that continues (WatchRequestTimeout). With blankDoing it also
// carries session_description "" — how a conversation switch and an
// opt-out retract the doing line (card 25, plan 5.3). The verdict is
// connect's (blankDoingLine): this function sees neither the document nor
// the existing map, and the member is otherwise absent, never "".
func (r *run) heartbeat(ctx context.Context, res resolved, sessionID string, blankDoing bool) error {
	hctx, cancel := context.WithTimeout(ctx, adapterclient.WatchRequestTimeout)
	defer cancel()
	name, activity, inbound := res.id.name, res.id.activity, res.dec.Policy.String()
	// brigade_version rides this heartbeat too (C-46): a session that
	// continues is exactly the case a plugin update produces — the hook is
	// the first process of the new binary to reach the backend, before the
	// replaced watcher's first heartbeat — so the roster learns here.
	// sync_peer rides it when the hook knows one (C-47); absent, the stored
	// peer stands (4.4.4).
	hb := &protocol.HeartbeatRequest{
		Activity:       &activity,
		SessionName:    &name,
		Inbound:        &inbound,
		BrigadeVersion: r.deps.BrigadeVersion(),
		SyncPeer:       r.deps.SyncPeer(),
	}
	if blankDoing {
		none := ""
		hb.SessionDescription = &none
	}
	_, err := res.client.Heartbeat(hctx, sessionID, hb)
	// Logged after the answer, so the line reports a blank that landed,
	// not one that was asked for: a failed heartbeat leaves the old
	// sentence standing (plan 8), and nothing retries it.
	if err == nil && blankDoing {
		r.log.Debug("doing line blanked at session start")
	}
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
// carries the RESOLVED profile, config dir, adapter command, frame
// instruction (the level, and the folded text only under custom) and
// doing mode (one of five words, card 25), never a raw option, never the
// frame_file path, never a settings rule, and never the token. The
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
		WorkspaceLabel:   res.workspaceLabel,
		LabelOption:      res.opts.Label,
		DoingMode:        res.doingMode,
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
		SyncAdapter:      res.sync.adapter,
		SyncFolders:      res.sync.folders,
		SyncRoot:         res.sync.root,
		MessageSound:     res.messageSound,
		HarnessVersion:   res.id.harnessVersion,
		RegisteredAt:     registeredAt,
		UpdatedAt:        now,
	}
}

// finish writes the maps (the by-pid map is rewritten on every
// SessionStart), runs the shadowing check and the weekly prune, and prints
// the context line with the policy warnings and the team-file notes.
// watcherRuns says whether a watcher serves the session now — spawned by
// this SessionStart, or live and kept — because file sync runs in the
// watcher: without one the sync line says off, whatever was resolved.
func (r *run) finish(f facts, in input, res resolved, m *sessionmap.ByPID, now time.Time, watcherRuns bool) {
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
	// Card 32: what the opened team file ignored, and a sync member it
	// could not use — both after consent only, and neither stops the
	// session connecting.
	warnings = append(warnings, teamFileNotes(res.teamFile)...)
	r.pruneCache(f, now)
	r.say(startLine(m.SessionName, m.BrigadeSessionID, m.TeamName, m.Inbound))
	if line := res.syncLine; line != "" {
		if res.sync.adapter != "" && !watcherRuns {
			line = syncOffLine(syncOffNoWatcher)
			if f.socket == "" && r.deps.Sink == "" {
				line = syncOffLine(syncOffNoSocket)
			}
		}
		r.say(line)
	}
	// The sound plays in the watcher, as file sync runs there: a session
	// with none says so with the same reasons (card 35).
	switch {
	case res.messageSound && !watcherRuns && f.socket == "" && r.deps.Sink == "":
		r.say(soundOffLine(syncOffNoSocket))
	case res.messageSound && !watcherRuns:
		r.say(soundOffLine(syncOffNoWatcher))
	case res.soundLine != "":
		r.say(res.soundLine)
	}
	if res.opts.SyncWarning != "" {
		warnings = append(warnings, res.opts.SyncWarning)
	}
	if res.opts.MessageSoundWarning != "" {
		warnings = append(warnings, res.opts.MessageSoundWarning)
	}
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
	found, ok := r.deps.LookPath(pathValue(r.environ), "brigade")
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
