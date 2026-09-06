package supabase

import (
	"context"
	"encoding/json/v2"
	"errors"
	iofs "io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
)

// session.json (5.1, as corrected by E0-6): the credential file beside
// team.json, mode 0600, written by adapterkit.WriteAtomic. Every
// read-refresh-write runs under the flock on the sidecar
// session.json.lock (adapterkit.LockFile, 5 ms retries, 10 s bound), the
// file is RE-READ after the lock is acquired, a refresh happens when fewer
// than refreshMargin remain before the JWT's exp, and:
//
//   - one-behind is answered 200 by GoTrue with the active token — that is
//     what recovers a crash between the refresh answer and the atomic write
//     (E0-6 (d)/(f)), never an error;
//   - two-or-more-behind is 400 refresh_token_already_used AND THE FAMILY
//     SURVIVES, so the file is re-read once under the lock and the newer
//     token retried once before the credential is declared terminal;
//   - refresh_token_not_found, session_not_found, session_expired and
//     user_not_found are terminal at once: session.json is deleted and the
//     answer is exit 4 "credential revoked; run `brigade team join` again";
//   - a read-only profile directory (EROFS/EPERM, the Bash sandbox)
//     refreshes in memory, uses the token and persists nothing; the older
//     refresh token left in the file is redeemed one-behind by the next
//     writer.
//
// /token is never called in a loop: at most two calls per command.

// credentialFileName is the credential file of 5.1.
const credentialFileName = "session.json"

// refreshMargin is auth-js's margin (5.1): refresh when fewer than 90 s
// remain. PostgREST tolerates ~30 s past exp, so this is the normal path
// and PGRST303 is the cold-start path (E0-6).
const refreshMargin = 90 * time.Second

// signOutTimeout caps the best-effort global sign-out of `profile reset`
// and `profile revoke-credentials` (5.1).
const signOutTimeout = 5 * time.Second

// credentialPath is ${profileDir}/session.json.
func (c *command) credentialPath() (string, error) {
	dir, err := c.profileDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, credentialFileName), nil
}

// readCredentialFile reads and parses session.json through the strict
// reader. A missing file is the underlying fs.ErrNotExist; a group- or
// world-readable one is `config` (U-10); a file that parses but is not
// usable (no tokens, no readable exp) is returned with usable false.
func (c *command) readCredentialFile() (*session, error) {
	path, err := c.credentialPath()
	if err != nil {
		return nil, err
	}
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		return nil, err
	}
	var s session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, errUnusableCredential
	}
	if !s.usable() {
		return nil, errUnusableCredential
	}
	return &s, nil
}

// errUnusableCredential marks a session.json that exists but cannot
// authenticate anything: it is `unauthenticated`, never `config`, because
// the file is the adapter's own and `team join` replaces it.
var errUnusableCredential = errors.New("session.json is not a usable credential")

// loadCredential is the ladder's credential step. present is false for an
// absent or unusable file (both `unauthenticated`, 4.6); a world-readable
// file is the strict reader's `config`.
func (c *command) loadCredential() (*session, bool, error) {
	s, err := c.readCredentialFile()
	switch {
	case err == nil:
		return s, true, nil
	case errors.Is(err, iofs.ErrNotExist):
		return nil, false, nil
	case errors.Is(err, errUnusableCredential):
		c.log.Warn("session.json is present but not a usable credential")
		return nil, false, nil
	default:
		return nil, false, err
	}
}

// saveCredential writes session.json atomically, 0600, creating the
// 0700 profile directory when absent.
func (c *command) saveCredential(s *session) error {
	path, err := c.credentialPath()
	if err != nil {
		return err
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return errInternal("the credential could not be encoded")
	}
	return adapterkit.WriteAtomic(path, append(data, '\n'))
}

// deleteCredential removes session.json and its lock sidecar. A missing
// file is not an error.
func (c *command) deleteCredential() error {
	path, err := c.credentialPath()
	if err != nil {
		return err
	}
	for _, p := range []string{path, adapterkit.SidecarPath(path)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, iofs.ErrNotExist) {
			return errInternal("the credential file could not be removed")
		}
	}
	return nil
}

// readOnlyError reports whether err means the profile directory cannot
// be written: the read-only fallback of 5.1.
func readOnlyError(err error) bool {
	return errors.Is(err, iofs.ErrPermission) || errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EACCES)
}

// lockCredential takes the sidecar flock. When the sidecar cannot be
// opened for writing the directory is read-only and the command proceeds
// unlocked in memory (the sandboxed process of E0-6); a lock held by
// another process past the 10 s bound is `unavailable`.
func (c *command) lockCredential() (unlock func(), readOnly bool, err error) {
	path, err := c.credentialPath()
	if err != nil {
		return nil, false, err
	}
	lock, err := adapterkit.LockFile(adapterkit.SidecarPath(path), adapterkit.DefaultLockTimeout)
	if err != nil {
		if readOnlyError(err) {
			c.log.Debug("profile directory is read-only; refreshing in memory only")
			return func() {}, true, nil
		}
		return nil, false, err
	}
	return func() { _ = lock.Unlock() }, false, nil
}

// accessToken returns an access token with at least refreshMargin of
// life: the credential loaded by authenticate, refreshed under the lock
// when it is inside the margin.
func (c *command) accessToken(ctx context.Context) (string, error) {
	if c.cred == nil {
		return "", errNoCredential()
	}
	if c.cred.fresh(c.now(), refreshMargin) {
		return c.cred.AccessToken, nil
	}
	return c.refreshCredential(ctx, false)
}

// forceRefresh is the reactive path (5.1): the server refused the JWT
// (PGRST301/PGRST303), so refresh now whatever the local clock says —
// unless another process has already stored a fresher token, which is
// adopted without a call.
func (c *command) forceRefresh(ctx context.Context) (string, error) {
	if c.cred == nil {
		return "", errNoCredential()
	}
	return c.refreshCredential(ctx, true)
}

// fresh reports whether more than margin remains before the token's exp
// at now.
func (s *session) fresh(now time.Time, margin time.Duration) bool {
	exp, ok := s.expiry()
	return ok && exp.Sub(now) > margin
}

// refreshCredential is the state machine described at the top of this
// file. It runs under the sidecar lock, re-reads the file, decides, calls
// /token at most twice, persists atomically and updates c.cred.
func (c *command) refreshCredential(ctx context.Context, force bool) (string, error) {
	unlock, readOnly, err := c.lockCredential()
	if err != nil {
		return "", err
	}
	defer unlock()

	presented := c.cred
	changed := false
	if onDisk, rerr := c.readCredentialFile(); rerr == nil {
		changed = onDisk.RefreshToken != presented.RefreshToken
		presented = onDisk
	}
	// Another process stored a token with margin left: use it. On the
	// forced path only a CHANGED file counts — the token the server just
	// refused is not made good by re-reading it.
	if presented.fresh(c.now(), refreshMargin) && (!force || changed) {
		c.cred = presented
		return presented.AccessToken, nil
	}

	next, ae, err := c.client.refresh(ctx, presented.RefreshToken)
	if err != nil {
		return "", err
	}
	if next == nil && ae.code == authRefreshTokenAlreadyUsed {
		// Two or more behind, and the family survives (E0-6): re-read once,
		// retry once with a NEWER token, and only then go terminal.
		if onDisk, rerr := c.readCredentialFile(); rerr == nil && onDisk.RefreshToken != presented.RefreshToken {
			c.log.Debug("refresh token already used; retrying with the token another process stored")
			presented = onDisk
			if next, ae, err = c.client.refresh(ctx, presented.RefreshToken); err != nil {
				return "", err
			}
		}
	}
	if next == nil {
		if ae.terminal() || ae.code == authRefreshTokenAlreadyUsed {
			c.log.Warn("credential is terminal; removing session.json", slog.String("code", ae.code))
			if !readOnly {
				_ = c.deleteCredential()
			}
			c.cred = nil
			return "", errCredentialRevoked()
		}
		return "", mapAuthError(*ae)
	}
	next.LastTeamRef = presented.LastTeamRef
	c.cred = next
	if readOnly {
		return next.AccessToken, nil
	}
	if err := c.saveCredential(next); err != nil {
		if readOnlyError(err) {
			c.log.Warn("credential refreshed but not persisted: the profile directory is read-only")
			return next.AccessToken, nil
		}
		c.log.Debug("credential write failed", adapterlog.Err(err))
		return "", errInternal("the refreshed credential could not be written")
	}
	return next.AccessToken, nil
}

// signUp mints the profile's anonymous principal (5.1) and persists it.
// Only `team create` and `team join` call it, through ensureIdentity;
// every other command maps a missing credential to `unauthenticated`.
func (c *command) signUp(ctx context.Context) error {
	s, err := c.client.signUpAnonymous(ctx)
	if err != nil {
		return err
	}
	if err := c.saveCredential(s); err != nil {
		c.log.Debug("credential write failed", adapterlog.Err(err))
		return errInternal("the new credential could not be written")
	}
	c.cred = s
	return nil
}

// rememberTeam stores last_team_ref in session.json, so a repeated `team
// leave` on an unbound profile still answers the team it left (C-08).
// A read-only directory is not an error: the binding in team.json is
// what matters and this member is a courtesy.
func (c *command) rememberTeam(teamRef string) error {
	if c.cred == nil || c.cred.LastTeamRef == teamRef {
		return nil
	}
	c.cred.LastTeamRef = teamRef
	if err := c.saveCredential(c.cred); err != nil && !readOnlyError(err) {
		return err
	}
	return nil
}

// signOutBestEffort revokes the credential's refresh-token family at the
// backend with a 5 s cap (5.1), refreshing the access token first as
// signOut explains. It reports whether the sign-out reached a definite
// answer; a network failure is logged and reported false.
func (c *command) signOutBestEffort(ctx context.Context) bool {
	if c.cred == nil || c.client == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, signOutTimeout)
	defer cancel()
	if err := c.signOut(ctx); err != nil {
		c.log.Warn("global sign-out failed; the credential may still be valid at the backend", adapterlog.Err(err))
		return false
	}
	return true
}
