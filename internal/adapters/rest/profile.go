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
	Certificate  string `json:"certificate,omitzero"`
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
	Certificate  string `json:"certificate,omitzero"`
	PrincipalRef string `json:"principal_ref,omitzero"`
}

// backendDocument is the adapter-specific `backend` member of the `team
// join` request (4.4.10): {url}, and {certificate} when the team file pins
// the server's own certificate.
type backendDocument struct {
	URL         string `json:"url"`
	Certificate string `json:"certificate,omitzero"`
}

// A server is where a profile's backend is and how its certificate is
// trusted: the base url, and the pin of the server's own certificate
// (adapterkit.CertificateFingerprint) or "" for the usual verification.
type server struct {
	url         string
	certificate string
}

// checkBackend is the url rule and the pin rule together. A pin is the
// stored spelling and goes with an https url only: over http there is no
// certificate to compare it with, and a pin that silently did nothing would
// read as protection.
func checkBackend(b server) error {
	if err := checkBackendURL(b.url); err != nil {
		return err
	}
	if b.certificate == "" {
		return nil
	}
	if !adapterkit.ValidCertificatePin(b.certificate) {
		return errInvalidField("certificate", reasonInvalidValue, "certificate must be sha256: and the 64 hex digits of the server certificate's fingerprint")
	}
	if !strings.HasPrefix(strings.ToLower(b.url), "https://") {
		return errInvalidField("certificate", reasonCertificateWithoutTLS, "a certificate is pinned for an https backend only")
	}
	return nil
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
	if checkBackend(server{url: p.URL, certificate: p.Certificate}) != nil {
		return errConfig("the profile's pinned certificate is not usable: it must be sha256: and 64 hex digits, on an https backend", reasonBackendCertificate)
	}
	client, err := newClient(p.URL, p.Certificate, c.environ, c.log)
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
// profile's own when it has one, else the request's `backend` member
// (`team join` only), else BRIGADE_REST_URL; with none of those, no
// profile is `config` profile_missing and a profile without a url is
// `config` backend_unconfigured, the answers every other command gives. A
// `backend` member on a profile that already names a DIFFERENT backend is
// `conflict` (4.4.10: accepted only when the profile has no backend
// configured); the same backend again is accepted as a no-op. The pin is
// part of the backend: the same url under another certificate is a
// different backend, and only `profile init --force` moves a profile to it.
func (c *command) resolveBackend(p *adapterkit.Profile, raw jsontext.Value) (server, error) {
	var requested server
	hasRequest := len(raw) > 0
	if hasRequest {
		var doc backendDocument
		if err := json.Unmarshal(raw, &doc); err != nil || doc.URL == "" {
			return server{}, errInvalidField("backend", reasonInvalidValue, "backend must be an object with url")
		}
		requested = server{url: normalizeURL(doc.URL), certificate: doc.Certificate}
	}
	if p != nil && p.URL != "" {
		own := server{url: p.URL, certificate: p.Certificate}
		if hasRequest && (requested.url != normalizeURL(own.url) || requested.certificate != own.certificate) {
			return server{}, errConflict("this profile already names a different backend; run `profile init --force` to replace it", reasonProfileExists)
		}
		return own, nil
	}
	if hasRequest {
		if err := checkBackend(requested); err != nil {
			return server{}, err
		}
		return requested, nil
	}
	if env := adapterkit.Getenv(c.environ, backendURLVar); env != "" {
		if checkBackendURL(env) != nil {
			return server{}, errConfig(backendURLVar+" is not a usable backend url (https, or http for a loopback host)", reasonInvalidValue)
		}
		return server{url: normalizeURL(env)}, nil
	}
	if p == nil {
		return server{}, errNoProfile()
	}
	return server{}, errNoBackend()
}

// bootstrapProfile writes the resolved backend into team.json when the
// file does not carry it yet (the implicit `profile init` of a binding
// command run on a fresh profile) and leaves an already configured file
// alone. It runs after the request was validated, so a refused document
// creates nothing. The profile it returns is the one on disk.
func (c *command) bootstrapProfile(p *adapterkit.Profile, b server) (*adapterkit.Profile, error) {
	if p != nil && p.URL == b.url && p.Certificate == b.certificate {
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
	p.URL, p.Certificate = b.url, b.certificate
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

// profileInit writes the backend: `profile init --url <base>
// [--certificate sha256:<hex>] [--force]`. Both are configuration, not
// secrets, so argv is acceptable (4.1). --certificate pins the server's own
// certificate, for a server whose certificate no authority signed; it is
// read as a person copies it (the server's start line, or openssl's colon
// form) and stored in one spelling. An existing profile is `conflict`
// unless --force, which rewrites the backend only, the url and the pin
// together, and never touches credential.json or the team binding.
func (c *command) profileInit() (any, error) {
	fs := newFlags()
	backendURL := fs.String("url", "", "the server's base url (https, or http for a loopback host)")
	certificate := fs.String("certificate", "", "the SHA-256 fingerprint of the server's own certificate, to trust that certificate and no other")
	force := fs.Bool("force", false, "replace the backend of an existing profile")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if *backendURL == "" {
		return nil, errUsage("--url <https-url> is required")
	}
	wanted := server{url: normalizeURL(*backendURL)}
	if *certificate != "" {
		pin, ok := adapterkit.NormalizeCertificatePin(*certificate)
		if !ok {
			return nil, errInvalidField("certificate", reasonInvalidValue, "certificate must be the 64 hex digits of the server certificate's SHA-256 fingerprint")
		}
		wanted.certificate = pin
	}
	if err := checkBackend(wanted); err != nil {
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
	p.URL, p.Certificate = wanted.url, wanted.certificate
	if err := adapterkit.SaveProfile(c.cfgDir, c.profileName, p); err != nil {
		return nil, err
	}
	state, _, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	return &profileActionResult{
		Name: c.profileName, State: state, URL: p.URL, Certificate: p.Certificate, PrincipalRef: principalOf(p, cred),
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
		out.URL, out.Certificate = p.URL, p.Certificate
		out.TeamRef, out.TeamName, out.HumanLabel = p.TeamRef, p.TeamName, p.HumanLabel
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
