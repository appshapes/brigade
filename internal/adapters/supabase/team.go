package supabase

import (
	"bufio"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The `team *` group (4.4.10, plan 5.11, task P2-7): create, join, leave,
// members. The binding rules are local and run before any network call;
// the secret is read from stdin or a no-echo prompt, sent to the backend
// once and never stored; every refusal of a secret is one fixed
// `unauthorized` text (4.5.7). P5-2 adds the three verbs of capability
// team.admin, which answer the team's owner only: rotate-secret,
// revoke-member and transfer. Their stdin and result documents are the
// protocol's (4.4.10, work item 274), which took the shapes this file had
// fixed for itself.

// The environment pair a binding command honours when there is no
// team.json to read a backend from (4.1: a BRIGADE_<ADAPTER>_* variable
// is honoured only when a human — or the conformance suite — runs the
// adapter from a shell; under a live session neither arrives). The pair
// is written to team.json exactly as `profile init` would write it, so
// every later command reads the file and never the environment; a profile
// that already names a backend is never overridden by the environment.
const (
	backendURLVar = "BRIGADE_SUPABASE_URL"
	backendKeyVar = "BRIGADE_SUPABASE_PUBLISHABLE_KEY"
)

// reasonProfileBound is the 4.6 conflict reason of the binding rules.
const reasonProfileBound = "profile_bound"

// reasonJoinAttempts is the details.reason of the join limiter (D6: five
// failures per principal per 15 minutes, then rate_limited even with the
// correct secret).
const reasonJoinAttempts = "join_attempts"

// The RPC names of the finished migration, and (P5-2) of the
// team-administration migration.
const (
	rpcCreateTeam       = "create_team"
	rpcJoinTeam         = "join_team"
	rpcLeaveTeam        = "leave_team"
	rpcListMembers      = "list_members"
	rpcRotateJoinSecret = "rotate_join_secret" //nolint:gosec // the RPC's name, not a credential (G101 matches the word secret)
	rpcRevokeMembership = "revoke_membership"
	rpcRevokeByVersion  = "revoke_memberships_by_version"
	rpcTransferTeam     = "transfer_team"
	rpcRecordTeamMove   = "record_team_move"
	rpcClearTeamMove    = "clear_team_move"
	rpcReadTeamMove     = "read_team_move"
)

// The `status` values join_team and create_team answer with (D15: the
// expected refusals of a join are results, not raises, so the attempt row
// commits).
const (
	statusCreated       = "created"
	statusJoined        = "joined"
	statusInvalidSecret = "invalid_secret"
	statusInvalidInput  = "invalid_input"
	statusRateLimited   = "rate_limited"
	statusRotated       = "rotated"
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

// memberEntry is one row of the `team members` roster (4.4.10), as
// list_members answers it. LastSeenAt is a pointer so a member that has
// never registered a session renders as JSON null rather than as the zero
// time. JoinedSecretVersion is the version of the secret the member last
// joined with, the member an adapter with team.admin adds to the roster:
// it is what `team revoke-member --max-version` sweeps by.
type memberEntry struct {
	PrincipalRef        string     `json:"principal_ref"`
	HumanLabel          string     `json:"human_label,omitzero"`
	Status              string     `json:"status"`
	JoinedAt            time.Time  `json:"joined_at"`
	JoinedSecretVersion int        `json:"joined_secret_version,omitzero"`
	LastSeenAt          *time.Time `json:"last_seen_at"`
	SessionCount        int        `json:"session_count"`
}

// teamMembersResult is the `team members` result (4.4.10, C-43).
type teamMembersResult struct {
	TeamRef    string        `json:"team_ref"`
	TeamName   string        `json:"team_name"`
	ServerTime time.Time     `json:"server_time"`
	Members    []memberEntry `json:"members"`
}

// createTeamAnswer is create_team's jsonb.
type createTeamAnswer struct {
	Status     string `json:"status"`
	TeamID     string `json:"team_id"`
	TeamName   string `json:"team_name"`
	JoinSecret string `json:"join_secret"`
}

// joinTeamAnswer is join_team's jsonb: one of the status values above,
// the team on success, retry_after_seconds when rate limited, and the
// advisory team_failures count.
type joinTeamAnswer struct {
	Status            string `json:"status"`
	TeamID            string `json:"team_id"`
	TeamName          string `json:"team_name"`
	Rejoined          bool   `json:"rejoined"`
	TeamFailures      int    `json:"team_failures"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
}

// leaveTeamAnswer is leave_team's jsonb.
type leaveTeamAnswer struct {
	TeamID string `json:"team_id"`
	Left   bool   `json:"left"`
}

// backendDocument is the adapter-specific `backend` member of the `team
// join` request (4.4.10): {url, publishable_key}.
type backendDocument struct {
	URL            string `json:"url"`
	PublishableKey string `json:"publishable_key"`
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
	case "rotate-secret":
		return c.teamRotateSecret()
	case "revoke-member":
		return c.teamRevokeMember()
	case "transfer":
		return c.teamTransfer()
	case "move":
		return c.teamMove()
	case "moved":
		return c.teamMoved()
	default:
		return nil, errUsage("unknown team verb")
	}
}

// teamCreate implements 4.4.10 (C-03, C-03b). The order is the fs
// adapter's: the local binding check and the backend resolution first
// (`conflict` on a bound profile, `config` without a backend), then the
// request from stdin, --name/--label or a TTY prompt, then the identity
// (a sign-up when session.json is absent) and one create_team call. The
// secret is the backend's; it is printed once, or written 0600 to
// --secret-file and omitted from stdout, and never stored.
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
	p, present, err := c.loadProfile()
	if err != nil {
		return nil, err
	}
	if present && p.TeamRef != "" {
		return nil, errConflict("this profile is already bound to a team", reasonProfileBound)
	}
	backend, err := c.resolveBackend(p, nil)
	if err != nil {
		return nil, err
	}
	req, err := c.teamCreateRequest(*name, *label, *prompt)
	if err != nil {
		return nil, err
	}
	if err := c.bootstrapProfile(p, backend); err != nil {
		return nil, err
	}
	if err := c.ensureIdentity(c.ctx); err != nil {
		return nil, err
	}
	humanLabel := req.HumanLabel
	if humanLabel == "" {
		humanLabel = c.profile.HumanLabel
	}
	var answer createTeamAnswer
	if err := c.rpc(c.ctx, rpcCreateTeam, rpcArgs{"p_name": req.TeamName, "p_human_label": optionalArg(humanLabel)}, &answer); err != nil {
		return nil, err
	}
	if answer.Status != statusCreated || !validUUID(answer.TeamID) {
		return nil, errUnexpectedResponse("the backend did not answer a created team")
	}
	secret, err := protocol.ParseJoinSecret(answer.JoinSecret)
	if err != nil || secret.TeamRef() != answer.TeamID {
		return nil, errUnexpectedResponse("the backend answered a join secret this adapter cannot parse")
	}
	teamName := answer.TeamName
	if teamName == "" {
		teamName = req.TeamName
	}
	principal := c.cred.principalRef()
	// The secret is shown ONCE (4.4.10). The file is written before the
	// profile is bound: a caller whose --secret-file cannot be written
	// keeps an unbound profile and can simply run `team create` again
	// (the team just created has one member and is reclaimed as abandoned
	// by housekeeping), instead of being bound to a team whose only key
	// nobody holds.
	if *secretFile != "" {
		if werr := adapterkit.WriteAtomic(*secretFile, []byte(secret.Secret()+"\n")); werr != nil {
			c.log.Warn("the join secret could not be written to --secret-file; the profile stays unbound and the team is abandoned")
			return nil, errConfig("the file named by --secret-file could not be written", "secret_file_unwritable")
		}
	}
	if err := c.bind(answer.TeamID, teamName, humanLabel); err != nil {
		return nil, err
	}
	if *secretFile != "" {
		c.warnSecretShownOnce("the join secret was written to the file named by --secret-file (mode 0600)")
		return &teamCreateResultWithoutSecret{
			TeamRef: answer.TeamID, TeamName: teamName, PrincipalRef: principal,
		}, nil
	}
	c.warnSecretShownOnce("the join secret is printed once and is never stored; anyone holding it can join this team")
	return &protocol.TeamCreateResult{
		TeamRef: answer.TeamID, TeamName: teamName, JoinSecret: secret.Secret(), PrincipalRef: principal,
	}, nil
}

// teamCreateRequest builds the 4.4.10 create request: --name (and
// --label) without touching stdin, --prompt from a terminal, else the one
// stdin document with --label overriding its human_label.
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

// teamJoin implements 4.4.10 (C-04, C-08). The profile file is read
// first (a malformed one is `config`), then the request — from stdin or,
// with --prompt, the secret without echo and then the label — because
// its `backend` member may be what configures an unconfigured profile;
// then the local binding rule: a secret naming another team than the
// bound one is `conflict`, a secret naming the bound team is a rejoin.
// Only then the identity (a sign-up when session.json is absent) and one
// join_team call. The backend answers every refusal of the secret as a
// 200 result: invalid_secret (a wrong secret, an unknown team, a banned
// principal — one 28-byte body for all three) and invalid_input (a
// well-formed secret whose team_ref is not a backend id, so it can name
// no team here) are the one fixed `unauthorized`; rate_limited is the
// join limiter with its retry_after.
func (c *command) teamJoin() (any, error) {
	fs := newFlags()
	label := fs.String("label", "", "this member's human label")
	prompt := fs.Bool("prompt", false, "ask on a terminal")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	p, present, err := c.loadProfile()
	if err != nil {
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
	if present && p.TeamRef != "" && p.TeamRef != secret.TeamRef() {
		return nil, errConflict("this profile is already bound to another team", reasonProfileBound)
	}
	backend, err := c.resolveBackend(p, req.Backend)
	if err != nil {
		return nil, err
	}
	if err := c.bootstrapProfile(p, backend); err != nil {
		return nil, err
	}
	if err := c.ensureIdentity(c.ctx); err != nil {
		return nil, err
	}
	humanLabel := req.HumanLabel
	if humanLabel == "" {
		humanLabel = c.profile.HumanLabel
	}
	var answer joinTeamAnswer
	args := rpcArgs{"p_join_secret": secret.Secret(), "p_human_label": optionalArg(humanLabel)}
	if err := c.rpc(c.ctx, rpcJoinTeam, args, &answer); err != nil {
		return nil, err
	}
	switch answer.Status {
	case statusJoined:
	case statusInvalidSecret, statusInvalidInput:
		return nil, errSecretRejected()
	case statusRateLimited:
		return nil, errRateLimited(reasonJoinAttempts, answer.RetryAfterSeconds*1000)
	default:
		return nil, errUnexpectedResponse("the backend did not answer a join status")
	}
	if !validUUID(answer.TeamID) || answer.TeamID != secret.TeamRef() {
		return nil, errUnexpectedResponse("the backend answered a team other than the one the secret names")
	}
	if answer.TeamFailures > 0 {
		c.log.Warn("the backend recorded failed join attempts against this team in the last 15 minutes",
			slog.Int("team_failures", answer.TeamFailures))
	}
	if err := c.bind(answer.TeamID, answer.TeamName, humanLabel); err != nil {
		return nil, err
	}
	return &protocol.TeamJoinResult{
		TeamRef: answer.TeamID, TeamName: answer.TeamName, PrincipalRef: c.cred.principalRef(), Rejoined: answer.Rejoined,
	}, nil
}

// teamJoinRequest builds the 4.4.10 join request from stdin or, with
// --prompt, from a terminal: the secret without echo (x/term.ReadPassword),
// then the label.
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

// teamLeave implements 4.4.10 (C-08): leave_team revokes this principal's
// own membership and closes its open sessions in the team, then the
// profile is unbound and the credential kept. It is idempotent — a second
// call answers from session.json's last_team_ref with no network call —
// and leave_team itself is uniform for an unknown team or an already
// revoked member. A profile that was NEVER bound has nothing to answer
// with, so it is `config`. A bound team_ref that is not a backend id (the
// suite's --rebind) can name no membership, so nothing is revoked and the
// profile is simply unbound.
func (c *command) teamLeave() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	if err := c.authenticate(false); err != nil {
		return nil, err
	}
	principal := c.cred.principalRef()
	teamRef := c.profile.TeamRef
	if teamRef == "" {
		teamRef = c.lastTeamRef()
		if teamRef == "" {
			return nil, errConfig("profile has never been bound to a team", reasonNoTeamBound)
		}
		return &protocol.TeamLeaveResult{TeamRef: teamRef, PrincipalRef: principal, Left: true}, nil
	}
	if validUUID(teamRef) {
		var answer leaveTeamAnswer
		if err := c.rpc(c.ctx, rpcLeaveTeam, rpcArgs{"p_team_id": teamRef}, &answer); err != nil {
			return nil, err
		}
		if !answer.Left {
			return nil, errUnexpectedResponse("the backend did not confirm the leave")
		}
	} else {
		c.log.Debug("the bound team_ref is not a backend id; nothing to revoke")
	}
	if err := c.unbind(); err != nil {
		return nil, err
	}
	if err := c.rememberTeam(teamRef); err != nil {
		return nil, err
	}
	return &protocol.TeamLeaveResult{TeamRef: teamRef, PrincipalRef: principal, Left: true}, nil
}

// teamMembers implements 4.4.10 (C-43): list_members answers every ACTIVE
// member of the profile's team with the last time any of its sessions was
// seen and how many of them are not offline. A non-member gets the uniform
// `unauthorized` (the RPC raises it with one text for a foreign team and a
// random id; a team_ref that is not a backend id answers it locally).
func (c *command) teamMembers() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	var answer teamMembersResult
	if err := c.rpc(c.ctx, rpcListMembers, rpcArgs{"p_team_id": c.profile.TeamRef}, &answer); err != nil {
		return nil, err
	}
	if answer.TeamRef == "" {
		answer.TeamRef = c.profile.TeamRef
	}
	if answer.Members == nil {
		answer.Members = []memberEntry{}
	}
	return &answer, nil
}

// resolveBackend is where a binding command's backend comes from: the
// profile's own pair when it has one, else the request's `backend`
// member (`team join` only), else the BRIGADE_SUPABASE_* pair of the
// environment; with none of those, no profile is `config` profile_missing
// and a profile without a pair is `config` backend_unconfigured, the
// same answers every other command gives. A `backend` member on a profile
// that already names a DIFFERENT backend is `conflict` (4.4.10: accepted
// only when the profile has no backend configured); the same backend
// again is accepted as a no-op.
func (c *command) resolveBackend(p *adapterkit.Profile, raw jsontext.Value) (backendDocument, error) {
	var requested backendDocument
	hasRequest := len(raw) > 0
	if hasRequest {
		if err := json.Unmarshal(raw, &requested); err != nil {
			return backendDocument{}, errBackendMember()
		}
	}
	if p != nil && p.URL != "" && p.PublishableKey != "" {
		configured := backendDocument{URL: p.URL, PublishableKey: p.PublishableKey}
		if hasRequest && normalizeBackend(requested) != configured {
			return backendDocument{}, errConflict("this profile already names a different backend; run `profile init --force` to replace it", reasonProfileExists)
		}
		return configured, nil
	}
	if hasRequest {
		if requested.URL == "" || requested.PublishableKey == "" {
			return backendDocument{}, errBackendMember()
		}
		if err := checkBackendURL(requested.URL); err != nil {
			return backendDocument{}, err
		}
		if err := checkPublishableKey(requested.PublishableKey); err != nil {
			return backendDocument{}, err
		}
		return normalizeBackend(requested), nil
	}
	env := backendDocument{
		URL:            adapterkit.Getenv(c.environ, backendURLVar),
		PublishableKey: adapterkit.Getenv(c.environ, backendKeyVar),
	}
	if env.URL != "" && env.PublishableKey != "" {
		if checkBackendURL(env.URL) != nil || checkPublishableKey(env.PublishableKey) != nil {
			return backendDocument{}, errConfig(backendURLVar+" and "+backendKeyVar+" are not a usable backend pair (https url, publishable key)", "invalid_value")
		}
		return normalizeBackend(env), nil
	}
	if p == nil {
		return backendDocument{}, errNoProfile()
	}
	return backendDocument{}, errNoBackend()
}

// normalizeBackend trims the url the way `profile init` stores it.
func normalizeBackend(b backendDocument) backendDocument {
	b.URL = strings.TrimRight(b.URL, "/")
	return b
}

// errBackendMember refuses a `backend` member that is not the {url,
// publishable_key} pair (4.4.10).
func errBackendMember() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeInvalidInput,
		Message: "backend must be an object with url and publishable_key",
		Details: map[string]string{"field": "backend", "reason": "invalid_value"},
	}
}

// bootstrapProfile writes the resolved backend pair into team.json
// when the file does not carry it yet — the implicit `profile init` of a
// binding command run on a fresh profile — and leaves an already
// configured file alone. It runs after the request was validated, so a
// refused document creates nothing.
func (c *command) bootstrapProfile(p *adapterkit.Profile, backend backendDocument) error {
	if p != nil && p.URL == backend.URL && p.PublishableKey == backend.PublishableKey {
		return nil
	}
	if p == nil {
		p = &adapterkit.Profile{
			Version:     adapterkit.ProfileVersion,
			Adapter:     adapterKind,
			SecretStore: adapterkit.SecretStoreFile,
			CreatedAt:   c.now().UTC(),
		}
	}
	p.URL, p.PublishableKey = backend.URL, backend.PublishableKey
	return adapterkit.SaveProfile(c.cfgDir, c.profileName, p)
}

// optionalArg maps an empty string to SQL null, so an absent human_label
// is stored as null rather than as "".
func optionalArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// checkSecretFile refuses a relative --secret-file — the path is resolved
// against the process's working directory, which under a hook is the
// project tree (3.2), and a join secret must never be written there — and
// a parent directory that does not exist, before any network call, so a
// mistyped path cannot cost a created team.
func checkSecretFile(path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return errUsage("--secret-file must be an absolute path")
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return errConfig("the directory of --secret-file does not exist", "secret_file_unwritable")
	}
	return nil
}

// warnSecretShownOnce puts the 4.4.10 warning on stderr. It names no
// secret: the warning is about one, it never carries one.
func (c *command) warnSecretShownOnce(text string) {
	_, _ = io.WriteString(c.stderr, progName+": "+text+"\n")
}

// terminal returns stdin as a terminal, or refuses with `usage`. --prompt
// without a TTY is `usage` (4.4.10, B-7): there is nobody to ask. The test
// is x/term.IsTerminal on the descriptor, never os.ModeCharDevice, which
// is true for /dev/null as well (5.11).
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

// ---- team administration (P5-2, plan 5.10, capability team.admin) ----
//
// Three pass-throughs that answer the team's owner only (4.4.10; the
// backend's word for the owner is the creator, teams.created_by, which
// transfer_team moves). Their stdin and result documents are the
// protocol's. Every one runs c.parse, then authenticate(true) — profile,
// backend, credential and a bound, uuid-shaped team_ref; a team_ref that
// is not a backend id answers errNotMember() locally, which keeps the
// oracle rule true without a dial — then one RPC. The backend raises one
// text for every refusal of the owner gate (an unknown team, a
// non-member, a revoked or banned member, an active member that is not
// the owner), which errors.go already maps to errNotMember()'s fixed
// message; a self-target and a transfer to a principal that is not an
// active member are `brigade:invalid_input:principal_ref`, mapped to
// invalid_input with details.field principal_ref. No new error row exists
// for them.

// rotateSecretAnswer is rotate_join_secret's jsonb. JoinSecret is the one
// member of this file that holds a secret after an RPC returns: it goes
// to the --secret-file and to nothing else — not the result, not a log.
type rotateSecretAnswer struct {
	Status          string    `json:"status"`
	TeamID          string    `json:"team_id"`
	TeamName        string    `json:"team_name"`
	SecretVersion   int       `json:"secret_version"`
	SecretRotatedAt time.Time `json:"secret_rotated_at"`
	JoinSecret      string    `json:"join_secret"`
}

// revokeMembershipAnswer is revoke_membership's jsonb.
type revokeMembershipAnswer struct {
	TeamID         string `json:"team_id"`
	PrincipalRef   string `json:"principal_ref"`
	Status         string `json:"status"`
	Changed        bool   `json:"changed"`
	SessionsClosed int    `json:"sessions_closed"`
}

// revokeByVersionAnswer is revoke_memberships_by_version's jsonb.
type revokeByVersionAnswer struct {
	TeamID         string `json:"team_id"`
	MaxVersion     int    `json:"max_version"`
	Revoked        int    `json:"revoked"`
	SessionsClosed int    `json:"sessions_closed"`
}

// transferTeamAnswer is transfer_team's jsonb.
type transferTeamAnswer struct {
	TeamID       string `json:"team_id"`
	PrincipalRef string `json:"principal_ref"`
	Transferred  bool   `json:"transferred"`
}

// teamRotateSecret implements `team rotate-secret --secret-file <path>`:
// rotate_join_secret mints a fresh secret, bumps secret_version and
// leaves every membership as it was (rotation alone revokes nobody; the
// version revoke is `team revoke-member --max-version`). --secret-file is
// REQUIRED and the new secret goes there, 0600, and never to stdout
// (4.5 rule 14): the result is protocol.TeamRotateSecretResult, which has
// no join_secret member. The order is teamCreate's: checkSecretFile before
// any network call, so a mistyped path cannot cost a rotation; then the
// RPC; then the file. A write failure AFTER a successful rotation is
// `config` secret_file_unwritable and the new secret is lost — the old
// one is already dead — so the only recovery is to rotate again, which
// the stderr line says. An answer of the RPC that this adapter cannot use
// is the same loss behind `internal`, and gets the same line.
func (c *command) teamRotateSecret() (any, error) {
	fs := newFlags()
	secretFile := fs.String("secret-file", "", "write the new join secret to this path (0600); required")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if *secretFile == "" {
		return nil, errUsage("--secret-file <path> is required: the new join secret is never written to stdout")
	}
	if err := checkSecretFile(*secretFile); err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	var answer rotateSecretAnswer
	if err := c.rpc(c.ctx, rpcRotateJoinSecret, rpcArgs{"p_team_id": c.profile.TeamRef}, &answer); err != nil {
		return nil, err
	}
	// From here on the RPC has answered, so the rotation was made: an
	// answer that is no use has lost the new secret as surely as a file
	// that cannot be written, and the administrator is told the same.
	const lost = "the backend rotated the join secret and its answer could not be used; the previous secret no longer works and the new one is lost: rotate again"
	if answer.Status != statusRotated || answer.TeamID != c.profile.TeamRef || answer.SecretVersion < 2 {
		c.log.Warn(lost)
		return nil, errUnexpectedResponse("the backend did not answer a rotated team")
	}
	secret, err := protocol.ParseJoinSecret(answer.JoinSecret)
	if err != nil || secret.TeamRef() != answer.TeamID {
		c.log.Warn(lost)
		return nil, errUnexpectedResponse("the backend answered a join secret this adapter cannot parse")
	}
	if werr := adapterkit.WriteAtomic(*secretFile, []byte(secret.Secret()+"\n")); werr != nil {
		c.log.Warn("the new join secret could not be written to --secret-file after the rotation; the previous secret no longer works and the new one is lost: rotate again")
		return nil, errConfig("the file named by --secret-file could not be written after the rotation; the new secret is lost, rotate again", "secret_file_unwritable")
	}
	c.warnSecretShownOnce("the new join secret was written to the file named by --secret-file (mode 0600); the previous secret no longer works")
	teamName := answer.TeamName
	if teamName == "" {
		teamName = c.profile.TeamName
	}
	return &protocol.TeamRotateSecretResult{
		TeamRef: answer.TeamID, TeamName: teamName, SecretVersion: answer.SecretVersion,
		RotatedAt: answer.SecretRotatedAt, SecretFile: *secretFile,
	}, nil
}

// teamRevokeMember implements `team revoke-member` in its two forms:
// `--principal <ref> [--ban]` (revoke_membership) or `--max-version <n>`
// (revoke_memberships_by_version), or, with no flag, the one stdin
// document of either shape; adapterkit.TeamRevokeFlags reads the request
// for every adapter that carries the command. A principal_ref that is not
// uuid-shaped can name nobody this owner may revoke and answers the
// uniform unauthorized before any dial, as a non-uuid team_ref does
// (4.4.10 allows it for a shape the backend never issues).
func (c *command) teamRevokeMember() (any, error) {
	fs := newFlags()
	flags := adapterkit.NewTeamRevokeFlags(fs)
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	req, err := flags.Request(c.stdin)
	if err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	if req.JoinedSecretVersionLTE != nil {
		var answer revokeByVersionAnswer
		args := rpcArgs{"p_team_id": teamRef, "p_max_version": *req.JoinedSecretVersionLTE}
		if err := c.rpc(c.ctx, rpcRevokeByVersion, args, &answer); err != nil {
			return nil, err
		}
		if answer.Revoked < 0 || answer.SessionsClosed < 0 {
			return nil, errUnexpectedResponse("the backend did not answer a version revoke")
		}
		return &protocol.TeamRevokeByVersionResult{
			TeamRef: teamRef, MaxVersion: *req.JoinedSecretVersionLTE, Revoked: answer.Revoked, SessionsClosed: answer.SessionsClosed,
		}, nil
	}
	if !validUUID(req.PrincipalRef) {
		return nil, errNotMember()
	}
	var answer revokeMembershipAnswer
	args := rpcArgs{"p_team_id": teamRef, "p_user_id": req.PrincipalRef, "p_ban": req.Ban}
	if err := c.rpc(c.ctx, rpcRevokeMembership, args, &answer); err != nil {
		return nil, err
	}
	if answer.Status == "" {
		return nil, errUnexpectedResponse("the backend did not answer a membership status")
	}
	return &protocol.TeamRevokeMemberResult{
		TeamRef: teamRef, PrincipalRef: req.PrincipalRef, Status: answer.Status,
		Changed: answer.Changed, SessionsClosed: answer.SessionsClosed,
	}, nil
}

// teamTransfer implements `team transfer --principal <ref>` (or stdin
// {principal_ref}): transfer_team sets teams.created_by to an ACTIVE
// member of the team. The previous owner keeps its membership and its
// sessions and becomes an ordinary member (5.10: a delegation run before
// a loss, not a departure); its next administrative command answers the
// uniform unauthorized. The same non-uuid rule as revoke-member.
func (c *command) teamTransfer() (any, error) {
	fs := newFlags()
	flags := adapterkit.NewTeamTransferFlags(fs)
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	req, err := flags.Request(c.stdin)
	if err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	if !validUUID(req.PrincipalRef) {
		return nil, errNotMember()
	}
	var answer transferTeamAnswer
	args := rpcArgs{"p_team_id": c.profile.TeamRef, "p_new_creator": req.PrincipalRef}
	if err := c.rpc(c.ctx, rpcTransferTeam, args, &answer); err != nil {
		return nil, err
	}
	if !answer.Transferred {
		return nil, errUnexpectedResponse("the backend did not confirm the transfer")
	}
	return &protocol.TeamTransferResult{TeamRef: c.profile.TeamRef, PrincipalRef: req.PrincipalRef, Transferred: true}, nil
}

// ---- a team's move (4.4.10, capability team.move; work item 273, ADR 0003) ----
//
// A team that leaves this project for another backend keeps its team id.
// Its owner records the move here with `team move`: where the team went
// and the move ticket, a join secret of the team at the new backend. An
// active member's `team moved` reads the record, which is what lets that
// member's harness follow the team on this project's word and never on a
// team file's alone. The three RPCs are the team-move migration's
// (20261114120000_team_move.sql). The ticket is a secret: it is read from
// the file --ticket-file names and written to the file --ticket-file
// names, and is in no result, no error and no log line.

// teamMoveMigration is the migration the three RPCs come with, named where
// a project without it is told so.
const teamMoveMigration = "20261114120000_team_move.sql"

// teamMoveAnswer is the jsonb of record_team_move, clear_team_move and
// read_team_move. Ticket is set by read_team_move alone and goes to the
// --ticket-file and to nothing else.
type teamMoveAnswer struct {
	Moved       bool      `json:"moved"`
	TeamID      string    `json:"team_id"`
	Adapter     string    `json:"adapter"`
	URL         string    `json:"url"`
	Certificate string    `json:"certificate"`
	Ticket      string    `json:"ticket"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// result builds the protocol's result from a recorded move the backend
// answered, and refuses an answer that is not one about teamRef or that
// the protocol's own shape would refuse.
func (a *teamMoveAnswer) result(teamRef string) (*protocol.TeamMoveResult, error) {
	res := &protocol.TeamMoveResult{
		TeamRef: teamRef, Moved: true, Adapter: a.Adapter, URL: a.URL, Certificate: a.Certificate, RecordedAt: a.RecordedAt,
	}
	if !a.Moved || a.TeamID != teamRef || res.Validate() != nil {
		return nil, errUnexpectedResponse("the backend did not answer a recorded move of this team")
	}
	return res, nil
}

// teamMove implements `team move` (4.4.10, C-51): the owner records where
// the team went, or clears the record with --clear. The request and the
// ticket file are read and checked before any network call; the ticket
// must be a join secret that carries this team's id. On a project without
// the team-move migration the command is `config` and says which
// migration its Project Owner has to apply.
func (c *command) teamMove() (any, error) {
	fs := newFlags()
	flags := adapterkit.NewTeamMoveFlags(fs)
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	move, err := flags.Request(c.stdin)
	if err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	var answer teamMoveAnswer
	if move.Clear {
		if err := c.rpc(c.ctx, rpcClearTeamMove, rpcArgs{"p_team_id": teamRef}, &answer); err != nil {
			return nil, withoutTeamMoveMigration(err)
		}
		if answer.Moved || answer.TeamID != teamRef {
			return nil, errUnexpectedResponse("the backend did not confirm that the move is cleared")
		}
		return &protocol.TeamMoveResult{TeamRef: teamRef}, nil
	}
	if err := adapterkit.CheckMoveTicket(move.Ticket, teamRef); err != nil {
		return nil, err
	}
	args := rpcArgs{
		"p_team_id": teamRef, "p_adapter": move.Destination.Adapter, "p_url": move.Destination.URL,
		"p_ticket": move.Ticket, "p_certificate": optionalArg(move.Destination.Certificate),
	}
	if err := c.rpc(c.ctx, rpcRecordTeamMove, args, &answer); err != nil {
		return nil, withoutTeamMoveMigration(err)
	}
	return answer.result(teamRef)
}

// teamMoved implements `team moved [--ticket-file <path>]` (4.4.10, C-52):
// every active member reads whether the team moved, and where. With
// --ticket-file the ticket of a recorded move is written to that path for
// its owner alone. A project without the team-move migration cannot say
// that a team moved, so it is answered as one that has not (PGRST202 on
// read_team_move): the member's harness then does what it does for a team
// that did not move, and nothing is lost that the migration does not add.
func (c *command) teamMoved() (any, error) {
	fs := newFlags()
	ticketFile := fs.String("ticket-file", "", "write the move ticket to this path (0600)")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if err := adapterkit.CheckTicketFileTarget(*ticketFile); err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	var answer teamMoveAnswer
	if err := c.rpc(c.ctx, rpcReadTeamMove, rpcArgs{"p_team_id": teamRef}, &answer); err != nil {
		if isMigrationDrift(err) {
			c.log.Debug("the backend has no record of moves (it lacks the team-move migration); answering that the team has not moved")
			return &protocol.TeamMoveResult{TeamRef: teamRef}, nil
		}
		return nil, err
	}
	if !answer.Moved {
		if answer.TeamID != teamRef {
			return nil, errUnexpectedResponse("the backend answered about another team")
		}
		return &protocol.TeamMoveResult{TeamRef: teamRef}, nil
	}
	res, err := answer.result(teamRef)
	if err != nil {
		return nil, err
	}
	if *ticketFile == "" {
		return res, nil
	}
	if adapterkit.CheckMoveTicket(answer.Ticket, teamRef) != nil {
		return nil, errUnexpectedResponse("the backend answered a move ticket this adapter cannot use")
	}
	if err := adapterkit.WriteTicketFile(*ticketFile, answer.Ticket); err != nil {
		return nil, err
	}
	res.TicketFile = *ticketFile
	return res, nil
}

// withoutTeamMoveMigration turns the migration-drift answer of a `team
// move` into the `config` it is for the team's owner: the project lacks
// the migration that keeps a move's record, and its Project Owner applies
// it. Every other error is returned as it is.
func withoutTeamMoveMigration(err error) error {
	if !isMigrationDrift(err) {
		return err
	}
	return errConfig("this backend cannot record a team's move: it lacks the migration "+teamMoveMigration+", which its Project Owner applies", reasonMigrationDrift)
}
