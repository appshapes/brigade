package syncthing

import (
	"encoding/json/v2"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// The three team.admin verbs (4.2, 4.7: adapter-defined, creator only).
// Their request and result shapes are the Supabase adapter's, so the
// harness's `team rotate-secret`, `team revoke-member` and `team transfer`
// read them unchanged. Every one of them rewrites team.json or writes a
// marker under revoked/ — the creator's files — and never a member's own.

type teamRotateSecretResult struct {
	TeamRef       string    `json:"team_ref"`
	TeamName      string    `json:"team_name"`
	SecretVersion int       `json:"secret_version"`
	RotatedAt     time.Time `json:"rotated_at"`
	SecretFile    string    `json:"secret_file"`
}

// teamRevokeRequest is the stdin document of `team revoke-member`, in one
// of two mutually exclusive forms: {principal_ref, ban} revokes (or bans)
// one member; {joined_secret_version_lte} revokes every member that joined
// at or below that secret version.
type teamRevokeRequest struct {
	PrincipalRef string `json:"principal_ref,omitzero"`
	Ban          bool   `json:"ban,omitzero"`
	MaxVersion   *int   `json:"joined_secret_version_lte,omitzero"`
}

type teamRevokeMemberResult struct {
	TeamRef        string `json:"team_ref"`
	PrincipalRef   string `json:"principal_ref"`
	Status         string `json:"status"`
	Changed        bool   `json:"changed"`
	SessionsClosed int    `json:"sessions_closed"`
}

type teamRevokeByVersionResult struct {
	TeamRef        string `json:"team_ref"`
	MaxVersion     int    `json:"max_version"`
	Revoked        int    `json:"revoked"`
	SessionsClosed int    `json:"sessions_closed"`
}

type teamTransferResult struct {
	TeamRef      string `json:"team_ref"`
	PrincipalRef string `json:"principal_ref"`
	Transferred  bool   `json:"transferred"`
}

const (
	statusRevoked = "revoked"
	statusBanned  = "banned"
)

// requireCreator is the ladder for an admin verb: a member, and the
// team's creator; the team file must be here to say who that is.
func (c *command) requireCreator() (*teamFile, error) {
	if err := c.teamScoped(); err != nil {
		return nil, err
	}
	team, ok, err := c.st.loadTeam(c.profile.TeamRef)
	if err != nil {
		return nil, err
	}
	if !ok || team.CreatedBy != c.cred.PrincipalRef {
		return nil, errNotCreator()
	}
	return team, nil
}

// teamRotateSecret mints a fresh secret, bumps secret_version and writes
// the new digest into team.json. Rotation revokes nobody: every member
// stays; a version revoke (`--max-version`) is the eviction. --secret-file
// is required: the new secret goes there, mode 0600, and nowhere else.
func (c *command) teamRotateSecret() (any, error) {
	fs := newFlags()
	secretFile := fs.String("secret-file", "", "write the new join secret to this path (0600); required")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if *secretFile == "" {
		return nil, errUsage("--secret-file is required: the new join secret is written there and never printed")
	}
	if err := checkSecretFile(*secretFile); err != nil {
		return nil, err
	}
	team, err := c.requireCreator()
	if err != nil {
		return nil, err
	}
	secret, err := newJoinSecret(team.TeamRef)
	if err != nil {
		return nil, err
	}
	if err := adapterkit.WriteAtomic(*secretFile, []byte(secret+"\n")); err != nil {
		return nil, errConfig("the file named by --secret-file could not be written", "secret_file_unwritable")
	}
	now := c.now().UTC()
	team.JoinSecretSHA256 = sha256hex(secret)
	team.SecretVersion++
	team.SecretRotatedAt = &now
	if err := writeJSON(c.st.teamPath(team.TeamRef), team); err != nil {
		return nil, err
	}
	c.warnSecretShownOnce("the new join secret was written to the file named by --secret-file (mode 0600); the old one no longer opens the team")
	return &teamRotateSecretResult{
		TeamRef: team.TeamRef, TeamName: team.TeamName, SecretVersion: team.SecretVersion,
		RotatedAt: now, SecretFile: *secretFile,
	}, nil
}

// teamRevokeMember writes the creator's revocation marker for one member
// (`--principal <ref> [--ban]`, or the stdin document) or for every member
// that joined at or below a secret version (`--max-version <n>`), never
// for the creator. Every member's reads filter on the marker at once;
// the member's device leaves every engine once the marker is revokeGrace
// old. A marker rewritten without --ban un-bans: the member may then
// rejoin with the current secret.
func (c *command) teamRevokeMember() (any, error) {
	fs := newFlags()
	principal := fs.String("principal", "", "the principal_ref to revoke (instead of stdin)")
	ban := fs.Bool("ban", false, "with --principal: also block a rejoin with the current secret")
	maxVersion := fs.Int("max-version", 0, "revoke every member that joined at or below this secret version (instead of stdin)")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	req, err := c.teamRevokeRequest(*principal, *ban, *maxVersion, flagsSeen(fs))
	if err != nil {
		return nil, err
	}
	team, err := c.requireCreator()
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	if req.MaxVersion != nil {
		members, err := c.st.activeMembers(team.TeamRef)
		if err != nil {
			return nil, err
		}
		out := &teamRevokeByVersionResult{TeamRef: team.TeamRef, MaxVersion: *req.MaxVersion}
		for _, m := range members {
			if m.PrincipalRef == c.cred.PrincipalRef || m.SecretVersion > *req.MaxVersion {
				continue
			}
			closed, err := c.st.revoke(team.TeamRef, m, c.cred.PrincipalRef, false, now)
			if err != nil {
				return nil, err
			}
			out.Revoked++
			out.SessionsClosed += closed
		}
		return out, nil
	}
	if req.PrincipalRef == c.cred.PrincipalRef {
		return nil, errInvalidField("principal_ref", "invalid_value", "you cannot revoke yourself; leaving is `team leave`")
	}
	m, found, err := c.st.loadMember(team.TeamRef, req.PrincipalRef)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errNotFound()
	}
	status := statusRevoked
	if req.Ban {
		status = statusBanned
	}
	existing, had, err := c.st.loadRevoked(team.TeamRef, req.PrincipalRef)
	if err != nil {
		return nil, err
	}
	if had && existing.Ban == req.Ban {
		return &teamRevokeMemberResult{TeamRef: team.TeamRef, PrincipalRef: req.PrincipalRef, Status: status, Changed: false}, nil
	}
	closed, err := c.st.revoke(team.TeamRef, *m, c.cred.PrincipalRef, req.Ban, now)
	if err != nil {
		return nil, err
	}
	return &teamRevokeMemberResult{
		TeamRef: team.TeamRef, PrincipalRef: req.PrincipalRef, Status: status, Changed: true, SessionsClosed: closed,
	}, nil
}

// revoke writes the marker for one member and answers how many of its
// sessions were open: they are closed from the team's point of view — the
// files are the member's own and stay as they are.
func (s *store) revoke(team string, m memberFile, by string, ban bool, now time.Time) (int, error) {
	marker := &revokedFile{PrincipalRef: m.PrincipalRef, DeviceID: m.DeviceID, RevokedAt: now, Ban: ban, By: by}
	if err := writeJSON(s.revokedPath(team, m.PrincipalRef), marker); err != nil {
		return 0, err
	}
	_, open, err := s.memberActivity(team, m.PrincipalRef, now)
	return open, err
}

// teamRevokeRequest builds the revoke request from the flags, or from the
// stdin document when no flag was given.
func (c *command) teamRevokeRequest(principal string, ban bool, maxVersion int, seen map[string]bool) (*teamRevokeRequest, error) {
	if seen["principal"] || seen["max-version"] {
		if seen["principal"] && seen["max-version"] {
			return nil, errUsage("--principal and --max-version are mutually exclusive")
		}
		if ban && !seen["principal"] {
			return nil, errUsage("--ban needs --principal (in the stdin form, set ban in the document)")
		}
		if seen["max-version"] {
			if maxVersion < 0 {
				return nil, errUsage("--max-version must not be negative")
			}
			return &teamRevokeRequest{MaxVersion: &maxVersion}, nil
		}
		if principal == "" || !safeRef(principal) {
			return nil, errUsage("--principal was given an unusable value")
		}
		return &teamRevokeRequest{PrincipalRef: principal, Ban: ban}, nil
	}
	data, err := adapterkit.ReadInput(c.stdin)
	if err != nil {
		return nil, err
	}
	req := &teamRevokeRequest{}
	if err := json.Unmarshal(data, req); err != nil {
		return nil, errInvalidField("principal_ref", "invalid_value", "the request is not a JSON object")
	}
	switch {
	case req.MaxVersion != nil && req.PrincipalRef != "":
		return nil, errInvalidField("principal_ref", "invalid_value", "principal_ref and joined_secret_version_lte are mutually exclusive")
	case req.MaxVersion != nil && req.Ban:
		return nil, errInvalidField("ban", "invalid_value", "ban applies to principal_ref only; a version revoke never bans")
	case req.MaxVersion != nil:
		if *req.MaxVersion < 0 {
			return nil, errInvalidField("joined_secret_version_lte", "invalid_value", "the version must not be negative")
		}
	case req.PrincipalRef == "" || !safeRef(req.PrincipalRef):
		return nil, errInvalidField("principal_ref", "required", "principal_ref is required")
	}
	return req, nil
}

// teamTransfer makes another active member the creator: team.json's
// created_by changes, and with it who may rotate, revoke and transfer.
func (c *command) teamTransfer() (any, error) {
	fs := newFlags()
	principal := fs.String("principal", "", "the principal_ref of the new creator (instead of stdin)")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	ref := *principal
	if flagsSeen(fs)["principal"] {
		if ref == "" {
			return nil, errUsage("--principal was given an empty value")
		}
	} else {
		data, err := adapterkit.ReadInput(c.stdin)
		if err != nil {
			return nil, err
		}
		var req struct {
			PrincipalRef string `json:"principal_ref"`
		}
		if err := json.Unmarshal(data, &req); err != nil || req.PrincipalRef == "" {
			return nil, errInvalidField("principal_ref", "required", "principal_ref is required")
		}
		ref = req.PrincipalRef
	}
	if !safeRef(ref) {
		return nil, errInvalidField("principal_ref", "invalid_value", "principal_ref is not an identifier")
	}
	team, err := c.requireCreator()
	if err != nil {
		return nil, err
	}
	if ref == c.cred.PrincipalRef {
		return &teamTransferResult{TeamRef: team.TeamRef, PrincipalRef: ref, Transferred: true}, nil
	}
	active, err := c.st.memberActiveIn(team.TeamRef, ref)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, errNotFound()
	}
	team.CreatedBy = ref
	if err := writeJSON(c.st.teamPath(team.TeamRef), team); err != nil {
		return nil, err
	}
	return &teamTransferResult{TeamRef: team.TeamRef, PrincipalRef: ref, Transferred: true}, nil
}
