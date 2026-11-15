package rest

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The `team *` group (4.4.10): create, join, leave, members, the three
// administration commands, and at the bottom of this file the three that
// take a team in and open and close its move. The binding rules
// are local and run before any request; the secret is read from stdin or a
// no-echo prompt, sent to the server once and never stored; the credential
// the server issues goes to credential.json and nowhere else.

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
	case "rotate-secret":
		return c.teamRotateSecret()
	case "revoke-member":
		return c.teamRevokeMember()
	case "transfer":
		return c.teamTransfer()
	case "take-in":
		return c.teamTakeIn()
	case "open-move":
		return c.teamOpenMove()
	case "close-move":
		return c.teamCloseMove()
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
	// The secret half of the join secret the answer carries is registered
	// before anything else is done with the answer, as a rotation's is, and
	// the credential beside it: the names of this answer are printed and
	// kept, and with --secret-file the secret itself is on no stream.
	c.hideSecretHalf(answer.JoinSecret)
	c.hideIssued(answer.Credential)
	c.hideInAnswer(&answer.TeamName, &answer.PrincipalRef)
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
	// What is presented may be the team's join secret or the ticket of its
	// move: either way its secret half is hidden before anything is sent.
	c.hideSecretHalf(secret.Secret())
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
	c.hideIssued(answer.Credential)
	c.hideInAnswer(&answer.TeamName, &answer.PrincipalRef)
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

// bindingCall is one create, join or take-in request, with the command's
// deadline and the creation token when there is one.
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
// project tree, and a join secret must never be written there), a parent
// directory that does not exist and one a file cannot be written to, before
// any request, so a mistyped path cannot cost a created team and a
// directory that takes no file cannot cost a rotation: the server ends the
// previous secret before the adapter has the new one to write.
// adapterkit.CanWriteAtomic proves the last by writing a file of its own
// beside the path and removing it; what is at the path stays as it is.
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
	if adapterkit.CanWriteAtomic(path) != nil {
		return errConfig("the file named by --secret-file cannot be written: its directory takes no new file, or the path names a directory", reasonSecretFile)
	}
	return nil
}

// minHiddenHalfChars is the fewest characters the secret half of a join
// secret has for hideSecretHalf to register it. A backend mints a half
// from at least 128 random bits, which no alphabet writes in fewer than
// sixteen characters, so every minted half is registered. A shorter one
// is somebody's own text, and a short text registered as it stands would
// be cut out of every line that happens to hold those characters: `ops`
// out of a team's name, a digit out of a status.
const minHiddenHalfChars = 16

// hideSecretHalf registers on the redactor the secret half of a join
// secret or a move ticket: what follows its last dot, when it is
// minHiddenHalfChars or longer. The redactor's own pattern replaces the
// whole `brg1.<team_ref>.<secret>` wherever it finds one and knows
// nothing of the half alone, so a server that broke the contract's rule
// and repeated only the half in a refusal would have this adapter print
// the one part of the secret that is secret: the reference is in the
// committed team file. It is called with what a command presents, before
// the request (`team join`, `team take-in`), and with what an answer
// carries, before anything else is done with the answer (`team create`,
// `team rotate-secret`, `team open-move`). A text with no dot has no half
// and registers nothing. What is registered is hidden from every log
// line, from the text of a refusal, and from the names of an answer
// (hideInAnswer).
func (c *command) hideSecretHalf(secret string) {
	dot := strings.LastIndexByte(secret, '.')
	if dot < 0 {
		return
	}
	if half := secret[dot+1:]; len(half) >= minHiddenHalfChars {
		c.redactor.Add(half)
	}
}

// hideInAnswer takes out of the names a server chose in an answer what
// this command registered on the redactor. A result is printed on stdout,
// and the team's name and the principal's reference of a join and a
// take-in are kept in team.json and credential.json: a server that broke
// the contract's rule and put into one of them the secret half of what the
// command presented, or of what its own answer carries, gets a marker in
// its place, on stdout and in the files. It is called once the half is
// registered, for the commands that register one: `team create`, `team
// join`, `team take-in`, `team rotate-secret` and `team open-move`. The
// three that are answered a credential register that first (hideIssued).
// The team's reference needs none: each of them holds it against the one
// it knows.
//
// Only what is registered is taken out (RedactExact), and not what a
// format pattern takes for a secret, as in the text of a refusal
// (readEnvelope): a name is a person's own text, kept as the team's name
// from then on, and a pattern would cut "Standard Bearer ops" down to
// "Standard" and a marker.
func (c *command) hideInAnswer(names ...*string) {
	for _, name := range names {
		*name = c.redactor.RedactExact(*name)
	}
}

// hideIssued registers the credential an answer carries, before the names
// of that answer are looked at (hideInAnswer) and long before it is kept
// (acceptCredential, which registers it for every line after). A server
// that repeated the credential it issued in the team's name or in the
// principal's reference would otherwise have it printed in the command's
// result and kept in team.json, where no credential belongs. What comes
// here is a credential by this adapter's rule or nothing: an answer that
// carried anything else was refused when it was read (the answers'
// Validate, shapes.go), and nothing is registered for none.
func (c *command) hideIssued(credential string) {
	c.redactor.Add(credential)
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

// ---- team administration (4.4.10, capability team.admin) ----
//
// Three commands that answer the team's owner only, each one request to an
// endpoint of the contract's section 3a. The flags, the stdin forms, the
// local checks and their messages are the Supabase adapter's, so a team's
// administrator types the same thing whichever backend the team is on; the
// requests of the two that take input are read by adapterkit. The order is
// that adapter's too: argv and the request first, so a malformed one costs
// nothing; then the ladder; then the question whether this server carries
// the endpoints at all; then the request. The server is the authority on
// who the owner is: every refusal of that is its one uniform
// `unauthorized`, passed on as it is.

// requireTeamAdmin asks the server whether it carries the administration
// endpoints. They are an addition to version 1 that a server may leave
// out, and one that does says so by not listing `team.admin` in `/v1/info`
// (the contract's section 5). Such a server would answer the request
// itself `not_found`, which reads as a session or a message that is not
// there. So the adapter asks first and answers `config`, with a message
// that says what is missing and whose it is to mend.
func (c *command) requireTeamAdmin() error {
	return c.requireCapability(capTeamAdmin,
		"the server this team is on does not offer team administration (rotate-secret, revoke-member, transfer); its operator needs to update it",
		reasonServerLacksTeamAdmin)
}

// requireCapability asks `/v1/info`, which needs no credential, whether
// the server lists capability, and answers `config` with message and
// reason when it does not.
func (c *command) requireCapability(capability, message, reason string) error {
	var info serverInfo
	if err := c.call(pathInfo, nil, &info); err != nil {
		return err
	}
	if !slices.Contains(info.Capabilities, capability) {
		return errConfig(message, reason)
	}
	return nil
}

// teamRotateSecret implements `team rotate-secret --secret-file <path>`:
// /v1/team/rotate-secret gives the team a new join secret and leaves every
// membership as it was (the version revoke is `team revoke-member
// --max-version`). --secret-file is REQUIRED and the new secret goes
// there, 0600, written atomically, and to no output (4.5.14): the result
// is protocol.TeamRotateSecretResult, which names the file and carries no
// secret. checkSecretFile runs before any request, so a mistyped path
// cannot cost a rotation. A write that fails AFTER the rotation is
// `config` secret_file_unwritable and the new secret is lost, the previous
// one being dead already; the only way on is to rotate again, which the
// message says. An answer that says ok and cannot be used is the same
// loss, and `internal` alone would not tell the administrator so:
// rotationLost says it on stderr.
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
	if err := c.requireTeamAdmin(); err != nil {
		return nil, err
	}
	var answer rotateSecretAnswer
	if err := c.call(pathTeamRotateSecret, &teamRequest{TeamRef: c.profile.TeamRef}, &answer); err != nil {
		var unreadable *unreadableResult
		if errors.As(err, &unreadable) {
			c.rotationLost()
		}
		return nil, err
	}
	// The new secret is registered on the redactor before anything else is
	// done with the answer, so no line this command writes can carry it, and
	// so is its secret half, which is not of a join secret's form.
	c.redactor.Add(answer.JoinSecret)
	c.hideSecretHalf(answer.JoinSecret)
	c.hideInAnswer(&answer.TeamName)
	if answer.TeamRef != c.profile.TeamRef {
		c.rotationLost()
		return nil, errUnexpectedResponse("the backend answered a team other than the one the profile is bound to")
	}
	secret, err := protocol.ParseJoinSecret(answer.JoinSecret)
	if err != nil || secret.TeamRef() != answer.TeamRef {
		c.rotationLost()
		return nil, errUnexpectedResponse("the backend answered a join secret this adapter cannot parse")
	}
	if werr := adapterkit.WriteAtomic(*secretFile, []byte(secret.Secret()+"\n")); werr != nil {
		c.log.Warn("the new join secret could not be written to --secret-file after the rotation; the previous secret no longer works and the new one is lost: rotate again")
		return nil, errConfig("the file named by --secret-file could not be written after the rotation; the new secret is lost, rotate again", reasonSecretFile)
	}
	c.warnSecretShownOnce("the new join secret was written to the file named by --secret-file (mode 0600); the previous secret no longer works")
	return &protocol.TeamRotateSecretResult{
		TeamRef: answer.TeamRef, TeamName: answer.TeamName, SecretVersion: answer.SecretVersion,
		RotatedAt: answer.RotatedAt, SecretFile: *secretFile,
	}, nil
}

// rotationLost says what an ok answer to a rotation that this adapter
// cannot use has cost: the server rotated, so the previous secret is dead,
// and the new one is in an answer nobody can read. The command still
// answers `internal`, which is what it is; this line, on stderr through
// the logger, is what tells the administrator to rotate again.
func (c *command) rotationLost() {
	c.log.Warn("the server rotated the join secret and its answer could not be used; the previous secret no longer works and the new one is lost: rotate again")
}

// teamRevokeMember implements `team revoke-member` in its two forms:
// `--principal <ref> [--ban]` or `--max-version <n>`, or, with no flag,
// the one stdin document of either shape. The request travels to
// /v1/team/revoke-member as it was validated, with the profile's team
// beside it, and the server answers the result of the form it was asked
// in. An answer about another team, another principal or another version
// than the one asked for is not this command's result, and is `internal`.
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
	if err := c.requireTeamAdmin(); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	body := &revokeMemberBody{TeamRevokeMemberRequest: *req, TeamRef: teamRef}
	if req.JoinedSecretVersionLTE != nil {
		out := &protocol.TeamRevokeByVersionResult{}
		if err := c.call(pathTeamRevokeMember, body, out); err != nil {
			return nil, err
		}
		if out.TeamRef != teamRef || out.MaxVersion != *req.JoinedSecretVersionLTE {
			return nil, errUnexpectedResponse("the backend answered a revocation other than the one it was asked for")
		}
		return out, nil
	}
	asked := protocol.MembershipRevoked
	if req.Ban {
		asked = protocol.MembershipBanned
	}
	out := &protocol.TeamRevokeMemberResult{}
	if err := c.call(pathTeamRevokeMember, body, out); err != nil {
		return nil, err
	}
	if out.TeamRef != teamRef || out.PrincipalRef != req.PrincipalRef || out.Status != asked {
		return nil, errUnexpectedResponse("the backend answered a revocation other than the one it was asked for")
	}
	return out, nil
}

// teamTransfer implements `team transfer --principal <ref>` (or stdin
// {principal_ref}): /v1/team/transfer makes an active member of the team
// its owner. The previous owner stays a member, and its next
// administration command is the server's uniform `unauthorized`.
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
	if err := c.requireTeamAdmin(); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	out := &protocol.TeamTransferResult{}
	if err := c.call(pathTeamTransfer, &transferBody{TeamTransferRequest: *req, TeamRef: teamRef}, out); err != nil {
		return nil, err
	}
	if out.TeamRef != teamRef || out.PrincipalRef != req.PrincipalRef {
		return nil, errUnexpectedResponse("the backend answered a transfer other than the one it was asked for")
	}
	return out, nil
}

// ---- taking a team in (4.4.10, capability team.take_in) ----
//
// Three commands of the backend a team moves to (work item 328, ADR 0005),
// each one request to an endpoint a server may leave out. `team take-in`
// is a binding command like `team join`: it needs no credential, and the
// server issues one with the result for a profile that holds none. `team
// open-move` and `team close-move` answer the team's owner only, like the
// administration commands, and follow their order: argv first, then the
// ladder, then the question whether this server carries the endpoints,
// then the request.

// requireTeamTakeIn asks the server whether it carries the take-in
// endpoints, as requireTeamAdmin asks about the administration ones: a
// server that leaves them out would answer the request `not_found`.
func (c *command) requireTeamTakeIn() error {
	return c.requireCapability(capTeamTakeIn,
		"the server does not offer taking a team in by request (take-in, open-move, close-move); its operator needs to update it, or takes the team in and opens the move with the server's own commands",
		reasonServerLacksTeamTakeIn)
}

// teamTakeIn implements `team take-in`: /v1/team/take-in takes in, at the
// profile's server, the team whose join secret the request carries, and
// makes the caller its owner; a team the server already holds under that
// secret is joined. The request is read by adapterkit (the document, or
// --secret-file, --name and --label in the place of its members), and the
// order is `team join`'s: the profile file, the request, the local binding
// rule (a secret naming another team than the bound one is `conflict`, the
// bound team itself is a repeat and proceeds), the backend, and only then
// anything on the network. The secret is sent once and never stored; the
// creation token rides as on `team create`, because a server that limits
// who may create a team limits who may take one in.
//
// The answer carries the credential of the owner the server made, and an
// answer that is lost would leave that owner with nobody: so the request
// carries a retry key, which is in take-in-key.json before anything is
// sent (retrykey.go). The same command run again finds the key and sends
// it, and the server answers for the principal the first request made,
// with a new credential. On an answer the credential is written first and
// the key file removed only then; on anything else the key stays where it
// is, for the next run. It stays as well when the answer was for a
// credential this profile holds that does not own the team, and the key
// was an earlier run's: the server never looked at it
// (keptForAnEarlierTakeIn).
func (c *command) teamTakeIn() (any, error) {
	fs := newFlags()
	flags := adapterkit.NewTeamTakeInFlags(fs)
	tokenFile := fs.String("create-token-file", "", "read the server's creation token from this file")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	p, present, err := c.loadProfile()
	if err != nil {
		return nil, err
	}
	taken, err := flags.Request(c.stdin)
	if err != nil {
		return nil, err
	}
	// The whole secret is not registered on the redactor: the redactor
	// replaces every text of a join secret's form wherever it finds one
	// (adapterkit/log), the whole of this one included, so a registration of
	// the same text would change no line. Its secret half is not of that
	// form, and is.
	c.hideSecretHalf(taken.Secret.Secret())
	if present && p.TeamRef != "" && p.TeamRef != taken.Secret.TeamRef() {
		return nil, errConflict("this profile is already bound to another team", reasonProfileBound)
	}
	where, err := c.resolveBackend(p, taken.Request.Backend)
	if err != nil {
		return nil, err
	}
	headers, err := createTokenHeaders(*tokenFile)
	if err != nil {
		return nil, err
	}
	if err := c.prepareBinding(p, where); err != nil {
		return nil, err
	}
	if err := c.requireTeamTakeIn(); err != nil {
		return nil, err
	}
	humanLabel := taken.Request.HumanLabel
	if humanLabel == "" {
		humanLabel = c.profile.HumanLabel
	}
	key, kept, err := c.takeInRetryKey(taken.Secret.TeamRef())
	if err != nil {
		return nil, err
	}
	body := &teamTakeInBody{
		JoinSecret: taken.Secret.Secret(), TeamName: taken.Request.TeamName, HumanLabel: humanLabel, RetryKey: key,
	}
	var answer teamTakeInAnswer
	if err := c.bindingCall(pathTeamTakeIn, body, &answer, headers); err != nil {
		return nil, err
	}
	if answer.TeamRef != taken.Secret.TeamRef() {
		return nil, errUnexpectedResponse("the backend answered a team other than the one the secret names")
	}
	c.hideIssued(answer.Credential)
	c.hideInAnswer(&answer.TeamName, &answer.PrincipalRef)
	if err := c.keepCredential(answer.Credential, answer.PrincipalRef); err != nil {
		return nil, err
	}
	// The credential is kept: the key has nothing left to repeat, unless it
	// is an earlier take-in's that this answer never looked at.
	if keptForAnEarlierTakeIn(kept, &answer) {
		c.leaveRetryKey()
	} else {
		c.dropRetryKey()
	}
	if err := c.bind(answer.TeamRef, answer.TeamName, answer.PrincipalRef, humanLabel); err != nil {
		return nil, err
	}
	return &answer.TeamTakeInResult, nil
}

// teamOpenMove implements `team open-move --ticket-file <path>
// [--ticket-days <n>]`: /v1/team/open-move opens the move of the profile's
// team and answers its ticket, once. --ticket-file is REQUIRED and the
// ticket goes there, 0600, written atomically, and to no output (4.5.14):
// the result is protocol.TeamOpenMoveResult, which names the file and
// carries no ticket. The path and the days are checked before any request.
// A write that fails AFTER the move was opened is `config`
// ticket_file_unwritable and the ticket is lost; opening the move again
// replaces it, which the message says. An answer that says ok and cannot
// be used is the same loss: ticketLost says it on stderr.
func (c *command) teamOpenMove() (any, error) {
	fs := newFlags()
	flags := adapterkit.NewTeamOpenMoveFlags(fs)
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	open, err := flags.Request()
	if err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	if err := c.requireTeamTakeIn(); err != nil {
		return nil, err
	}
	var answer openMoveAnswer
	if err := c.call(pathTeamOpenMove, &openMoveBody{TeamRef: c.profile.TeamRef, TicketDays: open.TicketDays}, &answer); err != nil {
		var unreadable *unreadableResult
		if errors.As(err, &unreadable) {
			c.ticketLost()
		}
		return nil, err
	}
	// The ticket is a join secret in form, which the redactor replaces
	// wherever it finds one; one that is not is refused below in fixed words.
	// Its secret half is not of that form, and is registered. Nothing after
	// this line writes any part of the answer but the result, which has no
	// ticket member, and the half is taken out of the name in it first.
	c.hideSecretHalf(answer.Ticket)
	c.hideInAnswer(&answer.TeamName)
	if answer.TeamRef != c.profile.TeamRef {
		c.ticketLost()
		return nil, errUnexpectedResponse("the backend answered a team other than the one the profile is bound to")
	}
	if adapterkit.CheckMoveTicket(answer.Ticket, answer.TeamRef) != nil {
		c.ticketLost()
		return nil, errUnexpectedResponse("the backend answered a move ticket this adapter cannot parse")
	}
	if werr := adapterkit.WriteAtomic(open.TicketFile, []byte(answer.Ticket+"\n")); werr != nil {
		c.log.Warn("the move ticket could not be written to --ticket-file after the move was opened; the ticket is lost: open the move again, which replaces it")
		return nil, errConfig("the file named by --ticket-file could not be written after the move was opened; the ticket is lost: open the move again, which replaces it", reasonTicketFile)
	}
	c.warnSecretShownOnce("the move ticket was written to the file named by --ticket-file (mode 0600)")
	return &protocol.TeamOpenMoveResult{
		TeamRef: answer.TeamRef, TeamName: answer.TeamName, EndsAt: answer.EndsAt,
		Replaced: answer.Replaced, TicketFile: open.TicketFile,
	}, nil
}

// ticketLost says what an ok answer to an open-move that this adapter
// cannot use has cost: the server opened the move, replacing whatever
// ticket the team had, and the new ticket is in an answer nobody can read.
// The command still answers `internal`; this line, on stderr through the
// logger, is what tells the owner to open the move again.
func (c *command) ticketLost() {
	c.log.Warn("the server opened the move and its answer could not be used; the ticket is lost, and one the team had before admits nobody: open the move again, which replaces it")
}

// teamCloseMove implements `team close-move`: /v1/team/close-move ends the
// move of the profile's team. Its ticket admits nobody from then on, and
// a team with no move answers closed false.
func (c *command) teamCloseMove() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	if err := c.requireTeamTakeIn(); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	out := &protocol.TeamCloseMoveResult{}
	if err := c.call(pathTeamCloseMove, &teamRequest{TeamRef: teamRef}, out); err != nil {
		return nil, err
	}
	if out.TeamRef != teamRef {
		return nil, errUnexpectedResponse("the backend answered a team other than the one the profile is bound to")
	}
	return out, nil
}
