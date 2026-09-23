package teamfile

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io/fs"
	"path"
	"strings"

	"github.com/appshapes/brigade/internal/protocol"
)

// SyncConfig is a usable `sync` member (folder-sync plan §4.1): the
// project's declaration of the folders its checkouts keep in sync, and
// the NAME of the sync adapter that does it. Adapter is a name resolved
// strictly user-side (§4.3: `syncthing` is the bundled one; any other is
// `brigade-sync-<name>` on the user's PATH), never a path or a command.
// Folders are clean relative paths from the repository toplevel, in the
// file's order. The json tags are the member's own shape, so `team create
// --force` writes a carried SyncConfig back unchanged in meaning.
type SyncConfig struct {
	Adapter string   `json:"adapter"`
	Folders []string `json:"folders"`
}

// DefaultSyncAdapter is the adapter a `sync` member without one names.
const DefaultSyncAdapter = "syncthing"

// The `sync` member's bounds (§4.1).
const (
	MaxSyncFolders     = 32
	MaxSyncFolderBytes = 128
)

// The closed list of reasons a `sync` member is not usable. None is a
// refusal: the hook renders the token in its one fixed line (`file sync
// is off for this session`) and the session connects. The first rule a
// member breaks, in the order below, is the one reported. The folder
// rules are only what an entry needs to name a folder under the checkout
// (§4.1, open by default): anything else — `.git`, the team file, a
// nested or repeated folder, an odd character — is the sync engine's to
// report through `status`, not this parser's to deny.
const (
	SyncNotObject         = "not_object"          // sync is not a JSON object
	SyncAdapterInvalid    = "adapter_invalid"     // adapter is not a string matching the adapter-name rule
	SyncFoldersInvalid    = "folders_invalid"     // folders is not an array of strings
	SyncTooManyFolders    = "too_many_folders"    // more than MaxSyncFolders entries
	SyncFolderTooLong     = "folder_too_long"     // an entry over MaxSyncFolderBytes bytes
	SyncFolderNotRelative = "folder_not_relative" // an entry starting with `/`
	SyncFolderNotClean    = "folder_not_clean"    // path.Clean would change it (empty, `a/`, `./a`, `a//b`, `a/./b`)
	SyncFolderRoot        = "folder_root"         // `.`, the whole checkout
)

// SyncReasons is the closed unusable-sync token list, in check order.
func SyncReasons() []string {
	return []string{
		SyncNotObject, SyncAdapterInvalid, SyncFoldersInvalid, SyncTooManyFolders,
		SyncFolderTooLong, SyncFolderNotRelative, SyncFolderNotClean, SyncFolderRoot,
	}
}

// syncMembers is what this version reads inside `sync`; any other inner
// member is ignored and named `sync.<name>`.
var syncMembers = map[string]bool{"adapter": true, "folders": true}

// parseSync validates the `sync` member. It never refuses: an unusable
// member is (nil, ignored, token). The secret walk over the whole
// document already ran, so no string here can be a join secret; and no
// value reaches the result unless every rule passed.
func parseSync(member jsontext.Value) (*SyncConfig, []string, string) {
	var obj map[string]jsontext.Value
	if member.Kind() != '{' || json.Unmarshal(member, &obj) != nil {
		return nil, nil, SyncNotObject
	}
	var ignored []string
	for name := range obj {
		if !syncMembers[name] {
			ignored = append(ignored, "sync."+displayName(name))
		}
	}
	cfg := &SyncConfig{Adapter: DefaultSyncAdapter, Folders: []string{}}
	if v, ok := obj["adapter"]; ok {
		if v.Kind() != '"' || json.Unmarshal(v, &cfg.Adapter) != nil || !adapterName.MatchString(cfg.Adapter) {
			return nil, ignored, SyncAdapterInvalid
		}
	}
	// No folders member (or an empty array) is a usable member that
	// syncs nothing: the harness says so in its own line (§4.3).
	if v, ok := obj["folders"]; ok {
		if v.Kind() != '[' || json.Unmarshal(v, &cfg.Folders) != nil {
			return nil, ignored, SyncFoldersInvalid
		}
	}
	if reason := checkFolders(cfg.Folders); reason != "" {
		return nil, ignored, reason
	}
	return cfg, ignored, ""
}

// checkFolders applies §4.1's folder rules: the count, then each entry
// alone.
func checkFolders(folders []string) string {
	if len(folders) > MaxSyncFolders {
		return SyncTooManyFolders
	}
	for _, f := range folders {
		if reason := checkFolder(f); reason != "" {
			return reason
		}
	}
	return ""
}

// checkFolder is one entry's rules: bounded, relative, already in the
// form path.Clean gives it (which also rules out the empty string, since
// path.Clean("") is "."), and not the checkout itself. The JSON decode
// already required valid UTF-8.
func checkFolder(f string) string {
	switch {
	case len(f) > MaxSyncFolderBytes:
		return SyncFolderTooLong
	case strings.HasPrefix(f, "/"):
		return SyncFolderNotRelative
	case path.Clean(f) != f:
		return SyncFolderNotClean
	case f == ".":
		return SyncFolderRoot
	}
	return ""
}

// SyncNotCarriedUnreadable is CarriedSync's cause when reading the file
// failed with an I/O error rather than a team-file refusal: the one
// cause in neither Reasons nor SyncReasons.
const SyncNotCarriedUnreadable = "unreadable"

// CarriedSync is what `team create --force` carries from the file it
// replaces (§4.1). The replacement ALWAYS goes ahead, as it did before
// card 32; this only decides whether the project's `sync` declaration
// comes along. A file Parse accepts with a usable member gives that
// member back as parsed, and no cause. No file, or a file with no `sync`
// member, gives nothing and no cause. Otherwise — an unusable member, a
// refused file that declares one, or a file refused unread (a symlink, a
// special, world-writable or oversized file, whose `sync` is unknowable)
// — nothing is carried, and cause is the fixed token (from Reasons,
// SyncReasons or SyncNotCarriedUnreadable, never file content) that the
// command's one human line names.
func CarriedSync(filePath string) (carried *SyncConfig, cause string) {
	data, err := readCapped(filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ""
		}
		var perr *protocol.Error
		if errors.As(err, &perr) {
			return nil, perr.Details["reason"]
		}
		return nil, SyncNotCarriedUnreadable
	}
	f, perr := parseBytes(filePath, data)
	if perr == nil {
		return f.Sync, f.SyncUnusable
	}
	var raw map[string]jsontext.Value
	if json.Unmarshal(data, &raw) == nil {
		if _, ok := raw["sync"]; ok {
			var pe *protocol.Error
			if errors.As(perr, &pe) {
				return nil, pe.Details["reason"]
			}
			return nil, ReasonMalformed
		}
	}
	// A refused file with no `sync` member (not JSON at all, say) has
	// nothing in it to carry.
	return nil, ""
}
