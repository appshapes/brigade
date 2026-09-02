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
// `unauthorized` text (4.5.7).

// The environment pair a binding command honours when there is no
// profile.json to read a backend from (4.1: a BRIGADE_<ADAPTER>_* variable
// is honoured only when a human — or the conformance suite — runs the
// adapter from a shell; under a live session neither arrives). The pair
// is written to profile.json exactly as `profile init` would write it, so
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

// The RPC names of the finished migration.
const (
	rpcCreateTeam  = "create_team"
	rpcJoinTeam    = "join_team"
	rpcLeaveTeam   = "leave_team"
	rpcListMembers = "list_members"
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
// time; joined_secret_version, which the RPC also answers, is not part of
// the protocol's shape and is dropped here.
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

// bootstrapProfile writes the resolved backend pair into profile.json
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
