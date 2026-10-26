package syncthing

import (
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	engine "github.com/appshapes/brigade/internal/syncthing"
)

// revokeGrace is how long a revocation marker is left to reach the
// revoked member's machine before every member's engine drops its device
// from the team folder: once dropped, nothing syncs with it, so the
// marker that ends its watch must already be there.
const revokeGrace = 15 * time.Second

// engineTick is how often a running watch does an engine round
// (reconcile): accepting pending devices, posting the team folder,
// dropping revoked devices. Acceptance latency for a joiner is one tick
// of some member's watch.
const engineTick = 5 * time.Second

// folderID is the team folder's Syncthing id: "brigade-<team_ref first
// 8>-team-messages". Four dash-separated parts on purpose, so the sync
// adapter's teamPrefix never reads it as a checkout folder and never
// pauses it.
func folderID(teamRef string) string {
	ref := strings.ReplaceAll(teamRef, "-", "")
	if len(ref) > 8 {
		ref = ref[:8]
	}
	return "brigade-" + ref + "-team-messages"
}

// folderLabel is the team folder's label in Syncthing's own UI: the team
// reference's first characters, never the team's name (free text a
// member chose).
func folderLabel(teamRef string) string {
	ref := teamRef
	if len(ref) > 8 {
		ref = ref[:8]
	}
	return "Brigade team " + ref
}

// attached is a running engine with this machine's device id.
type attached struct {
	e   *engine.Engine
	api *engine.API
	id  string
}

// attach counts sessionID into the machine's engine, starting it when it
// is stopped, and answers its API and device id. In local mode it answers
// nil: there is no engine. holder is the process that holds the
// reference; this process's own pid for a command that exits at once (the
// reference is pruned at the next attach or detach) and for the watch,
// which detaches itself.
func (c *command) attach(sessionID string) (*attached, error) {
	e, err := c.engine()
	if err != nil || e == nil {
		return nil, err
	}
	id, _, err := e.Attach(sessionID, os.Getpid())
	if err != nil {
		return nil, err
	}
	api, err := e.API()
	if err != nil {
		return nil, err
	}
	return &attached{e: e, api: api, id: id}, nil
}

// replica is what one engine round reads from the team's directory under
// the lock, before any REST call.
type replica struct {
	team    *teamFile // nil while the team file has not arrived
	members []memberFile
	revoked []revokedFile
}

// readReplica reads the team's current membership under the lock.
func (c *command) readReplica(teamRef string) (*replica, error) {
	st, err := c.lockStore()
	if err != nil {
		return nil, err
	}
	defer st.close()
	r := &replica{}
	if r.team, _, err = st.loadTeam(teamRef); err != nil {
		return nil, err
	}
	if r.members, err = st.activeMembers(teamRef); err != nil {
		return nil, err
	}
	if r.revoked, err = st.revokedMarkers(teamRef); err != nil {
		return nil, err
	}
	return r, nil
}

// reconcile is one engine round for the profile's team (README, "The
// engine round"):
//
//   - a device waiting for acceptance whose announced name is a proof
//     keyed by the team's current digest is accepted, unless it is a
//     banned member's device; a revoked (not banned) member's device
//     whose proof is newer than the revocation is re-accepted and the
//     marker deleted — that is its rejoin;
//   - the team folder is posted with the union of the devices it already
//     has, the seeds, every active member's device and the devices
//     accepted this round — never this machine's own, never a revoked
//     member's — and with the change-detection delay at one second;
//   - a revoked member's device stays on the folder until its marker is
//     revokeGrace old, so the marker has had time to reach it; then it
//     leaves the folder and the engine.
//
// The replica is read under the store lock; the REST calls run without
// it. Nothing here is a rule about what the round may touch: the team
// folder is this adapter's own, and its device list is the membership.
func (c *command) reconcile(a *attached, teamRef string, seeds []string) error {
	r, err := c.readReplica(teamRef)
	if err != nil {
		return err
	}
	now := c.now()
	banned := map[string]bool{}
	marker := map[string]revokedFile{}
	// expired is the markers past the grace: their devices leave the
	// folder and the engine this round. A younger marker leaves its
	// device on the folder, so the marker itself reaches it.
	expired := map[string]bool{}
	for _, rv := range r.revoked {
		if rv.DeviceID == "" {
			continue
		}
		marker[rv.DeviceID] = rv
		if rv.Ban {
			banned[rv.DeviceID] = true
		}
		if now.Sub(rv.RevokedAt) >= revokeGrace {
			expired[rv.DeviceID] = true
		}
	}
	accepted, rejoined := c.acceptPending(a, r, banned, marker)
	if len(rejoined) > 0 {
		if err := c.deleteMarkers(teamRef, rejoined); err != nil {
			return err
		}
	}
	// The device set: what the folder has, plus the seeds, the members
	// and this round's acceptances; minus this machine and the revoked.
	want := map[string]string{}
	add := func(id, name string) {
		if id == "" || id == a.id || !checkDeviceID(id) {
			return
		}
		if expired[id] && !slices.Contains(rejoined, id) {
			return
		}
		if _, ok := want[id]; !ok || name != "" {
			want[id] = name
		}
	}
	folders, err := a.api.Folders()
	if err != nil {
		return err
	}
	var current *engine.ConfiguredFolder
	for i := range folders {
		if folders[i].ID == folderID(teamRef) {
			current = &folders[i]
		}
	}
	if current != nil {
		for _, d := range current.Devices {
			add(d.DeviceID, "")
		}
	}
	for _, s := range seeds {
		add(s, "brigade seed")
	}
	for _, m := range r.members {
		add(m.DeviceID, memberName(m))
	}
	for _, id := range accepted {
		add(id, "brigade member")
	}
	if err := c.ensureDevices(a, want); err != nil {
		return err
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	dir, err := c.rootPath()
	if err != nil {
		return err
	}
	path := (&store{root: dir}).teamDir(teamRef)
	if current == nil || current.Path != path || !sameDevices(current.Devices, ids) {
		refs := make([]engine.DeviceRef, 0, len(ids))
		for _, id := range ids {
			refs = append(refs, engine.DeviceRef{DeviceID: id})
		}
		if err := a.api.SetFolder(engine.FolderConfig{
			ID: folderID(teamRef), Label: folderLabel(teamRef), Path: path, Type: "sendreceive",
			Devices:          refs,
			Versioning:       engine.VersioningConfig{Type: "", Params: map[string]string{}},
			FSWatcherEnabled: true,
			FSWatcherDelayS:  1,
			RescanIntervalS:  60,
		}); err != nil {
			return err
		}
		c.log.Info("the team folder is shared", slog.Int("devices", len(ids)))
	}
	// Revoked devices past the grace leave the engine, and with it every
	// folder they were on.
	for id := range marker {
		if slices.Contains(rejoined, id) || !expired[id] {
			continue
		}
		known, err := a.api.Devices()
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(known, func(d engine.ConfiguredDevice) bool { return d.DeviceID == id }) {
			continue
		}
		if err := a.api.RemoveDevice(id); err != nil {
			c.log.Warn("a revoked member's device could not be removed; the next round tries again", adapterlog.Err(err))
			continue
		}
		c.log.Info("removed a revoked member's device from the engine")
	}
	return nil
}

// memberName is the Syncthing device name a member's device gets on this
// machine: its label when it has one, else the principal's first
// characters. Syncthing's UI is the only reader.
func memberName(m memberFile) string {
	if l := strings.Join(strings.Fields(m.HumanLabel), " "); l != "" {
		return "brigade: " + l
	}
	ref := m.PrincipalRef
	if len(ref) > 8 {
		ref = ref[:8]
	}
	return "brigade: " + ref
}

// acceptPending accepts the pending devices that carry a valid proof and
// answers their ids, and the ids among them whose revocation marker the
// proof's timestamp supersedes (their rejoin).
func (c *command) acceptPending(a *attached, r *replica, banned map[string]bool, marker map[string]revokedFile) (accepted, rejoined []string) {
	if r.team == nil {
		return nil, nil // no digest to verify against yet
	}
	pending, err := a.api.PendingDevices()
	if err != nil {
		c.log.Debug("pending devices could not be listed", adapterlog.Err(err))
		return nil, nil
	}
	for id, p := range pending {
		if id == a.id || !checkDeviceID(id) {
			continue
		}
		at, ok := verifyProof(r.team.JoinSecretSHA256, id, p.Name)
		if !ok {
			continue
		}
		if banned[id] {
			continue
		}
		if rv, revoked := marker[id]; revoked {
			if !at.After(rv.RevokedAt) {
				continue
			}
			rejoined = append(rejoined, id)
		}
		if err := a.api.SetDevice(engine.DeviceConfig{DeviceID: id, Name: "brigade member", Addresses: []string{"dynamic"}}); err != nil {
			c.log.Warn("a joining device could not be accepted", adapterlog.Err(err))
			continue
		}
		c.log.Info("accepted a joining device onto the team")
		accepted = append(accepted, id)
	}
	return accepted, rejoined
}

// ensureDevices adds to the engine every device of want it does not hold
// yet, with the name want gives it and dynamic addresses. A device
// Syncthing refuses (4xx) is logged by status and skipped, so one bad id
// never costs the team its round.
func (c *command) ensureDevices(a *attached, want map[string]string) error {
	known, err := a.api.Devices()
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(known))
	for _, d := range known {
		have[d.DeviceID] = true
	}
	for id, name := range want {
		if have[id] {
			continue
		}
		if name == "" {
			name = "brigade member"
		}
		if err := a.api.SetDevice(engine.DeviceConfig{DeviceID: id, Name: name, Addresses: []string{"dynamic"}}); err != nil {
			if s := engine.HTTPStatus(err); s >= 400 && s < 500 {
				c.log.Warn("syncthing refused a device; skipped", slog.Int("status", s))
				delete(want, id)
				continue
			}
			return err
		}
	}
	return nil
}

// deleteMarkers removes the revocation markers of the devices that
// rejoined, under the lock: a deletion, never a rewrite of the creator's
// file.
func (c *command) deleteMarkers(teamRef string, devices []string) error {
	st, err := c.lockStore()
	if err != nil {
		return err
	}
	defer st.close()
	markers, err := st.revokedMarkers(teamRef)
	if err != nil {
		return err
	}
	for _, rv := range markers {
		if !slices.Contains(devices, rv.DeviceID) {
			continue
		}
		if err := os.Remove(st.revokedPath(teamRef, rv.PrincipalRef)); err != nil && !os.IsNotExist(err) {
			return errInternal("a revocation marker could not be removed")
		}
		c.log.Info("a revoked member rejoined; its marker is removed")
	}
	return nil
}

// sameDevices reports whether a folder's device list names exactly ids.
func sameDevices(have []engine.DeviceRef, ids []string) bool {
	got := make([]string, 0, len(have))
	for _, d := range have {
		got = append(got, d.DeviceID)
	}
	slices.Sort(got)
	return slices.Equal(got, ids)
}

// announce names this machine's device with the joining proof, so the
// seeds it connects to can accept it (team join).
func (c *command) announce(a *attached, digestHex string) error {
	name, err := proofName(digestHex, a.id, c.now())
	if err != nil {
		return err
	}
	return a.api.PatchDeviceName(a.id, name)
}

// completeJoin finishes a pending join once the team file has reached
// this machine: the digest the join presented must be the team's, and
// then the member file records the secret version and the profile the
// team's name. A digest that no longer opens the team — a rotation
// happened before the folder arrived — leaves the membership as left,
// and the person joins again with the current secret. Called under the
// store lock by authorize.
func (c *command) completeJoin() error {
	if c.cred.JoinDigest == "" || c.profile.TeamRef == "" {
		return nil
	}
	teamRef, principal := c.profile.TeamRef, c.cred.PrincipalRef
	team, ok, err := c.st.loadTeam(teamRef)
	if err != nil || !ok {
		return err
	}
	m, found, err := c.st.loadMember(teamRef, principal)
	if err != nil {
		return err
	}
	if !found {
		m = &memberFile{PrincipalRef: principal, HumanLabel: c.profile.HumanLabel, JoinedAt: c.now().UTC()}
	}
	if team.JoinSecretSHA256 != c.cred.JoinDigest {
		m.Status = memberLeft
		c.log.Warn("the join secret no longer opens this team; join again with the current secret")
	} else {
		m.Status, m.SecretVersion = memberActive, team.SecretVersion
		c.profile.TeamName = team.TeamName
	}
	if err := writeJSON(c.st.memberPath(teamRef, principal), m); err != nil {
		return err
	}
	c.cred.JoinDigest = ""
	if err := c.saveCredential(c.cred); err != nil {
		return err
	}
	return c.saveProfile()
}
