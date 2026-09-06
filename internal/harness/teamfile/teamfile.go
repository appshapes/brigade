// Package teamfile reads the one file a project may commit to name its
// Brigade team: `.brigade.json` at the repository toplevel (plan P7-2,
// brief §1). The file is discovery, never authority — it carries only
// public values, every reader shares this one parser, and nothing in it
// can name a path, a command or an option. The schema is CLOSED: an
// unknown member refuses loudly, naming the member and never its value,
// which is how `adapter_command`, `profile`, `config_dir`, `team_inbound`,
// `frame` and every future behavior-carrying field stays out for good.
//
// Refusals are `config` errors whose details.reason comes from the closed
// token list in Reasons; the SessionStart hook renders each token as its
// own fixed line (team_file_<reason>), so the list may not grow silently.
// File contents never appear in an error: the only file-sourced string
// any caller may surface is TeamName, which leaves this package already
// sanitized and capped.
package teamfile

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/protocol"
)

// FileName is the committed team file's name at the repository toplevel.
const FileName = ".brigade.json"

// MaxBytes is the hard size cap: six short public fields need well under
// 4 KiB, so anything larger is not a team file and is refused unread.
const MaxBytes = 4096

// The closed reason-token list (plan P7-2; the hook renders one fixed
// line per token). Adding a token here without a hook line and a test is
// caught by the enumeration test over Reasons.
const (
	ReasonNotRegularFile     = "not_regular_file"    // symlink, directory, FIFO, socket or device at the path
	ReasonWorldWritable      = "world_writable"      // mode grants write to other
	ReasonTooLarge           = "too_large"           // larger than MaxBytes
	ReasonSecretShaped       = "secret_shaped"       // the whole-file scan found a brg1. join-secret shape
	ReasonSecretKey          = "secret_key"          // publishable_key carries an sb_secret_ prefix
	ReasonMalformed          = "malformed"           // not a JSON object, or a required member missing/mistyped
	ReasonUnknownField       = "unknown_field"       // a member outside the closed schema
	ReasonUnsupportedVersion = "unsupported_version" // version is not 1
	ReasonURLNotHTTPS        = "url_not_https"       // url is not https and not loopback http
	ReasonAdapterUnknown     = "adapter_unknown"     // adapter is not a well-formed adapter name
)

// Reasons is the closed token list, in rendering order. Tests enumerate
// it so an unlisted token—or a token with no hook line—fails loudly.
func Reasons() []string {
	return []string{
		ReasonNotRegularFile, ReasonWorldWritable, ReasonTooLarge,
		ReasonSecretShaped, ReasonSecretKey, ReasonMalformed,
		ReasonUnknownField, ReasonUnsupportedVersion, ReasonURLNotHTTPS,
		ReasonAdapterUnknown,
	}
}

// File is a parsed team file. TeamName is ALREADY sanitized (T1) and
// capped to TeamNameMaxRunes — the raw committed string never leaves this
// package. Everything else is validated shape, safe to echo.
type File struct {
	Version        int
	Adapter        string
	URL            string
	PublishableKey string
	TeamRef        string
	TeamName       string
}

// TeamNameMaxRunes caps the one display string the repo may put in front
// of a human (brief §1: sanitized, ≤32).
const TeamNameMaxRunes = 32

// adapterName pins `adapter` to a dialect NAME — resolution of name to
// executable is strictly user-side, so the repo can never name code.
var adapterName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// secretShape is the launcher's tested brg1 join-secret rule: the bare
// prefix is legitimate text; only the full three-part shape is a secret.
var secretShape = regexp.MustCompile(`brg1\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{8,}`)

// members is the closed schema. True means required.
var members = map[string]bool{
	"version": true, "adapter": true, "url": true,
	"publishable_key": true, "team_ref": true, "team_name": false,
}

// Parse reads and validates the team file at path. Every check runs on
// the OPEN descriptor (the file cannot be swapped between check and
// read), the open itself refuses to follow a symlink, and a refused
// file's content is never surfaced anywhere.
func Parse(path string) (*File, error) {
	data, err := readCapped(path)
	if err != nil {
		return nil, err
	}
	if secretShape.Match(data) {
		return nil, refusal(path, ReasonSecretShaped,
			"the team file contains what looks like a join secret; remove it and rotate the secret now")
	}
	return parseDocument(path, data)
}

// secretLike reports whether a DECODED string is or carries a join
// secret. The raw-byte scan in Parse is evadable by JSON escaping
// (`brg1.…` decodes to a usable secret the raw scan never saw), and
// the shape regexp cannot cross a dotted team_ref that ParseJoinSecret
// deliberately allows — so every decoded value and member name passes
// through here too (verifier finding D1/D2).
func secretLike(s string) bool {
	if secretShape.MatchString(s) {
		return true
	}
	_, err := protocol.ParseJoinSecret(s)
	return err == nil
}

// readCapped opens without following symlinks and enforces the stat
// checks (regular, not world-writable, within MaxBytes) on the open
// descriptor before reading a byte.
func readCapped(path string) ([]byte, error) {
	// O_NONBLOCK keeps a FIFO at the path from hanging the open; once
	// Stat confirms a regular file it has no effect on the read.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors0(err, syscall.ELOOP, syscall.EMLINK) {
			return nil, refusal(path, ReasonNotRegularFile, "the team file must be a regular file, not a symbolic link")
		}
		// A socket or device refuses the open itself: EOPNOTSUPP (darwin
		// socket), ENXIO (linux socket, dead FIFO end), ENODEV (D3).
		if errors0(err, syscall.EOPNOTSUPP, syscall.ENXIO, syscall.ENODEV) {
			return nil, refusal(path, ReasonNotRegularFile, "the team file must be a regular file")
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, refusal(path, ReasonNotRegularFile, "the team file must be a regular file")
	}
	if fi.Mode().Perm()&0o002 != 0 {
		return nil, refusal(path, ReasonWorldWritable, "the team file is world-writable; anyone on this machine could repoint the team (chmod o-w it)")
	}
	if fi.Size() > MaxBytes {
		return nil, refusal(path, ReasonTooLarge, "the team file is larger than a team file can be")
	}
	data := make([]byte, fi.Size())
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, fmt.Errorf("teamfile: read %s: %w", path, err)
	}
	return data, nil
}

// errors0 reports whether err wraps any of the given errnos. EMLINK is
// what some filesystems answer for O_NOFOLLOW on a symlink; ELOOP is the
// POSIX answer.
func errors0(err error, targets ...syscall.Errno) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}

// parseDocument decodes the closed six-member schema. The unknown-member
// check runs over the member NAMES only, before any value is decoded, so
// a refusal can never echo a value.
func parseDocument(path string, data []byte) (*File, error) {
	var raw map[string]jsontext.Value
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, refusal(path, ReasonMalformed, "the team file is not a JSON object")
	}
	for name := range raw {
		if _, ok := members[name]; !ok {
			// A member NAME is decoded text too: an escaped secret smuggled
			// as a name must refuse as a secret, never echo (D1).
			if secretLike(name) {
				return nil, refusal(path, ReasonSecretShaped,
					"the team file contains what looks like a join secret; remove it and rotate the secret now")
			}
			e := refusal(path, ReasonUnknownField, "the team file's schema is closed; it carries a member this version does not define")
			e.Details["field"] = capRunes(protocol.SanitizeAttribute(name), TeamNameMaxRunes)
			return nil, e
		}
	}
	for name, required := range members {
		if _, ok := raw[name]; !ok && required {
			return nil, refusal(path, ReasonMalformed, "the team file is missing a required member: "+name)
		}
	}
	return validate(path, raw)
}

// validate types and checks each member of an already-closed document.
func validate(path string, raw map[string]jsontext.Value) (*File, error) {
	var f File
	if err := json.Unmarshal(raw["version"], &f.Version); err != nil || f.Version != 1 {
		if err != nil {
			return nil, refusal(path, ReasonMalformed, "the team file's version member must be a number")
		}
		return nil, refusal(path, ReasonUnsupportedVersion, "the team file names a version this build does not read")
	}
	for name, dst := range map[string]*string{
		"adapter": &f.Adapter, "url": &f.URL,
		"publishable_key": &f.PublishableKey, "team_ref": &f.TeamRef,
	} {
		if err := json.Unmarshal(raw[name], dst); err != nil || *dst == "" {
			return nil, refusal(path, ReasonMalformed, "the team file's "+name+" member must be a non-empty string")
		}
		if secretLike(*dst) {
			return nil, refusal(path, ReasonSecretShaped,
				"the team file contains what looks like a join secret; remove it and rotate the secret now")
		}
	}
	if name, ok := raw["team_name"]; ok {
		var s string
		if err := json.Unmarshal(name, &s); err != nil {
			return nil, refusal(path, ReasonMalformed, "the team file's team_name member must be a string")
		}
		if secretLike(s) {
			return nil, refusal(path, ReasonSecretShaped,
				"the team file contains what looks like a join secret; remove it and rotate the secret now")
		}
		f.TeamName = capRunes(protocol.SanitizeAttribute(s), TeamNameMaxRunes)
	}
	if !adapterName.MatchString(f.Adapter) {
		return nil, refusal(path, ReasonAdapterUnknown, "the team file's adapter member must be a lowercase adapter name, never a command or path")
	}
	if strings.HasPrefix(f.PublishableKey, "sb_secret_") {
		return nil, refusal(path, ReasonSecretKey, "the team file's publishable_key looks like a Supabase secret key; remove it and rotate that key in the dashboard")
	}
	if err := checkURL(path, f.URL); err != nil {
		return nil, err
	}
	return &f, nil
}

// checkURL mirrors the adapter's rule: https, or http only for a
// loopback host (127/8, ::1, localhost) — U-26.
func checkURL(path, raw string) error {
	bad := func() error {
		return refusal(path, ReasonURLNotHTTPS, "the team file's url must use https (http is allowed only for 127.0.0.1/localhost)")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return refusal(path, ReasonMalformed, "the team file's url member is not a URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return bad()
	default:
		return bad()
	}
}

// isLoopbackHost accepts 127/8, ::1 and localhost, matching the
// supabase adapter's own rule.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// capRunes truncates s to at most n runes on a rune boundary.
func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// refusal is the `config` failure of a team-file check: fixed text, a
// reason token from the closed list, and never a byte of file content.
func refusal(path, reason, message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: message,
		Details: map[string]string{"path": path, "reason": reason},
	}
}
