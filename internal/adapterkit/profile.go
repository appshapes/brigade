package adapterkit

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// The profile file of 5.2:
// ${BRIGADE_CONFIG_DIR}/profiles/<name>/profile.json, mode 0600 in a 0700
// directory, carrying configuration and identity references — NEVER a
// secret. Credentials live beside it in the adapter's own file
// (session.json for the Supabase adapter), which is not adapterkit's
// schema.

// ProfileVersion is the profile file schema version this binary reads and
// writes.
const ProfileVersion = 1

// DefaultProfileName is the profile used when neither --profile nor
// BRIGADE_PROFILE names one (4.1).
const DefaultProfileName = "default"

// SecretStoreFile is the secret_store value for credentials kept in a
// file beside the profile — the only store v1 implements (5.2).
const SecretStoreFile = "file"

const (
	profilesDirName = "profiles"
	profileFileName = "profile.json"
)

// Profile is the 5.2 profile file schema. All fields but Version and
// Adapter are optional: `profile init` writes the backend pair, `team
// join`/`team create` fill in the team binding, and `team leave` clears
// it again.
type Profile struct {
	// Version is the schema version; ProfileVersion for files this binary
	// writes.
	Version int `json:"version"`
	// Adapter names the adapter that owns the profile, e.g. "supabase".
	Adapter string `json:"adapter"`
	// URL is the backend URL (Supabase: the project URL).
	URL string `json:"url,omitzero"`
	// PublishableKey is the backend's publishable API key. Publishable
	// keys are configuration, not secrets (5.1).
	PublishableKey string `json:"publishable_key,omitzero"`
	// TeamRef is the opaque reference of the team the profile is bound
	// to; empty while unbound.
	TeamRef string `json:"team_ref,omitzero"`
	// TeamName is the human name of that team.
	TeamName string `json:"team_name,omitzero"`
	// PrincipalRef is the opaque reference of the principal the profile's
	// credential authenticates.
	PrincipalRef string `json:"principal_ref,omitzero"`
	// HumanLabel is the optional operator-chosen label (e.g. an email).
	HumanLabel string `json:"human_label,omitzero"`
	// SecretStore names where the credential lives; SecretStoreFile in v1.
	SecretStore string `json:"secret_store,omitzero"`
	// CreatedAt is when the profile was first written (RFC 3339).
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Validate checks the members every profile file must carry. Failures are
// `config` (exit 11): the file exists but this binary cannot honour it.
func (p *Profile) Validate() error {
	if p.Version != ProfileVersion {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "profile file version is not supported by this binary",
			Details: map[string]string{"reason": "unsupported_version"},
		}
	}
	if p.Adapter == "" {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "profile file names no adapter",
			Details: map[string]string{"field": "adapter", "reason": "required"},
		}
	}
	return nil
}

// CheckProfileName validates a profile name before it is used as a path
// component: 1–64 characters from [A-Za-z0-9._-], not starting with a
// dot. That excludes path separators, "." and "..", so a hostile
// --profile or BRIGADE_PROFILE value cannot traverse out of the profiles
// directory. The offending value is deliberately not echoed.
func CheckProfileName(name string) error {
	bad := func() error {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "profile name must be 1-64 characters from letters, digits, dot, dash and underscore, and must not start with a dot",
			Details: map[string]string{"reason": "invalid_profile_name"},
		}
	}
	if name == "" || len(name) > 64 || name[0] == '.' {
		return bad()
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return bad()
		}
	}
	return nil
}

// ProfileDir returns ${configDir}/profiles/<name> after validating name.
func ProfileDir(configDir, name string) (string, error) {
	if err := CheckProfileName(name); err != nil {
		return "", err
	}
	return filepath.Join(configDir, profilesDirName, name), nil
}

// ProfilePath returns the profile.json path inside ProfileDir.
func ProfilePath(configDir, name string) (string, error) {
	dir, err := ProfileDir(configDir, name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, profileFileName), nil
}

// LoadProfile reads, parses and validates a profile file. Every failure
// is `config` (4.6: "profile missing, invalid or world-readable"): a
// missing file, a group- or world-readable file (via ReadStrict), a file
// that is not valid JSON (fixed message; the decoder's text is not
// echoed), and a file failing Validate.
func LoadProfile(configDir, name string) (*Profile, error) {
	path, err := ProfilePath(configDir, name)
	if err != nil {
		return nil, err
	}
	data, err := ReadStrict(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "profile is not configured; run `profile init` or `team join` first",
			Details: map[string]string{"profile": name, "reason": "profile_missing"},
		}
	}
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "profile file is not a valid JSON document",
			Details: map[string]string{"profile": name, "reason": "malformed_json"},
		}
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// SaveProfile validates p and writes it atomically as profile.json, mode
// 0600, creating the 0700 profile directory chain as needed.
func SaveProfile(configDir, name string, p *Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	dir, err := ProfileDir(configDir, name)
	if err != nil {
		return err
	}
	if err := MkdirPrivate(dir); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return WriteAtomic(filepath.Join(dir, profileFileName), append(data, '\n'))
}
