package rest

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The `team *` group (4.4.10): create, join, leave, members. The binding
// rules are local and run before any request; the secret is read from
// stdin or a no-echo prompt, sent to the server once and never stored; the
// credential the server issues goes to credential.json and nowhere else.

// maxCreateTokenBytes bounds the file --create-token-file names.
const maxCreateTokenBytes = 4096

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

// teamCreate implements 4.4.10 (C-03, C-03b) as one request to
// /v1/team/create. The order is the other adapters': the local binding
// check and the backend resolution first (`conflict` on a bound profile,
// `config` without a backend), then the request from stdin, --name/--label
// or a TTY prompt, then the call. The server creates a principal for a
// profile that holds no credential and issues its credential with the
// result (the contract's section 3); a profile that holds one creates the
// team as that principal. The join secret is the server's; it is printed
// once, or written 0600 to --secret-file and omitted from stdout, and
// never stored.
func (c *command) teamCreate() (any, error) {
	fs := newFlags()
	name := fs.String("name", "", "the team name (instead of stdin)")
	label := fs.String("label", "", "this member's human label")
	prompt := fs.Bool("prompt", false, "ask on a terminal")
	secretFile := fs.String("secret-file", "", "write the join secret to this path (0600) instead of stdout")
	tokenFile := fs.String("create-token-file", "", "read the server's creation token from this file")
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
	where, err := c.resolveBackend(p, nil)
	if err != nil {
		return nil, err
	}
	headers, err := createTokenHeaders(*tokenFile)
	if err != nil {
		return nil, err
	}
	req, err := c.teamCreateRequest(*name, *label, *prompt)
	if err != nil {
		return nil, err
	}
	if err := c.prepareBinding(p, where); err != nil {
		return nil, err
	}
	humanLabel := req.HumanLabel
	if humanLabel == "" {
		humanLabel = c.profile.HumanLabel
	}
	var answer teamCreateAnswer
	if err := c.bindingCall(pathTeamCreate, &teamCreateBody{TeamName: req.TeamName, HumanLabel: humanLabel}, &answer, headers); err != nil {
		return nil, err
	}
	secret, err := protocol.ParseJoinSecret(answer.JoinSecret)
	if err != nil || secret.TeamRef() != answer.TeamRef {
		return nil, errUnexpectedResponse("the backend answered a join secret this adapter cannot parse")
	}
	if err := c.keepCredential(answer.Credential, answer.PrincipalRef); err != nil {
		return nil, err
	}
	// The secret is shown ONCE (4.4.10). The file is written before the
	// profile is bound: a caller whose --secret-file cannot be written keeps
	// an unbound profile instead of being bound to a team whose only key
	// nobody holds.
	if *secretFile != "" {
		if werr := adapterkit.WriteAtomic(*secretFile, []byte(secret.Secret()+"\n")); werr != nil {
			c.log.Warn("the join secret could not be written to --secret-file; the profile stays unbound")
			return nil, errConfig("the file named by --secret-file could not be written", reasonSecretFile)
		}
	}
	if err := c.bind(answer.TeamRef, answer.TeamName, answer.PrincipalRef, humanLabel); err != nil {
		return nil, err
	}
	if *secretFile != "" {
		c.warnSecretShownOnce("the join secret was written to the file named by --secret-file (mode 0600)")
		return &teamCreateResultWithoutSecret{
			TeamRef: answer.TeamRef, TeamName: answer.TeamName, PrincipalRef: answer.PrincipalRef,
		}, nil
	}
	c.warnSecretShownOnce("the join secret is printed once and is never stored; anyone holding it can join this team")
	return &answer.TeamCreateResult, nil
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

// teamJoin implements 4.4.10 (C-04, C-08) as one request to /v1/team/join.
// The profile file is read first (a malformed one is `config`), then the
// request, because its `backend` member may be what configures an
// unconfigured profile; then the local binding rule: a secret naming
// another team than the bound one is `conflict`, a secret naming the bound
// team is a rejoin. A profile that holds a credential joins as that
// principal, so a member who left comes back as the same membership
// (`rejoined: true`); a profile that holds none, after `profile
// revoke-credentials` above all, is a new principal to the server, which
// issues its credential with the result (the contract's section 3).
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
	where, err := c.resolveBackend(p, req.Backend)
	if err != nil {
		return nil, err
	}
	if err := c.prepareBinding(p, where); err != nil {
		return nil, err
	}
	humanLabel := req.HumanLabel
	if humanLabel == "" {
		humanLabel = c.profile.HumanLabel
	}
	var answer teamJoinAnswer
	if err := c.bindingCall(pathTeamJoin, &teamJoinBody{JoinSecret: secret.Secret(), HumanLabel: humanLabel}, &answer, nil); err != nil {
		return nil, err
	}
	if answer.TeamRef != secret.TeamRef() {
		return nil, errUnexpectedResponse("the backend answered a team other than the one the secret names")
	}
	if err := c.keepCredential(answer.Credential, answer.PrincipalRef); err != nil {
		return nil, err
	}
	if err := c.bind(answer.TeamRef, answer.TeamName, answer.PrincipalRef, humanLabel); err != nil {
		return nil, err
	}
	return &answer.TeamJoinResult, nil
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

// prepareBinding is the binding commands' half of the ladder: the backend
// url is written to team.json when the file does not carry it yet, the
// client is built, and the credential is loaded when the profile holds
// one. Holding none is not a failure here: the server issues one.
func (c *command) prepareBinding(p *adapterkit.Profile, where server) error {
	p, err := c.bootstrapProfile(p, where)
	if err != nil {
		return err
	}
	if err := c.connect(p); err != nil {
		return err
	}
	cred, ok, err := c.loadCredential()
	if err != nil {
		return err
	}
	if ok {
		c.cred = cred
	}
	return nil
}

// bindingCall is one create or join request, with the command's deadline
// and the creation token when there is one.
func (c *command) bindingCall(path string, in any, out protocol.Validator, headers map[string]string) error {
	ctx, cancel := context.WithTimeout(c.ctx, requestTimeout)
	defer cancel()
	return c.do(ctx, &request{path: path, in: in, out: out, headers: headers})
}

// keepCredential stores the credential a create or a join came back with.
// The contract's section 3: a new principal always gets one, and a known
// principal gets none or a new one that replaces the old. A result with no
// credential for a profile that holds none leaves nothing to act with, so
// it is a malformed answer.
func (c *command) keepCredential(issued, principalRef string) error {
	if issued == "" {
		if c.cred == nil {
			return errUnexpectedResponse("the backend created a principal and issued no credential for it")
		}
		return nil
	}
	return c.acceptCredential(issued, principalRef)
}

// teamLeave implements 4.4.10 (C-08): /v1/team/leave revokes this
// principal's own membership and closes its open sessions in the team,
// then the profile is unbound and the credential kept. It is idempotent: a
// second call answers from credential.json's last_team_ref with no
// request. A profile that was NEVER bound has nothing to answer with, so
// it is `config`.
func (c *command) teamLeave() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	if err := c.authenticate(false); err != nil {
		return nil, err
	}
	principal := principalOf(c.profile, c.cred)
	teamRef := c.profile.TeamRef
	if teamRef == "" {
		if c.cred.LastTeamRef == "" || principal == "" {
			return nil, errConfig("profile has never been bound to a team", reasonNoTeamBound)
		}
		return &protocol.TeamLeaveResult{TeamRef: c.cred.LastTeamRef, PrincipalRef: principal, Left: true}, nil
	}
	var answer protocol.TeamLeaveResult
	if err := c.call(pathTeamLeave, &teamRequest{TeamRef: teamRef}, &answer); err != nil {
		return nil, err
	}
	if answer.TeamRef != teamRef {
		return nil, errUnexpectedResponse("the backend answered a team other than the one the profile is bound to")
	}
	if err := c.unbind(); err != nil {
		return nil, err
	}
	if err := c.rememberTeam(teamRef, answer.PrincipalRef); err != nil {
		return nil, err
	}
	return &answer, nil
}

// teamMembers implements 4.4.10 (C-43): /v1/team/members answers every
// ACTIVE member of the profile's team. The team the profile names travels
// with the request, so a principal that is not an active member of it gets
// the server's uniform `unauthorized`.
func (c *command) teamMembers() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	var answer teamMembersResult
	if err := c.call(pathTeamMembers, &teamRequest{TeamRef: c.profile.TeamRef}, &answer); err != nil {
		return nil, err
	}
	return &answer, nil
}

// createTokenHeaders reads the operator's creation token from the file
// --create-token-file names (the contract's section 3: from a file, never
// from argv) and returns it as the request header. Without the flag there
// is no header.
func createTokenHeaders(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, errUsage("--create-token-file must be an absolute path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errConfig("the file named by --create-token-file could not be read", reasonCreateTokenFile)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxCreateTokenBytes+1))
	if err != nil || len(data) > maxCreateTokenBytes {
		return nil, errConfig("the file named by --create-token-file could not be read", reasonCreateTokenFile)
	}
	token := strings.TrimSpace(string(data))
	if !validHeaderToken(token) {
		return nil, errConfig("the file named by --create-token-file does not hold a creation token", reasonCreateTokenFile)
	}
	return map[string]string{createTokenHeader: token}, nil
}

// validHeaderToken reports whether s can be a header value as it stands:
// 1 to 512 printable ASCII characters and no space. The contract gives the
// creation token no alphabet, so this is only what keeps a file's content
// from splitting a header.
func validHeaderToken(s string) bool {
	if s == "" || len(s) > maxCredentialChars {
		return false
	}
	for i := range len(s) {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// checkSecretFile refuses a relative --secret-file (the path is resolved
// against the process's working directory, which under a hook is the
// project tree, and a join secret must never be written there) and a
// parent directory that does not exist, before any request, so a mistyped
// path cannot cost a created team.
func checkSecretFile(path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return errUsage("--secret-file must be an absolute path")
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return errConfig("the directory of --secret-file does not exist", reasonSecretFile)
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
// is true for the null device as well.
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
