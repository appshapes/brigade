package rest

import (
	"context"
	"encoding/json/v2"
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// credential.json: the credential file beside team.json, mode 0600,
// written by adapterkit.WriteAtomic and read by adapterkit.ReadStrict,
// which refuses a copy that group or other can read. The credential is an
// opaque bearer string the server issued once (the contract's sections 1
// and 3); the server keeps its hash. There is nothing to refresh: it is
// valid until `/v1/credential/revoke`, or until a `team join` is handed a
// new one that replaces it.

// credentialFileName is the credential file.
//
//nolint:gosec // G101: this is a FILE NAME, not a credential; the file's content is the credential
const credentialFileName = "credential.json"

// The contract's bounds on a credential: 16 to 512 characters from
// `A-Z a-z 0-9 . _ ~ + / = -`.
const (
	minCredentialChars = 16
	maxCredentialChars = 512
)

// revokeTimeout caps the revocation request of `profile reset` and
// `profile revoke-credentials`.
const revokeTimeout = 5 * time.Second

// A credential is the content of credential.json. LastTeamRef keeps the
// team most recently bound or left, so a repeated `team leave` on an
// unbound profile can still answer a non-empty team_ref (C-08).
type credential struct {
	Credential   string    `json:"credential"`
	PrincipalRef string    `json:"principal_ref,omitzero"`
	LastTeamRef  string    `json:"last_team_ref,omitzero"`
	CreatedAt    time.Time `json:"created_at,omitzero"`
}

// validCredential reports whether s is a credential by the contract's
// rule. The alphabet has no space, no control character and no separator
// a header could be split on, which is what makes it safe to place after
// "Bearer ".
func validCredential(s string) bool {
	if len(s) < minCredentialChars || len(s) > maxCredentialChars {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '~' || c == '+' || c == '/' || c == '=' || c == '-':
		default:
			return false
		}
	}
	return true
}

// credentialPath is ${profileDir}/credential.json.
func (c *command) credentialPath() (string, error) {
	dir, err := c.profileDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, credentialFileName), nil
}

// errUnusableCredential marks a credential.json that exists but cannot
// authenticate anything: it is `unauthenticated`, never `config`, because
// the file is the adapter's own and `team join` replaces it.
var errUnusableCredential = errors.New("credential.json is not a usable credential")

// readCredentialFile reads and parses credential.json through the strict
// reader. A missing file is the underlying fs.ErrNotExist; a group- or
// world-readable one is `config`; a file that parses but holds no
// credential of the contract's shape is errUnusableCredential.
func (c *command) readCredentialFile() (*credential, error) {
	path, err := c.credentialPath()
	if err != nil {
		return nil, err
	}
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		return nil, err
	}
	var cred credential
	if err := json.Unmarshal(data, &cred); err != nil || !validCredential(cred.Credential) {
		return nil, errUnusableCredential
	}
	return &cred, nil
}

// loadCredential is the ladder's credential step. present is false for an
// absent or unusable file (both `unauthenticated`, 4.6); a world-readable
// file is the strict reader's `config`. The credential is registered on
// the redactor before it is returned.
func (c *command) loadCredential() (*credential, bool, error) {
	cred, err := c.readCredentialFile()
	switch {
	case err == nil:
		c.redactor.Add(cred.Credential)
		return cred, true, nil
	case errors.Is(err, iofs.ErrNotExist):
		return nil, false, nil
	case errors.Is(err, errUnusableCredential):
		c.log.Warn("credential.json is present but not a usable credential")
		return nil, false, nil
	default:
		return nil, false, err
	}
}

// saveCredential writes credential.json atomically, 0600, creating the
// 0700 profile directory when absent.
func (c *command) saveCredential(cred *credential) error {
	path, err := c.credentialPath()
	if err != nil {
		return err
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		return errInternal("the profile directory could not be created")
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return errInternal("the credential could not be encoded")
	}
	if err := adapterkit.WriteAtomic(path, append(data, '\n')); err != nil {
		c.log.Debug("credential write failed", adapterlog.Err(err))
		return errInternal("the credential could not be written")
	}
	return nil
}

// deleteCredential removes credential.json. A missing file is not an
// error.
func (c *command) deleteCredential() error {
	path, err := c.credentialPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return errInternal("the credential file could not be removed")
	}
	return nil
}

// bearer is the credential a request carries, or "" when the command
// holds none. The watch makes requests from two goroutines, so the
// credential is read and replaced under credMu.
func (c *command) bearer() string {
	c.credMu.Lock()
	defer c.credMu.Unlock()
	if c.cred == nil {
		return ""
	}
	return c.cred.Credential
}

// adoptStoredCredential answers a request the server refused as
// `unauthenticated`: it re-reads credential.json and, when the file holds
// another credential than the refused one, switches to it. It reports
// whether the command now holds a credential other than the refused one.
func (c *command) adoptStoredCredential(refused string) bool {
	c.credMu.Lock()
	defer c.credMu.Unlock()
	if c.cred == nil {
		return false
	}
	if c.cred.Credential != refused {
		return true
	}
	stored, err := c.readCredentialFile()
	if err != nil || stored.Credential == refused {
		return false
	}
	c.log.Debug("the credential file holds a newer credential; retrying with it")
	c.redactor.Add(stored.Credential)
	c.cred = stored
	return true
}

// acceptCredential stores a credential the server issued with a create or
// a join: it replaces whatever the profile held. A value outside the
// contract's shape is refused before anything is written.
func (c *command) acceptCredential(issued, principalRef string) error {
	if !validCredential(issued) {
		return errUnexpectedResponse("the backend issued a credential this adapter cannot use")
	}
	c.redactor.Add(issued)
	next := &credential{Credential: issued, PrincipalRef: principalRef, CreatedAt: c.now().UTC()}
	if c.cred != nil {
		next.LastTeamRef = c.cred.LastTeamRef
	}
	if err := c.saveCredential(next); err != nil {
		return err
	}
	c.cred = next
	return nil
}

// rememberTeam stores last_team_ref and the principal in credential.json,
// so a repeated `team leave` on an unbound profile still answers the team
// it left (C-08).
func (c *command) rememberTeam(teamRef, principalRef string) error {
	if c.cred == nil || (c.cred.LastTeamRef == teamRef && c.cred.PrincipalRef == principalRef) {
		return nil
	}
	c.cred.LastTeamRef, c.cred.PrincipalRef = teamRef, principalRef
	return c.saveCredential(c.cred)
}

// revoke asks the server to forget the credential (`/v1/credential/revoke`)
// within revokeTimeout. A server that no longer knows it has nothing left
// to forget, so `unauthenticated` counts as done.
func (c *command) revoke() error {
	ctx, cancel := context.WithTimeout(c.ctx, revokeTimeout)
	defer cancel()
	var out revokeResult
	err := c.do(ctx, &request{path: pathCredentialRevoke, out: &out})
	if err != nil && asProtocolError(err).Code == protocol.CodeUnauthenticated {
		return nil
	}
	return err
}
