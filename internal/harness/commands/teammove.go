package commands

import (
	"context"
	json "encoding/json/v2"
	"flag"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teammove"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
)

// RefusalMoveInSession is the whole message of the `usage` refusal of
// `team move`'s writing forms inside a session whose member set the option
// `team_move_in_session` to `off` (work item 328, ADR 0006). Until that
// decision the refusal was unconditional (work item 273, ADR 0003): a move
// sends every member's sessions to another backend, whose operator reads
// what they send. It is now a member's choice, on by default, because the
// refusal never was a boundary: a command run with the session's variables
// removed passes it.
const RefusalMoveInSession = "run this in your own terminal: the option team_move_in_session is off here, so a session does not move a team, record where one went or clear that record (`brigade team move --show` reads the record anywhere)"

// moveFlags is the argv of `brigade team move`.
type moveFlags struct {
	ticketFile string
	from       string
	clear      bool
	show       bool
	// The one command of a move (work item 328): where the team goes, the
	// file that holds its join secret, and what the server may need.
	to              string
	secretFile      string
	certificate     string
	createTokenFile string
	ticketDays      int
	ticketDaysSet   bool
}

// parseMoveFlags reads the four forms: `--to <server url> --secret-file
// <path>` moves the team, `--ticket-file <path>` records a move whose
// ticket the server's operator wrote, `--clear` clears the record and
// `--show` reads it, each with an optional `--from <url>`. Exactly one form
// goes on a command line, and what belongs to `--to` goes with it only.
func parseMoveFlags(args []string) (moveFlags, error) {
	var m moveFlags
	fs := flag.NewFlagSet("team move", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&m.ticketFile, "ticket-file", "", "")
	fs.StringVar(&m.from, "from", "", "")
	fs.BoolVar(&m.clear, "clear", false, "")
	fs.BoolVar(&m.show, "show", false, "")
	fs.StringVar(&m.to, "to", "", "")
	fs.StringVar(&m.secretFile, "secret-file", "", "")
	fs.StringVar(&m.certificate, "certificate", "", "")
	fs.StringVar(&m.createTokenFile, "create-token-file", "", "")
	fs.IntVar(&m.ticketDays, "ticket-days", 0, "")
	const forms = "team move takes --to <server url> --secret-file <path> to move this project's team to a Brigade server " +
		"(with --certificate <sha256:…>, --create-token-file <path> and --ticket-days <n> when the server needs them), " +
		"--ticket-file <path> to record a move whose ticket the server's operator wrote, --clear to clear the record, or --show to read it; " +
		"--from <url> names the backend the team is leaving when this machine has joined the team on more than one"
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return m, usage(forms)
	}
	given := 0
	for _, on := range []bool{m.to != "", m.ticketFile != "", m.clear, m.show} {
		if on {
			given++
		}
	}
	if given != 1 {
		return m, usage(forms)
	}
	fs.Visit(func(f *flag.Flag) { m.ticketDaysSet = m.ticketDaysSet || f.Name == "ticket-days" })
	if m.to == "" && (m.secretFile != "" || m.certificate != "" || m.createTokenFile != "" || m.ticketDaysSet) {
		return m, usage(forms)
	}
	return m, nil
}

// teamMove implements `brigade team move`, the administrator's half of a
// team's move to another backend:
//
//	brigade team move --to <url> --secret-file <path>   move the team
//	brigade team move --ticket-file <path>              record a move
//	brigade team move --clear                           clear the record
//	brigade team move --show                            read it
//
// `--to` is the whole move in one command (work item 328; teammoveto.go).
// The other three are the record alone (work item 273, ADR 0003), for a
// server that takes no team in by request: its operator takes the team in
// and opens the move, and the administrator points the team file at the
// server, joins there and records the move with the operator's ticket.
//
// For `--ticket-file` the destination is never typed: it is the backend
// this project's team file names, so the record names exactly what every
// member's session will compare it with. The source is the backend this
// machine joined the team on before: the one other membership of the same
// team it holds. A record is refused until this machine holds the
// membership on the backend the file names, because a member the move's
// ticket lets in never becomes the owner.
//
// The ticket is the file the new backend's operator wrote. It is read
// from that file by the adapter, checked here first so a wrong file fails
// before anything is asked, and is never an argument and never printed.
//
// Inside a session the three writing forms run unless the member set the
// option `team_move_in_session` to `off` (ADR 0006). Nobody is there to
// answer a question, so what a terminal would ask is printed as a
// statement, as a join's is, and the command proceeds.
func (inv Invocation) teamMove(rest []string) error {
	flags, err := parseMoveFlags(rest)
	if err != nil {
		return err
	}
	if inv.inSession() && !flags.show {
		if err := inv.moveAllowedInSession(); err != nil {
			return err
		}
	}
	if flags.to != "" {
		return inv.moveTo(flags)
	}
	top, err := inv.requireToplevel("team move")
	if err != nil {
		return err
	}
	path, ok := teamfile.Discover(inv.mustGetwd())
	if !ok {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "this project has no " + teamfile.FileName + ": a move is recorded from a checkout whose team file names the backend the team went to",
			Details: map[string]string{"reason": "team_file_missing"},
		}
	}
	f, err := teamfile.Parse(path)
	if err != nil {
		return err
	}
	configDir, err := inv.storeDir()
	if err != nil {
		return err
	}
	source, sourceKey, err := moveSource(configDir, f, flags.from)
	if err != nil {
		return err
	}
	if flags.ticketFile != "" && sourceKey == teamstore.Key(f.Adapter, f.URL, f.TeamRef) {
		// --from named the backend the file names: a record there of a move
		// to itself would send nobody anywhere.
		return noMoveToRecord()
	}
	t, err := inv.dialectTarget(source.Adapter, sourceKey)
	if err != nil {
		return err
	}
	switch {
	case flags.show:
		return inv.showMove(t, source, f)
	case flags.clear:
		return inv.clearMove(t, source, f)
	}
	return inv.recordMove(t, source, f, top, configDir, flags.ticketFile)
}

// moveSource finds the backend whose record of the move is meant. With no
// --from it is the backend the team is leaving: the membership of the
// file's team that this machine holds on a backend OTHER than the one the
// file names. With --from it is whichever membership of the team that url
// or host names, the file's own backend included: that is how a record is
// read or cleared once the team file has been pointed back at the backend
// that holds it. None is `config`, and says which of the usual causes it
// is: a --from that matches nothing, a team file that still names the only
// backend the team was joined on, or a machine that never joined the team.
// More than one needs --from.
func moveSource(configDir string, f *teamfile.File, from string) (*adapterkit.Profile, string, error) {
	bindings, keys, err := listBindings(configDir)
	if err != nil {
		return nil, "", err
	}
	destination := teamstore.Key(f.Adapter, f.URL, f.TeamRef)
	var found []int
	onDestination := false
	for i, b := range bindings {
		if b.TeamRef != f.TeamRef {
			continue
		}
		if from != "" {
			if b.URL == from || teammove.Host(b.URL) == from {
				found = append(found, i)
			}
			continue
		}
		if keys[i] == destination {
			onDestination = true
			continue
		}
		found = append(found, i)
	}
	switch {
	case len(found) == 1:
		return bindings[found[0]], keys[found[0]], nil
	case len(found) > 1:
		return nil, "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "this machine has joined this team on more than one other backend; name the one it is leaving with --from <url>",
			Details: map[string]string{"reason": "team_ambiguous"},
		}
	case from != "":
		return nil, "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "--from names no backend this machine has joined this team on; `brigade team list` shows what it has joined",
			Details: map[string]string{"reason": "team_unknown"},
		}
	case onDestination:
		return nil, "", noMoveToRecord()
	}
	return nil, "", &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "this machine has not joined this team on the backend it is leaving: run this where the team's administrator joined it",
		Details: map[string]string{"reason": "team_unknown"},
	}
}

// noMoveToRecord refuses a command whose source would be the backend the
// team file itself names.
func noMoveToRecord() *protocol.Error {
	return &protocol.Error{
		Code: protocol.CodeConfig,
		Message: "this project's " + teamfile.FileName + " names the backend the record would be kept on, so there is no move to record from here: " +
			"point the file at the backend the team is going to, join it there, and run this again " +
			"(to read or clear a record that backend already holds, add --from <its url or host> to --show or --clear)",
		Details: map[string]string{"reason": "no_move"},
	}
}

// requireMoveRecord refuses, before anything is asked of it, a backend
// whose adapter keeps no record of moves: it does not advertise the
// capability, so it is asked nothing (4.7). The adapter's describe is
// answered from local files.
func requireMoveRecord(t *target) error {
	desc, err := call(func(ctx context.Context) (*protocol.DescribeResult, error) { return t.client.Describe(ctx) })
	if err != nil {
		return err
	}
	if !slices.Contains(desc.Capabilities, teammove.Capability) {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the backend this team was joined on keeps no record of where a team goes: its adapter does not offer `" + teammove.Capability + "`, so its members join the new backend themselves, with the secret file",
			Details: map[string]string{"reason": "no_team_move"},
		}
	}
	return nil
}

// recordMove records the move on the backend the team is leaving.
func (inv Invocation) recordMove(t *target, source *adapterkit.Profile, f *teamfile.File, top, configDir, ticketFile string) error {
	if strings.HasPrefix(ticketFile, protocol.JoinSecretPrefix) {
		// A ticket where a path should be would ride argv; the value is
		// never echoed.
		return usage("a move ticket must never be passed on the command line; --ticket-file names a file, and the ticket goes in it")
	}
	ticketFile = expandHome(ticketFile, inv.Environ)
	if !filepath.IsAbs(ticketFile) {
		return usage("--ticket-file must be an absolute path outside this project (a relative path would be read from the repository)")
	}
	if err := checkTicketFileOutside(ticketFile, top); err != nil {
		return err
	}
	if resolved, rerr := filepath.EvalSymlinks(ticketFile); rerr == nil {
		if err := checkTicketFileOutside(resolved, top); err != nil {
			return err
		}
	}
	// The same two checks the adapter makes, made here first: a wrong file
	// is refused before anyone is asked to confirm anything.
	ticket, err := adapterkit.ReadTicketFile(ticketFile)
	if err != nil {
		return err
	}
	if err := adapterkit.CheckMoveTicket(ticket, f.TeamRef); err != nil {
		return err
	}
	// The administrator joins the new backend first, with the secret, and
	// so owns the team there: a member the ticket lets in never does.
	joined, berr := teamstore.LoadBinding(configDir, teamstore.Key(f.Adapter, f.URL, f.TeamRef))
	if berr != nil || teamstore.VerifyBinding(joined, f) != nil {
		return &protocol.Error{
			Code: protocol.CodeConfig,
			Message: "join the backend this project's " + teamfile.FileName + " names first, with the team's secret file (`brigade team join --secret-file <path>`): " +
				"the first member to join there with the secret owns the team, and a member that a move's ticket lets in never does",
			Details: map[string]string{"reason": "not_joined_at_destination"},
		}
	}
	if err := requireMoveRecord(t); err != nil {
		return err
	}
	line := "record that team \"" + nameLine(f.TeamName) + "\" (" + sanitizeID(f.TeamRef) + ") moved from " + attrLine(hostOf(source.URL)) +
		" to " + moveDestination(f.Adapter, f.URL, f.Certificate)
	if err := inv.consentToMove(line, "Every member's next session follows it there with no command of theirs.", "not recorded; nothing was changed"); err != nil {
		return err
	}
	req := &protocol.TeamMoveRequest{Adapter: f.Adapter, URL: f.URL, Certificate: f.Certificate}
	res, err := inv.callMove(t, "move", []string{"--ticket-file", ticketFile}, req)
	if err != nil {
		return err
	}
	if !res.Moved || res.Adapter != f.Adapter || res.URL != f.URL || res.Certificate != f.Certificate {
		return protocolMismatch("team move answered a record other than the one it was given")
	}
	if inv.JSON {
		return writeJSON(inv.Out, res)
	}
	return writeLines(inv.Out,
		"recorded on "+attrLine(hostOf(source.URL))+": team \""+nameLine(f.TeamName)+"\" ("+sanitizeID(f.TeamRef)+") moved to "+
			moveDestination(f.Adapter, f.URL, f.Certificate),
		"next: commit and push "+teamfile.FileName+" in every repository of the team; a member's session follows at its next start, "+
			"for as long as the new backend keeps the move open (`brigade team move --clear` stops this backend from saying where the team went)",
	)
}

// moveAllowedInSession reads the option `team_move_in_session` for a
// command run inside a session, from the start facts SessionStart wrote:
// a plugin option never reaches the Bash tool (P7-11), so the facts are
// the one place such a command can read it. Off, or a value that is
// neither word, is the in-session refusal. A session with no facts is
// refused with what the facts' reader says: without them the command could
// not know which store to act on either.
func (inv Invocation) moveAllowedInSession() error {
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return err
	}
	facts, err := inv.startFacts(stateDir)
	if err != nil {
		return err
	}
	if on, _ := config.ParseTeamMoveInSession(facts.TeamMoveInSession); !on {
		return inSessionRefusal(RefusalMoveInSession)
	}
	return nil
}

// consentToMove shows what a writing form of `team move` is about to do.
// At a terminal it asks, and an answer that is not yes is the `usage`
// refusal declined says. Inside a session nobody is there to ask, stdin
// being /dev/null, so the invocation is the consent, as it is for a join
// (confirmGate): the same line is printed, as a statement, and the command
// proceeds. With --json the statement goes to stderr, where the question
// goes at a terminal, so stdout stays one envelope.
func (inv Invocation) consentToMove(line, consequence, declined string) error {
	if inv.inSession() {
		said := line + ". " + consequence + " Consented by this invocation."
		if inv.JSON {
			_, err := io.WriteString(inv.Err, said+"\n")
			return err
		}
		return writeLines(inv.Out, said)
	}
	ok, err := inv.confirm(line + "? " + consequence + " [y/N] ")
	if err != nil {
		return err
	}
	if !ok {
		return &protocol.Error{
			Code:    protocol.CodeUsage,
			Message: declined,
			Details: map[string]string{"reason": "declined"},
		}
	}
	return nil
}

// checkTicketFileOutside is the join secret file's rule for a move's ticket
// file: named plainly, and never under the checkout.
func checkTicketFileOutside(ticketFile, top string) error {
	switch fileOutside(ticketFile, top) {
	case outsideDotDot:
		return usage("--ticket-file must not contain a `..` component; spell the path plainly")
	case outsideInside:
		return usage("--ticket-file must not be inside this project: a move ticket must never live under the repository")
	}
	return nil
}

// clearMove clears the record on the backend the team left.
func (inv Invocation) clearMove(t *target, source *adapterkit.Profile, f *teamfile.File) error {
	if err := requireMoveRecord(t); err != nil {
		return err
	}
	res, err := inv.callMove(t, "move", []string{"--clear"}, nil)
	if err != nil {
		return err
	}
	if res.Moved {
		return protocolMismatch("team move --clear answered a record")
	}
	if inv.JSON {
		return writeJSON(inv.Out, res)
	}
	return writeLines(inv.Out, "cleared: "+attrLine(hostOf(source.URL))+" no longer says where team \""+nameLine(f.TeamName)+"\" ("+
		sanitizeID(f.TeamRef)+") went, so no session follows it from here; the move's ticket admits at the new backend until its operator closes the move")
}

// showMove reads the record from the backend the team left and says
// whether it names what this project's team file names, which is the
// comparison a member's session makes.
func (inv Invocation) showMove(t *target, source *adapterkit.Profile, f *teamfile.File) error {
	if err := requireMoveRecord(t); err != nil {
		return err
	}
	res, err := inv.callMove(t, "moved", nil, nil)
	if err != nil {
		return err
	}
	if inv.JSON {
		return writeJSON(inv.Out, res)
	}
	team := "team \"" + nameLine(f.TeamName) + "\" (" + sanitizeID(f.TeamRef) + ")"
	if !res.Moved {
		return writeLines(inv.Out, "no move recorded on "+attrLine(hostOf(source.URL))+" for "+team+": no session follows it from there")
	}
	same := "this project's " + teamfile.FileName + " names the same backend, so a member's session follows"
	if res.Adapter != f.Adapter || res.URL != f.URL || res.Certificate != f.Certificate {
		same = "this project's " + teamfile.FileName + " names ANOTHER backend, so no session follows: record the move again, or correct the file"
	}
	return writeLines(inv.Out, "recorded on "+attrLine(hostOf(source.URL))+" at "+res.RecordedAt.UTC().Format("2006-01-02 15:04")+" UTC: "+team+
		" moved to "+moveDestination(res.Adapter, res.URL, res.Certificate), same)
}

// callMove runs one of the two commands of capability team.move on the
// backend the team is leaving and decodes its result.
func (inv Invocation) callMove(t *target, verb string, flags []string, stdin any) (*protocol.TeamMoveResult, error) {
	return call(func(ctx context.Context) (*protocol.TeamMoveResult, error) {
		env, err := t.client.Call(ctx, "team", verb, flags, stdin)
		if err != nil {
			return nil, err
		}
		var res protocol.TeamMoveResult
		if uerr := json.Unmarshal(env.Result, &res); uerr != nil || res.Validate() != nil {
			return nil, protocolMismatch("team " + verb + " answered a shape this build does not read")
		}
		return &res, nil
	})
}

// moveDestination renders where a team went for a person: the host, the
// adapter when it is not the bundled one for a server, and the certificate
// pin in full when there is one.
func moveDestination(adapter, url, certificate string) string {
	out := attrLine(hostOf(url)) + " via adapter \"" + attrLine(adapter) + "\""
	if certificate != "" {
		out += ", server certificate " + pinLine(certificate)
	}
	return out
}
