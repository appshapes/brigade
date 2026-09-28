// Package foldersync is the harness side of the sync-adapter protocol
// (folder-sync plan §4.3, docs/sync-adapters.md): the request and result
// shapes of its five verbs, the Client that resolves a sync adapter by
// NAME and runs one verb through adapterkit.Spawn, and the derivations
// both ends of a team must agree on byte for byte — the folder id, path
// and label handed to `apply`, and the `<adapter>:<descriptor>` form a
// session's peer takes on the wire (sync_peer, C-47).
//
// Brigade never carries a file byte. A sync adapter is an executable that
// drives some sync engine (the bundled one drives Syncthing); the harness
// only tells it which folders this checkout shares and which teammates'
// peers to introduce, learned from the team roster, and asks how it is
// going. The watcher is the one long-running caller (watch/sync.go);
// `brigade sync status` the other.
package foldersync

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// ProtocolVersion is the sync-adapter protocol's version, answered by
// `describe` in its own protocol_version member. The envelope around
// every result is BAP/1's (4.3), so the envelope's protocol_version stays
// protocol.ProtocolVersion.
const ProtocolVersion = "sync/1"

// CallTimeout is the budget of one verb (§4.3): the ordinary 20 s
// request/response budget of 4.1. An `attach` that starts an engine is
// the slowest verb and fits well inside it.
const CallTimeout = 20 * time.Second

// BundledAdapter is the one sync adapter the plugin binary carries: argv
// [<plugin binary>, "sync-adapter", "syncthing", <verb>]. Every other
// name resolves to ExternalPrefix+name on PATH, the git-subcommand
// convention.
const (
	BundledAdapter = "syncthing"
	ExternalPrefix = "brigade-sync-"
)

// The five verbs (§4.3). Every one is idempotent.
const (
	VerbDescribe = "describe"
	VerbAttach   = "attach"
	VerbApply    = "apply"
	VerbStatus   = "status"
	VerbDetach   = "detach"
)

// DescribeResult is `describe`'s result: the adapter's own name and
// version, and the sync protocol it speaks.
type DescribeResult struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocol_version"`
}

// SessionRequest is the request of `attach` and `detach`: the Brigade
// state directory the adapter keeps its engine under, and the Brigade
// session whose reference it adds or drops. The engine runs while at
// least one session holds a reference — sync only while a session is
// active (§1). PID, optional, is the process that holds the reference
// (the watcher's own): an adapter may prune a reference whose process is
// gone without a detach. Absent (0), the reference is the detach's alone
// to drop.
type SessionRequest struct {
	StateDir  string `json:"state_dir"`
	SessionID string `json:"session_id"`
	PID       int    `json:"pid,omitzero"`
}

// AttachResult is `attach`'s result: this machine's peer descriptor, the
// part of sync_peer after the adapter prefix (a Syncthing device id).
type AttachResult struct {
	Peer string `json:"peer"`
}

// A Folder is one folder `apply` shares: the id both ends derive
// (FolderID), the absolute local path and a human label. Replaces,
// optional, is the id the same folder had under the derivation of 0.11.0
// to 0.15.0 (LegacyFolderID): an adapter whose engine still holds the
// folder under that id at Path carries it over to ID (card 42). Absent
// when the two ids are one.
type Folder struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Label    string `json:"label"`
	Replaces string `json:"replaces,omitzero"`
}

// A Peer is one teammate's peer `apply` introduces: the descriptor with
// the adapter prefix stripped, and a human label.
type Peer struct {
	Peer  string `json:"peer"`
	Label string `json:"label"`
}

// ApplyRequest is `apply`'s request: the whole desired set, every time.
type ApplyRequest struct {
	StateDir  string   `json:"state_dir"`
	SessionID string   `json:"session_id"`
	Folders   []Folder `json:"folders"`
	Peers     []Peer   `json:"peers"`
}

// FolderState is one folder in `apply`'s and `status`'s results. Path is
// set by `status` only. State is the adapter's own word (Syncthing's
// idle, scanning, syncing, error, …, or conflict_path); the harness
// prints it and never branches on it.
type FolderState struct {
	ID    string `json:"id"`
	Path  string `json:"path,omitzero"`
	State string `json:"state"`
}

// StateConflictPath is the folder state for a folder this checkout
// cannot hold because another checkout on the machine holds it — the same
// folder of a second clone of the repository (§4.4). The bundled adapter
// reports it from `apply`; `brigade sync status` shows it for a folder the
// engine holds at another path, and the watcher's notice line counts it.
const StateConflictPath = "conflict_path"

// StatePaused is the folder state the bundled adapter's `apply` reports
// for a folder this checkout shared and its project no longer lists: it
// paused the folder, whose files stay. The watcher's notice line counts
// it apart from the listed folders.
const StatePaused = "paused"

// PeerState is one peer in `apply`'s and `status`'s results.
type PeerState struct {
	Peer      string `json:"peer"`
	Connected bool   `json:"connected"`
}

// ApplyResult is `apply`'s result.
type ApplyResult struct {
	Folders []FolderState `json:"folders"`
	Peers   []PeerState   `json:"peers"`
}

// StatusRequest is `status`'s request.
type StatusRequest struct {
	StateDir string `json:"state_dir"`
}

// StatusResult is `status`'s result: whether the engine runs, this
// machine's descriptor, and every folder and peer it knows.
type StatusResult struct {
	Running bool          `json:"running"`
	Peer    string        `json:"peer"`
	Folders []FolderState `json:"folders"`
	Peers   []PeerState   `json:"peers"`
}

// DetachResult is `detach`'s result: whether this detach stopped the
// engine (it was the last session).
type DetachResult struct {
	Stopped bool `json:"stopped"`
}

// FolderID is the folder id both ends of a team derive for one folder of
// one repository's team file (§4.3, card 42): "brigade-", the first 8
// characters of team_ref with its dashes removed, "-", and the first 12
// hex digits of the SHA-256 of "<scope>/<folder>" — scope the
// repository's name (teamfile.RepoName, the name in its `origin` URL) and
// folder exactly as the team file writes it. Every checkout of the
// repository reads the same file, has the same origin and belongs to the
// same team, so every one arrives at the same id without exchanging it;
// the team_ref part keeps two teams' folders apart on one engine, and the
// scope keeps apart two repositories of one team that list the same
// folder. An empty scope — a checkout with no name to derive — gives
// LegacyFolderID.
func FolderID(teamRef, scope, folder string) string {
	if scope == "" {
		return LegacyFolderID(teamRef, folder)
	}
	return folderID(teamRef, scope+"/"+folder)
}

// LegacyFolderID is the derivation of 0.11.0 to 0.15.0: the hash is of
// the folder alone, so two repositories of one team that listed the same
// folder derived one id, and one engine held it for one of them only.
// Folders hands it to the adapter as Replaces, and `brigade sync status`
// still knows a folder by it.
func LegacyFolderID(teamRef, folder string) string {
	return folderID(teamRef, folder)
}

// folderID is the id's shape, whatever was hashed.
func folderID(teamRef, hashed string) string {
	ref := strings.ReplaceAll(teamRef, "-", "")
	if len(ref) > 8 {
		ref = ref[:8]
	}
	sum := sha256.Sum256([]byte(hashed))
	return "brigade-" + ref + "-" + hex.EncodeToString(sum[:])[:12]
}

// Label is a folder's human label, "<scope>/<folder>": the very text
// FolderID hashes, which is how the bundled adapter proves a folder's
// name from its label. The folder alone when scope is empty.
func Label(scope, folder string) string {
	if scope == "" {
		return folder
	}
	return scope + "/" + folder
}

// Folders derives `apply`'s folder list from the by-pid map's frozen
// members: id = FolderID, path = <root>/<folder>, label = Label, and
// replaces = the folder's LegacyFolderID when that is another id.
func Folders(teamRef, root, scope string, folders []string) []Folder {
	out := make([]Folder, 0, len(folders))
	for _, f := range folders {
		folder := Folder{ID: FolderID(teamRef, scope, f), Path: filepath.Join(root, f), Label: Label(scope, f)}
		if legacy := LegacyFolderID(teamRef, f); legacy != folder.ID {
			folder.Replaces = legacy
		}
		out = append(out, folder)
	}
	return out
}

// WirePeer is the sync_peer value a session publishes: the adapter name,
// a colon, the descriptor `attach` returned.
func WirePeer(adapter, descriptor string) string {
	return adapter + ":" + descriptor
}

// PeerFor strips adapter's prefix from a sync_peer value: the descriptor
// and true when the value carries exactly that prefix and something after
// it, else false — a peer of another adapter is never handed to this one
// (§4.3).
func PeerFor(adapter, wire string) (string, bool) {
	descriptor, ok := strings.CutPrefix(wire, adapter+":")
	if !ok || descriptor == "" {
		return "", false
	}
	return descriptor, true
}

// shortPrincipalChars is how much of a principal_ref labels a peer whose
// session carries no human label — the eight characters the roster shows
// for an unlabelled session (commands/format.go shortPrincipal).
const shortPrincipalChars = 8

// PeerLabel is a teammate's peer label: the session's human_label
// sanitised as a label and folded onto one line, else the first eight
// characters of its principal_ref. Both are remote text; the adapter
// shows the label to a human (a Syncthing device name) and never acts on
// it.
func PeerLabel(humanLabel, principalRef string) string {
	if l := strings.Join(strings.Fields(protocol.SanitizeLabel(humanLabel)), " "); l != "" {
		return l
	}
	ref := strings.Join(strings.Fields(protocol.Sanitize(principalRef)), "")
	if r := []rune(ref); len(r) > shortPrincipalChars {
		return string(r[:shortPrincipalChars])
	}
	return ref
}
