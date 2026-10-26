package syncthing

import (
	"bufio"
	"encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// teamCreateResultWithoutSecret is the `team create` result when
// --secret-file was given: the secret went to that file with mode 0600 and
// is omitted from stdout (4.4.10).
type teamCreateResultWithoutSecret struct {
	TeamRef      string `json:"team_ref"`
	TeamName     string `json:"team_name"`
	PrincipalRef string `json:"principal_ref"`
}

// memberEntry is one row of the `team members` roster (4.4.10).
type memberEntry struct {
	PrincipalRef string     `json:"principal_ref"`
	HumanLabel   string     `json:"human_label,omitzero"`
	Status       string     `json:"status"`
	JoinedAt     time.Time  `json:"joined_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	SessionCount int        `json:"session_count"`
	DeviceID     string     `json:"device_id,omitzero"`
}

// teamMembersResult is the `team members` result (4.4.10, C-43).
type teamMembersResult struct {
	TeamRef    string        `json:"team_ref"`
	TeamName   string        `json:"team_name"`
	ServerTime time.Time     `json:"server_time"`
	Members    []memberEntry `json:"members"`
}

// joinBackend is the adapter-defined `backend` member of a join request
// (4.4.10): the seed devices of the project's team file and the team's
// name, which the join answers before the team file has arrived.
type joinBackend struct {
	Devices  []string `json:"devices"`
	TeamName string   `json:"team_name"`
}

// teamCommand dispatches the `team *` group.
func (c *command) teamCommand() (any, error) {
	switch c.verb {
	case "create":
		return c.teamCreate()
	case "join":
		return c.teamJoin()
	case "leave":
		return c.teamLeave()
	case "members":
		return c.teamMembers()
	case "rotate-secret":
		return c.teamRotateSecret()
	case "revoke-member":
		return c.teamRevokeMember()
	case "transfer":
		return c.teamTransfer()
	default:
		return nil, errUsage("unknown team verb")
	}
}

// teamCreate implements 4.4.10 (C-03). The request comes from stdin, or
// from --name/--label, or from a TTY prompt with --prompt; the binding
// conflict is checked BEFORE anything is created. --team-ref is this
// adapter's own: the harness's `team migrate` keeps a team's reference
// when it moves the team off another backend, so the folder ids the
// project already syncs stay what they are.
func (c *command) teamCreate() (any, error) {
	fs := newFlags()
	name := fs.String("name", "", "the team name (instead of stdin)")
	label := fs.String("label", "", "this member's human label")
	prompt := fs.Bool("prompt", false, "ask on a terminal")
	secretFile := fs.String("secret-file", "", "write the join secret to this path (0600) instead of stdout")
	teamRef := fs.String("team-ref", "", "create the team under this reference (a move from another backend)")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if err := checkSecretFile(*secretFile); err != nil {
		return nil, err
	}
	if *teamRef != "" && !safeRef(*teamRef) {
		return nil, errUsage("--team-ref must be an identifier of letters, digits, dashes and underscores")
	}
	if p, ok, err := c.loadProfile(); err != nil {
		return nil, err
	} else if ok && p.TeamRef != "" {
		return nil, errConflict("this profile is already bound to a team", reasonProfileBound)
	}
	req, err := c.teamCreateRequest(*name, *label, *prompt)
	if err != nil {
		return nil, err
	}
	if err := c.ensureIdentity(); err != nil {
		return nil, err
	}
	a, err := c.attach("team-create-" + c.cred.PrincipalRef)
	if err != nil {
		return nil, err
	}
	if err := c.openStore(); err != nil {
		return nil, err
	}
	ref := *teamRef
	if ref == "" {
		if ref, err = newRef(); err != nil {
			return nil, err
		}
	} else if c.st.teamExists(ref) {
		return nil, errConflict("a team with this reference already exists on this machine", reasonTeamExists)
	}
	secret, err := newJoinSecret(ref)
	if err != nil {
		return nil, err
	}
	// The secret is shown ONCE (4.4.10), so the file is written before
	// the team exists and before the profile is bound: a caller whose
	// --secret-file could not be written would otherwise hold no copy of
	// a secret that is already the only key to a team its own profile is
	// now bound to.
	if *secretFile != "" {
		if werr := adapterkit.WriteAtomic(*secretFile, []byte(secret+"\n")); werr != nil {
			return nil, errConfig("the file named by --secret-file could not be written", "secret_file_unwritable")
		}
	}
	now := c.now().UTC()
	principal := c.cred.PrincipalRef
	if err := c.st.ensureIgnore(ref); err != nil {
		return nil, err
	}
	team := &teamFile{
		TeamRef: ref, TeamName: req.TeamName, JoinSecretSHA256: sha256hex(secret), SecretVersion: 1,
		CreatedBy: principal, CreatedAt: now,
	}
	if err := writeJSON(c.st.teamPath(ref), team); err != nil {
		return nil, err
	}
	humanLabel := req.HumanLabel
	if humanLabel == "" {
		humanLabel = c.profile.HumanLabel
	}
	member := &memberFile{
		PrincipalRef: principal, HumanLabel: humanLabel, Status: memberActive, JoinedAt: now, SecretVersion: 1,
	}
	var seeds []string
	if a != nil {
		member.DeviceID = a.id
		seeds = []string{a.id}
	}
	if err := writeJSON(c.st.memberPath(ref, principal), member); err != nil {
		return nil, err
	}
	if err := c.bind(ref, req.TeamName, humanLabel, seeds, ""); err != nil {
		return nil, err
	}
	c.st.close()
	c.st = nil
	if a != nil {
		if err := c.reconcile(a, ref, seeds); err != nil {
			c.log.Warn("the team folder could not be configured yet; the first session's watch tries again", adapterlog.Err(err))
		}
	}
	if *secretFile != "" {
		c.warnSecretShownOnce("the join secret was written to the file named by --secret-file (mode 0600)")
		return &teamCreateResultWithoutSecret{TeamRef: ref, TeamName: req.TeamName, PrincipalRef: principal}, nil
	}
	c.warnSecretShownOnce("the join secret is printed once and is never stored; anyone holding it can join this team")
	return &protocol.TeamCreateResult{
		TeamRef: ref, TeamName: req.TeamName, JoinSecret: secret, PrincipalRef: principal,
	}, nil
}

// teamCreateRequest builds the 4.4.10 request from --name, from a TTY
// prompt or from the stdin document. When --name is given stdin is NOT
// read at all.
func (c *command) teamCreateRequest(name, label string, prompt bool) (*protocol.TeamCreateRequest, error) {
	req := &protocol.TeamCreateRequest{TeamName: name, HumanLabel: label}
	switch {
	case name != "":
	case prompt:
		tty, err := c.terminal()
		if err != nil {
			return nil, err
		}
		lines := bufio.NewReader(tty)
		if req.TeamName, err = askLine(c.stderr, lines, "team name: "); err != nil {
			return nil, err
		}
		if label == "" {
			if req.HumanLabel, err = askLine(c.stderr, lines, "your label (optional): "); err != nil {
				return nil, err
			}
		}
	default:
		data, err := adapterkit.ReadInput(c.stdin)
		if err != nil {
			return nil, err
		}
		req = &protocol.TeamCreateRequest{}
		if err := protocol.Unmarshal(data, req); err != nil {
			return nil, err
		}
		if label != "" {
			req.HumanLabel = label
		}
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return req, nil
}

// teamJoin implements 4.4.10 (C-04, C-08) over a replicated folder. The
// binding rule is LOCAL and runs first: a secret naming another team is
// `conflict`. Then one of two paths:
//
//   - the team file is here (the folder arrived earlier, or the store is
//     shared with the creator's): the secret is checked against its
//     digest — a wrong secret, an unknown team and a banned principal are
//     one fixed `unauthorized` (4.5.7) — and the membership is written;
//   - it is not, and the engine runs: the join is PENDING. The seeds of
//     the request's backend are added to the engine with the team folder,
//     this machine's device is named with the proof, and the membership
//     is written with the digest kept in the credential; completeJoin
//     checks it when the folder arrives. The result's team_name is the
//     backend's, which the team file replaces then.
func (c *command) teamJoin() (any, error) {
	fs := newFlags()
	label := fs.String("label", "", "this member's human label")
	prompt := fs.Bool("prompt", false, "ask on a terminal")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	req, err := c.teamJoinRequest(*label, *prompt)
	if err != nil {
		return nil, err
	}
	secret, err := protocol.ParseJoinSecret(req.JoinSecret)
	if err != nil {
		return nil, err
	}
	backend, err := decodeBackend(req.Backend)
	if err != nil {
		return nil, err
	}
	bound := ""
	if p, ok, perr := c.loadProfile(); perr != nil {
		return nil, perr
	} else if ok {
		bound = p.TeamRef
	}
	if bound != "" && bound != secret.TeamRef() {
		return nil, errConflict("this profile is already bound to another team", reasonProfileBound)
	}
	if err := c.ensureIdentity(); err != nil {
		return nil, err
	}
	if err := c.openStore(); err != nil {
		return nil, err
	}
	teamRef, principal := secret.TeamRef(), c.cred.PrincipalRef
	digest := sha256hex(secret.Secret())
	team, present, err := c.st.loadTeam(teamRef)
	if err != nil {
		return nil, err
	}
	if present {
		if team.JoinSecretSHA256 != digest {
			return nil, errSecretRejected()
		}
		if rv, revoked, err := c.st.loadRevoked(teamRef, principal); err != nil {
			return nil, err
		} else if revoked {
			if rv.Ban {
				return nil, errSecretRejected()
			}
			if err := os.Remove(c.st.revokedPath(teamRef, principal)); err != nil && !os.IsNotExist(err) {
				return nil, errInternal("a revocation marker could not be removed")
			}
		}
	}
	member, rejoined, err := c.st.loadMember(teamRef, principal)
	if err != nil {
		return nil, err
	}
	humanLabel := req.HumanLabel
	if humanLabel == "" && rejoined {
		humanLabel = member.HumanLabel
	}
	joinedAt := c.now().UTC()
	if rejoined {
		joinedAt = member.JoinedAt
	}
	next := &memberFile{PrincipalRef: principal, HumanLabel: humanLabel, Status: memberActive, JoinedAt: joinedAt}
	if rejoined {
		next.DeviceID = member.DeviceID
	}
	teamName := backend.TeamName
	seeds := backend.Devices
	var a *attached
	if present {
		next.SecretVersion = team.SecretVersion
		teamName = team.TeamName
		if a, err = c.attach("team-join-" + principal); err != nil {
			c.log.Debug("the engine is not available; the folder is here already", adapterlog.Err(err))
			a = nil
		}
	} else {
		if c.local() {
			return nil, errSecretRejected()
		}
		if len(seeds) == 0 {
			return nil, errConfig("this team has not reached this machine and the project's team file names no seed device", reasonNoSeed)
		}
		if teamName == "" {
			return nil, errInvalidField("backend.team_name", "required", "a join before the team file has arrived needs the team's name")
		}
		if a, err = c.attach("team-join-" + principal); err != nil {
			return nil, err
		}
		if err := c.announce(a, digest); err != nil {
			return nil, err
		}
		if err := c.st.ensureIgnore(teamRef); err != nil {
			return nil, err
		}
	}
	if a != nil {
		next.DeviceID = a.id
	}
	if err := writeJSON(c.st.memberPath(teamRef, principal), next); err != nil {
		return nil, err
	}
	pendingDigest := ""
	if !present {
		pendingDigest = digest
	}
	if err := c.bind(teamRef, teamName, humanLabel, seeds, pendingDigest); err != nil {
		return nil, err
	}
	c.st.close()
	c.st = nil
	if a != nil {
		if err := c.reconcile(a, teamRef, seeds); err != nil {
			c.log.Warn("the team folder could not be configured yet; the first session's watch tries again", adapterlog.Err(err))
		}
	}
	return &protocol.TeamJoinResult{
		TeamRef: teamRef, TeamName: teamName, PrincipalRef: principal, Rejoined: rejoined,
	}, nil
}

// decodeBackend reads the join request's backend member: absent is an
// empty backend; present, it must carry well-formed device ids.
func decodeBackend(raw []byte) (*joinBackend, error) {
	b := &joinBackend{}
	if len(raw) == 0 {
		return b, nil
	}
	if err := json.Unmarshal(raw, b); err != nil {
		return nil, errInvalidField("backend", "invalid_value", "backend must be an object with devices and team_name")
	}
	for _, d := range b.Devices {
		if !checkDeviceID(d) {
			return nil, errInvalidField("backend.devices", "invalid_value", "a seed device is not a Syncthing device id")
		}
	}
	if b.TeamName != "" {
		if err := (&protocol.TeamCreateRequest{TeamName: b.TeamName}).Validate(); err != nil {
			return nil, errInvalidField("backend.team_name", "invalid_value", "the team name is not usable")
		}
	}
	return b, nil
}

// teamJoinRequest builds the 4.4.10 join request from stdin or, with
// --prompt, from a terminal: the secret without echo, then the label.
func (c *command) teamJoinRequest(label string, prompt bool) (*protocol.TeamJoinRequest, error) {
	req := &protocol.TeamJoinRequest{HumanLabel: label}
	if prompt {
		tty, err := c.terminal()
		if err != nil {
			return nil, err
		}
		_, _ = io.WriteString(c.stderr, "join secret (not echoed): ")
		raw, rerr := term.ReadPassword(int(tty.Fd()))
		_, _ = io.WriteString(c.stderr, "\n")
		if rerr != nil {
			return nil, errUsage("the join secret could not be read from the terminal")
		}
		req.JoinSecret = strings.TrimSpace(string(raw))
		if label == "" {
			if req.HumanLabel, err = askLine(c.stderr, bufio.NewReader(tty), "your label (optional): "); err != nil {
				return nil, err
			}
		}
		return req, req.Validate()
	}
	data, err := adapterkit.ReadInput(c.stdin)
	if err != nil {
		return nil, err
	}
	req = &protocol.TeamJoinRequest{}
	if err := protocol.Unmarshal(data, req); err != nil {
		return nil, err
	}
	if label != "" {
		req.HumanLabel = label
	}
	return req, req.Validate()
}

// teamLeave implements 4.4.10 (C-08): this principal's own membership
// file says left, its open sessions close and the profile unbinds. It is
// idempotent — a second call answers from the credential's last_team_ref.
func (c *command) teamLeave() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	if err := c.authenticate(false); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	if teamRef == "" {
		teamRef = c.cred.LastTeamRef
		if teamRef == "" {
			return nil, errConfig("profile has never been bound to a team", "no_team_bound")
		}
		return &protocol.TeamLeaveResult{TeamRef: teamRef, PrincipalRef: c.cred.PrincipalRef, Left: true}, nil
	}
	if err := c.openStore(); err != nil {
		return nil, err
	}
	if err := c.st.leaveTeam(teamRef, c.cred.PrincipalRef); err != nil {
		return nil, err
	}
	c.profile.TeamRef, c.profile.TeamName, c.profile.Devices = "", "", nil
	if err := c.saveProfile(); err != nil {
		return nil, err
	}
	c.cred.LastTeamRef, c.cred.JoinDigest = teamRef, ""
	if err := c.saveCredential(c.cred); err != nil {
		return nil, err
	}
	return &protocol.TeamLeaveResult{TeamRef: teamRef, PrincipalRef: c.cred.PrincipalRef, Left: true}, nil
}

// leaveTeam marks the principal's own membership left and closes its own
// sessions. Silent about a membership that is not there.
func (s *store) leaveTeam(team, principal string) error {
	if m, ok, err := s.loadMember(team, principal); err != nil {
		return err
	} else if ok && m.Status != memberLeft {
		m.Status = memberLeft
		if err := writeJSON(s.memberPath(team, principal), m); err != nil {
			return err
		}
	}
	_, err := s.closeOwnSessions(team, principal)
	return err
}

// teamMembers implements 4.4.10 (C-43): every ACTIVE member of the
// profile's team.
func (c *command) teamMembers() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	if err := c.teamScoped(); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	team, ok, err := c.st.loadTeam(teamRef)
	if err != nil {
		return nil, err
	}
	teamName := c.profile.TeamName
	if ok {
		teamName = team.TeamName
	}
	members, err := c.st.activeMembers(teamRef)
	if err != nil {
		return nil, err
	}
	now := c.st.now()
	out := make([]memberEntry, 0, len(members))
	for _, m := range members {
		entry := memberEntry{
			PrincipalRef: m.PrincipalRef, HumanLabel: m.HumanLabel,
			Status: m.Status, JoinedAt: m.JoinedAt, DeviceID: m.DeviceID,
		}
		if entry.LastSeenAt, entry.SessionCount, err = c.st.memberActivity(teamRef, m.PrincipalRef, now); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return &teamMembersResult{TeamRef: teamRef, TeamName: teamName, ServerTime: now.UTC(), Members: out}, nil
}

// bind writes the team binding into the profile and remembers the team
// (and a pending join's digest) in the credential.
func (c *command) bind(teamRef, teamName, humanLabel string, seeds []string, pendingDigest string) error {
	c.profile.TeamRef, c.profile.TeamName = teamRef, teamName
	c.profile.PrincipalRef, c.profile.HumanLabel = c.cred.PrincipalRef, humanLabel
	c.profile.Devices = seeds
	if err := c.saveProfile(); err != nil {
		return err
	}
	c.cred.LastTeamRef, c.cred.JoinDigest = teamRef, pendingDigest
	return c.saveCredential(c.cred)
}

func (c *command) saveProfile() error {
	return adapterkit.SaveProfile(c.cfgDir, c.profileName, c.profile)
}

// checkSecretFile refuses a relative --secret-file: under a hook the
// working directory is the project tree, and a join secret must never be
// written there.
func checkSecretFile(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return nil
	}
	return errUsage("--secret-file must be an absolute path")
}

// warnSecretShownOnce puts the 4.4.10 warning on stderr. It names no
// secret: the warning is about one, it never carries one.
func (c *command) warnSecretShownOnce(text string) {
	_, _ = io.WriteString(c.stderr, progName+": "+text+"\n")
}

// terminal returns stdin as a terminal, or refuses with `usage`.
func (c *command) terminal() (*os.File, error) {
	f, ok := c.stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil, errUsage("--prompt needs a terminal on stdin")
	}
	return f, nil
}

// askLine writes a prompt to stderr and reads one echoed line.
func askLine(w io.Writer, r *bufio.Reader, prompt string) (string, error) {
	_, _ = io.WriteString(w, prompt)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", errUsage("the terminal input ended before an answer was given")
	}
	return strings.TrimSpace(line), nil
}
