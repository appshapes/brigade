package watch

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/foldersync"
	"github.com/appshapes/brigade/internal/protocol"
)

// DefaultSyncInterval is how often the sync goroutine reads the roster
// and re-applies the folders and peers (folder-sync plan §4.3): a
// teammate who starts a session is introduced within a minute, and
// `apply` is idempotent, so a quiet minute costs one list and one apply.
const DefaultSyncInterval = 60 * time.Second

// syncNoticeMessageChars caps how much of an adapter's own error message
// the one notice line carries: enough for "syncthing is not on PATH",
// short enough that a chatty adapter cannot fill the next prompt's
// context.
const syncNoticeMessageChars = 160

// syncSetup is the file-sync configuration the hook froze into the by-pid
// map (folder-sync plan §4.3), read once when the watcher starts: the
// hook respawns the watcher when any of it changes (hook/start.go,
// "sync changed"). The zero value (no adapter) runs no sync goroutine.
type syncSetup struct {
	adapter   string
	folders   []string
	root      string
	pluginBin string
}

// enabled reports whether the map froze a sync configuration.
func (s syncSetup) enabled() bool { return s.adapter != "" }

// runSync is the sync goroutine (folder-sync plan §4.3), started beside
// the injector when the map carries sync members and ended by ctx once
// the session's own exit path has run. It drives the sync adapter through
// its verbs:
//
//   - describe: an adapter that cannot be spawned or answers an error
//     earns ONE notice line (the next prompt prints it) and the goroutine
//     ends — the session goes on without file sync;
//   - attach: the engine runs on this machine with this session counted,
//     and its descriptor becomes this session's sync_peer, heartbeated at
//     the next liveness tick so the roster carries it at once;
//   - apply, immediately and then every syncInterval: the project's
//     folders and every teammate's peer the roster lists for the same
//     repository, whatever its state — an offline peer is still worth
//     introducing, the engine connects when it can. `attach` is repeated
//     before each apply (it is idempotent) so a reference a replaced
//     watcher's late `detach` dropped is restored within one interval;
//   - detach, on the way out: the engine stops with the last session.
//
// The adapter's results are logged as scalars only — counts and states,
// never a body — and a changed summary is one notice line.
func (w *watcher) runSync(ctx context.Context) {
	client := &foldersync.Client{
		Adapter:   w.sync.adapter,
		PluginBin: w.sync.pluginBin,
		StateDir:  w.rc.env.StateDir,
		Environ:   w.environ,
		Logger:    w.log,
		Command:   w.deps.SyncCommand,
	}
	desc, err := client.Describe(ctx)
	if err != nil {
		w.syncUnavailable(err)
		return
	}
	w.log.Info("sync adapter described",
		slog.String("sync_adapter", w.sync.adapter), slog.String("name", protocol.SanitizeAttribute(desc.Name)),
		slog.String("version", protocol.SanitizeAttribute(desc.Version)), slog.Int("folders", len(w.sync.folders)))
	if err := w.syncAttach(ctx, client); err != nil {
		w.syncUnavailable(err)
		return
	}
	defer w.syncDetach(client)
	tick := time.NewTicker(w.deps.SyncInterval)
	defer tick.Stop()
	lastSummary := ""
	for {
		if summary := w.syncApply(ctx, client); summary != "" && summary != lastSummary {
			lastSummary = summary
			w.writeNotice(summary)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if err := w.syncAttach(ctx, client); err != nil && ctx.Err() == nil {
			w.log.Warn("sync attach failed; the last peer stands", slog.String("code", string(codeOf(err))), adlog.Err(err))
		}
	}
}

// syncAttach runs `attach` and publishes the descriptor: stored into
// syncPeer with the adapter's prefix (the wire form, §4.3), and — when it
// is new — heartbeated at the next liveness tick rather than the next
// heartbeat interval, so teammates' next `apply` can introduce it.
func (w *watcher) syncAttach(ctx context.Context, client *foldersync.Client) error {
	res, err := client.Attach(ctx, w.sessionID)
	if err != nil {
		return err
	}
	wire := foldersync.WirePeer(w.sync.adapter, res.Peer)
	if old := w.syncPeer.Load(); old == nil || *old != wire {
		w.syncPeer.Store(&wire)
		w.state.heartbeatSoon()
		w.log.Info("sync attached; the next heartbeat carries the peer", slog.String("sync_adapter", w.sync.adapter))
	}
	return nil
}

// syncApply is one round: the roster, then `apply` with the folders and
// the teammates' peers. It returns the summary line for the notice, or ""
// when the round did not complete (logged; the next round tries again).
func (w *watcher) syncApply(ctx context.Context, client *foldersync.Client) string {
	own := ""
	if p := w.syncPeer.Load(); p != nil {
		own, _ = foldersync.PeerFor(w.sync.adapter, *p)
	}
	peers, err := w.syncPeers(ctx, own)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("sync: the roster could not be read; apply skipped this round",
				slog.String("code", string(codeOf(err))), adlog.Err(err))
		}
		return ""
	}
	label := w.state.snapshot().workspaceLabel
	folders := foldersync.Folders(w.teamRef, w.sync.root, label, w.sync.folders)
	res, err := client.Apply(ctx, w.sessionID, folders, peers)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("sync apply failed", slog.String("code", string(codeOf(err))), adlog.Err(err))
		}
		return ""
	}
	connected := 0
	for _, p := range res.Peers {
		if p.Connected {
			connected++
		}
	}
	states := make([]string, 0, len(res.Folders))
	for _, f := range res.Folders {
		states = append(states, protocol.SanitizeAttribute(f.State))
	}
	w.log.Info("sync applied",
		slog.Int("folders", len(res.Folders)), slog.Int("peers_offered", len(peers)),
		slog.Int("peers", len(res.Peers)), slog.Int("connected", connected),
		slog.String("folder_states", strings.Join(states, ",")))
	return "Brigade sync: " + strconv.Itoa(len(res.Folders)) + " folders, " +
		strconv.Itoa(connected) + " of " + strconv.Itoa(len(res.Peers)) + " peers connected"
}

// syncPeers reads the roster through the watcher's own backend client
// (offline sessions included) and keeps the teammates' peers this adapter
// can use: not this session, the same workspace_label — the same
// repository, so two repositories of one team never share a folder id —
// and a sync_peer carrying THIS adapter's prefix, which is stripped. A
// descriptor is offered once, and never this machine's own (another
// session here shares its engine). A session that shares no workspace
// label cannot tell its repository's sessions from the rest, so it
// offers no peer at all.
func (w *watcher) syncPeers(ctx context.Context, own string) ([]foldersync.Peer, error) {
	lctx, cancel := context.WithTimeout(ctx, adapterclient.DefaultTimeout)
	defer cancel()
	list, err := w.client.ListSessions(lctx, true)
	if err != nil {
		return nil, err
	}
	label := w.state.snapshot().workspaceLabel
	peers := []foldersync.Peer{}
	if label == "" {
		return peers, nil
	}
	seen := map[string]bool{own: true}
	for _, rec := range list.Sessions {
		if rec.IsSelf || rec.WorkspaceLabel == nil || *rec.WorkspaceLabel != label || rec.SyncPeer == nil {
			continue
		}
		d, ok := foldersync.PeerFor(w.sync.adapter, *rec.SyncPeer)
		if !ok || seen[d] {
			continue
		}
		seen[d] = true
		peers = append(peers, foldersync.Peer{Peer: d, Label: foldersync.PeerLabel(rec.HumanLabel, rec.PrincipalRef)})
	}
	return peers, nil
}

// syncDetach runs `detach` on the way out, with its own budget: ctx has
// already ended by then, and the engine must still hear that this session
// is gone.
func (w *watcher) syncDetach(client *foldersync.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), foldersync.CallTimeout)
	defer cancel()
	res, err := client.Detach(ctx, w.sessionID)
	if err != nil {
		w.log.Warn("sync detach failed", slog.String("code", string(codeOf(err))), adlog.Err(err))
		return
	}
	w.log.Info("sync detached", slog.Bool("stopped", res.Stopped))
}

// syncUnavailable is the one notice for an adapter the session cannot
// use; the goroutine ends after it and the session runs without file
// sync until its next start.
func (w *watcher) syncUnavailable(err error) {
	w.log.Warn("sync adapter unavailable; file sync is off for this session",
		slog.String("sync_adapter", w.sync.adapter), slog.String("code", string(codeOf(err))), adlog.Err(err))
	w.writeNotice(syncUnavailableNotice(w.sync.adapter, err))
}

// syncUnavailableNotice renders that notice: an external adapter missing
// from PATH is named as the executable the user would install; any other
// failure carries its code and the adapter's own message, sanitised onto
// one capped line (it is local text from the user's own adapter, and it
// is the one place a "syncthing is not on PATH" can reach the human).
func syncUnavailableNotice(adapter string, err error) string {
	name := protocol.SanitizeAttribute(adapter)
	if foldersync.IsNotFound(err) {
		exe := name
		if adapter != foldersync.BundledAdapter {
			exe = foldersync.ExternalPrefix + name
		}
		return "Brigade sync: " + exe + " is not on PATH; file sync is off for this session"
	}
	code, msg := codeOf(err), ""
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		msg = strings.Join(strings.Fields(protocol.Sanitize(perr.Message)), " ")
		msg = protocol.TruncateRunes(msg, syncNoticeMessageChars)
	}
	detail := string(code)
	if msg != "" {
		detail += ": " + msg
	}
	return "Brigade sync: the " + name + " sync adapter is not usable (" + detail + "); file sync is off for this session"
}
