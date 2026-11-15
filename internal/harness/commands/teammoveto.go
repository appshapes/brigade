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
// 328, ADR 0006 and ADR 0007) is a team's whole move to a Brigade server
// in one command, run by the team's administrator in a checkout of the
// team. Until it, the move was six steps by two people: the server's
// operator took the team in and opened its move with the server stopped
// and handed a ticket back as a file, and the administrator edited the
// team file by hand, joined the server and recorded the move.
//
// The backend the team leaves is whichever keeps the record of where a team
// went: a Supabase project, the filesystem store, and from 0.57.0 another
// Brigade server, of 0.8.0 or later (work item 341, ADR 0010).
//
// The order is the design's, and each step is one a second run repeats
// without harm:
//
//	a. check, changing nothing but the date of a record that is there (checkMove)
//	b. take the team in on the server, as its owner (takeIn)
//	b'. clear what the server itself records of the team having left it (clearReturn)
//	c. open the move; the ticket goes to a private file (openMove)
//	d. record the move on the backend the team leaves (recordMoveTo)
//	e. only then rewrite the team file, the binding and the pin (writeMove)
//	f. say what was done and what is left (reportMove)
//
// Step b' is for a team that comes back (A to B, then B to A): the server
// it returns to still says that the team went away. That record is cleared
// before the backend the team leaves records anything, so that the two
// never name each other: a session that follows one record is not sent
// back by the other. A clear that fails stops the move there, with nothing
// recorded on the backend the team leaves.
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
	// reasonServerLacksMove is the `rest` adapter's own word for a server
	// that keeps no record of where a team went: one older than 0.8.0.
	reasonServerLacksMove = "server_lacks_team_move"
	// reasonReturnNotCleared: the server the team moves to records that the
	// team left it, and that record could not be cleared.
	reasonReturnNotCleared = "destination_record_not_cleared"
	// reasonSameServer: the server named by --to holds the team with the
	// very membership this machine has on the backend the team is on.
	reasonSameServer      = "same_server"
	reasonOtherJoinSecret = "other_join_secret" //nolint:gosec // G101: a details.reason token, not a credential
	reasonNotTheCreator   = "not_the_teams_creator"
	reasonNotOwnerThere   = "not_owner_at_destination"
	reasonCreateToken     = "create_token" //nolint:gosec // G101: a details.reason token, not a credential
	reasonTeamNameUnknown = "team_name_unknown"
	// reasonTeamFileUnwritable: the team file as it is cannot be rewritten
	// to name the server.
	reasonTeamFileUnwritable = "team_file_unwritable"
	// reasonTicketFileUnwritable is the adapters' own word (4.4.10) for a
	// ticket file that cannot be written, and reasonRecordChanged this
	// command's for a record that was cleared while it was being checked.
	reasonTicketFileUnwritable = "ticket_file_unwritable"
	reasonRecordChanged        = "record_changed"
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
	TeamRef        string    `json:"team_ref"`
	TeamName       string    `json:"team_name"`
	From           moveEnd   `json:"from"`
	To             moveEnd   `json:"to"`
	TakenIn        bool      `json:"taken_in"`
	PrincipalRef   string    `json:"principal_ref"`
	MoveEndsAt     time.Time `json:"move_ends_at"`
	TicketReplaced bool      `json:"ticket_replaced"`
	RecordedAt     time.Time `json:"recorded_at"`
	// ReturnCleared says that the server the team moved to held a record of
	// the team having left it, which this run cleared.
	ReturnCleared bool           `json:"destination_record_cleared"`
	TeamFile      string         `json:"team_file"`
	OlderSessions []olderSession `json:"older_sessions"`
	// RosterTruncated says that older_sessions was read from a roster the
	// backend cut short, and may lack sessions.
	RosterTruncated bool `json:"roster_truncated"`
	Gateway         bool `json:"gateway"`
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
	// recorded says that it holds a record of this very move, which a run
	// before this one made. rosterCut says that the backend answered a
	// roster it had cut short, so older may lack sessions.
	older     []olderSession
	rosterCut bool
	replaces  *protocol.TeamMoveResult
	recorded  bool
	// memberThere says that this machine holds a membership of the team on
	// the server already: the team was there before, or a run before this
	// one took it in. returned is the record the server itself held of the
	// team having left it, which step b' cleared; nil when it held none.
	memberThere bool
	returned    *protocol.TeamMoveResult
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
	if err := inv.clearReturn(m); err != nil {
		return err
	}
	ticketFile, err := m.ticketFile()
	if err != nil {
		return m.stopped(err, "the team is taken in on "+m.toHost()+", and its move was not opened")
	}
	// The ticket's file is gone when the command returns, whichever way it
	// returns. A command that a signal ends here returns nowhere and
	// leaves the file, private as it is: the next run for the same team
	// writes over it and removes it (moveTicketDir).
	defer func() { _ = os.Remove(ticketFile) }()
	opened, err := inv.openMove(m, ticketFile)
	if err != nil {
		return m.stopped(err, "the team is taken in on "+m.toHost()+", and its move was not opened")
	}
	recorded, err := inv.recordMoveTo(m, ticketFile)
	if err != nil {
		where := "the team is taken in on " + m.toHost() + " and its move is open, and " + m.fromHost() + " did not record it"
		if m.recorded && opened.Replaced {
			// The record a run before this one made is still there, with
			// the ticket this run's open has just replaced on the server.
			where += "; the record it holds from a run before this one hands out a ticket that this run replaced, so no member follows until the record is made again"
		}
		return m.stopped(err, where)
	}
	if err := inv.writeMove(m, taken); err != nil {
		return m.stopped(err, "the move is recorded on "+m.fromHost()+", and this checkout was not written")
	}
	return inv.reportMove(m, taken, opened, recorded)
}

// checkMove is step a: everything that can refuse a move, asked before
// anything is changed. It reads argv, the team file, the store and the
// secret's file, then asks the backend the team leaves three things with
// the session this machine holds there: whether it can keep a record,
// whether this member may write it, and which sessions will not follow.
// One thing the second question changes: a record that is there already is
// recorded again as it is, which moves its date and nothing else
// (recordAgain). The server is asked nothing here, not what it offers
// either: both adapters' describes are answered from local files, the old
// backend's questions come last of the checks, and whether the server
// takes a team in is the first thing its adapter asks it in step b, before
// the request that does.
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
	if err := checkMoveDestination(to, certificate); err != nil {
		return nil, err
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
	if tokenFile != "" {
		if fileOutside(tokenFile, top) != "" {
			return nil, tokenInside()
		}
		// As for the secret's file: a path outside the checkout that is a
		// link to a file inside it is inside it.
		if resolved, rerr := filepath.EvalSymlinks(tokenFile); rerr == nil && fileOutside(resolved, top) != "" {
			return nil, tokenInside()
		}
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
	// The server as this machine already knows it: an address that is
	// trivially the one the team file or a membership of the team carries
	// (the host in another case, the scheme's own port, a slash at the end)
	// is that server, under the spelling it is known by. Without this the
	// same server would be taken for another, and joined a second time.
	to = knownSpelling(configDir, f, to)
	if f.Adapter == config.BundledRestName && f.URL == to && f.Certificate != certificate {
		return nil, onThatServerAlready(to, f.Certificate, certificate)
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
	if err := m.rewritable(); err != nil {
		return nil, err
	}
	if m.source, m.sourceKey, err = moveFrom(configDir, f, flags.from, m.pointed); err != nil {
		var perr *protocol.Error
		if !m.pointed || flags.from != "" || !errors.As(err, &perr) || perr.Details["reason"] != "team_ambiguous" {
			return nil, err
		}
		// The file names the server already and this machine has joined the
		// team on more than one other backend: a team that moved before. The
		// one it is leaving is found, not asked for, where it can be.
		if m.source, m.sourceKey, err = inv.sourceOfRerun(m); err != nil {
			return nil, err
		}
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
	if there, berr := teamstore.LoadBinding(configDir, m.destKey); berr == nil && there.TeamRef == f.TeamRef {
		m.memberThere = true
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

// checkMoveDestination holds the destination to the limits of the record it
// goes into (4.4.10). The old backend would refuse to record a destination
// past them, and it is asked only after the server took the team in and
// opened its move: so the limits are held here, before anyone is asked.
// The adapter's name and a pin in its one spelling are always within
// theirs; the address is what can be too long.
func checkMoveDestination(to, certificate string) error {
	record := &protocol.TeamMoveRequest{Adapter: config.BundledRestName, URL: to, Certificate: certificate}
	if record.Validate() != nil {
		return usage("--to is longer than the record of a move can hold: " + strconv.Itoa(protocol.MaxMoveURLChars) + " characters at most")
	}
	return nil
}

// tokenInside refuses a creation token's file that is, or is a link to, a
// file under the checkout.
func tokenInside() *protocol.Error {
	return usage("--create-token-file must be a plainly spelled path outside this project: a creation token must never live under the repository")
}

// rewritable tries step e's rewrite of the team file on the file as it is,
// and writes nothing. Step e comes after the server took the team in and
// the old backend recorded the move, so a file that cannot be made to name
// the server is found here instead: one that would grow past what a team
// file can be, and a destination a team file may not carry. A file that
// names the server already is not rewritten, and not tried.
func (m *serverMove) rewritable() error {
	if m.pointed {
		return nil
	}
	_, err := teamfile.ToServer(m.filePath, m.dest.URL, m.dest.Certificate)
	if err == nil {
		return nil
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		out := *perr
		out.Message = "this project's " + teamfile.FileName + " cannot be rewritten to name the server: " + perr.Message + ". Nothing was changed"
		return &out
	}
	return &protocol.Error{
		Code: protocol.CodeConfig,
		Message: "this project's " + teamfile.FileName + " cannot be rewritten to name that server: the file that would result is not one a session reads. " +
			"Nothing was changed",
		Details: map[string]string{"reason": reasonTeamFileUnwritable},
	}
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
	if from != "" && !teammove.SameAddress(from, f.URL) && from != teammove.Host(f.URL) {
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

// knownSpelling answers the address this machine already knows a server
// by, when to is trivially the same address (teammove.SameAddress): the
// team file's own, else that of a membership of the team on a Brigade
// server. Any other address is answered as it was typed.
func knownSpelling(configDir string, f *teamfile.File, to string) string {
	if f.Adapter == config.BundledRestName && teammove.SameAddress(to, f.URL) {
		return f.URL
	}
	bindings, _, err := listBindings(configDir)
	if err != nil {
		return to
	}
	for _, b := range bindings {
		if b.TeamRef == f.TeamRef && b.Adapter == config.BundledRestName && teammove.SameAddress(to, b.URL) {
			return b.URL
		}
	}
	return to
}

// onThatServerAlready refuses a move to the server the team file names,
// under another certificate than the file has or under none (work item
// 341). Until this the file did not count as naming the server already,
// because the pins differed; the backend the team "leaves" was then the
// very membership on that server, the profile was repointed to the pin
// that was typed, a move was opened, and the server recorded that the team
// had moved to itself. A team does not move to the server it is on,
// whatever the certificate, and it is refused before anything is aligned
// or asked, as the record's own forms refuse (noMoveToRecord). What the
// person may have meant has two other names: a server that has a new
// certificate is a change of the team file and a join, and a move to this
// server that is run again names the pin the file has.
func onThatServerAlready(to, has, typed string) *protocol.Error {
	host := attrLine(hostOf(to))
	differs := "with another certificate than the one --certificate names"
	switch {
	case typed == "":
		differs = "with a certificate's pin, and --certificate names none"
	case has == "":
		differs = "with no certificate's pin, and --certificate names one"
	}
	again := "a move to " + host + " that is run again names what the file has: no --certificate"
	if has != "" {
		again = "a move to " + host + " that is run again names the pin the file has: `--certificate " + pinLine(has) + "`"
	}
	return &protocol.Error{
		Code: protocol.CodeConfig,
		Message: host + " is the server this team is on: this project's " + teamfile.FileName + " names it, " + differs +
			". A team is moved to another server, never onto the one it is on. A server's new certificate is a change of `certificate` in " +
			teamfile.FileName + " and a `brigade team join` in each checkout, not a move; and " + again + ". Nothing was changed",
		Details: map[string]string{"reason": "no_move"},
	}
}

// sourceOfRerun finds the backend the team is leaving for a command that
// is run again on a machine that has joined the team on more than one
// other backend, which is every machine whose team moved before (work item
// 341). Until this the command asked for --from, though it was the same
// command a page, a skill and every stop of the command say to run again.
//
// What says which backend the team is leaving is this checkout: the
// backend its pin still names, which a run that was stopped after it wrote
// the team file and before it wrote the pin leaves, or the backend its pin
// says it followed from (MovedFrom, which a session's follow writes). That
// candidate is taken when its own record names the server, or when no
// candidate's record does (a record that was cleared). A record alone
// never chooses: a backend the team left long ago for this very server
// still says so, and it would be taken for the one that counts, the
// command recording there while the sessions on the backend the team is
// really on are told nothing. Where the checkout says nothing, as in the
// checkout the command itself pinned to the server, or says one thing
// while a record says another, nothing is chosen: the command changes
// nothing, names the candidates, and prints itself for each with its
// --from.
//
// A candidate whose record cannot be read is not one that holds none: it
// may be the very backend the team is leaving. So nothing is chosen while
// one does not answer either: the command names it, and says to run it
// again when it answers, or with --from.
func (inv Invocation) sourceOfRerun(m *serverMove) (*adapterkit.Profile, string, error) {
	bindings, keys, err := listBindings(m.configDir)
	if err != nil {
		return nil, "", err
	}
	var candidates, recorded, unread []int
	for i, b := range bindings {
		if b.TeamRef != m.file.TeamRef || keys[i] == m.destKey {
			continue
		}
		candidates = append(candidates, i)
		t, terr := inv.dialectTarget(b.Adapter, keys[i])
		if terr != nil {
			unread = append(unread, i)
			continue
		}
		record, _, rerr := inv.recordOn(t)
		switch {
		case rerr != nil:
			unread = append(unread, i)
		case record != nil && record.Moved && record.Adapter == m.dest.Adapter && teammove.SameAddress(record.URL, m.dest.URL):
			recorded = append(recorded, i)
		}
	}
	hostOfCandidate := func(i int) string {
		host := teammove.Host(bindings[i].URL)
		for _, other := range candidates {
			if other != i && teammove.Host(bindings[other].URL) == host {
				// Two of them on one host: the whole address tells them apart.
				return attrLine(bindings[i].URL)
			}
		}
		return attrLine(host)
	}
	hostsOf := func(named []int) string {
		hosts := make([]string, 0, len(named))
		for _, i := range named {
			hosts = append(hosts, hostOfCandidate(i))
		}
		slices.Sort(hosts)
		return strings.Join(hosts, ", ")
	}
	reruns := func() string {
		lines := make([]string, 0, len(candidates))
		for _, i := range candidates {
			lines = append(lines, "`brigade team move --to "+toServer(m.dest.URL, m.dest.Certificate)+" --secret-file <path> --from "+hostOfCandidate(i)+"`")
		}
		slices.Sort(lines)
		return strings.Join(lines, ", or ")
	}
	if len(unread) > 0 {
		it, did := "it answers", " did not answer what it records of this team"
		if len(unread) > 1 {
			it = "they answer"
		}
		return nil, "", &protocol.Error{
			Code: protocol.CodeUnavailable,
			Message: "this machine has joined this team on more than one other backend (" + hostsOf(candidates) + "), and " + hostsOf(unread) + did +
				", so which one the team is leaving is not known: a backend that cannot be read may be the one. Run the same command again when " + it +
				", or with the backend it leaves named: " + reruns() + ". Nothing was changed",
			Details: map[string]string{"reason": "team_ambiguous"},
		}
	}
	// The candidate this checkout names: the backend its pin still names, or
	// the one its pin says it followed from.
	own := -1
	if pin, pinned, perr := teamstore.LookupPin(m.configDir, m.canon); perr == nil && pinned && pin.TeamRef == m.file.TeamRef {
		pinKey := teamstore.Key(pin.Adapter, pin.URL, pin.TeamRef)
		for _, i := range candidates {
			if keys[i] == pinKey || (pin.MovedFrom != "" && pinKey == m.destKey && bindings[i].URL == pin.MovedFrom) {
				own = i
				break
			}
		}
	}
	if own >= 0 && (slices.Contains(recorded, own) || len(recorded) == 0) {
		return bindings[own], keys[own], nil
	}
	why := "none of them records a move to " + m.toHost() + ", and nothing in this checkout says which one it was joined to before"
	switch {
	case own >= 0:
		why = "this checkout was joined to " + hostOfCandidate(own) + ", and it is " + hostsOf(recorded) + " that records a move to " + m.toHost()
	case len(recorded) > 0:
		why = hostsOf(recorded) + " records a move to " + m.toHost() + ", which says that the team once left it for this server and not that it is leaving it now, and nothing in this checkout says which one it was joined to before"
	}
	return nil, "", &protocol.Error{
		Code: protocol.CodeConfig,
		Message: "this machine has joined this team on more than one other backend (" + hostsOf(candidates) + "): " + why +
			", so which one the team is leaving is not known. Run the same command with the backend it leaves named: " + reruns() + ". Nothing was changed",
		Details: map[string]string{"reason": "team_ambiguous"},
	}
}

// recordOn asks a backend what it records of where the team went, under
// the command's budget (teammove.Recorded, the one probe every part of
// Brigade uses). keeps is false for a backend that keeps no such record.
func (inv Invocation) recordOn(t *target) (record *protocol.TeamMoveResult, keeps bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), adapterclient.DefaultTimeout)
	defer cancel()
	return teammove.Recorded(ctx, t.client)
}

// clearOn asks a backend to clear its record of where the team went, under
// the command's budget (teammove.ClearRecord, the one clear every part of
// Brigade uses), and says what became of it. The error is the cause of an
// outcome that is not teammove.Cleared.
func (inv Invocation) clearOn(t *target) (teammove.ClearOutcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), adapterclient.DefaultTimeout)
	defer cancel()
	return teammove.ClearRecord(ctx, t.client)
}

// toServer is what follows `--to` in a command a refusal prints for its
// reader to run: the server's address, and `--certificate` with its pin
// where the server has one. A line printed without the pin is not the same
// command for a server that made its own certificate: it names a server
// with no pin, which is another destination than the one the team file or
// a membership names, and the command would refuse it or fail to trust it.
func toServer(url, certificate string) string {
	if certificate == "" {
		return attrLine(url)
	}
	return attrLine(url) + " --certificate " + pinLine(certificate)
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
// Whether it can keep a record, and whether this member may write it, are
// asked together, before the server is asked anything: the server's first
// request carries the team's join secret, and a member who did not create
// the team must be refused before it is sent. How it is asked depends on
// whether a record is there.
//
// Where there is none, it is cleared. Clearing no record is a no-op by the
// protocol's own rule (4.4.10), and it is answered only to the team's
// creator and only by a backend that keeps records: a Supabase project
// without the team-move migration reads "no move" to every member and
// refuses the clear by naming the migration.
//
// Where a record is there, the backend keeps records, and clearing it is no
// way to ask: the record is recorded again, as it is (recordAgain).
//
// Then the roster: the sessions that are online on a plugin older than the
// first that follows a recorded move. Their members join the server
// themselves, or update first; a member with no session online is not
// seen here, and a roster the backend cut short is said to be one.
func (inv Invocation) checkOldBackend(m *serverMove) error {
	record, err := inv.callMove(m.from, "moved", nil, nil)
	if err != nil {
		return err
	}
	if !record.Moved {
		if _, err := inv.callMove(m.from, "move", []string{"--clear"}, nil); err != nil {
			return m.cannotRecord(err)
		}
	} else {
		if record, err = inv.recordAgain(m); err != nil {
			return err
		}
		if record.Adapter != m.dest.Adapter || record.URL != m.dest.URL || record.Certificate != m.dest.Certificate {
			m.replaces = record
		} else {
			m.recorded = true
		}
	}
	list, err := call(func(ctx context.Context) (*adapterclient.ListResult, error) {
		return m.from.client.ListSessions(ctx, false)
	})
	if err != nil {
		return err
	}
	m.rosterCut = list.Truncated
	for i := range list.Sessions {
		s := &list.Sessions[i]
		// From a Brigade server a session follows from a later plugin than
		// from any other backend: the one whose `rest` adapter reads a
		// server's record.
		if s.BrigadeVersion == nil || !teammove.OlderThanFollowFrom(m.source.Adapter, *s.BrigadeVersion) {
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

// recordAgain asks, where the backend the team leaves holds a record of a
// move already, whether this member may write it: it records again the
// record that is there, and answers that record. The record's ticket is
// read into the private file step c uses, by the command every active
// member is answered, and is handed back with the destination the record
// names by the command only the team's creator is answered. The creator's
// record keeps its destination and its ticket, and gets a new date. Anyone
// else is refused here as where there is no record, with nothing changed,
// and before the server is asked anything (ADR 0007): until this check a
// record that was there left the question to step d, by which time the
// server had been sent the join secret and had taken the team in.
//
// The ticket goes from its file to the old backend's adapter by the file's
// path, and is in no argument, no line and no result. The file is removed
// when the check ends, whatever its outcome.
//
// The record that is written again is the one whose ticket was read. A
// record that is gone by then was cleared while this command looked at it:
// the command stops and says to run it again, and does not put back a
// record its administrator has just cleared. What it cannot see is a clear
// made after the ticket was read and before the record is written again,
// which that write undoes; only the team's creator can make either.
func (inv Invocation) recordAgain(m *serverMove) (*protocol.TeamMoveResult, error) {
	file, err := m.ticketFile()
	if err != nil {
		return nil, m.noTicketFile()
	}
	defer func() { _ = os.Remove(file) }()
	held, err := inv.callMove(m.from, "moved", []string{"--ticket-file", file}, nil)
	if err != nil {
		var perr *protocol.Error
		if errors.As(err, &perr) && perr.Details["reason"] == reasonTicketFileUnwritable {
			return nil, m.noTicketFile()
		}
		return nil, err
	}
	if !held.Moved {
		return nil, &protocol.Error{
			Code: protocol.CodeConflict,
			Message: "the record of a move that " + m.fromHost() + " held was cleared while this command was checking it. " +
				"Run the same command again: it then moves a team that has no record. Nothing was changed",
			Details: map[string]string{"reason": reasonRecordChanged},
		}
	}
	if held.TicketFile != file {
		return nil, protocolMismatch("team moved answered a ticket file other than the one it was given")
	}
	// What the file holds is looked at as step c looks at the server's: a
	// ticket of this team. An adapter that wrote nothing, or something
	// else, is found here, before the record is touched.
	ticket, err := adapterkit.ReadTicketFile(file)
	if err != nil {
		return nil, m.noRecordTicket(err)
	}
	if err := adapterkit.CheckMoveTicket(ticket, m.file.TeamRef); err != nil {
		return nil, m.noRecordTicket(err)
	}
	again, err := inv.callMove(m.from, "move", []string{"--ticket-file", file},
		&protocol.TeamMoveRequest{Adapter: held.Adapter, URL: held.URL, Certificate: held.Certificate})
	if err != nil {
		return nil, m.cannotRecord(err)
	}
	if !again.Moved || again.Adapter != held.Adapter || again.URL != held.URL || again.Certificate != held.Certificate {
		return nil, protocolMismatch("team move answered a record other than the one it was given")
	}
	return held, nil
}

// noRecordTicket says, in front of what was wrong with the file, that the
// old backend's adapter did not hand over the ticket of the record it
// holds, and that nothing was changed. The text of the file is in no part
// of it.
func (m *serverMove) noRecordTicket(err error) error {
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		return err
	}
	out := *perr
	out.Message = m.fromHost() + " holds a record of a move already, and its adapter did not hand over that record's ticket: " + perr.Message + ". Nothing was changed"
	return &out
}

// noTicketFile refuses a move whose check needs the ticket's file and
// cannot have it: the directory under Brigade's state directory is not
// one, or takes no file.
func (m *serverMove) noTicketFile() *protocol.Error {
	return &protocol.Error{
		Code: protocol.CodeConfig,
		Message: m.fromHost() + " holds a record of a move already, and checking that you may replace it needs a private file for its ticket, which could not be written: " +
			"make " + pathLine(filepath.Join(m.to.stateDir, moveTicketDir)) + " a directory that you can write, or remove what stands in its place, and run this again. Nothing was changed",
		Details: map[string]string{"reason": reasonTicketFileUnwritable},
	}
}

// cannotRecord turns the two refusals of the creator question, the clear
// of no record and the record written again alike, into what they mean for
// a move, each with what to do. Any other error is the backend's own and is
// returned as it is.
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
	case perr.Details["reason"] == reasonServerLacksMove:
		return &protocol.Error{
			Code: protocol.CodeConfig,
			Message: "the server this team is on (" + m.fromHost() + ") keeps no record of where a team goes: it is older than 0.8.0. " +
				"Its operator updates it to 0.8.0 or later; then run this again. Until then a team on it is moved by pointing " + teamfile.FileName +
				" at the new server, and each member joins there with the secret file. Nothing was changed",
			Details: map[string]string{"reason": reasonServerLacksMove},
		}
	case perr.Code == protocol.CodeUnauthorized:
		who, whose, where := "the member who created this team on "+m.fromHost(), "administrator's", "administrator's machine"
		if m.source.Adapter == config.BundledRestName {
			// A team on a server has an owner, which a transfer and the
			// server's operator can change: the creator is not always it.
			who, whose, where = "the owner of this team on "+m.fromHost(), "owner's", "owner's machine, or have the owner transfer the team to you first"
		}
		return &protocol.Error{
			Code: protocol.CodeUnauthorized,
			Message: "only " + who + " can move it: where a team went is its " + whose + " to record there. " +
				"Run this on the " + where + ". Nothing was changed",
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
	if m.memberThere {
		// The server is asked nothing before the answer, so whether it
		// holds such a record is not known yet: what is done about one is.
		out += " This machine has joined the team on " + m.toHost() + " before: if " + m.toHost() +
			" records that the team left it, that record is cleared first, because the team is there again."
	}
	if older := olderLine(m.older, m.rosterCut, teammove.FollowsFrom(m.source.Adapter)); older != "" {
		out += " " + older + "."
	}
	return out
}

// olderLine names the sessions that will not follow, "" when there is
// none. follows is the first plugin version that follows a move from the
// backend the team leaves (teammove.FollowsFrom). A roster the backend cut short may lack some: the line then says
// that the list is not complete, and says it where no session was named.
func olderLine(older []olderSession, cut bool, follows string) string {
	const notAll = "the roster it was read from was cut short, so this list is not complete"
	if len(older) == 0 {
		if cut {
			return "no session that is online was seen on a plugin older than " + follows + ", but " + notAll
		}
		return ""
	}
	if cut {
		return olderLine(older, false, follows) + "; " + notAll
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
		return "1 session that is online runs a plugin older than " + follows + " and will not follow until its member updates it: " + list
	}
	return strconv.Itoa(len(older)) + " sessions that are online run a plugin older than " + follows +
		" and will not follow until their members update it: " + list
}

// takeIn is step b: the server takes the team in, with this member as its
// owner, under the label the member has on the backend the team leaves.
// That label is the membership's, never one read from the Claude profile
// of the terminal the command happens to run in (work item 326): a member
// of the team is the same member on the server, as one who follows is.
//
// The profile at the server is the team's key there on every attempt, the
// first included, so a run that is stopped after the server answered finds
// the credential the server issued and is the owner again, and one whose
// answer was lost finds whatever the adapter kept in that profile's
// directory to be the same caller with. A profile that is there under
// another certificate is moved to the one the administrator named, as a
// join does (alignRestBackend).
//
// The answer must say that the caller owns the team, and that is all it is
// read for: `taken_in` false beside `owner` true is a team this caller
// took in before, on a run that stopped or whose answer was lost, and the
// move goes on. A team that the server holds and someone else owns is
// joined and not owned, and its move is not this member's to open: the
// command stops here, before a move is opened and before anything is
// written, and says that the server's operator can name the team's owner.
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
		if fresh && refusedBeforeTheServerActed(err) {
			inv.forgetUnusedProfile(m)
		}
		return nil, m.notTakenIn(err)
	}
	if taken.TeamRef != m.file.TeamRef {
		return nil, protocolMismatch("team take-in answered a team other than the one the secret names")
	}
	if !taken.Owner {
		if known := m.sameData(taken.PrincipalRef); known != nil {
			return nil, inv.sameServer(m, known, fresh)
		}
		return nil, &protocol.Error{
			Code: protocol.CodeConflict,
			Message: m.team() + " is held on " + m.toHost() + " and owned by someone else, so its move is not yours to open there: the server's operator can name its owner. " +
				"This machine is a member of the team there, and `brigade-server --set-owner " + sanitizeID(m.file.TeamRef) + " --principal " + sanitizeID(taken.PrincipalRef) +
				"` names it the owner (docs/project-owner/server/name-the-owner.md in the Brigade repository); then run this again. Nothing was recorded, and " + teamfile.FileName + " is unchanged",
			Details: map[string]string{"reason": reasonNotOwnerThere},
		}
	}
	return taken, nil
}

// sameData answers the membership of this machine that the server which
// just answered "held, and owned by someone else" holds too, nil when it
// holds none: the principal of a membership this machine has of the team,
// on any backend, is on that server's roster. A server makes the
// references of its principals itself, at random, so one on two servers
// means one set of data: a server this machine knows under another address
// (another name of the machine, a spelling the command could not see
// through), or a server started from a copy of its data directory. Every
// membership the machine holds is looked for, not only the one on the
// backend the team is leaving: a team that moved from A to B and is
// pointed at A by another address is A all the same. The membership the
// take-in itself made, mine, is the one that is always there and is left
// out. The roster names no owner; a principal of this machine's being
// there is the whole test. A roster that cannot be read answers nil, and
// the command says what it has always said.
func (m *serverMove) sameData(mine string) *adapterkit.Profile {
	bindings, keys, err := listBindings(m.configDir)
	if err != nil {
		return nil
	}
	held := map[string]*adapterkit.Profile{}
	for i, b := range bindings {
		if b.TeamRef == m.file.TeamRef && keys[i] != m.destKey && b.PrincipalRef != "" && b.PrincipalRef != mine {
			// The backend the team is leaving first: it is the one a line names.
			if _, seen := held[b.PrincipalRef]; !seen || keys[i] == m.sourceKey {
				held[b.PrincipalRef] = b
			}
		}
	}
	if len(held) == 0 {
		return nil
	}
	roster, err := call(func(ctx context.Context) (*adapterclient.MembersResult, error) { return m.to.client.TeamMembers(ctx) })
	if err != nil {
		return nil
	}
	var known *adapterkit.Profile
	for i := range roster.Members {
		b, ok := held[roster.Members[i].PrincipalRef]
		if !ok {
			continue
		}
		if b.URL == m.source.URL && b.Adapter == m.source.Adapter {
			return b
		}
		if known == nil {
			known = b
		}
	}
	return known
}

// sameServer refuses a move to a server this machine already knows under
// another address, or to a copy of one's data (sameData), and ends the
// membership the take-in made there: this machine had no credential for
// that address, so the take-in was the join of a new member, and the
// administrator would be on the roster twice. known is the membership the
// server was found to hold. Only a membership this run made is ended
// (fresh). When it cannot be, the refusal names the one command that ends
// it. It never names the operator's --set-owner: on that server it would
// take the team from the administrator's own membership.
func (inv Invocation) sameServer(m *serverMove, known *adapterkit.Profile, fresh bool) error {
	left := false
	if fresh {
		if _, err := call(func(ctx context.Context) (*protocol.Envelope, error) {
			return m.to.client.Call(ctx, "team", "leave", nil, nil)
		}); err == nil {
			left = true
			if _, rerr := call(func(ctx context.Context) (*protocol.Envelope, error) {
				return m.to.client.Call(ctx, "profile", "reset", nil, nil)
			}); rerr != nil {
				inv.logger().Debug("the profile of a membership that was ended was not removed")
			}
		}
	}
	second := "The membership this command made on " + m.toHost() + " to find that out is ended"
	if !left {
		second = "This machine holds a second membership of the team under that address, which `brigade adapter " + config.BundledRestName +
			" --profile " + m.destKey + " team leave` ends"
	}
	knownHost := attrLine(hostOf(known.URL))
	what := "and a team is not moved to either. " +
		"A server's address changes by a fresh server at the new one, which its operator starts (docs/project-owner/server/move-to-another-server.md in the Brigade repository). "
	if known.URL != m.source.URL || known.Adapter != m.source.Adapter {
		// A server the team was on before, not the one it is on: a team may
		// go back to it, by the address this machine knows it by.
		what = "and a team is not moved to a copy. To move the team back to " + knownHost + ", name it by the address this machine knows it by: `--to " + toServer(known.URL, known.Certificate) + "`. "
	}
	return &protocol.Error{
		Code: protocol.CodeConflict,
		Message: m.toHost() + " holds " + m.team() + " with the membership this machine has on " + knownHost +
			": it is the same server under another address, or a copy of its data, " + what +
			second + ". Nothing was recorded, and " + teamfile.FileName + " is unchanged",
		Details: map[string]string{"reason": reasonSameServer},
	}
}

// refusedBeforeTheServerActed reports whether a take-in's failure is one
// after which the server holds nothing new: a refusal the adapter or the
// server made. `unavailable` and `internal` are not: the request may have
// reached the server and been carried out, with the answer lost on the way
// back, and so is a failure the adapter did not produce at all (a deadline,
// a child that died) or an answer this build could not read. What the
// adapter left in the profile's directory is then what makes the next run
// the same caller to the server, so nothing of it may be removed.
func refusedBeforeTheServerActed(err error) bool {
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		return false
	}
	return perr.Code != protocol.CodeUnavailable && perr.Code != protocol.CodeInternal && perr.Code != protocol.CodeProtocolMismatch
}

// forgetUnusedProfile removes the profile the server's adapter wrote for a
// take-in that was refused, when this command is what made it and the
// server issued it nothing: the adapter writes the backend down before it
// asks the server anything, and a refused move should leave the store as
// it found it. It runs only after a refusal (refusedBeforeTheServerActed):
// a take-in whose answer was lost leaves the same profile, with no
// credential, and that profile's directory is what the adapter retries
// from, the same caller to the server on every attempt. `profile status`
// is answered from local files, and `profile reset` on a profile that
// holds no credential asks the server nothing and removes the directory.
// A profile that holds a credential is kept whatever the failure was.
// Best effort: a profile that stays is one the next run uses.
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
			Code: protocol.CodeConflict,
			Message: m.toHost() + " already holds a team of this reference under another join secret, so this team cannot be taken in there: tell the server's operator. " +
				"If the team was on " + m.toHost() + " before and its join secret was rotated since on either side, name the file of the secret it has on " + m.toHost() + ". " + unchanged,
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

// clearReturn is step b': the server the team moves to is asked whether
// it records that this team left it, and a record that is there is
// cleared. That is a team that comes back: it moved from this server to
// the backend it is now leaving, its owner recorded that here, and the
// record is still here. Left as it is, the two backends would each say
// that the team is on the other, and whichever a team file named would be
// followed: a session could be sent back and forth by a file alone.
//
// It runs after the take-in, which is what makes this machine a member
// that may read the record and says that it owns the team, and before
// anything is recorded on the backend the team leaves. A clear that is
// refused or fails stops the move: the team is taken in, no move is
// opened, and the backend the team leaves holds what it held. A server
// that keeps no such record (one before 0.8.0) answers that the team did
// not move, and nothing is cleared.
func (inv Invocation) clearReturn(m *serverMove) error {
	held, keeps, err := inv.recordOn(m.to)
	if err != nil {
		return m.returnNotCleared(err, nil, false)
	}
	if !keeps || !held.Moved {
		return nil
	}
	switch outcome, cerr := inv.clearOn(m.to); outcome {
	case teammove.ClearRefused:
		// The server refused before it acted, or answered a record: it stands.
		return m.returnNotCleared(cerr, held, true)
	case teammove.ClearUnanswered:
		return m.returnNotCleared(cerr, held, false)
	case teammove.Cleared:
		m.returned = held
	}
	return nil
}

// returnNotCleared says, in front of the step's own failure, which server
// holds what when step b' stops a move: the server took the team in, it
// still holds its record that the team left it (held, when that was read),
// and the backend the team leaves was asked to record nothing. stands says
// that the record is known to stand: the clear was refused, or answered a
// record. A clear that failed any other way may have reached the server
// and been carried out with its answer lost, and the message says that,
// and where the record is read.
func (m *serverMove) returnNotCleared(err error, held *protocol.TeamMoveResult, stands bool) error {
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		perr = &protocol.Error{Code: protocol.CodeInternal, Message: "the step failed on this machine"}
	}
	out := *perr
	where := "the team is taken in on " + m.toHost() + ", and whether " + m.toHost() + " records that the team left it could not be read"
	switch {
	case held != nil && stands:
		where = "the team is taken in on " + m.toHost() + ", and " + m.toHost() + " still records that the team left it for " +
			moveDestination(held.Adapter, held.URL, held.Certificate) + ": that record could not be cleared"
	case held != nil:
		// The request may have reached the server and been carried out,
		// with the answer lost on the way back: what is true is that it is
		// not known, and where to read it.
		where = "the team is taken in on " + m.toHost() + ", which recorded that the team left it for " +
			moveDestination(held.Adapter, held.URL, held.Certificate) + ": the answer to the clear of that record did not arrive, so the record may be cleared all the same " +
			"(`brigade team move --show --from " + m.toHost() + "` says)"
	}
	out.Message = where + ". Its move was not opened, and " + m.fromHost() + " was asked to record nothing: it holds what it held, and no session follows this move. " +
		"The cause: " + perr.Message + ". Run the same command again to finish the move: every step of it is safe to repeat"
	out.Details = map[string]string{"reason": reasonReturnNotCleared}
	if cause := perr.Details["reason"]; cause != "" {
		out.Details["cause"] = cause
	}
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
	if m.returned != nil {
		// Step b' cleared the record the server held of the team's earlier
		// move away from it, and that is not undone by a later step that
		// fails: whoever had not followed that earlier move follows nothing
		// until this one is finished.
		where += " (the record " + m.toHost() + " held that the team had left it for " +
			moveDestination(m.returned.Adapter, m.returned.URL, m.returned.Certificate) +
			" is cleared, so a member who had not followed that earlier move does not follow it now)"
	}
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
			From:            moveEnd{Adapter: attrLine(m.source.Adapter), URL: attrLine(m.source.URL)},
			To:              moveEnd{Adapter: m.dest.Adapter, URL: m.dest.URL, Certificate: m.dest.Certificate},
			TakenIn:         taken.TakenIn,
			PrincipalRef:    sanitizeID(taken.PrincipalRef),
			MoveEndsAt:      opened.EndsAt,
			TicketReplaced:  opened.Replaced,
			RecordedAt:      recorded.RecordedAt,
			ReturnCleared:   m.returned != nil,
			TeamFile:        m.filePath,
			OlderSessions:   older,
			RosterTruncated: m.rosterCut,
			Gateway:         gateway,
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
	}
	if m.returned != nil {
		lines = append(lines, "cleared the record on "+m.toHost()+" that the team had left it for "+
			moveDestination(m.returned.Adapter, m.returned.URL, m.returned.Certificate)+": the team is on "+m.toHost()+" again")
	}
	lines = append(lines,
		open,
		"recorded on "+m.fromHost()+": "+m.team()+" moved to "+moveDestination(m.dest.Adapter, m.dest.URL, m.dest.Certificate),
		file,
		"left to do:",
		"  commit and push "+teamfile.FileName+" in every repository of the team: a member's next session in a checkout that has the new file follows, with no command of theirs, until "+ends,
	)
	if older := olderLine(m.older, m.rosterCut, teammove.FollowsFrom(m.source.Adapter)); older != "" {
		lines = append(lines, "  "+older)
	}
	if gateway {
		gateways := "  this team's " + teamfile.FileName + " has a gateway member: the mail and Slack gateways are the server operator's to configure on " + m.toHost() +
			" (docs/project-owner/server/gateways.md in the Brigade repository)"
		if m.source.Adapter == config.BundledRestName {
			// The server the team leaves keeps the team as it is, its
			// gateways included: they go on answering there.
			gateways += "; the ones on " + m.fromHost() + " go on carrying mail and Slack to the team there until its operator takes the team out of that server's gateway configuration " +
				"(docs/project-owner/server/move-to-another-server.md)"
		}
		lines = append(lines, gateways)
	}
	if m.source.Adapter == config.BundledRestName {
		lines = append(lines, "  "+m.fromHost()+" keeps the team as it is: nothing there was ended, and a member who has not followed still reaches it. "+
			"Ending it there is a later step of its own (docs/team-admin/members/move-the-team.md)")
	}
	return writeLines(inv.Out, append(lines,
		"to undo: `"+m.clearCommand()+"` stops "+m.fromHost()+" from saying where the team went, so no session follows from then on. "+
			"The move on "+m.toHost()+" stays open until "+ends+", or until its operator closes it or you rotate the team's join secret there",
	)...)
}

// clearCommand is the command that clears the record this move made, as
// the administrator runs it in this checkout afterwards. The record's own
// forms find the backend that holds it as the one other membership of the
// team this machine holds beside the one the team file names; a team that
// moved before leaves more than one, and the command then needs --from to
// say which. So the backend is named whenever it would be asked for.
func (m *serverMove) clearCommand() string {
	bindings, keys, err := listBindings(m.configDir)
	if err != nil {
		return "brigade team move --clear"
	}
	others := 0
	for i, b := range bindings {
		if b.TeamRef == m.file.TeamRef && keys[i] != m.destKey {
			others++
		}
	}
	if others > 1 {
		return "brigade team move --clear --from " + teammove.Host(m.source.URL)
	}
	return "brigade team move --clear"
}
