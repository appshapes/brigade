package syncthing

import (
	"os"
	"slices"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// profileStatusResult is `profile status` (a convention command, so the
// shape is this adapter's own). It never carries a secret (4.5.14). The
// harness reads device_id after `team create` to write the seed into the
// project's team file, and team_folder to tell a joiner whether the team
// has reached this machine.
type profileStatusResult struct {
	Name         string   `json:"name"`
	State        string   `json:"state"`
	TeamRef      string   `json:"team_ref,omitzero"`
	TeamName     string   `json:"team_name,omitzero"`
	PrincipalRef string   `json:"principal_ref,omitzero"`
	HumanLabel   string   `json:"human_label,omitzero"`
	DeviceID     string   `json:"device_id,omitzero"`
	Engine       string   `json:"engine"`
	TeamFolder   string   `json:"team_folder"`
	Seeds        []string `json:"seeds"`
}

// The engine and team_folder words of profile status.
const (
	engineRunning = "running"
	engineStopped = "stopped"
	engineLocal   = "local"
	folderPresent = "present"
	folderPending = "pending"
	folderNone    = "none"
)

// profileActionResult is the answer of `profile init`, `profile reset` and
// `profile revoke-credentials`.
type profileActionResult struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	PrincipalRef string `json:"principal_ref,omitzero"`
}

// identity reads the two local files behind `describe.profile.state`
// (4.4.1) and reports the state they imply: unconfigured (no profile
// file) → unauthenticated (profile, no credential) → not_member (both, no
// team) → joined. A malformed or world-readable profile is `config`, never
// a silent "unconfigured": only ABSENCE is unconfigured.
func (c *command) identity() (string, *adapterkit.Profile, *credentialFile, error) {
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

// profileInit creates the profile directory, an unbound team.json and a
// credential.json carrying a fresh principal. An existing profile is
// `conflict` unless --force, which rewrites both files with a NEW
// principal (4.2).
func (c *command) profileInit() (any, error) {
	fs := newFlags()
	force := fs.Bool("force", false, "replace an existing profile with a fresh principal")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	_, exists, err := c.loadProfile()
	if err != nil && !*force {
		return nil, err
	}
	if exists && !*force {
		return nil, errConflict("this profile is already configured; pass --force to replace it", reasonProfileExists)
	}
	ref, err := newRef()
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	p := &adapterkit.Profile{
		Version:      adapterkit.ProfileVersion,
		Adapter:      Name,
		PrincipalRef: ref,
		SecretStore:  adapterkit.SecretStoreFile,
		CreatedAt:    now,
	}
	if err := adapterkit.SaveProfile(c.cfgDir, c.profileName, p); err != nil {
		return nil, err
	}
	if err := c.saveCredential(&credentialFile{PrincipalRef: ref, CreatedAt: now}); err != nil {
		return nil, err
	}
	return &profileActionResult{
		Name: c.profileName, State: protocol.ProfileStateNotMember, PrincipalRef: ref,
	}, nil
}

// profileStatus reports the local state, the identity references, this
// machine's device id, whether the engine runs and whether the team's
// folder has arrived. It starts nothing: the engine is asked only when
// its pidfile names a live daemon, and the store is read without the
// lock, one file.
func (c *command) profileStatus() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	state, p, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	out := &profileStatusResult{Name: c.profileName, State: state, Engine: engineStopped, TeamFolder: folderNone, Seeds: []string{}}
	if p != nil {
		out.TeamRef, out.TeamName, out.HumanLabel = p.TeamRef, p.TeamName, p.HumanLabel
		out.Seeds = slices.Clone(p.Devices)
		if out.Seeds == nil {
			out.Seeds = []string{}
		}
	}
	if cred != nil {
		out.PrincipalRef = cred.PrincipalRef
	}
	if c.local() {
		out.Engine = engineLocal
	} else if e, err := c.engine(); err == nil && e != nil {
		if api, err := e.Running(); err == nil {
			if st, err := api.SystemStatus(); err == nil {
				out.Engine, out.DeviceID = engineRunning, st.MyID
			}
		}
	}
	if p != nil && p.TeamRef != "" {
		root, err := c.rootPath()
		if err != nil {
			return nil, err
		}
		s := &store{root: root, now: c.now}
		out.TeamFolder = folderPending
		if s.teamExists(p.TeamRef) {
			out.TeamFolder = folderPresent
		}
		if out.DeviceID == "" && cred != nil {
			if m, ok, err := s.loadMember(p.TeamRef, cred.PrincipalRef); err == nil && ok {
				out.DeviceID = m.DeviceID
			}
		}
	}
	return out, nil
}

// profileReset deletes the profile directory. Before it does, and only as
// a best effort, it leaves the bound team — its own member file says
// left, its own sessions close — so a leaked copy of the credential file
// then buys nothing.
func (c *command) profileReset() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	state, p, cred, err := c.identity()
	if err != nil {
		return nil, err
	}
	if state == protocol.ProfileStateJoined {
		if err := c.openStore(); err != nil {
			return nil, err
		}
		if err := c.st.leaveTeam(p.TeamRef, cred.PrincipalRef); err != nil {
			return nil, err
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

// profileRevokeCredentials deletes credential.json only. The profile and
// its team binding stay, so the state becomes `unauthenticated` and a
// later `team join` with the bound team's secret is a rejoin that keeps the
// same principal_ref (4.2).
func (c *command) profileRevokeCredentials() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	path, err := c.credentialPath()
	if err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, errInternal("the credential file could not be removed")
	}
	state, _, _, err := c.identity()
	if err != nil {
		return nil, err
	}
	return &profileActionResult{Name: c.profileName, State: state}, nil
}

// ensureIdentity makes sure a profile and a credential exist before a
// binding command runs: `team create` and `team join` are the two commands
// a scratch principal may run with no setup at all (C-03, C-04). A profile
// that exists but has lost its credential gets a new one carrying the
// SAME principal_ref — that is the rejoin after `profile
// revoke-credentials`.
func (c *command) ensureIdentity() error {
	p, ok, err := c.loadProfile()
	if err != nil {
		return err
	}
	now := c.now().UTC()
	if !ok {
		p = &adapterkit.Profile{
			Version:     adapterkit.ProfileVersion,
			Adapter:     Name,
			SecretStore: adapterkit.SecretStoreFile,
			CreatedAt:   now,
		}
	}
	cred, hasCred, err := c.loadCredential()
	if err != nil {
		return err
	}
	if !hasCred {
		ref := p.PrincipalRef
		if ref == "" {
			if ref, err = newRef(); err != nil {
				return err
			}
		}
		cred = &credentialFile{PrincipalRef: ref, CreatedAt: now, LastTeamRef: p.TeamRef}
	}
	if p.PrincipalRef == "" {
		p.PrincipalRef = cred.PrincipalRef
	}
	if err := adapterkit.SaveProfile(c.cfgDir, c.profileName, p); err != nil {
		return err
	}
	if err := c.saveCredential(cred); err != nil {
		return err
	}
	c.profile, c.cred = p, cred
	return nil
}
