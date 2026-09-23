package commands

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/foldersync"
	"github.com/appshapes/brigade/internal/protocol"
)

// The fixed texts of `brigade sync status` the tests assert word for word.
const (
	// SyncUsage is the whole `usage` message for anything but the one verb.
	SyncUsage = "sync takes one verb: status"
	// SyncOffLine is the human output for a session that syncs nothing:
	// the SessionStart line said why (the option, no folders, an unusable
	// member), so this line only points back at it.
	SyncOffLine = "file sync is off for this session (the Brigade line at session start says why)"
	// SyncNote is the note of the --json result: folders and peers are
	// the engine's own report, and a peer's label is its owner's words.
	SyncNote = "state as the sync adapter reports it; peer labels are their owners' own words, unverified"
)

// The words `brigade sync status` prints for the engine and a peer.
const (
	syncRunning     = "running"
	syncStopped     = "stopped"
	syncConnected   = "yes"
	syncUnconnected = "no"
	// syncAbsent is the state of a configured folder the engine does not
	// report: the watcher has not applied it yet, or its apply failed. A
	// folder the engine holds at another checkout's path shows
	// foldersync.StateConflictPath instead of that checkout's state.
	syncAbsent = "not shared yet"
)

// shortPeerChars is how much of a peer descriptor labels a peer the
// roster does not name: seven characters, Syncthing's own short device id.
const shortPeerChars = 7

// syncStatusResult is the --json result of `brigade sync status`: the
// adapter's status result, plus the adapter name and the folders as this
// session configured them (id, path, label), so a reader can tell this
// project's folders from another project's on the same engine.
type syncStatusResult struct {
	Sync       string                   `json:"sync"`
	Adapter    string                   `json:"adapter,omitzero"`
	Running    bool                     `json:"running"`
	Peer       string                   `json:"peer"`
	Folders    []foldersync.FolderState `json:"folders"`
	Peers      []syncPeerJSON           `json:"peers"`
	Configured []foldersync.Folder      `json:"configured"`
	Note       string                   `json:"note"`
}

// syncPeerJSON is one peer of the --json result: the adapter's report and
// the roster's label for it, "" when the roster does not name it.
type syncPeerJSON struct {
	Peer      string `json:"peer"`
	Connected bool   `json:"connected"`
	Label     string `json:"label,omitzero"`
}

// Sync implements `brigade sync status [--json]` (folder-sync plan §4.3):
// inside a session, the sync adapter and folders the SessionStart hook
// froze into the by-pid map, and the adapter's `status` verb — the
// engine's state, this machine's peer, this project's folders (label,
// state) and every peer (label, connected). A peer's label is looked up in
// the team roster best effort; the adapter answers for itself. Outside a
// session it refuses, like `doing`: there is no map to read.
func Sync(inv Invocation) error {
	if len(inv.Args) != 1 || inv.Args[0] != "status" {
		return usage(SyncUsage)
	}
	if !inv.inSession() {
		return notInSession("sync status")
	}
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return err
	}
	m, err := config.Session(inv.Environ, stateDir)
	if err != nil {
		return err
	}
	if m.SyncAdapter == "" {
		if inv.JSON {
			return writeJSON(inv.Out, syncStatusResult{Sync: config.SyncOff, Folders: []foldersync.FolderState{}, Peers: []syncPeerJSON{}, Configured: []foldersync.Folder{}, Note: SyncOffLine})
		}
		return writeLines(inv.Out, SyncOffLine)
	}
	client := &foldersync.Client{
		Adapter:   m.SyncAdapter,
		PluginBin: m.PluginBin,
		StateDir:  stateDir,
		Environ:   inv.Environ,
		Logger:    inv.logger(),
		Command:   inv.Deps.SyncCommand,
	}
	ctx, cancel := context.WithTimeout(context.Background(), foldersync.CallTimeout)
	defer cancel()
	st, err := client.Status(ctx)
	if err != nil {
		return err
	}
	configured := foldersync.Folders(m.TeamRef, m.SyncRoot, m.WorkspaceLabel, m.SyncFolders)
	labels := inv.peerLabels(m.SyncAdapter)

	if inv.JSON {
		res := syncStatusResult{
			Sync: config.SyncOn, Adapter: m.SyncAdapter, Running: st.Running, Peer: sanitizeID(st.Peer),
			Folders: []foldersync.FolderState{}, Peers: []syncPeerJSON{}, Configured: configured, Note: SyncNote,
		}
		for _, f := range st.Folders {
			res.Folders = append(res.Folders, foldersync.FolderState{ID: sanitizeID(f.ID), Path: sanitizeID(f.Path), State: protocol.SanitizeAttribute(f.State)})
		}
		for _, p := range st.Peers {
			res.Peers = append(res.Peers, syncPeerJSON{Peer: sanitizeID(p.Peer), Connected: p.Connected, Label: labels[p.Peer]})
		}
		return writeJSON(inv.Out, res)
	}

	engine := syncStopped
	if st.Running {
		engine = syncRunning
	}
	head := "sync: " + attrLine(m.SyncAdapter) + ", engine " + engine
	if peer := idLine(st.Peer); peer != "" {
		head += ", this machine's peer " + peer
	}
	out := []string{head}
	reported := make(map[string]foldersync.FolderState, len(st.Folders))
	for _, f := range st.Folders {
		reported[f.ID] = f
	}
	folderRows := [][]string{{"FOLDER", "STATE"}}
	for _, f := range configured {
		state := syncAbsent
		if e, ok := reported[f.ID]; ok {
			state = enumLine(e.State)
			// The engine holds this folder id at another checkout's path
			// (a second clone of the repository on this machine): its
			// state is that checkout's, and this one is not shared.
			if e.Path != "" && !samePath(e.Path, f.Path) {
				state = foldersync.StateConflictPath
			}
		}
		folderRows = append(folderRows, []string{workspaceLine(f.Label), state})
	}
	for _, row := range padTable(folderRows) {
		out = append(out, tableRow(row))
	}
	if len(st.Peers) > 0 {
		peerRows := [][]string{{"PEER", "CONNECTED"}}
		for _, p := range st.Peers {
			label := labels[p.Peer]
			if label == "" {
				label = "[" + shortPeer(p.Peer) + "]"
			}
			connected := syncUnconnected
			if p.Connected {
				connected = syncConnected
			}
			peerRows = append(peerRows, []string{label, connected})
		}
		for _, row := range padTable(peerRows) {
			out = append(out, tableRow(row))
		}
	}
	return writeLines(inv.Out, out...)
}

// peerLabels maps each teammate's peer descriptor (this adapter's prefix
// stripped) to the label the roster gives its session, best effort: a
// session map the backend adapter cannot serve, or a roster that cannot
// be read, labels nothing, and the command still prints what the sync
// adapter said. The labels are sanitised and folded onto one line.
func (inv Invocation) peerLabels(adapter string) map[string]string {
	labels := map[string]string{}
	t, err := inv.sessionTarget()
	if err != nil {
		inv.logger().Debug("sync status: no roster for peer labels", log.Err(err))
		return labels
	}
	ctx, cancel := context.WithTimeout(context.Background(), adapterclient.DefaultTimeout)
	defer cancel()
	list, err := t.client.ListSessions(ctx, true)
	if err != nil {
		inv.logger().Debug("sync status: no roster for peer labels", log.Err(err))
		return labels
	}
	for _, rec := range list.Sessions {
		if rec.SyncPeer == nil {
			continue
		}
		if d, ok := foldersync.PeerFor(adapter, *rec.SyncPeer); ok && labels[d] == "" {
			labels[d] = foldersync.PeerLabel(rec.HumanLabel, rec.PrincipalRef)
		}
	}
	inv.logger().Debug("sync status: peer labels", slog.Int("labelled", len(labels)))
	return labels
}

// shortPeer is the first shortPeerChars characters of a sanitised
// descriptor, "?" for one that sanitises away.
func shortPeer(descriptor string) string {
	id := idLine(descriptor)
	if id == "" {
		return "?"
	}
	if r := []rune(id); len(r) > shortPeerChars {
		return string(r[:shortPeerChars])
	}
	return id
}

// samePath reports whether two paths name one directory: equal once
// cleaned, or once symlinks are resolved (macOS's /var is /private/var).
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
