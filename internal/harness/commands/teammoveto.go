package commands

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teammove"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
)

// `brigade team move --to <server url> --secret-file <path>` (work item
// 328, ADR 0005 and ADR 0006) is a team's whole move to a Brigade server
// in one command, run by the team's administrator in a checkout of the
// team. Until it, the move was six steps by two people: the server's
// operator took the team in and opened its move with the server stopped
// and handed a ticket back as a file, and the administrator edited the
// team file by hand, joined the server and recorded the move.
//
// The order is the design's, and each step is one a second run repeats
// without harm:
//
//	a. check, changing nothing (checkMove)
//	b. take the team in on the server, as its owner (takeIn)
//	c. open the move; the ticket goes to a private file (openMove)
//	d. record the move on the backend the team leaves (recordMoveTo)
//	e. only then rewrite the team file, the binding and the pin (writeMove)
//	f. say what was done and what is left (reportMove)
//
// A repeated take-in is a join that still owns, a second open replaces the
// ticket, a second record replaces the first, and the files are written
// from the same values. One value, the destination the administrator
// typed, goes to the record and to the team file, so the two cannot differ:
// a record that names anything but what the file names is followed by
// nobody.
//
// The join secret is read from the file the administrator names and goes
// to the server's adapter in its stdin document. The ticket goes from the
// server's adapter to a file and from that file to the old backend's
// adapter. Neither is an argument, and neither is in a line, a result or
// an error of this command.

// teamMoveMigration is the Supabase migration that keeps a move's record,
// named where a project without it is told what to apply.
const teamMoveMigration = "20261114120000_team_move"

// takeInCapability is the 4.7 capability of the three commands a team is
// taken in and its move opened and closed with. An adapter that does not
// advertise it is asked none of them.
const takeInCapability = "team.take_in"

// moveTicketDir is the directory under Brigade's state directory that
// holds a move's ticket for the moment between the server's answer and the
// old backend's record. The file in it is named after the team's key at the
// server, so a run that was killed in that moment leaves a file the next
// run for the same team writes over and removes.
const moveTicketDir = "team-move"

// olderShown caps how many sessions on an older plugin a line names.
const olderShown = 10

// supabaseHost reads a hosted Supabase project's ref off its host.
var supabaseHost = regexp.MustCompile(`^([a-z0-9]{1,40})\.supabase\.co$`)

// The details.reason values of the refusals this command makes itself.
// Three are an adapter's own word, kept so that a caller reading --json
// sees one reason whoever refused.
const (
	reasonNotJoinedAtSource = "not_joined_at_source"
	reasonNoTeamTakeIn      = "no_team_take_in"
	reasonServerLacksTakeIn = "server_lacks_team_take_in"
	reasonMigrationDrift    = "migration_drift"
	reasonOtherJoinSecret   = "other_join_secret" //nolint:gosec // G101: a details.reason token, not a credential
	reasonNotTheCreator     = "not_the_teams_creator"
	reasonNotOwnerThere     = "not_owner_at_destination"
	reasonCreateToken       = "create_token" //nolint:gosec // G101: a details.reason token, not a credential
	reasonTeamNameUnknown   = "team_name_unknown"
)

// An olderSession is one session of the team that runs a plugin too old to
// follow a recorded move.
type olderSession struct {
	Session string `json:"session_name"`
	Member  string `json:"member"`
	Version string `json:"brigade_version"`
}

// moveEnd is one end of a move in the --json result.
type moveEnd struct {
	Adapter     string `json:"adapter"`
	URL         string `json:"url"`
	Certificate string `json:"certificate,omitzero"`
}

// moveToResult is the --json result of `team move --to`.
type moveToResult struct {
	TeamRef        string         `json:"team_ref"`
	TeamName       string         `json:"team_name"`
	From           moveEnd        `json:"from"`
	To             moveEnd        `json:"to"`
	TakenIn        bool           `json:"taken_in"`
	PrincipalRef   string         `json:"principal_ref"`
	MoveEndsAt     time.Time      `json:"move_ends_at"`
	TicketReplaced bool           `json:"ticket_replaced"`
	RecordedAt     time.Time      `json:"recorded_at"`
	TeamFile       string         `json:"team_file"`
	OlderSessions  []olderSession `json:"older_sessions"`
	Gateway        bool           `json:"gateway"`
}

// A serverMove is one `team move --to` as its checks worked it out.
type serverMove struct {
	// filePath is the checkout's team file and canon the canonical
	// directory that holds it, the key of the checkout's pin.
	filePath string
	canon    string
	// file is the team file as it is. dest is what it becomes: the server,
	// its pin, the same team. pointed says that the file names the server
	// already, which is how a run that was stopped after it wrote the file
	// finds the checkout.
	file    *teamfile.File
	dest    *teamfile.File
	pointed bool
	// configDir is the member's store. source is this machine's membership
	// on the backend the team leaves, under sourceKey; destKey is the
	// team's key at the server.
	configDir string
	source    *adapterkit.Profile
	sourceKey string
	destKey   string
	// from and to are the adapters of the two backends.
	from *target
	to   *target
	// secret is the team's join secret, for the server's adapter alone.
	// tokenFile names the file of the server's creation token, "" for a
	// server that asks for none; ticketDays is 0 for the server's default.
	secret     string
	tokenFile  string
	ticketDays int
	// label and teamName are the member's label and the team's name on the
	// backend the team leaves: what the server is told.
	label    string
	teamName string
	// older lists the team's sessions that will not follow, and replaces is
	// a record the old backend already holds of a move somewhere else.
	older    []olderSession
	replaces *protocol.TeamMoveResult
}

// moveTo runs the move.
func (inv Invocation) moveTo(flags moveFlags) error {
	m, err := inv.checkMove(flags)
	if err != nil {
		return err
	}
	if err := inv.consentToMove(m.question(), m.consequence(), "not moved; nothing was changed"); err != nil {
		return err
	}
	taken, err := inv.takeIn(m)
	if err != nil {
		return err
	}
	ticketFile, err := m.ticketFile()
	if err != nil {
		return err
	}
	// The ticket's file is gone when the command ends, however it ends.
	defer func() { _ = os.Remove(ticketFile) }()
	opened, err := inv.openMove(m, ticketFile)
	if err != nil {
		return m.stopped(err, "the team is taken in on "+m.toHost()+", and its move was not opened")
	}
	recorded, err := inv.recordMoveTo(m, ticketFile)
	if err != nil {
		return m.stopped(err, "the team is taken in on "+m.toHost()+" and its move is open, and "+m.fromHost()+" did not record it")
	}
	if err := inv.writeMove(m, taken); err != nil {
		return m.stopped(err, "the move is recorded on "+m.fromHost()+", and this checkout was not written")
	}
	_ = os.Remove(ticketFile)
	return inv.reportMove(m, taken, opened, recorded)
}

// checkMove is step a: everything that can refuse a move, asked before
// anything is changed. It reads argv, the team file, the store and the
// secret's file, then asks the backend the team leaves three things with
// the session this machine holds there: whether it can keep a record,
// whether this member may write it, and which sessions will not follow.
// The server is asked nothing here: whether it takes a team in is the
// first thing its adapter asks it in step b, before the request that does.
func (inv Invocation) checkMove(flags moveFlags) (*serverMove, error) {
	to, err := moveServerURL(flags.to)
	if err != nil {
		return nil, err
	}
	certificate := ""
	if flags.certificate != "" {
		pin, ok := adapterkit.NormalizeCertificatePin(flags.certificate)
		if !ok {
			return nil, usage("--certificate must be the SHA-256 fingerprint of the server's certificate: 64 hex digits, as the server's start line or openssl prints them")
		}
		if !strings.HasPrefix(strings.ToLower(to), "https://") {
			return nil, usage("--certificate goes with an https --to: over http there is no certificate to pin")
		}
		certificate = pin
	}
	if flags.ticketDaysSet && (flags.ticketDays < 1 || flags.ticketDays > protocol.MoveTicketMaxDays) {
		return nil, usage("--ticket-days must be between 1 and " + strconv.Itoa(protocol.MoveTicketMaxDays))
	}
	if flags.secretFile == "" {
		return nil, usage("team move --to needs --secret-file <path>: the file that holds the team's join secret, the one `team create` wrote; the secret is never typed and never an argument")
	}
	if strings.HasPrefix(flags.secretFile, protocol.JoinSecretPrefix) {
		// A join secret where a path should be rode argv; it is never echoed.
		return nil, usage("join secrets must never be passed on the command line; --secret-file names a file, and the secret goes in it")
	}
	secretFile := expandHome(flags.secretFile, inv.Environ)
	if !filepath.IsAbs(secretFile) {
		return nil, usage("--secret-file must be an absolute path outside this project (a relative path would be read from the repository)")
	}
	tokenFile := ""
	if flags.createTokenFile != "" {
		tokenFile = expandHome(flags.createTokenFile, inv.Environ)
		if !filepath.IsAbs(tokenFile) {
			return nil, usage("--create-token-file must be an absolute path")
		}
	}
	top, err := inv.requireToplevel("team move")
	if err != nil {
		return nil, err
	}
	if err := checkSecretFileOutside(secretFile, top); err != nil {
		return nil, err
	}
	if resolved, rerr := filepath.EvalSymlinks(secretFile); rerr == nil {
		if err := checkSecretFileOutside(resolved, top); err != nil {
			return nil, err
		}
	}
	if tokenFile != "" && fileOutside(tokenFile, top) != "" {
		return nil, usage("--create-token-file must be a plainly spelled path outside this project: a creation token must never live under the repository")
	}
	path, ok := teamfile.Discover(inv.mustGetwd())
	if !ok {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "this project has no " + teamfile.FileName + ": a team is moved from a checkout of the team, by its administrator",
			Details: map[string]string{"reason": "team_file_missing"},
		}
	}
	f, err := teamfile.Parse(path)
	if err != nil {
		return nil, err
	}
	canon, err := teamfile.Canonicalize(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	configDir, err := inv.storeDir()
	if err != nil {
		return nil, err
	}
	m := &serverMove{
		filePath: path, canon: canon, file: f, configDir: configDir,
		dest: &teamfile.File{
			Version: f.Version, Adapter: config.BundledRestName, URL: to, Certificate: certificate,
			TeamRef: f.TeamRef, TeamName: f.TeamName,
		},
		pointed:   f.Adapter == config.BundledRestName && f.URL == to && f.Certificate == certificate,
		destKey:   teamstore.Key(config.BundledRestName, to, f.TeamRef),
		tokenFile: tokenFile,
	}
	if flags.ticketDaysSet {
		m.ticketDays = flags.ticketDays
	}
	if m.source, m.sourceKey, err = moveFrom(configDir, f, flags.from, m.pointed); err != nil {
		return nil, err
	}
	m.label = m.source.HumanLabel
	if m.teamName = m.source.TeamName; m.teamName == "" {
		m.teamName = f.TeamName
	}
	if m.teamName == "" {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "this machine does not know the team's name: neither its membership nor this project's " + teamfile.FileName + " carries one; add team_name to the file and run this again",
			Details: map[string]string{"reason": reasonTeamNameUnknown},
		}
	}
	// The secret: a file that can be read, holding a join secret of this
	// team. Nothing has been sent anywhere yet.
	if m.secret, err = inv.joinSecret(secretFile); err != nil {
		return nil, err
	}
	if err := checkMoveSecret(m.secret, f); err != nil {
		return nil, err
	}
	if tokenFile != "" {
		if info, serr := os.Stat(tokenFile); serr != nil || !info.Mode().IsRegular() {
			return nil, &protocol.Error{
				Code:    protocol.CodeConfig,
				Message: "the file named by --create-token-file is not there or is not a file: it is the file the server's operator gave you, with the server's creation token in it",
				Details: map[string]string{"reason": "create_token_file"},
			}
		}
	}
	// The two adapters, each asked only what it advertises (4.7). A describe
	// is answered from local files.
	if m.from, err = inv.dialectTarget(m.source.Adapter, m.sourceKey); err != nil {
		return nil, err
	}
	if err := requireMoveRecord(m.from); err != nil {
		return nil, err
	}
	if m.to, err = inv.dialectTarget(config.BundledRestName, m.destKey); err != nil {
		return nil, err
	}
	if err := requireTakeIn(m.to); err != nil {
		return nil, err
	}
	if err := inv.checkOldBackend(m); err != nil {
		return nil, err
	}
	return m, nil
}

// moveServerURL reads --to: the server's base URL by the rule its adapter
// holds a backend to (the REST contract's section 1), so a URL the adapter
// would refuse is refused before anything else is looked at. The value is
// never echoed, and what is kept has no trailing slash, which is the one
// spelling the adapter stores and the team's key is made from.
func moveServerURL(raw string) (string, error) {
	if raw == "" {
		return "", usage("--to names the Brigade server the team moves to: its URL, https://<host>[:<port>]")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", usage("--to must be the server's URL, https://<host>[:<port>], with no credentials, query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !thisMachine(u.Hostname()) {
			return "", usage("--to must use https (http is allowed only for a server on this machine: 127.0.0.1, ::1 or localhost)")
		}
	default:
		return "", usage("--to must use https (http is allowed only for a server on this machine: 127.0.0.1, ::1 or localhost)")
	}
	return strings.TrimRight(raw, "/"), nil
}

// thisMachine reports whether host is one of the three the REST contract
// lets a server be reached at over plain http: 127.0.0.1, ::1 in any
// spelling of the address, and localhost.
func thisMachine(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback))
}

// moveFrom finds this machine's membership on the backend the team leaves.
// That is the backend the project's team file names, which the machine
// must have joined the team on, member for member (the key is never
// trusted alone). When the file names the server already, a run before
// this one wrote it and stopped: the backend the team leaves is then the
// one other membership of the team this machine holds, as it is for the
// record's own forms (moveSource). --from names the backend when more than
// one could be meant; beside a file that still names the old backend it
// can only name that backend.
func moveFrom(configDir string, f *teamfile.File, from string, pointed bool) (*adapterkit.Profile, string, error) {
	if pointed {
		source, key, err := moveSource(configDir, f, from)
		var perr *protocol.Error
		if errors.As(err, &perr) && perr.Details["reason"] == "no_move" {
			return nil, "", &protocol.Error{
				Code: protocol.CodeConfig,
				Message: "this project's " + teamfile.FileName + " names that server already, and this machine holds no membership of the team on another backend: " +
					"there is nothing left to move from here (`brigade team status` names the team this checkout is on)",
				Details: map[string]string{"reason": "no_move"},
			}
		}
		return source, key, err
	}
	if from != "" && from != f.URL && from != teammove.Host(f.URL) {
		return nil, "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "--from names another backend than this project's " + teamfile.FileName + ": a team moves from the backend its team file names",
			Details: map[string]string{"reason": "team_unknown"},
		}
	}
	key := teamstore.Key(f.Adapter, f.URL, f.TeamRef)
	source, err := teamstore.LoadBinding(configDir, key)
	if err != nil && !bindingMissing(err) {
		return nil, "", err
	}
	if err != nil || teamstore.VerifyBinding(source, f) != nil {
		return nil, "", &protocol.Error{
			Code: protocol.CodeConfig,
			Message: "this machine has not joined this project's team on the backend its " + teamfile.FileName + " names: " +
				"a team is moved by its administrator, from a checkout that is joined to it (`brigade team join`)",
			Details: map[string]string{"reason": reasonNotJoinedAtSource},
		}
	}
	return source, key, nil
}

// checkMoveSecret is checkJoinSecret for the secret a move presents to the
// server: it parses, and the team inside it is the file's. A secret of
// another team would take that team in, under this team's name.
func checkMoveSecret(secret string, f *teamfile.File) error {
	err := checkJoinSecret(secret, f)
	var perr *protocol.Error
	if errors.As(err, &perr) && perr.Details["reason"] == "secret_team_mismatch" {
		return &protocol.Error{
			Code: protocol.CodeInvalidInput,
			Message: "the file named by --secret-file holds the join secret of another team than this project's " + teamfile.FileName +
				" names: name the file `team create` wrote for this team. Nothing was sent, and nothing was changed",
			Details: perr.Details,
		}
	}
	return err
}

// requireTakeIn refuses, before anything is asked of it, an adapter for a
// Brigade server that does not carry the commands a team is taken in with:
// it does not advertise the capability, so it is asked none of them (4.7).
// The name `rest` is always the bundled adapter, which advertises it, so
// this holds the protocol's rule for a build whose adapter one day does
// not, and for nothing a member can configure.
func requireTakeIn(t *target) error {
	desc, err := call(func(ctx context.Context) (*protocol.DescribeResult, error) { return t.client.Describe(ctx) })
	if err != nil {
		return err
	}
	if !slices.Contains(desc.Capabilities, takeInCapability) {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the adapter this machine runs for a Brigade server does not offer `" + takeInCapability + "`, so it cannot ask a server to take a team in: the server's operator takes the team in and opens its move, and you record it with `brigade team move --ticket-file <path>`",
			Details: map[string]string{"reason": reasonNoTeamTakeIn},
		}
	}
	return nil
}

// checkOldBackend asks the backend the team leaves what a move needs of
// it, with the session this machine holds there.
//
// Whether it can keep a record, and whether this member may write it,
// are asked together and by a command that changes nothing: the record is
// read, and where there is none, it is cleared. Clearing no record is a
// no-op by the protocol's own rule (4.4.10), and it is answered only to
// the team's creator and only by a backend that keeps records: a Supabase
// project without the team-move migration reads "no move" to every member
// and refuses the clear by naming the migration. Where a record is there
// already, the backend keeps records, and whether this member may replace
// it is answered by step d.
//
// Then the roster: the sessions that are online on a plugin older than the
// first that follows a recorded move. Their members join the server
// themselves, or update first; a member with no session online is not
// seen here.
func (inv Invocation) checkOldBackend(m *serverMove) error {
	record, err := inv.callMove(m.from, "moved", nil, nil)
	if err != nil {
		return err
	}
	switch {
	case !record.Moved:
		if _, err := inv.callMove(m.from, "move", []string{"--clear"}, nil); err != nil {
			return m.cannotRecord(err)
		}
	case record.Adapter != m.dest.Adapter || record.URL != m.dest.URL || record.Certificate != m.dest.Certificate:
		m.replaces = record
	}
	list, err := call(func(ctx context.Context) (*adapterclient.ListResult, error) {
		return m.from.client.ListSessions(ctx, false)
	})
	if err != nil {
		return err
	}
	for i := range list.Sessions {
		s := &list.Sessions[i]
		if s.BrigadeVersion == nil || !teammove.OlderThanFollow(*s.BrigadeVersion) {
			continue
		}
		m.older = append(m.older, olderSession{
			Session: tableName(s.SessionName),
			Member:  rosterMember(s.HumanLabel, s.PrincipalRef),
			Version: attrLine(*s.BrigadeVersion),
		})
	}
	return nil
}

// cannotRecord turns the two refusals of the probe in checkOldBackend into
// what they mean for a move, each with what to do. Any other error is the
// backend's own and is returned as it is.
func (m *serverMove) cannotRecord(err error) error {
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		return err
	}
	switch {
	case perr.Details["reason"] == reasonMigrationDrift:
		project := "<the project's ref>"
		if ref := supabaseHost.FindStringSubmatch(teammove.Host(m.source.URL)); ref != nil {
			project = ref[1]
		}
		return &protocol.Error{
			Code: protocol.CodeConfig,
			Message: "the project this team is on (" + m.fromHost() + ") cannot keep the record of a move: it lacks the migration " + teamMoveMigration +
				". The project's owner applies it with `make backend-install project=" + project + "` in a checkout of the Brigade repository; then run this again. Nothing was changed",
			Details: map[string]string{"reason": reasonMigrationDrift},
		}
	case perr.Code == protocol.CodeUnauthorized:
		return &protocol.Error{
			Code: protocol.CodeUnauthorized,
			Message: "only the member who created this team on " + m.fromHost() + " can move it: where a team went is its administrator's to record there. " +
				"Run this on the administrator's machine. Nothing was changed",
			Details: map[string]string{"reason": reasonNotTheCreator},
		}
	}
	return err
}

// fromHost and toHost are the two backends as a line names them.
func (m *serverMove) fromHost() string { return attrLine(hostOf(m.source.URL)) }
func (m *serverMove) toHost() string   { return attrLine(hostOf(m.dest.URL)) }

// team is the team as a line names it.
func (m *serverMove) team() string {
	return "team \"" + nameLine(m.teamName) + "\" (" + sanitizeID(m.file.TeamRef) + ")"
}

// question is what the administrator is shown before anything is changed.
func (m *serverMove) question() string {
	return "move " + m.team() + " from " + m.fromHost() + " to " + moveDestination(m.dest.Adapter, m.dest.URL, m.dest.Certificate)
}

// consequence says what answering yes does, and what the checks found
// that the administrator should know before answering.
func (m *serverMove) consequence() string {
	out := "The server takes the team in with you as its owner and opens its move, " + m.fromHost() +
		" records where the team went, and this project's " + teamfile.FileName +
		" is rewritten to name the server; every member's next session then follows with no command of theirs."
	if m.replaces != nil {
		out += " This replaces the record " + m.fromHost() + " holds of a move to " +
			moveDestination(m.replaces.Adapter, m.replaces.URL, m.replaces.Certificate) + "."
	}
	if older := olderLine(m.older); older != "" {
		out += " " + older + "."
	}
	return out
}

// olderLine names the sessions that will not follow, "" when there is
// none.
func olderLine(older []olderSession) string {
	if len(older) == 0 {
		return ""
	}
	names := make([]string, 0, olderShown)
	for i, s := range older {
		if i == olderShown {
			break
		}
		names = append(names, s.Session+" ("+s.Member+", "+s.Version+")")
	}
	list := strings.Join(names, ", ")
	if rest := len(older) - olderShown; rest > 0 {
		list += " and " + strconv.Itoa(rest) + " more"
	}
	if len(older) == 1 {
		return "1 session that is online runs a plugin older than " + teammove.FollowsFromVersion + " and will not follow until its member updates it: " + list
	}
	return strconv.Itoa(len(older)) + " sessions that are online run a plugin older than " + teammove.FollowsFromVersion +
		" and will not follow until their members update it: " + list
}

// takeIn is step b: the server takes the team in, with this member as its
// owner, under the label the member has on the backend the team leaves.
// That label is the membership's, never one read from the Claude profile
// of the terminal the command happens to run in (work item 326): a member
// of the team is the same member on the server, as one who follows is.
//
// The profile at the server is the team's key there from the first
// request, so a run that is stopped after the server answered finds the
// credential the server issued and is the owner again. A profile that is
// there under another certificate is moved to the one the administrator
// named, as a join does (alignRestBackend).
//
// The answer must say that the caller owns the team. A team that the
// server holds and someone else owns is joined and not owned, and its move
// is not this member's to open: the command stops here, before a move is
// opened and before anything is written, and says that the server's
// operator can name the team's owner.
func (inv Invocation) takeIn(m *serverMove) (*protocol.TeamTakeInResult, error) {
	fresh := false
	if _, err := teamstore.LoadBinding(m.configDir, m.destKey); err != nil {
		if !bindingMissing(err) {
			return nil, err
		}
		fresh = true
	}
	if err := inv.alignRestBackend(m.to, m.dest); err != nil {
		return nil, err
	}
	backend, err := json.Marshal(teammove.JoinBackend(m.dest))
	if err != nil {
		return nil, err
	}
	req := &protocol.TeamTakeInRequest{JoinSecret: m.secret, TeamName: m.teamName, HumanLabel: m.label, Backend: backend}
	var flags []string
	if m.tokenFile != "" {
		flags = []string{"--create-token-file", m.tokenFile}
	}
	taken, err := call(func(ctx context.Context) (*protocol.TeamTakeInResult, error) {
		env, err := m.to.client.Call(ctx, "team", "take-in", flags, req)
		if err != nil {
			return nil, err
		}
		var res protocol.TeamTakeInResult
		if uerr := json.Unmarshal(env.Result, &res); uerr != nil || res.Validate() != nil {
			return nil, protocolMismatch("team take-in answered a shape this build does not read")
		}
		return &res, nil
	})
	if err != nil {
		if fresh {
			inv.forgetUnusedProfile(m)
		}
		return nil, m.notTakenIn(err)
	}
	if taken.TeamRef != m.file.TeamRef {
		return nil, protocolMismatch("team take-in answered a team other than the one the secret names")
	}
	if !taken.Owner {
		return nil, &protocol.Error{
			Code: protocol.CodeConflict,
			Message: m.team() + " is held on " + m.toHost() + " and owned by someone else, so its move is not yours to open there: the server's operator can name its owner. " +
				"This machine joined the team there as a member just now, and `brigade-server --set-owner " + sanitizeID(m.file.TeamRef) + " --principal " + sanitizeID(taken.PrincipalRef) +
				"` names it the owner (docs/project-owner/server/name-the-owner.md in the Brigade repository); then run this again. Nothing was recorded, and " + teamfile.FileName + " is unchanged",
			Details: map[string]string{"reason": reasonNotOwnerThere},
		}
	}
	return taken, nil
}

// forgetUnusedProfile removes the profile the server's adapter wrote for a
// take-in that was refused, when this command is what made it and the
// server issued it nothing: the adapter writes the backend down before it
// asks the server anything, and a refused move should leave the store as
// it found it. `profile status` is answered from local files, and `profile
// reset` on a profile that holds no credential asks the server nothing and
// removes the directory. A profile that holds a credential is kept: the
// server may have taken the team in for it before the answer was lost, and
// that credential is then the owner's. Best effort: a profile that stays
// is one the next run uses.
func (inv Invocation) forgetUnusedProfile(m *serverMove) {
	state, err := call(func(ctx context.Context) (string, error) {
		env, err := m.to.client.Call(ctx, "profile", "status", nil, nil)
		if err != nil {
			return "", err
		}
		var status struct {
			State string `json:"state"`
		}
		if uerr := json.Unmarshal(env.Result, &status); uerr != nil {
			return "", uerr
		}
		return status.State, nil
	})
	if err != nil || state != protocol.ProfileStateUnauthenticated {
		return
	}
	if _, err := call(func(ctx context.Context) (*protocol.Envelope, error) {
		return m.to.client.Call(ctx, "profile", "reset", nil, nil)
	}); err != nil {
		inv.logger().Debug("the profile of a refused take-in was not removed")
	}
}

// notTakenIn turns a take-in's refusal into what it means for a move, with
// what to do. Each of these is answered before the server took anything
// in.
func (m *serverMove) notTakenIn(err error) error {
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		return err
	}
	const unchanged = "Nothing was recorded, and " + teamfile.FileName + " is unchanged"
	switch {
	case perr.Details["reason"] == reasonServerLacksTakeIn:
		return &protocol.Error{
			Code: protocol.CodeConfig,
			Message: "the server at " + m.toHost() + " takes no team in by request: it is older than 0.7.0. Its operator takes the team in and opens its move with the server's own commands, " +
				"`brigade-server --import-team <name> --join-secret-file <file>` and `brigade-server --open-move " + sanitizeID(m.file.TeamRef) + " --ticket-file <file>`, and gives you the ticket's file; " +
				"you then point " + teamfile.FileName + " at the server, join it there and record the move with `brigade team move --ticket-file <path>` " +
				"(docs/team-admin/members/move-the-team.md in the Brigade repository). " + unchanged,
			Details: map[string]string{"reason": reasonServerLacksTakeIn},
		}
	case perr.Details["reason"] == reasonOtherJoinSecret:
		return &protocol.Error{
			Code:    protocol.CodeConflict,
			Message: m.toHost() + " already holds a team of this reference under another join secret, so this team cannot be taken in there: tell the server's operator. " + unchanged,
			Details: map[string]string{"reason": reasonOtherJoinSecret},
		}
	case perr.Code == protocol.CodeUnauthorized:
		what := "it asks for a creation token: ask its operator for the token's file and pass --create-token-file <path>"
		if m.tokenFile != "" {
			what = "it refused the creation token in the file --create-token-file names: ask its operator for the current one"
		}
		return &protocol.Error{
			Code:    protocol.CodeUnauthorized,
			Message: m.toHost() + " does not let this caller put a team on it; " + what + ". " + unchanged,
			Details: map[string]string{"reason": reasonCreateToken},
		}
	}
	out := *perr
	out.Message = "the server at " + m.toHost() + " did not take the team in: " + perr.Message + ". " + unchanged
	return &out
}

// ticketFile is where the move's ticket is kept between the server's
// answer and the old backend's record: a file named after the team's key
// at the server, in a directory of Brigade's own state directory that is
// the member's alone. It is not a file the member is meant to see, let
// alone keep, which is why it is not beside the join secret's.
func (m *serverMove) ticketFile() (string, error) {
	dir := filepath.Join(m.to.stateDir, moveTicketDir)
	if err := adapterkit.MkdirPrivate(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, m.destKey+".ticket"), nil
}

// openMove is step c: the server opens the team's move for its owner and
// its adapter writes the ticket to ticketFile, mode 0600. The ticket is
// read back here only to see that it is one of this team, so a server's
// answer that is not fails before the old backend is asked to record it.
func (inv Invocation) openMove(m *serverMove, ticketFile string) (*protocol.TeamOpenMoveResult, error) {
	flags := []string{"--ticket-file", ticketFile}
	if m.ticketDays > 0 {
		flags = append(flags, "--ticket-days", strconv.Itoa(m.ticketDays))
	}
	opened, err := call(func(ctx context.Context) (*protocol.TeamOpenMoveResult, error) {
		env, err := m.to.client.Call(ctx, "team", "open-move", flags, nil)
		if err != nil {
			return nil, err
		}
		var res protocol.TeamOpenMoveResult
		if uerr := json.Unmarshal(env.Result, &res); uerr != nil || res.Validate() != nil {
			return nil, protocolMismatch("team open-move answered a shape this build does not read")
		}
		return &res, nil
	})
	if err != nil {
		return nil, err
	}
	if opened.TeamRef != m.file.TeamRef || opened.TicketFile != ticketFile {
		return nil, protocolMismatch("team open-move answered a move other than the one it was asked for")
	}
	ticket, err := adapterkit.ReadTicketFile(ticketFile)
	if err != nil {
		return nil, err
	}
	if err := adapterkit.CheckMoveTicket(ticket, m.file.TeamRef); err != nil {
		return nil, err
	}
	return opened, nil
}

// recordMoveTo is step d: the backend the team leaves records where it
// went, with the ticket from ticketFile. The destination is the one the
// administrator typed, which step e writes into the team file.
func (inv Invocation) recordMoveTo(m *serverMove, ticketFile string) (*protocol.TeamMoveResult, error) {
	req := &protocol.TeamMoveRequest{Adapter: m.dest.Adapter, URL: m.dest.URL, Certificate: m.dest.Certificate}
	recorded, err := inv.callMove(m.from, "move", []string{"--ticket-file", ticketFile}, req)
	if err != nil {
		return nil, err
	}
	if !recorded.Moved || recorded.Adapter != m.dest.Adapter || recorded.URL != m.dest.URL || recorded.Certificate != m.dest.Certificate {
		return nil, protocolMismatch("team move answered a record other than the one it was given")
	}
	return recorded, nil
}

// writeMove is step e, and nothing before it wrote the checkout: the
// binding of the membership at the server, then the team file, then the
// checkout's pin. The file goes before the pin on purpose. A run stopped
// between the two leaves a file that names the server and a pin that names
// the old backend, which is exactly what the administrator's next session
// follows by the record just made; the other order would leave a pin that
// nothing matches until the command is run again.
//
// The pin carries no MovedFrom: that member marks a pin a session wrote by
// following, and this one a command wrote on its member's word.
func (inv Invocation) writeMove(m *serverMove, taken *protocol.TeamTakeInResult) error {
	if err := write.EnsureBinding(m.configDir, m.destKey, m.dest.Adapter, m.dest.URL, "", m.dest.Certificate,
		taken.TeamRef, taken.TeamName, taken.PrincipalRef); err != nil {
		return err
	}
	teammove.CarryLabelOrigin(m.configDir, m.sourceKey, m.destKey, m.label, inv.Deps.now().UTC(), inv.logger())
	if !m.pointed {
		data, err := teamfile.ToServer(m.filePath, m.dest.URL, m.dest.Certificate)
		if err != nil {
			return err
		}
		if err := adapterkit.WriteAtomicMode(m.filePath, data, 0o644); err != nil {
			return err
		}
	}
	return write.Pin(m.configDir, m.canon, teamstore.Pin{
		Adapter: m.dest.Adapter, URL: m.dest.URL, Certificate: m.dest.Certificate,
		TeamRef: m.dest.TeamRef, ConsentedAt: inv.Deps.now().UTC(),
	})
}

// stopped says, in front of a step's own failure, how far the move got
// and that the same command finishes it.
func (m *serverMove) stopped(err error, where string) error {
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		perr = &protocol.Error{Code: protocol.CodeInternal, Message: "the step failed on this machine"}
	}
	out := *perr
	out.Message = where + ": " + perr.Message + ". Run the same command again to finish the move: every step of it is safe to repeat"
	return &out
}

// reportMove is step f: what was done, and what is left and whose.
func (inv Invocation) reportMove(m *serverMove, taken *protocol.TeamTakeInResult, opened *protocol.TeamOpenMoveResult, recorded *protocol.TeamMoveResult) error {
	gateway := m.file.Gateway != nil || m.file.GatewayUnusable != ""
	if inv.JSON {
		older := m.older
		if older == nil {
			older = []olderSession{}
		}
		return writeJSON(inv.Out, moveToResult{
			TeamRef: sanitizeID(m.file.TeamRef), TeamName: protocol.SanitizeName(taken.TeamName),
			From:           moveEnd{Adapter: attrLine(m.source.Adapter), URL: attrLine(m.source.URL)},
			To:             moveEnd{Adapter: m.dest.Adapter, URL: m.dest.URL, Certificate: m.dest.Certificate},
			TakenIn:        taken.TakenIn,
			PrincipalRef:   sanitizeID(taken.PrincipalRef),
			MoveEndsAt:     opened.EndsAt,
			TicketReplaced: opened.Replaced,
			RecordedAt:     recorded.RecordedAt,
			TeamFile:       m.filePath,
			OlderSessions:  older,
			Gateway:        gateway,
		})
	}
	ends := opened.EndsAt.UTC().Format("2006-01-02")
	took := "took " + m.team() + " in on " + m.toHost() + ": you own it there"
	if !taken.TakenIn {
		took = m.team() + " was on " + m.toHost() + " already, and you own it there"
	}
	open := "opened its move: the ticket admits the team's members until " + ends
	if opened.Replaced {
		open = "opened its move again, which replaced the ticket it had: the new one admits the team's members until " + ends
	}
	file := "wrote " + teamfile.FileName + ": it names " + m.toHost() + ", and this checkout is joined to the team there"
	if m.pointed {
		file = teamfile.FileName + " named " + m.toHost() + " already, and this checkout is joined to the team there"
	}
	lines := []string{
		took,
		labelSent(m.label, taken.PrincipalRef),
		open,
		"recorded on " + m.fromHost() + ": " + m.team() + " moved to " + moveDestination(m.dest.Adapter, m.dest.URL, m.dest.Certificate),
		file,
		"left to do:",
		"  commit and push " + teamfile.FileName + " in every repository of the team: a member's next session in a checkout that has the new file follows, with no command of theirs, until " + ends,
	}
	if older := olderLine(m.older); older != "" {
		lines = append(lines, "  "+older)
	}
	if gateway {
		lines = append(lines, "  this team's "+teamfile.FileName+" has a gateway member: the mail and Slack gateways are the server operator's to configure on "+m.toHost()+
			" (docs/project-owner/server/gateways.md in the Brigade repository)")
	}
	return writeLines(inv.Out, append(lines,
		"to undo: `brigade team move --clear` stops "+m.fromHost()+" from saying where the team went, so no session follows from then on. "+
			"The move on "+m.toHost()+" stays open until "+ends+", or until its operator closes it or you rotate the team's join secret there",
	)...)
}
