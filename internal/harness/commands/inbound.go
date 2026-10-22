package commands

import (
	"context"
	"log/slog"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// inboundResult is the --json result of `brigade inbound` (card 126).
type inboundResult struct {
	SelfSessionID string `json:"self_session_id"`
	// Inbound is the effective policy after the call: accept, hold or
	// refuse — the stronger of Decided and Requested.
	Inbound string `json:"inbound"`
	// Decided is the session's own policy: what the hook decided from the
	// team_inbound option, the native settings scan and the permission
	// mode, and re-decides at every prompt. A request cannot rise above it.
	Decided string `json:"decided"`
	// Requested is the request in force after the call: hold, refuse, or
	// "" for none.
	Requested string `json:"requested"`
	// Changed is true when this call moved the effective policy.
	Changed bool `json:"changed"`
	// Held is the number of messages held for this session and not yet
	// released, read from its pending file.
	Held int `json:"held"`
	// Published is true when this call's heartbeat carried the new policy
	// to the roster; false when nothing moved (nothing was sent) and when
	// the heartbeat failed (the watcher carries it at its next one).
	Published bool   `json:"published"`
	Note      string `json:"note"`
}

// The fixed texts of `brigade inbound` (card 126) the tests assert word
// for word.
const (
	// InboundUsage is the whole `usage` message for a bad argument list.
	InboundUsage = "inbound takes one word — accept, hold or refuse — or none to show the policy in force"
	// InboundNoteHold closes the output while the session holds: where the
	// held messages are read and released, and how the hold is lifted.
	InboundNoteHold = "held messages: brigade inbox lists them and brigade inbox release delivers them, in your own terminal; brigade inbound accept lifts the hold"
	// InboundNoteRefuse closes the output while the session refuses by its
	// own request.
	InboundNoteRefuse = "nothing is delivered or acknowledged while the session refuses; brigade inbound accept returns to the session's own policy"
	// InboundNoteCeiling closes the output while the session refuses by
	// its own policy, which no request in the session can lift. It names
	// the three sources of a refuse decision the way `inbox release` does,
	// and the file a warning named at session start.
	InboundNoteCeiling = "the session's own policy is refuse — its team_inbound option, a crossSessionInbound hold or refuse in a Claude Code settings file, or a permission mode that bypasses prompts without a crossSessionInbound accept in the user settings file (the session-start context line names the file) — and brigade inbound cannot lift it"
	// InboundNoteAccept closes the output while the session accepts.
	InboundNoteAccept = "team messages are delivered as they arrive; brigade inbound hold keeps them for your release, brigade inbound refuse turns them away"
	// InboundRosterLine is printed when the policy moved but this call's
	// heartbeat did not reach the adapter: the map — what the watcher and
	// the hooks enforce — is written either way.
	InboundRosterLine = "roster: not updated by this command; the session's watcher carries the policy at its next heartbeat"
	// InboundNote is the note of every --json result.
	InboundNote = "the effective policy is the stronger of the session's own (decided) and the request made in it; a request only tightens, lasts for the session's process, and never changes an option or a settings file"
)

// Inbound implements `brigade inbound [accept|hold|refuse] [--json]`
// (card 126): the per-session, runtime side of the inbound policy. With
// no word it reports the policy in force, where it came from and how many
// messages wait, spawning nothing. With `hold` or `refuse` it records the
// request in the by-pid map — the watcher applies the new value at its
// next liveness tick and the prompt hook's re-decision keeps it (the
// decision is the ceiling; the effective policy is the stronger of the
// two) — and sends one `session heartbeat` carrying `inbound`, so the
// roster and a teammate's `send --member` see it at once. With `accept`
// it clears the request and the session returns to its own policy, which
// is `accept` only if the hook decided so: a decided `refuse` stays, with
// the note saying why and that nothing here can lift it.
//
// Held messages are the hold policy's own (P5-9): the pending file,
// `brigade inbox` and `brigade inbox release` in a terminal, nothing new.
// A policy that did not move writes nothing and sends nothing, so a
// repeated `hold` is a status call. The call is in-session only: the
// request is the session's, and a terminal has no session to speak for.
func Inbound(inv Invocation) error {
	request, change, err := inboundArgs(inv.Args)
	if err != nil {
		return err
	}
	if !inv.inSession() {
		return notInSession("inbound")
	}
	if !change {
		return inboundStatus(inv)
	}
	t, err := inv.resolveSession("")
	if err != nil {
		return err
	}
	m := t.session
	self := t.selfSessionID()
	before := m.Inbound
	if m.InboundRequest == request && m.Decided() == m.InboundDecided {
		// The same request, in a map that already carries its decision:
		// nothing to write, nothing to send.
		return inboundReport(inv, m, t.stateDir, false, false)
	}
	m.SetInboundRequest(request)
	m.UpdatedAt = inv.Deps.now()
	if err := (sessionmap.Store{StateDir: t.stateDir}).WriteByPID(m); err != nil {
		return err
	}
	changed := m.Inbound != before
	inv.logger().Info("inbound request recorded", slog.String("inbound", m.Inbound), slog.String("decided", m.InboundDecided), slog.String("requested", m.InboundRequest))
	published := false
	if changed {
		ctx, cancel := context.WithTimeout(context.Background(), adapterclient.RegisterTimeout)
		defer cancel()
		next := m.Inbound
		if _, herr := t.client.Heartbeat(ctx, self, &protocol.HeartbeatRequest{Inbound: &next}); herr != nil {
			inv.logger().Warn("inbound heartbeat failed; the watcher carries the policy", log.Err(herr))
		} else {
			published = true
		}
	}
	return inboundReport(inv, m, t.stateDir, changed, published)
}

// inboundArgs parses the one optional word: "" and false for a status
// call; the policy word and true for a change, where `accept` is the
// empty request (the lifting of one).
func inboundArgs(args []string) (request string, change bool, err error) {
	switch {
	case len(args) == 0:
		return "", false, nil
	case len(args) > 1:
		return "", false, usage(InboundUsage)
	}
	switch args[0] {
	case protocol.InboundAccept:
		return "", true, nil
	case protocol.InboundHold, protocol.InboundRefuse:
		return args[0], true, nil
	}
	return "", false, usage(InboundUsage)
}

// inboundStatus is the no-argument form: the map read through the strict
// reader, the pending file counted, nothing spawned.
func inboundStatus(inv Invocation) error {
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return err
	}
	m, err := config.Session(inv.Environ, stateDir)
	if err != nil {
		return err
	}
	return inboundReport(inv, m, stateDir, false, false)
}

// inboundReport prints the three lines, or the --json envelope, for the
// policy m carries: the effective policy with its origin, the held count
// with up to three sender names, and the fixed note of the policy.
func inboundReport(inv Invocation, m *sessionmap.ByPID, stateDir string, changed, published bool) error {
	held := heldEntries(inv, stateDir, m.BrigadeSessionID)
	if inv.JSON {
		return writeJSON(inv.Out, inboundResult{
			SelfSessionID: m.BrigadeSessionID,
			Inbound:       m.Inbound,
			Decided:       m.Decided(),
			Requested:     m.InboundRequest,
			Changed:       changed,
			Held:          len(held),
			Published:     published,
			Note:          InboundNote,
		})
	}
	lines := []string{inboundLine(m), heldLine(held), inboundNote(m)}
	if changed && !published {
		lines = append(lines, InboundRosterLine)
	}
	return writeLines(inv.Out, lines...)
}

// heldEntries reads the session's pending file and keeps the entries not
// yet released; a missing file is none, and a refused or malformed one is
// logged and counted as none (the notice's own rule).
func heldEntries(inv Invocation, stateDir, sessionID string) []inbound.PendingEntry {
	pending, err := inbound.FilePendingStore{Path: inbound.PendingPath(stateDir, sessionID), SessionID: sessionID}.Load()
	if err != nil {
		inv.logger().Warn("pending file refused; held count unknown", log.Err(err))
		return nil
	}
	return unreleased(pending.Entries)
}

// inboundLine is the first line: `inbound: <effective> (<origin>)`, where
// the origin says whether a request in this session is what set it.
func inboundLine(m *sessionmap.ByPID) string {
	decided, request := m.Decided(), m.InboundRequest
	origin := "the session's own policy; no request in this session"
	switch {
	case request != "" && policy.Tighten(policy.Policy(decided), request).String() == request && request != decided:
		origin = "requested in this session; the session's own policy is " + enumLine(decided)
	case request != "":
		origin = "the session's own policy; the request for " + enumLine(request) + " in this session adds nothing"
	}
	return "inbound: " + enumLine(m.Inbound) + " (" + origin + ")"
}

// heldLine is the second line: the count, and the first distinct sender
// names (sanitised, unverified) when any are held.
func heldLine(held []inbound.PendingEntry) string {
	if len(held) == 0 {
		return "held: 0"
	}
	names, distinct := senderNames(held)
	line := "held: " + itoa(len(held)) + " (from " + strings.Join(names, ", ")
	if distinct > len(names) {
		line += " and " + itoa(distinct-len(names)) + " more"
	}
	return line + ")"
}

// inboundNote is the third line, fixed per effective policy and, under
// refuse, per whether a request in the session can lift it.
func inboundNote(m *sessionmap.ByPID) string {
	switch m.Inbound {
	case protocol.InboundHold:
		return InboundNoteHold
	case protocol.InboundRefuse:
		if m.Decided() == protocol.InboundRefuse {
			return InboundNoteCeiling
		}
		return InboundNoteRefuse
	}
	return InboundNoteAccept
}
