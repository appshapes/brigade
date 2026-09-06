package fs

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// teamCreateResultWithoutSecret is the `team create` result when
// --secret-file was given: the secret went to that file with mode 0600 and
// is omitted from stdout. 4.4.10 names this exception explicitly, which is
// why it is a local struct and not protocol.TeamCreateResult with a blank
// member (that would fail the shape's own Validate).
type teamCreateResultWithoutSecret struct {
	TeamRef      string `json:"team_ref"`
	TeamName     string `json:"team_name"`
	PrincipalRef string `json:"principal_ref"`
}

// memberEntry is one row of the `team members` roster (4.4.10). LastSeenAt
// is a pointer so a member that has never registered a session renders as
// JSON null rather than as the zero time.
type memberEntry struct {
	PrincipalRef string     `json:"principal_ref"`
	HumanLabel   string     `json:"human_label,omitzero"`
	Status       string     `json:"status"`
	JoinedAt     time.Time  `json:"joined_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	SessionCount int        `json:"session_count"`
}

// teamMembersResult is the `team members` result (4.4.10, C-43).
type teamMembersResult struct {
	TeamRef    string        `json:"team_ref"`
	TeamName   string        `json:"team_name"`
	ServerTime time.Time     `json:"server_time"`
	Members    []memberEntry `json:"members"`
}

// teamCommand dispatches the `team *` convention group.
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
	default:
		return nil, errUsage("unknown team verb")
	}
}

// teamCreate implements 4.4.10 (C-03, C-03b). The request comes from
// stdin, or from --name/--label, or from a TTY prompt with --prompt; the
// binding conflict is checked BEFORE anything is created, so a second
// `team create` leaves the first team's binding and credential untouched.
func (c *command) teamCreate() (any, error) {
	fs := newFlags()
	name := fs.String("name", "", "the team name (instead of stdin)")
	label := fs.String("label", "", "this member's human label")
	prompt := fs.Bool("prompt", false, "ask on a terminal")
	secretFile := fs.String("secret-file", "", "write the join secret to this path (0600) instead of stdout")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if err := checkSecretFile(*secretFile); err != nil {
		return nil, err
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
	if err := c.openStore(); err != nil {
		return nil, err
	}
	teamRef, err := newRef()
	if err != nil {
		return nil, err
	}
	secret, err := newJoinSecret(teamRef)
	if err != nil {
		return nil, err
	}
	// The secret is shown ONCE (4.4.10), so the file is written before
	// the team exists and before the profile is bound: a caller whose
	// --secret-file could not be written would otherwise hold no copy of
	// a secret that is already the only key to a team its own profile is
	// now bound to, unable to retry (`conflict profile_bound`) and unable
	// to invite anyone.
	if *secretFile != "" {
		if werr := adapterkit.WriteAtomic(*secretFile, []byte(secret+"\n")); werr != nil {
			return nil, errConfig("the file named by --secret-file could not be written", "secret_file_unwritable")
		}
	}
	now := c.now().UTC()
	principal := c.cred.PrincipalRef
	team := &teamFile{
		TeamRef: teamRef, TeamName: req.TeamName, JoinSecretSHA26: sha256hex(secret),
		CreatedBy: principal, CreatedAt: now,
	}
	if err := writeJSON(c.st.teamPath(teamRef), team); err != nil {
		return nil, err
	}
	humanLabel := req.HumanLabel
	if humanLabel == "" {
		humanLabel = c.profile.HumanLabel
	}
	member := &memberFile{
		PrincipalRef: principal, HumanLabel: humanLabel, Status: memberActive, JoinedAt: now,
	}
	if err := writeJSON(c.st.memberPath(teamRef, principal), member); err != nil {
		return nil, err
	}
	if err := c.bind(teamRef, req.TeamName, humanLabel); err != nil {
		return nil, err
	}
	if *secretFile != "" {
		c.warnSecretShownOnce("the join secret was written to the file named by --secret-file (mode 0600)")
		return &teamCreateResultWithoutSecret{
			TeamRef: teamRef, TeamName: req.TeamName, PrincipalRef: principal,
		}, nil
	}
	c.warnSecretShownOnce("the join secret is printed once and is never stored; anyone holding it can join this team")
	return &protocol.TeamCreateResult{
		TeamRef: teamRef, TeamName: req.TeamName, JoinSecret: secret, PrincipalRef: principal,
	}, nil
}

// teamCreateRequest builds the 4.4.10 request from --name, from a TTY
// prompt or from the stdin document. When --name is given stdin is NOT
// read at all, which is what lets a script create a team with no here-doc.
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

// teamJoin implements 4.4.10 (C-04, C-08). The binding rule is LOCAL and
// runs before the store is touched: a secret naming another team is
// `conflict`, a secret naming the bound team is a rejoin. Every refusal of
// the secret itself is one fixed `unauthorized` message with no details,
// so a wrong secret, an unknown team and a banned principal are
// byte-identical (4.5.7).
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
	team, ok, err := c.st.loadTeam(secret.TeamRef())
	if err != nil {
		return nil, err
	}
	if !ok || team.JoinSecretSHA26 != sha256hex(secret.Secret()) {
		return nil, errSecretRejected()
	}
	principal := c.cred.PrincipalRef
	member, rejoined, err := c.st.loadMember(team.TeamRef, principal)
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
	next := &memberFile{
		PrincipalRef: principal, HumanLabel: humanLabel, Status: memberActive, JoinedAt: joinedAt,
	}
	if err := writeJSON(c.st.memberPath(team.TeamRef, principal), next); err != nil {
		return nil, err
	}
	if err := c.bind(team.TeamRef, team.TeamName, humanLabel); err != nil {
		return nil, err
	}
	return &protocol.TeamJoinResult{
		TeamRef: team.TeamRef, TeamName: team.TeamName, PrincipalRef: principal, Rejoined: rejoined,
	}, nil
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

// teamLeave implements 4.4.10 (C-08): revoke this principal's own
// membership, close its open sessions in that team and unbind the profile.
// It is idempotent — a second call answers from the credential's
// last_team_ref — and it is never an error for a membership that is
// already revoked or a team that is gone. A profile that was NEVER bound
// has nothing to answer with, so it is `config`.
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
		return &protocol.TeamLeaveResult{
			TeamRef: teamRef, PrincipalRef: c.cred.PrincipalRef, Left: true,
		}, nil
	}
	if err := c.openStore(); err != nil {
		return nil, err
	}
	if err := c.st.revokeMembership(teamRef, c.cred.PrincipalRef); err != nil {
		return nil, err
	}
	c.profile.TeamRef, c.profile.TeamName = "", ""
	if err := adapterkit.SaveProfile(c.cfgDir, c.profileName, c.profile); err != nil {
		return nil, err
	}
	c.cred.LastTeamRef = teamRef
	if err := c.saveCredential(c.cred); err != nil {
		return nil, err
	}
	return &protocol.TeamLeaveResult{
		TeamRef: teamRef, PrincipalRef: c.cred.PrincipalRef, Left: true,
	}, nil
}

// teamMembers implements 4.4.10 (C-43): every ACTIVE member of the
// profile's team, with the last time any of its sessions was seen and how
// many of them are not offline. A non-member gets the uniform
// `unauthorized`, byte-identical to the answer for a team that does not
// exist.
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
	if !ok {
		return nil, errNotMember()
	}
	principals, err := jsonStems(c.st.membersDir(teamRef))
	if err != nil {
		return nil, err
	}
	now := c.st.now()
	members := make([]memberEntry, 0, len(principals))
	for _, principal := range principals {
		m, found, err := c.st.loadMember(teamRef, principal)
		if err != nil {
			return nil, err
		}
		if !found || m.Status != memberActive {
			continue
		}
		entry := memberEntry{
			PrincipalRef: m.PrincipalRef, HumanLabel: m.HumanLabel,
			Status: m.Status, JoinedAt: m.JoinedAt,
		}
		if entry.LastSeenAt, entry.SessionCount, err = c.st.memberActivity(teamRef, principal, now); err != nil {
			return nil, err
		}
		members = append(members, entry)
	}
	return &teamMembersResult{
		TeamRef: teamRef, TeamName: team.TeamName, ServerTime: now.UTC(), Members: members,
	}, nil
}

// bind writes the team binding into team.json and remembers the team in
// the credential, so a later `team leave` on an already-unbound profile
// can still answer a non-empty team_ref.
func (c *command) bind(teamRef, teamName, humanLabel string) error {
	c.profile.TeamRef, c.profile.TeamName = teamRef, teamName
	c.profile.PrincipalRef, c.profile.HumanLabel = c.cred.PrincipalRef, humanLabel
	if err := adapterkit.SaveProfile(c.cfgDir, c.profileName, c.profile); err != nil {
		return err
	}
	c.cred.LastTeamRef = teamRef
	return c.saveCredential(c.cred)
}

// checkSecretFile refuses a relative --secret-file. The path is resolved
// against the process's working directory, which under a hook is the
// project tree (3.2), and a join secret must never be written there —
// the same reason --root refuses a relative value.
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

// terminal returns stdin as a terminal, or refuses with `usage`. --prompt
// without a TTY is `usage` (4.4.10, B-7): there is nobody to ask.
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

// revokeMembership flips a member to revoked and closes every open session
// that principal has in the team. It is idempotent and silent about a team
// or a membership that is not there: `team leave` and `profile reset` are
// both defined as never failing for that reason.
func (s *store) revokeMembership(team, principal string) error {
	if m, ok, err := s.loadMember(team, principal); err != nil {
		return err
	} else if ok && m.Status != memberRevoked {
		m.Status = memberRevoked
		if err := writeJSON(s.memberPath(team, principal), m); err != nil {
			return err
		}
	}
	ids, err := jsonStems(s.sessionsDir(team))
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, id := range ids {
		f, ok, err := s.loadSession(team, id)
		if err != nil {
			return err
		}
		if !ok || f.PrincipalRef != principal || f.ClosedAt != nil {
			continue
		}
		f.ClosedAt = &now
		if err := s.saveSession(team, f); err != nil {
			return err
		}
	}
	return nil
}

// memberActivity reports the most recent last_seen_at over a principal's
// sessions in this team, in ANY state (nil when it has none), and how many
// of those sessions are not offline (4.4.10 `team members`).
func (s *store) memberActivity(team, principal string, now time.Time) (*time.Time, int, error) {
	ids, err := jsonStems(s.sessionsDir(team))
	if err != nil {
		return nil, 0, err
	}
	var last *time.Time
	count := 0
	for _, id := range ids {
		f, ok, err := s.loadSession(team, id)
		if err != nil {
			return nil, 0, err
		}
		if !ok || f.PrincipalRef != principal {
			continue
		}
		if last == nil || f.LastSeenAt.After(*last) {
			seen := f.LastSeenAt
			last = &seen
		}
		if sessionState(f, now) != protocol.SessionStateOffline {
			count++
		}
	}
	return last, count, nil
}
