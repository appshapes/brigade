package teamfile

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io/fs"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/protocol"
)

// SyncConfig is a usable `sync` member (folder-sync plan §4.1): the
// project's declaration of the folders its checkouts keep in sync, and
// the NAME of the sync adapter that does it. Adapter is a name resolved
// strictly user-side (§4.3: `syncthing` is the bundled one; any other is
// `brigade-sync-<name>` on the user's PATH), never a path or a command.
// Folders are relative to the repository toplevel, lexically confined to
// it, in the file's order. The json tags are the member's own shape, so
// `team create --force` writes a carried SyncConfig back unchanged in
// meaning.
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
// member breaks, in the order below, is the one reported.
const (
	SyncNotObject         = "not_object"          // sync is not a JSON object
	SyncAdapterInvalid    = "adapter_invalid"     // adapter is not a string matching the adapter-name rule
	SyncFoldersInvalid    = "folders_invalid"     // folders is not an array of strings
	SyncTooManyFolders    = "too_many_folders"    // more than MaxSyncFolders entries
	SyncFolderEmpty       = "folder_empty"        // an empty entry
	SyncFolderTooLong     = "folder_too_long"     // an entry over MaxSyncFolderBytes bytes
	SyncFolderBadChar     = "folder_bad_char"     // a backslash, a double quote, a control or format character
	SyncFolderNotRelative = "folder_not_relative" // an entry starting with `/`
	SyncFolderDotDot      = "folder_dotdot"       // a `..` segment
	SyncFolderNotClean    = "folder_not_clean"    // path.Clean would change it (`a/`, `./a`, `a//b`, `a/./b`)
	SyncFolderRoot        = "folder_root"         // `.`, the whole checkout
	SyncFolderGit         = "folder_git"          // a `.git` segment at any depth, in any letter case
	SyncFolderTeamFile    = "folder_team_file"    // `.brigade.json` itself
	SyncFolderDuplicate   = "folder_duplicate"    // the same folder twice (letter case folded)
	SyncFolderNested      = "folder_nested"       // one folder inside another (letter case folded)
)

// SyncReasons is the closed unusable-sync token list, in check order.
func SyncReasons() []string {
	return []string{
		SyncNotObject, SyncAdapterInvalid, SyncFoldersInvalid, SyncTooManyFolders,
		SyncFolderEmpty, SyncFolderTooLong, SyncFolderBadChar, SyncFolderNotRelative,
		SyncFolderDotDot, SyncFolderNotClean, SyncFolderRoot, SyncFolderGit,
		SyncFolderTeamFile, SyncFolderDuplicate, SyncFolderNested,
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

// checkFolders applies §4.1's folder rules: each entry alone, then the
// set. Comparisons across entries fold letter case, because the default
// macOS filesystem does: `Docs` and `docs` are one directory there.
func checkFolders(folders []string) string {
	if len(folders) > MaxSyncFolders {
		return SyncTooManyFolders
	}
	for _, f := range folders {
		if reason := checkFolder(f); reason != "" {
			return reason
		}
	}
	seen := make(map[string]bool, len(folders))
	for _, f := range folders {
		key := strings.ToLower(f)
		if seen[key] {
			return SyncFolderDuplicate
		}
		seen[key] = true
	}
	for _, a := range folders {
		for _, b := range folders {
			if strings.HasPrefix(strings.ToLower(b), strings.ToLower(a)+"/") {
				return SyncFolderNested
			}
		}
	}
	return ""
}

// checkFolder is one entry's rules. Together they make the entry a
// plain relative path that cannot leave the checkout lexically, cannot
// reach the repository's own metadata, and cannot overwrite the team
// file; what the filesystem does at that path (a symlink out of the
// checkout, say) is the sync side's to check at run time.
func checkFolder(f string) string {
	switch {
	case f == "":
		return SyncFolderEmpty
	case len(f) > MaxSyncFolderBytes:
		return SyncFolderTooLong
	case !utf8.ValidString(f) || strings.ContainsFunc(f, badFolderRune):
		return SyncFolderBadChar
	case strings.HasPrefix(f, "/"):
		return SyncFolderNotRelative
	}
	segments := strings.Split(f, "/")
	for _, s := range segments {
		if s == ".." {
			return SyncFolderDotDot
		}
	}
	if path.Clean(f) != f {
		return SyncFolderNotClean
	}
	if f == "." {
		return SyncFolderRoot
	}
	for _, s := range segments {
		if strings.EqualFold(s, ".git") {
			return SyncFolderGit
		}
	}
	if strings.EqualFold(f, FileName) {
		return SyncFolderTeamFile
	}
	return ""
}

// badFolderRune is the character rule: no backslash (a Windows separator
// and an escape everywhere else), no double quote, no control character
// and no Unicode format character (the bidi overrides a folder label
// would otherwise carry to a human).
func badFolderRune(r rune) bool {
	return r == '\\' || r == '"' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

// ReasonSyncNotCarried is `team create --force`'s refusal when the file
// it would replace declares a `sync` member it cannot carry across.
const ReasonSyncNotCarried = "sync_not_carried"

// CarriedSync is what `team create --force` must carry from the file it
// replaces (§4.1): the existing file is read with Parse's own checks and
// parser, and a replacement must never drop the project's `sync`
// declaration. (nil, nil): no file, or a file that declares none. A
// usable member comes back as parsed. A member the replacement could not
// carry faithfully — unusable, or in a file Parse refuses, or in a file
// Parse would not even read — is a `config` refusal naming only fixed
// tokens, and the remedy is to fix or remove the member first.
func CarriedSync(filePath string) (*SyncConfig, error) {
	data, err := readCapped(filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		var perr *protocol.Error
		if errors.As(err, &perr) {
			// A symlink, a special file, a world-writable or oversized file
			// is refused unread, so whether it declares `sync` is unknowable:
			// a replacement that went ahead could be dropping one.
			return nil, notCarried(filePath, perr.Details["reason"])
		}
		return nil, err
	}
	f, perr := parseBytes(filePath, data)
	if perr == nil {
		if f.SyncUnusable != "" {
			return nil, notCarried(filePath, f.SyncUnusable)
		}
		return f.Sync, nil
	}
	var raw map[string]jsontext.Value
	if json.Unmarshal(data, &raw) == nil {
		if _, ok := raw["sync"]; ok {
			var pe *protocol.Error
			reason := ReasonMalformed
			if errors.As(perr, &pe) {
				reason = pe.Details["reason"]
			}
			return nil, notCarried(filePath, reason)
		}
	}
	// A refused file with no `sync` member (not JSON at all, say) is
	// replaced as before: there is nothing in it to carry.
	return nil, nil
}

// notCarried is the refusal: the cause is a token from Reasons or
// SyncReasons, never file content.
func notCarried(filePath, cause string) *protocol.Error {
	e := refusal(filePath, ReasonSyncNotCarried,
		"the existing "+FileName+" may declare a sync member that team create --force cannot carry into the new file ("+cause+"); fix or remove that member, then run team create --force again")
	e.Details["cause"] = cause
	return e
}
