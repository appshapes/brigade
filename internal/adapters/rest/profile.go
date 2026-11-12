package rest

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	iofs "io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// adapterKind is the `adapter` member of the profile file.
const adapterKind = "rest"

// backendURLVar is the variable a binding command honours when the profile
// names no backend (4.1: a BRIGADE_<ADAPTER>_* variable is honoured only
// when a human, or the conformance suite, runs the adapter from a shell;
// under a live session it never arrives). The url is written to team.json
// exactly as `profile init` would write it, so every later command reads
// the file and never the environment; a profile that already names a
// backend is never overridden by the environment.
const backendURLVar = "BRIGADE_REST_URL"

// profileStatusResult is `profile status` (a convention command, so the
// shape is this adapter's own). It never carries the credential (4.5.14).
type profileStatusResult struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	URL          string `json:"url,omitzero"`
	TeamRef      string `json:"team_ref,omitzero"`
	TeamName     string `json:"team_name,omitzero"`
	PrincipalRef string `json:"principal_ref,omitzero"`
	HumanLabel   string `json:"human_label,omitzero"`
}

// profileActionResult is the answer of `profile init`, `profile reset`
// and `profile revoke-credentials`.
type profileActionResult struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	URL          string `json:"url,omitzero"`
	PrincipalRef string `json:"principal_ref,omitzero"`
}

// backendDocument is the adapter-specific `backend` member of the `team
// join` request (4.4.10): {url}.
type backendDocument struct {
	URL string `json:"url"`
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
// file), unauthenticated (profile, no usable credential.json), not_member
// (both, no team) or joined. It never dials (C-01, C-07). A malformed or
// world-readable profile is `config`: only ABSENCE is unconfigured.
func (c *command) identity() (string, *adapterkit.Profile, *credential, error) {
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

// principalOf is the principal_ref the local files name: the profile's,
// else the credential file's.
func principalOf(p *adapterkit.Profile, cred *credential) string {
	if p != nil && p.PrincipalRef != "" {
		return p.PrincipalRef
	}
	if cred != nil {
		return cred.PrincipalRef
	}
	return ""
}

// connect builds the client for a profile's backend. The url rule is
// applied again here, not only where the url was first stored: a team.json
// edited to an http url of another host must never be sent a credential.
func (c *command) connect(p *adapterkit.Profile) error {
	if p.URL == "" {
		return errNoBackend()
	}
	if checkBackendURL(p.URL) != nil {
		return errConfig("the profile's backend url is not usable: it must be https (http only for a loopback host)", reasonBackendURL)
	}
	client, err := newClient(p.URL, c.environ, c.log)
	if err != nil {
		return err
	}
	c.profile, c.client = p, client
	return nil
}

// authenticate is the ladder every command but the binding ones climbs,
// in order (4.6; C-06, C-08): no profile is `config`; a profile with no
// backend is `config`; no usable credential is `unauthenticated`; and, for
// a command that needs one, no team is `config` ("credential present, no
// team bound"). On success c.profile, c.cred and c.client are set and
// nothing has dialled.
func (c *command) authenticate(needTeam bool) error {
	p, ok, err := c.loadProfile()
	if err != nil {
		return err
	}
	if !ok {
		return errNoProfile()
	}
	if err := c.connect(p); err != nil {
		return err
	}
	cred, ok, err := c.loadCredential()
	if err != nil {
		return err
	}
	if !ok {
		return errNoCredential()
	}
	c.cred = cred
	if needTeam && p.TeamRef == "" {
		return errNoTeam()
	}
	return nil
}

// bind writes the team binding into team.json and remembers the team in
// credential.json, so a later `team leave` on an already-unbound profile
// can still answer a non-empty team_ref (C-08).
func (c *command) bind(teamRef, teamName, principalRef, humanLabel string) error {
	c.profile.TeamRef, c.profile.TeamName = teamRef, teamName
	c.profile.PrincipalRef, c.profile.HumanLabel = principalRef, humanLabel
	if err := adapterkit.SaveProfile(c.cfgDir, c.profileName, c.profile); err != nil {
		return err
	}
	return c.rememberTeam(teamRef, principalRef)
}

// unbind clears the team binding (`team leave`, 4.2): the credential
// stays, so describe.profile.state becomes not_member and a later `team
// join` with the secret acts for the same principal.
func (c *command) unbind() error {
	c.profile.TeamRef, c.profile.TeamName = "", ""
	return adapterkit.SaveProfile(c.cfgDir, c.profileName, c.profile)
}

// checkBackendURL is the url rule of the contract's section 1:
// `https://<host>[:<port>][/<prefix>]`, or `http://` for a loopback host
// only, because an http backend would carry the credential in clear on
// every request. Credentials in the url, a query and a fragment are
// refused. A refusal is `invalid_input` with details.field "url".
func checkBackendURL(raw string) error {
	bad := func(reason string) error {
		return errInvalidField("url", reason, "backend url must use https (http is allowed only for 127.0.0.1, ::1 and localhost)")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
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

// isLoopbackHost accepts the three hosts the contract names: 127.0.0.1,
// ::1 (in any spelling of the address) and localhost.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback))
}

// normalizeURL trims the url the way `profile init` stores it.
func normalizeURL(raw string) string {
	return strings.TrimRight(raw, "/")
}

// resolveBackend is where a binding command's backend comes from: the
// profile's own url when it has one, else the request's `backend` member
// (`team join` only), else BRIGADE_REST_URL; with none of those, no
// profile is `config` profile_missing and a profile without a url is
// `config` backend_unconfigured, the answers every other command gives. A
// `backend` member on a profile that already names a DIFFERENT backend is
// `conflict` (4.4.10: accepted only when the profile has no backend
// configured); the same backend again is accepted as a no-op.
func (c *command) resolveBackend(p *adapterkit.Profile, raw jsontext.Value) (string, error) {
	requested, hasRequest := "", len(raw) > 0
	if hasRequest {
		var doc backendDocument
		if err := json.Unmarshal(raw, &doc); err != nil || doc.URL == "" {
			return "", errInvalidField("backend", reasonInvalidValue, "backend must be an object with url")
		}
		requested = normalizeURL(doc.URL)
	}
	if p != nil && p.URL != "" {
		if hasRequest && requested != normalizeURL(p.URL) {
			return "", errConflict("this profile already names a different backend; run `profile init --force` to replace it", reasonProfileExists)
		}
		return p.URL, nil
	}
	if hasRequest {
		if err := checkBackendURL(requested); err != nil {
			return "", err
		}
		return requested, nil
	}
	if env := adapterkit.Getenv(c.environ, backendURLVar); env != "" {
		if checkBackendURL(env) != nil {
			return "", errConfig(backendURLVar+" is not a usable backend url (https, or http for a loopback host)", reasonInvalidValue)
		}
		return normalizeURL(env), nil
	}
	if p == nil {
		return "", errNoProfile()
	}
	return "", errNoBackend()
}

// bootstrapProfile writes the resolved backend url into team.json when the
// file does not carry it yet (the implicit `profile init` of a binding
// command run on a fresh profile) and leaves an already configured file
// alone. It runs after the request was validated, so a refused document
// creates nothing. The profile it returns is the one on disk.
func (c *command) bootstrapProfile(p *adapterkit.Profile, backendURL string) (*adapterkit.Profile, error) {
	if p != nil && p.URL == backendURL {
		return p, nil
	}
	if p == nil {
		p = &adapterkit.Profile{
			Version:     adapterkit.ProfileVersion,
			Adapter:     adapterKind,
			SecretStore: adapterkit.SecretStoreFile,
			CreatedAt:   c.now().UTC(),
		}
	}
	p.URL = backendURL
	if err := adapterkit.SaveProfile(c.cfgDir, c.profileName, p); err != nil {
		return nil, err
	}
	return p, nil
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

// profileInit writes the backend url: `profile init --url <base>
// [--force]`. The url is configuration, not a secret, so argv is
// acceptable (4.1). An existing profile is `conflict` unless --force,
// which rewrites the url only and never touches credential.json or the
// team binding.
func (c *command) profileInit() (any, error) {
	fs := newFlags()
	backendURL := fs.String("url", "", "the server's base url (https, or http for a loopback host)")
	force := fs.Bool("force", false, "replace the backend of an existing profile")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if *backendURL == "" {
		return nil, errUsage("--url <https-url> is required")
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
	p.URL = normalizeURL(*backendURL)
	if err := adapterkit.SaveProfile(c.cfgDir, c.profileName, p); err != nil {
		return nil, err
	}
	state, _, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	return &profileActionResult{
		Name: c.profileName, State: state, URL: p.URL, PrincipalRef: principalOf(p, cred),
	}, nil
}

// profileStatus reports the local state and the identity references. It
// touches neither the server nor the network, and the credential itself is
// never printed.
func (c *command) profileStatus() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	state, p, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	out := &profileStatusResult{Name: c.profileName, State: state, PrincipalRef: principalOf(p, cred)}
	if p != nil {
		out.URL, out.TeamRef, out.TeamName, out.HumanLabel = p.URL, p.TeamRef, p.TeamName, p.HumanLabel
	}
	return out, nil
}

// profileReset asks the server to forget the credential, best effort with
// a 5 s cap and failure ignored (a leaked copy of credential.json then
// buys nothing), and deletes the profile directory.
func (c *command) profileReset() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	_, p, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	if p != nil && cred != nil && c.connect(p) == nil {
		c.cred = cred
		if rerr := c.revoke(); rerr != nil {
			c.log.Warn("the backend did not confirm the revocation; the credential may still be valid there",
				slog.String("code", string(asProtocolError(rerr).Code)))
		}
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

// profileRevokeCredentials makes the server forget the credential and
// removes credential.json, leaving team.json and its team binding in place
// for a rejoin (4.2). Unlike `profile reset`, a revocation that cannot
// reach the server is reported as it failed and nothing is deleted, so the
// user can retry rather than be left with a live credential and no local
// handle on it.
func (c *command) profileRevokeCredentials() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	state, p, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	if cred != nil {
		if err := c.connect(p); err != nil {
			return nil, err
		}
		c.cred = cred
		if err := c.revoke(); err != nil {
			return nil, err
		}
		if err := c.deleteCredential(); err != nil {
			return nil, err
		}
		state = protocol.ProfileStateUnauthenticated
	}
	return &profileActionResult{Name: c.profileName, State: state, URL: urlOf(p)}, nil
}

// urlOf is the profile's backend url, or "" without a profile.
func urlOf(p *adapterkit.Profile) string {
	if p == nil {
		return ""
	}
	return p.URL
}
