// Package teamstore is the user-side trust root of the repo-file model
// (plan P7-4, brief §3/§5): the team KEY that names a credential
// directory, the BINDING (`teams/<key>/team.json`, the adapterkit
// profile schema) verified field-by-field against a parsed repo file —
// the hash is never trusted alone — and the per-checkout PINS in
// `projects.json` that separate membership from attachment: a repo file
// attaches a session only where a human ran `team join` in that checkout.
//
// This package only READS the store. Every writer lives in the `write`
// subpackage, which `internal/harness/hook` is forbidden to import by a
// depguard rule — "the hook only reads the store" is a compile-time
// property, not a convention.
package teamstore

import (
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/protocol"
)

// KeyLen is the team key's length: 32 lowercase hex characters, which
// CheckProfileName accepts unchanged — the fact that keeps the frozen
// adapter protocol's --profile vocabulary usable as internal plumbing.
const KeyLen = 32

// Key derives the team key that names `teams/<key>/`: lowercase hex
// sha256 of the three values that identify a team across machines,
// newline-separated, truncated to KeyLen. The publishable key is
// deliberately NOT hashed: a key rotation must not move the credential
// directory. The adapter never computes this — only the harness does.
func Key(adapter, url, teamRef string) string {
	sum := sha256.Sum256([]byte(adapter + "\n" + url + "\n" + teamRef))
	return hex.EncodeToString(sum[:])[:KeyLen]
}

// LoadBinding reads `teams/<key>/team.json` through the adapterkit
// schema (correction 5: the kit schema is not forked). A missing binding
// answers fs.ErrNotExist for the caller to map.
func LoadBinding(configDir, key string) (*adapterkit.Profile, error) {
	return adapterkit.LoadProfile(configDir, key)
}

// VerifyBinding compares a binding against a parsed repo file on the
// four pinned members. team_name drift deliberately does NOT refuse: the
// display name is cosmetic and a PR may retitle a team without
// disconnecting anyone. The mismatch refusal names the member, never a
// value.
func VerifyBinding(b *adapterkit.Profile, f *teamfile.File) error {
	for _, c := range []struct{ field, binding, file string }{
		{"adapter", b.Adapter, f.Adapter},
		{"url", b.URL, f.URL},
		{"publishable_key", b.PublishableKey, f.PublishableKey},
		{"team_ref", b.TeamRef, f.TeamRef},
	} {
		if c.binding != c.file {
			return &protocol.Error{
				Code:    protocol.CodeConfig,
				Message: "the team binding does not match the project's team file",
				Details: map[string]string{"reason": "binding_mismatch", "field": c.field},
			}
		}
	}
	return nil
}

// A Pin records a human's consent: this checkout (keyed by the canonical
// path of the directory holding its team file) attaches to this team.
// The four members mirror VerifyBinding's; consented_at is when the
// human said yes.
type Pin struct {
	Adapter        string    `json:"adapter"`
	URL            string    `json:"url"`
	PublishableKey string    `json:"publishable_key"`
	TeamRef        string    `json:"team_ref"`
	ConsentedAt    time.Time `json:"consented_at"`
}

// Matches reports whether the pin and a parsed repo file agree on every
// pinned member — the hook's attach condition (brief §5).
func (p *Pin) Matches(f *teamfile.File) bool {
	return p.Adapter == f.Adapter && p.URL == f.URL &&
		p.PublishableKey == f.PublishableKey && p.TeamRef == f.TeamRef
}

// PinsFile is the projects.json shape shared with the write subpackage.
type PinsFile struct {
	Version  int            `json:"version"`
	Projects map[string]Pin `json:"projects"`
}

// PinsVersion is the projects.json schema version this binary writes.
const PinsVersion = 1

// PinsPath is `${configDir}/projects.json` — 0600, flock'd by writers,
// read here through ReadStrict so a group-readable pin store refuses.
func PinsPath(configDir string) string {
	return filepath.Join(configDir, "projects.json")
}

// LookupPin answers the pin for a canonical checkout directory. No pins
// file, or no entry, is (nil, false, nil) — the caller (the hook, at
// DEBUG) logs the canonical key it looked up, which is what makes an
// APFS case-fold mismatch diagnosable (review low fix 7).
func LookupPin(configDir, canonicalDir string) (*Pin, bool, error) {
	data, err := adapterkit.ReadStrict(PinsPath(configDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var pins PinsFile
	if err := json.Unmarshal(data, &pins); err != nil {
		return nil, false, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "projects.json does not parse; re-run `brigade team join` to rewrite it",
			Details: map[string]string{"reason": "pins_malformed"},
		}
	}
	p, ok := pins.Projects[canonicalDir]
	if !ok {
		return nil, false, nil
	}
	return &p, true, nil
}
