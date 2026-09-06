package supabase

import (
	"context"
	"errors"
	iofs "io/fs"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// adapterKind is the `adapter` member of the profile file (5.2).
const adapterKind = "supabase"

// profileStatusResult is `profile status` (a convention command, so the
// shape is this adapter's own). It never carries a token (4.5.14): the
// backend url and the token's expiry are the most it says about the
// credential.
type profileStatusResult struct {
	Name           string    `json:"name"`
	State          string    `json:"state"`
	URL            string    `json:"url,omitzero"`
	TeamRef        string    `json:"team_ref,omitzero"`
	TeamName       string    `json:"team_name,omitzero"`
	PrincipalRef   string    `json:"principal_ref,omitzero"`
	HumanLabel     string    `json:"human_label,omitzero"`
	TokenExpiresAt time.Time `json:"token_expires_at,omitzero"`
}

// profileActionResult is the answer of `profile init`, `profile reset`
// and `profile revoke-credentials`.
type profileActionResult struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	URL          string `json:"url,omitzero"`
	PrincipalRef string `json:"principal_ref,omitzero"`
}

// loadProfile reads team.json. present is false only when the file is
// ABSENT; a malformed or world-readable file is `config` (4.6), never a
// silent "unconfigured".
func (c *command) loadProfile() (*adapterkit.Profile, bool, error) {
	path, err := adapterkit.ProfilePath(c.cfgDir, c.profileName)
	if err != nil {
		return nil, false, err
	}
	if _, err := os.Lstat(path); errors.Is(err, iofs.ErrNotExist) {
		return nil, false, nil
	}
	p, err := adapterkit.LoadProfile(c.cfgDir, c.profileName)
	if err != nil {
		return nil, false, err
	}
	return p, true, nil
}

// profileDir is ${BRIGADE_CONFIG_DIR}/teams/<name>.
func (c *command) profileDir() (string, error) {
	return adapterkit.ProfileDir(c.cfgDir, c.profileName)
}

// identity reads the two local files behind `describe.profile.state`
// (4.4.1) and reports the state they imply: unconfigured (no profile
// file) → unauthenticated (profile, no usable session.json) → not_member
// (both, no team) → joined. It never dials (C-01, C-07). A malformed or
// world-readable profile is `config`: only ABSENCE is unconfigured.
func (c *command) identity() (string, *adapterkit.Profile, *session, error) {
	p, ok, err := c.loadProfile()
	if err != nil || !ok {
		return protocol.ProfileStateUnconfigured, nil, nil, err
	}
	cred, hasCred, err := c.loadCredential()
	if err != nil {
		return "", nil, nil, err
	}
	if !hasCred {
		return protocol.ProfileStateUnauthenticated, p, nil, nil
	}
	if p.TeamRef == "" {
		return protocol.ProfileStateNotMember, p, cred, nil
	}
	return protocol.ProfileStateJoined, p, cred, nil
}

// authenticate is the ladder of the adapter design's section 5, in order
// (4.6; C-06, C-08): no profile is `config`; a profile with no backend is
// `config`; no usable credential is `unauthenticated`; and — for every
// command that is not itself a binding command — no team is `config`
// ("credential present, no team bound"). A bound team_ref that is not
// uuid-shaped (the suite's default --rebind writes an arbitrary string)
// can name no team, so a team-scoped command answers the uniform
// `unauthorized` locally, byte-identical to a team that does not exist.
// On success c.profile, c.cred and c.client are set and nothing has
// dialled.
func (c *command) authenticate(needTeam bool) error {
	p, ok, err := c.loadProfile()
	if err != nil {
		return err
	}
	if !ok {
		return errNoProfile()
	}
	if p.URL == "" || p.PublishableKey == "" {
		return errNoBackend()
	}
	client, err := newClient(p.URL, p.PublishableKey, c.environ, c.log)
	if err != nil {
		return err
	}
	cred, ok, err := c.loadCredential()
	if err != nil {
		return err
	}
	if !ok {
		return errNoCredential()
	}
	c.profile, c.cred, c.client = p, cred, client
	if needTeam {
		if p.TeamRef == "" {
			return errNoTeam()
		}
		if !validUUID(p.TeamRef) {
			return errNotMember()
		}
	}
	return nil
}

// ensureIdentity is the binding commands' half of the ladder (`team
// create`, `team join`): the profile must exist and name a backend
// (`config` otherwise — this adapter needs `profile init` first, unlike
// the fs adapter), and a missing credential is minted by an anonymous
// sign-up (5.1: the only two commands that do). A profile that has lost
// its credential gets a NEW principal — the accepted trade-off of the
// logical plan — and the rejoin then keys on the team, not the principal.
func (c *command) ensureIdentity(ctx context.Context) error {
	p, ok, err := c.loadProfile()
	if err != nil {
		return err
	}
	if !ok {
		return errNoProfile()
	}
	if p.URL == "" || p.PublishableKey == "" {
		return errNoBackend()
	}
	client, err := newClient(p.URL, p.PublishableKey, c.environ, c.log)
	if err != nil {
		return err
	}
	c.profile, c.client = p, client
	cred, hasCred, err := c.loadCredential()
	if err != nil {
		return err
	}
	if hasCred {
		c.cred = cred
		return nil
	}
	return c.signUp(ctx)
}

// bind writes the team binding into team.json and remembers the team
// in session.json, so a later `team leave` on an already-unbound profile
// can still answer a non-empty team_ref (C-08).
func (c *command) bind(teamRef, teamName, humanLabel string) error {
	c.profile.TeamRef, c.profile.TeamName = teamRef, teamName
	c.profile.PrincipalRef, c.profile.HumanLabel = c.cred.principalRef(), humanLabel
	if err := adapterkit.SaveProfile(c.cfgDir, c.profileName, c.profile); err != nil {
		return err
	}
	return c.rememberTeam(teamRef)
}

// unbind clears the team binding (`team leave`, 4.2): the credential
// stays, so describe.profile.state becomes not_member and a later `team
// join` with the secret reuses the principal.
func (c *command) unbind() error {
	c.profile.TeamRef, c.profile.TeamName = "", ""
	return adapterkit.SaveProfile(c.cfgDir, c.profileName, c.profile)
}

// lastTeamRef is the team a `team leave` on an unbound profile answers
// with: the binding if any, else session.json's last_team_ref, else "".
func (c *command) lastTeamRef() string {
	if c.profile != nil && c.profile.TeamRef != "" {
		return c.profile.TeamRef
	}
	if c.cred != nil {
		return c.cred.LastTeamRef
	}
	return ""
}

// checkBackendURL is the https-only rule of 5.2: a backend url must use
// https unless its host is a loopback address (the local stack), because
// an http backend would send the anonymous JWT and the refresh token in
// clear on every command. A refusal is `invalid_input` with the plan's
// fixed message and details.field "url" (U-26).
func checkBackendURL(raw string) error {
	bad := func(reason string) error {
		return &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "backend url must use https (http is allowed only for 127.0.0.1/localhost)",
			Details: map[string]string{"field": "url", "reason": reason},
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return bad("malformed_url")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return bad("http_not_loopback")
	default:
		return bad("scheme_not_https")
	}
}

// isLoopbackHost accepts 127.0.0.1, ::1, any 127/8 address and localhost.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// secretKeyPrefix is the prefix of a Supabase secret key, assembled at
// run time so the shipped binary never carries the literal: the
// no-secrets scan (scripts/ci/no-secrets.sh) greps every built binary for
// that shape, and Go's string table would otherwise place the prefix
// right before some other literal and produce a false hit.
func secretKeyPrefix() string {
	return strings.Join([]string{"sb", "secret", ""}, "_")
}

// checkPublishableKey refuses a secret key on argv (T5: the secret and
// service-role keys must never appear in the adapter): a value with the
// sb_secret_ prefix is not configuration. Anything else — a publishable
// key or the legacy JWT-shaped anon key — is accepted as given.
func checkPublishableKey(key string) error {
	if key == "" {
		return errUsage("--key <publishable-key> is required")
	}
	if strings.HasPrefix(key, secretKeyPrefix()) {
		return &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "a secret key must never be used by the adapter; pass the publishable key",
			Details: map[string]string{"field": "key", "reason": "secret_key"},
		}
	}
	return nil
}

// profileCommand dispatches the `profile *` convention group.
func (c *command) profileCommand() (any, error) {
	switch c.verb {
	case "init":
		return c.profileInit()
	case "status":
		return c.profileStatus()
	case "reset":
		return c.profileReset()
	case "revoke-credentials":
		return c.profileRevokeCredentials()
	default:
		return nil, errUsage("unknown profile verb")
	}
}

// profileInit writes the backend pair of 5.2: `profile init --url <https
// url> --key <publishable key> [--force]`. Both values are configuration,
// not secrets, so argv is acceptable (4.1). An existing profile is
// `conflict` unless --force, which rewrites the two backend members only
// and never touches session.json or the team binding.
func (c *command) profileInit() (any, error) {
	fs := newFlags()
	backendURL := fs.String("url", "", "the Supabase project url (https, or http for a loopback host)")
	key := fs.String("key", "", "the project's publishable key")
	force := fs.Bool("force", false, "replace the backend of an existing profile")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if *backendURL == "" {
		return nil, errUsage("--url <https-url> is required")
	}
	if err := checkPublishableKey(*key); err != nil {
		return nil, err
	}
	if err := checkBackendURL(*backendURL); err != nil {
		return nil, err
	}
	p, exists, err := c.loadProfile()
	if err != nil && !*force {
		return nil, err
	}
	if exists && !*force {
		return nil, errConflict("this profile is already configured; pass --force to replace its backend", reasonProfileExists)
	}
	if p == nil {
		p = &adapterkit.Profile{
			Version:     adapterkit.ProfileVersion,
			Adapter:     adapterKind,
			SecretStore: adapterkit.SecretStoreFile,
			CreatedAt:   c.now().UTC(),
		}
	}
	p.URL, p.PublishableKey = strings.TrimRight(*backendURL, "/"), *key
	if err := adapterkit.SaveProfile(c.cfgDir, c.profileName, p); err != nil {
		return nil, err
	}
	state, _, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	return &profileActionResult{
		Name: c.profileName, State: state, URL: p.URL, PrincipalRef: cred.principalRef(),
	}, nil
}

// profileStatus reports the local state and the identity references. It
// touches neither the backend nor the network; the token itself is never
// printed, only its expiry (5.2).
func (c *command) profileStatus() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	state, p, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	out := &profileStatusResult{Name: c.profileName, State: state}
	if p != nil {
		out.URL, out.TeamRef, out.TeamName, out.HumanLabel = p.URL, p.TeamRef, p.TeamName, p.HumanLabel
	}
	if cred != nil {
		out.PrincipalRef = cred.principalRef()
		if exp, ok := cred.expiry(); ok {
			out.TokenExpiresAt = exp.UTC()
		}
	}
	return out, nil
}

// profileReset signs the principal out globally, best effort with a 5 s
// cap and failure ignored (5.1: a leaked copy of session.json then buys
// nothing), and deletes the profile directory.
func (c *command) profileReset() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	_, p, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	if p != nil && cred != nil && p.URL != "" && p.PublishableKey != "" {
		if c.client, err = newClient(p.URL, p.PublishableKey, c.environ, c.log); err != nil {
			return nil, err
		}
		c.cred = cred
		c.signOutBestEffort(c.ctx)
	}
	dir, err := c.profileDir()
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, errInternal("the profile directory could not be removed")
	}
	return &profileActionResult{Name: c.profileName, State: protocol.ProfileStateUnconfigured}, nil
}

// signOut revokes the credential's refresh-token family at the backend
// (the global sign-out of 5.1). The access token is refreshed FIRST — one
// /token call, under the lock — because GoTrue's /logout answers 401 for
// an access token it will not verify, an expired one after an idle hour
// above all, and a 401 taken for "already dead" would delete the local
// file while the family lived on: the opposite of what the command is
// for (measured live against GoTrue in the P2-6..P2-10 adversarial
// pass). A refresh that is terminal means the family is already gone —
// refreshCredential has removed session.json — so there is nothing left
// to revoke and the sign-out is done; any other refresh failure is
// reported as is and nothing is deleted.
func (c *command) signOut(ctx context.Context) error {
	token, err := c.forceRefresh(ctx)
	if err != nil {
		if perr := asProtocolError(err); perr.Code == protocol.CodeUnauthenticated && perr.Details["reason"] == reasonCredentialRevoked {
			c.log.Debug("the refresh-token family is already revoked; nothing to sign out")
			return nil
		}
		return err
	}
	return c.client.signOutGlobal(ctx, token)
}

// profileRevokeCredentials invalidates the credential at the backend
// (the global sign-out of 5.1, after the refresh signOut explains) and
// removes session.json, leaving team.json and its team binding in
// place for a rejoin (4.2). Unlike `profile reset`, a sign-out that
// cannot reach the backend is reported as `unavailable` and nothing is
// deleted, so the user can retry rather than be left with a live family
// and no local handle on it; a backend that says the freshly refreshed
// token is already dead counts as done.
func (c *command) profileRevokeCredentials() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	state, p, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	if cred != nil {
		if p.URL == "" || p.PublishableKey == "" {
			return nil, errNoBackend()
		}
		if c.client, err = newClient(p.URL, p.PublishableKey, c.environ, c.log); err != nil {
			return nil, err
		}
		c.cred = cred
		ctx, cancel := context.WithTimeout(c.ctx, signOutTimeout)
		defer cancel()
		if err := c.signOut(ctx); err != nil {
			return nil, err
		}
		if err := c.deleteCredential(); err != nil {
			return nil, err
		}
		state = protocol.ProfileStateUnauthenticated
	}
	return &profileActionResult{Name: c.profileName, State: state}, nil
}
