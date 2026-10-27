// apply.go is the sync adapter's policy over the shared engine
// (internal/syncthing): which devices and folders an apply posts, what it
// keeps, what it moves, pauses and reports.

package syncthing

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	engine "github.com/appshapes/brigade/internal/syncthing"
)

// An instance is the engine under one state directory, driven by this
// adapter's policy.
type instance struct {
	e *engine.Engine
	a *adapter
}

func newInstance(stateDir string, a *adapter) *instance {
	return &instance{e: engine.New(stateDir, a.d, a.log, a.environ), a: a}
}

// attach counts the session in and makes sure the daemon runs (plan 4.3).
func (in *instance) attach(sessionID string, holder int) (*attachResult, error) {
	id, _, err := in.e.Attach(sessionID, holder)
	if err != nil {
		return nil, err
	}
	return &attachResult{Peer: id}, nil
}

// detach counts the session out and stops the daemon when it was the last.
func (in *instance) detach(sessionID string) (*detachResult, error) {
	stopped, err := in.e.Detach(sessionID)
	if err != nil {
		return nil, err
	}
	return &detachResult{Stopped: stopped}, nil
}

// apply introduces the peers and shares the folders with every one of them
// (plan 4.3, 4.4; card 42), changing in the instance whatever that takes:
//
//   - a peer the instance does not know is added; one it already holds is
//     left as it is, because a person may have added it by hand, with its
//     own name and addresses;
//   - a folder is posted with the devices it already has plus the peers,
//     so an always-on Syncthing a person added to the folder stays;
//   - a folder the instance still holds under the id it had before
//     0.16.0 (the request's `replaces`), at this checkout's path, is moved
//     to its id: the earlier folder's configuration is removed and the
//     folder posted under the new id with the earlier one's devices
//     (replaced). Syncthing keeps every file and scans the path again;
//   - a folder id the instance holds at another path is another
//     checkout's on this machine — a second clone — and a path another id
//     holds is its holder's: both are reported `conflict_path` and left
//     where they are, because moving one would move it back and forth
//     between two sessions every round;
//   - a folder THIS checkout shared and its project no longer lists is
//     paused and reported `paused`. Its files stay, and it syncs again
//     when the project lists it again, because every folder apply posts
//     carries paused: false;
//   - a folder whose directory cannot be created is reported `rejected`,
//     and the rest are applied.
//
// None of these is a rule about what apply may touch. Brigade is open by
// default and restricted by configuration alone (the `sync` option): what
// apply leaves as it found it, it leaves because a team relies on it.
func (in *instance) apply(folders []folderSpec, peers []peerSpec) (*applyResult, error) {
	for _, f := range folders {
		if f.ID == "" || !filepath.IsAbs(f.Path) {
			return nil, errInput("folders", "every folder needs an id and an absolute path")
		}
	}
	for _, p := range peers {
		if p.Peer == "" {
			return nil, errInput("peers", "every peer needs a descriptor")
		}
	}
	c, err := in.e.Running()
	if err != nil {
		return nil, err
	}

	// One teammate's unusable descriptor (Syncthing answers 4xx) must not
	// stop the rest of the team syncing: it is skipped, logged by status
	// code only, and left out of the folders' device lists (a folder
	// naming an unknown device would be refused whole). A device already
	// configured is accepted as it stands, never re-posted.
	known, err := c.Devices()
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(known))
	for _, d := range known {
		have[d.DeviceID] = true
	}
	devs := make([]engine.DeviceRef, 0, len(peers))
	for _, p := range peers {
		if have[p.Peer] {
			devs = append(devs, engine.DeviceRef{DeviceID: p.Peer})
			continue
		}
		err := c.SetDevice(engine.DeviceConfig{DeviceID: p.Peer, Name: p.Label, Addresses: []string{"dynamic"}})
		if err != nil {
			if s := engine.HTTPStatus(err); s >= 400 && s < 500 {
				in.a.log.Warn("syncthing refused a peer; skipped", slog.Int("status", s))
				continue
			}
			return nil, err
		}
		devs = append(devs, engine.DeviceRef{DeviceID: p.Peer})
	}

	existing, err := c.Folders()
	if err != nil {
		return nil, err
	}
	result := &applyResult{Folders: make([]folderState, 0, len(folders)), Peers: make([]peerState, 0, len(peers))}
	for _, f := range folders {
		path := filepath.Clean(f.Path)
		var had []engine.DeviceRef
		if old := replaced(existing, f, path); old != nil {
			// Copied first: removing the entry shifts the slice old points into.
			oldID, oldDevices := old.ID, old.Devices
			if err := c.RemoveFolder(oldID); err != nil {
				// The folder keeps syncing under its earlier id, which
				// still holds the path; the next round tries again.
				in.a.log.Warn("a folder could not be moved to its new id",
					slog.String("folder_id", f.ID), slog.String("replaces", oldID), adapterlog.Err(err))
				result.Folders = append(result.Folders, folderState{ID: f.ID, State: stateConflictPath})
				continue
			}
			in.a.log.Info("moved a folder to its new id", slog.String("folder_id", f.ID), slog.String("replaces", oldID))
			had = oldDevices
			existing = slices.DeleteFunc(existing, func(e engine.ConfiguredFolder) bool { return e.ID == oldID })
		}
		current, conflict := place(existing, f.ID, path)
		if conflict {
			result.Folders = append(result.Folders, folderState{ID: f.ID, State: stateConflictPath})
			continue
		}
		if current != nil {
			had = unionDevices(had, current.Devices)
		}
		// The folder is the project's (a path it lists), so it is created
		// with an ordinary directory mode, not the state tree's 0700.
		if err := os.MkdirAll(path, 0o755); err != nil { //nolint:gosec // G301: a project folder, readable like its siblings
			in.a.log.Warn("a folder could not be created; rejected", slog.String("folder_id", f.ID), adapterlog.Err(err))
			result.Folders = append(result.Folders, folderState{ID: f.ID, State: stateRejected})
			continue
		}
		err := c.SetFolder(engine.FolderConfig{
			ID: f.ID, Label: f.Label, Path: path, Type: "sendreceive",
			Devices:          unionDevices(had, devs),
			Versioning:       engine.VersioningConfig{Type: "trashcan", Params: map[string]string{"cleanoutDays": "14"}},
			FSWatcherEnabled: true,
			RescanIntervalS:  60,
		})
		if err != nil {
			if s := engine.HTTPStatus(err); s >= 400 && s < 500 {
				in.a.log.Warn("syncthing refused a folder", slog.String("folder_id", f.ID), slog.Int("status", s))
				result.Folders = append(result.Folders, folderState{ID: f.ID, State: stateRejected})
				continue
			}
			return nil, err
		}
		result.Folders = append(result.Folders, folderState{ID: f.ID, State: c.FolderState(f.ID)})
	}
	for _, e := range formerFolders(existing, folders) {
		if !e.Paused {
			// A failed pause is logged and tried again next round; it
			// never costs the listed folders their apply.
			if err := c.PauseFolder(e.ID, true); err != nil {
				in.a.log.Warn("a folder the project no longer lists could not be paused", slog.String("folder_id", e.ID), adapterlog.Err(err))
				continue
			}
			in.a.log.Info("paused a folder the project no longer lists", slog.String("folder_id", e.ID))
		}
		result.Folders = append(result.Folders, folderState{ID: e.ID, State: statePaused})
	}

	conns, err := c.Connections()
	if err != nil {
		return nil, err
	}
	for _, p := range peers {
		result.Peers = append(result.Peers, peerState{Peer: p.Peer, Connected: conns[p.Peer]})
	}
	return result, nil
}

// stateConflictPath is the folder state for a folder this checkout cannot
// hold (plan 4.4): its id is held at another path — the same folder of a
// second clone of the repository on this machine — or its path is held by
// another folder id.
const stateConflictPath = "conflict_path"

// stateRejected is the folder state for a folder Syncthing refused, or
// whose directory could not be created.
const stateRejected = "rejected"

// statePaused is the folder state for a folder this checkout shared and
// its project no longer lists: apply paused it (formerFolders).
const statePaused = "paused"

// replaced finds the folder f takes the place of (card 42): the one the
// instance holds under f.Replaces — the id f had before 0.16.0 — at path,
// when removing it lets f be placed there. nil when f names no earlier
// id, the instance holds none at path (one at another path is another
// checkout's, and stays), f's own id is held at another path, or a third
// id holds path: then nothing is removed, and place decides as before.
func replaced(existing []engine.ConfiguredFolder, f folderSpec, path string) *engine.ConfiguredFolder {
	if f.Replaces == "" || f.Replaces == f.ID {
		return nil
	}
	var old *engine.ConfiguredFolder
	for i := range existing {
		e := &existing[i]
		same := samePath(e.Path, path)
		switch {
		case e.ID == f.Replaces:
			if same {
				old = e
			}
		case e.ID == f.ID:
			if !same {
				return nil
			}
		case same:
			return nil
		}
	}
	return old
}

// formerFolders is the folders of existing that THIS checkout shared and
// its project no longer lists: apply pauses them. A folder qualifies
// when all of this holds, which is what tells it from another checkout's
// folder — another repository of the same team, a second clone on
// another branch, a folder a person configured by hand:
//
//   - its id carries this team's prefix, "brigade-<ref8>-", which every
//     requested id shares (a request whose ids do not share one pauses
//     nothing, and so does an empty one);
//   - it is not requested, under its id or as the earlier id of a
//     requested folder (`replaces`): that one is listed, and on its way
//     to its new id;
//   - its label carries a folder name its id proves (folderName), so the
//     name is proven, not guessed;
//   - its path is where this checkout puts that name: the checkout's
//     root (or the ancestor a ".." folder climbs to) is recovered the same
//     way from a requested folder's proven name and path.
func formerFolders(existing []engine.ConfiguredFolder, folders []folderSpec) []engine.ConfiguredFolder {
	if len(folders) == 0 {
		return nil
	}
	prefix, ok := teamPrefix(folders[0].ID)
	if !ok {
		return nil
	}
	// An anchor is an ancestor of the checkout's root: base is the root
	// with up levels climbed.
	type anchor struct {
		up   int
		base string
	}
	requested := make(map[string]bool, 2*len(folders))
	var anchors []anchor
	for _, f := range folders {
		if p, ok := teamPrefix(f.ID); !ok || p != prefix {
			return nil
		}
		requested[f.ID] = true
		if f.Replaces != "" {
			requested[f.Replaces] = true
		}
		name, ok := folderName(prefix, f.ID, f.Label)
		if !ok {
			continue
		}
		up, rest := climb(name)
		if base, ok := trimFolder(filepath.Clean(f.Path), rest); ok {
			anchors = append(anchors, anchor{up: up, base: base})
		}
	}
	var out []engine.ConfiguredFolder
	for _, e := range existing {
		if requested[e.ID] || !strings.HasPrefix(e.ID, prefix) {
			continue
		}
		name, ok := folderName(prefix, e.ID, e.Label)
		if !ok {
			continue
		}
		up, rest := climb(name)
		for _, a := range anchors {
			if up < a.up {
				continue
			}
			base := a.base
			for range up - a.up {
				base = filepath.Dir(base)
			}
			if samePath(e.Path, filepath.Join(base, rest)) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// teamPrefix is a Brigade folder id's team part, "brigade-<ref8>-", or
// false for an id of another shape.
func teamPrefix(id string) (string, bool) {
	parts := strings.Split(id, "-")
	if len(parts) != 3 || parts[0] != "brigade" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[0] + "-" + parts[1] + "-", true
}

// folderName finds the folder name a Brigade label carries, proven by
// the folder's id under prefix (docs/sync-adapters.md, "Folder ids"):
//
//   - since 0.16.0 the id hashes the whole label, "<repository>/<folder>",
//     and the name is what follows the label's FIRST "/": a repository's
//     name has none, and the folder may be nested. A label that reads
//     this way is read this way only;
//   - a folder shared by 0.11.0 to 0.15.0 hashes the name alone: the
//     whole label (a session that shared no workspace label) or the text
//     after any "/" of it.
func folderName(prefix, id, label string) (string, bool) {
	if scope, name, ok := strings.Cut(label, "/"); ok && scope != "" && usableName(name) && hashesTo(prefix, id, label) {
		return name, true
	}
	for i := -1; i < len(label); i++ {
		if i >= 0 && label[i] != '/' {
			continue
		}
		if name := label[i+1:]; usableName(name) && hashesTo(prefix, id, name) {
			return name, true
		}
	}
	return "", false
}

// usableName reports whether name can be a folder of the team file: a
// clean relative path other than ".".
func usableName(name string) bool {
	return name != "" && name != "." && !strings.HasPrefix(name, "/") &&
		filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) == name
}

// hashesTo reports whether id is prefix and the first 12 hex digits of
// text's SHA-256.
func hashesTo(prefix, id, text string) bool {
	sum := sha256.Sum256([]byte(text))
	return prefix+hex.EncodeToString(sum[:])[:12] == id
}

// climb splits a clean relative folder name into the ".." levels it
// climbs (path.Clean puts them all first) and the rest.
func climb(name string) (int, string) {
	up := 0
	for {
		switch {
		case name == "..":
			return up + 1, ""
		case strings.HasPrefix(name, "../"):
			up++
			name = name[len("../"):]
		default:
			return up, name
		}
	}
}

// trimFolder is p with the folder part rest removed from its end — the
// directory rest was joined to — or false when p does not end in rest.
func trimFolder(p, rest string) (string, bool) {
	if rest == "" {
		return p, true
	}
	base, ok := strings.CutSuffix(p, string(filepath.Separator)+filepath.FromSlash(rest))
	if !ok {
		return "", false
	}
	if base == "" {
		base = string(filepath.Separator)
	}
	return base, true
}

// place finds folder id in the instance's configuration: the entry when
// it is configured at path (nil when it is not configured yet), and
// conflict when this checkout leaves it where it is — the id is
// configured at a different path (another checkout on this machine got
// there first; re-pointing it would move the folder between the two every
// round), or another id already syncs path. Syncthing 2.1.5 accepts two
// ids at one path (measured 2026-09-28); what two folders pulling into
// the same files do was not measured, and this adapter keeps a path to
// one id.
func place(existing []engine.ConfiguredFolder, id, path string) (*engine.ConfiguredFolder, bool) {
	var current *engine.ConfiguredFolder
	for i := range existing {
		e := &existing[i]
		same := samePath(e.Path, path)
		switch {
		case e.ID == id && !same:
			return nil, true
		case e.ID == id:
			current = e
		case same:
			return nil, true
		}
	}
	return current, false
}

// samePath reports whether a and b name one directory: equal once
// cleaned, or equal once symlinks are resolved (macOS's /var is
// /private/var, and a path posted by an earlier round may be either
// spelling).
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// unionDevices is had followed by every device of add it does not name
// yet: a device a person added to the folder stays in its list, and an
// entry the list already has is kept exactly as Syncthing reported it.
func unionDevices(had, add []engine.DeviceRef) []engine.DeviceRef {
	out := make([]engine.DeviceRef, 0, len(had)+len(add))
	seen := make(map[string]bool, len(had)+len(add))
	for _, d := range had {
		if d.DeviceID == "" || seen[d.DeviceID] {
			continue
		}
		seen[d.DeviceID] = true
		out = append(out, d)
	}
	for _, d := range add {
		if !seen[d.DeviceID] {
			seen[d.DeviceID] = true
			out = append(out, d)
		}
	}
	return out
}

// status reports the engine as it is: every folder and every other device
// the instance holds (it is shared by every session and team on this
// machine), with their states. No daemon is running=false and empty lists,
// not a failure.
func (in *instance) status() (*statusResult, error) {
	result := &statusResult{Folders: []folderState{}, Peers: []peerState{}}
	if _, ok := in.e.Daemon(); !ok {
		return result, nil
	}
	c, err := in.e.API()
	if err != nil {
		return nil, err
	}
	st, err := c.SystemStatus()
	if err != nil {
		return nil, err
	}
	result.Running = true
	result.Peer = st.MyID
	folders, err := c.Folders()
	if err != nil {
		return nil, err
	}
	for _, f := range folders {
		result.Folders = append(result.Folders, folderState{ID: f.ID, Path: f.Path, State: c.FolderState(f.ID)})
	}
	devices, err := c.Devices()
	if err != nil {
		return nil, err
	}
	conns, err := c.Connections()
	if err != nil {
		return nil, err
	}
	for _, d := range devices {
		if d.DeviceID == st.MyID {
			continue
		}
		result.Peers = append(result.Peers, peerState{Peer: d.DeviceID, Connected: conns[d.DeviceID]})
	}
	return result, nil
}
