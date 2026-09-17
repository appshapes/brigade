// Package account reads the Claude account's email address from Claude
// Code's own configuration file, best effort and READ-ONLY (Trello card
// 24, part B): the `oauthAccount.emailAddress` string in `.claude.json`,
// which Claude Code keeps at $CLAUDE_CONFIG_DIR/.claude.json when that
// variable is set and at $HOME/.claude.json otherwise (E0-5, E0-7 — E0-7
// also records a fresh config directory whose `.claude.json` has no
// `oauthAccount` key at all, which is exactly the "" case below).
//
// It exists for ONE purpose: the default display label a `team create` or
// `team join` sends when it was given no --label, so a roster reads as
// people rather than as UUIDs (P15-1). The value is an ordinary unverified
// label like any other and proves nothing; the server-stamped principal
// remains the identity.
//
// Best effort without exception. A missing file, an unreadable one,
// malformed JSON, a missing `oauthAccount`, a missing, empty or
// wrong-typed `emailAddress` — every one of them answers "" with a
// DIAGNOSTIC error, worth a debug line and never an action. Nothing here
// ever writes, and the only names it opens are the two `.claude.json`
// paths: the 0600 $CLAUDE_CONFIG_DIR/sessions/*.key peer keys that live
// under the same directory are never read, the same rule
// internal/harness/registry follows.
//
// The file's contents are Claude Code's to change, so the value leaves
// this package through protocol.SanitizeLabel — the control-character
// rules and the MaxHumanLabelChars cap — and this package never logs it,
// never puts it on argv and never writes it to a file.
package account

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
)

// FileName is the one file name this package opens, under either of the
// two directories Paths names.
const FileName = ".claude.json"

// MaxFileBytes caps what Email reads of one file. `.claude.json` is not a
// small file — it accumulates per-project history — so the cap is
// generous; it is here to bound the work on a corrupt or hostile file,
// not to police a real one. The document is streamed into the decoder and
// only the `oauthAccount` subtree is retained.
const MaxFileBytes = 64 << 20

// The diagnostic errors. Each is fixed text, and every failure answers
// one of them: neither the path (it carries the member's home directory
// and their CLAUDE_CONFIG_DIR) nor the value is ever part of one, so the
// caller's debug line is safe to write as it stands.
var (
	// ErrNoConfigDir: neither CLAUDE_CONFIG_DIR nor HOME gives an absolute
	// directory, so there is no file to look in.
	ErrNoConfigDir = errors.New("account: no Claude Code config directory to read")
	// ErrNoFile: the file is not there — the ordinary case on a machine
	// that has never signed in.
	ErrNoFile = errors.New("account: no Claude Code config file to read")
	// ErrUnreadable: the file is there and could not be opened, for any
	// other reason (a mode, a directory in its place).
	ErrUnreadable = errors.New("account: the Claude Code config file cannot be opened")
	// ErrMalformed: the file could not be read to the end, or is not a
	// JSON object.
	ErrMalformed = errors.New("account: the Claude Code config file is not readable as a JSON object")
	// ErrNoEmail: the file parsed but carries no usable
	// oauthAccount.emailAddress.
	ErrNoEmail = errors.New("account: the Claude Code config file carries no account email")
)

// Paths are the files Email reads, in order: the one under the Claude
// Code config directory (CLAUDE_CONFIG_DIR when set, else HOME/.claude —
// config.ClaudeConfigDir, never a hardcoded ~/.claude), then the one
// directly under HOME. Empty when neither can be resolved.
func Paths(environ []string) []string {
	var paths []string
	if dir, err := config.ClaudeConfigDir(environ); err == nil {
		paths = append(paths, filepath.Join(dir, FileName))
	}
	if home := adapterkit.Getenv(environ, "HOME"); filepath.IsAbs(home) {
		if p := filepath.Join(filepath.Clean(home), FileName); !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	return paths
}

// Email answers the sanitised account email for environ, or "" when there
// is none to be had. The config-directory file wins: the home file is
// read only when the first names no email, for whatever reason. The
// returned error is DIAGNOSTIC ONLY — a debug line at most, never an
// action — and is the FIRST failure seen, so a missing first file with a
// malformed second one still reports something useful. A "" answer with a
// nil error cannot happen: every empty answer carries a reason.
func Email(environ []string) (string, error) {
	paths := Paths(environ)
	if len(paths) == 0 {
		return "", ErrNoConfigDir
	}
	var first error
	for _, p := range paths {
		email, err := emailFrom(p)
		if email != "" {
			return email, nil
		}
		if first == nil {
			first = err
		}
	}
	return "", first
}

// emailFrom reads one file. The decode is two steps so that a member of
// an unexpected SHAPE costs only itself: the document yields the raw
// `oauthAccount` value, and only that subtree is parsed further — the
// tolerance internal/harness/registry applies to the session registry,
// for the same reason (the format is Claude Code's to change).
func emailFrom(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		// The open error is NOT returned as it came: it carries the path,
		// and the path carries the member's home directory and their
		// CLAUDE_CONFIG_DIR, neither of which the redacting logger knows
		// to redact. It becomes one of the two fixed-text sentinels, so
		// the debug line the caller writes says what happened and names
		// nothing of the member's.
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNoFile
		}
		return "", ErrUnreadable
	}
	defer func() { _ = f.Close() }()
	var doc struct {
		OAuthAccount jsontext.Value `json:"oauthAccount"`
	}
	if err := json.UnmarshalRead(io.LimitReader(f, MaxFileBytes+1), &doc); err != nil {
		return "", ErrMalformed
	}
	var acct map[string]jsontext.Value
	if len(doc.OAuthAccount) == 0 || json.Unmarshal(doc.OAuthAccount, &acct) != nil {
		return "", ErrNoEmail
	}
	var email string
	if raw, ok := acct["emailAddress"]; !ok || json.Unmarshal(raw, &email) != nil {
		return "", ErrNoEmail
	}
	if email = protocol.SanitizeLabel(strings.TrimSpace(email)); email == "" {
		return "", ErrNoEmail
	}
	return email, nil
}
