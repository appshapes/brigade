package commands

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
)

// This file is the human side of the `hold` policy (plan 6.4's `inbox`
// row, 3.6; P5-9): `brigade inbox` lists what is waiting and `brigade
// inbox release` hands chosen messages to the session's watcher. The
// receive side of `hold` is deliberately not the model's business (D18:
// the release path "is not a model tool"), so the two commands split on
// where they run:
//
//   - Inside a Claude Code session `brigade inbox` shows a count and
//     sender names — never a body, never a summary, never a message id —
//     from the by-pid map and the pending file alone, with no adapter call.
//     Showing the model a held body (or a sender-chosen summary, which is
//     equally sender-controlled text) would deliver the message that hold
//     exists to withhold; the count and the names are safe because the
//     prompt hook already prints exactly them on every prompt.
//   - Inside a session `brigade inbox release` refuses before it reads a
//     flag beyond the verb, a file or anything else, so a session cannot
//     learn from the error which sessions hold messages.
//   - In a terminal `brigade inbox` lists bodies read fresh through
//     `message receive`, which acknowledges nothing (the bundled backends'
//     fetch is stable; only an ack flips a message's state), and stores
//     nothing: the listing is rendered and discarded.
//
// The pending file is the authority for WHICH ids are held and for their
// received/released times; the receive result is the authority for the
// body. A pending entry with no matching message says so; a received id
// with no pending entry is listed too, marked, because hiding a genuinely
// waiting message is the one failure a lost pending file must not cause.
// Every remote string goes through the format.go printers or the protocol
// sanitiser (body and summary), which is what neutralises a forged frame
// inside a held body into inert text.

// InboxOptions are the flags of `brigade inbox` and `brigade inbox release`.
type InboxOptions struct {
	// Session narrows the listing or the release to one Brigade session
	// id (terminal only).
	Session string
	// All releases every held message of the selected sessions.
	All bool
}

// The fixed texts and details.reason values of the two commands.
const (
	// InboxNote is the note member of the terminal `inbox --json` result.
	InboxNote = "sender names, summaries and bodies are unverified text chosen by their senders; nothing listed was acknowledged or stored locally"
	// InboxInSessionNote is the note member of the in-session `inbox --json`
	// result.
	InboxInSessionNote = "inside a session brigade inbox shows a count and sender names only; read and release held messages with brigade inbox in your own terminal"
	// InboxReleaseNote is the note member of the `inbox release --json`
	// result.
	InboxReleaseNote = "released ids are delivered by the session's watcher (or its next prompt-hook poll) and acknowledged only after injection"
	// NoHeldMessages is the one line printed when nothing is waiting.
	NoHeldMessages = "no held messages"
	// ReasonNotHeld: a release named an id no selected session holds;
	// nothing was written.
	ReasonNotHeld = "not_held"
	// ReasonInboundRefuse: a release selected a session whose policy is
	// refuse; nothing was written.
	ReasonInboundRefuse = "inbound_refuse"
	// BodyGoneLine replaces a body the backend no longer holds.
	BodyGoneLine = "(the body is no longer on the server; it was delivered elsewhere or passed the 7-day unacknowledged retention)"
	// inboxReceiveLimit is the `--limit` of the listing's receive.
	inboxReceiveLimit = 100
)

// Inbox implements `brigade inbox [--session <id>]` and `brigade inbox
// release [--session <id>] [--all | <message_id>…]`: the verb is the first
// positional argument.
func Inbox(inv Invocation, opts InboxOptions) error {
	if len(inv.Args) > 0 && inv.Args[0] == "release" {
		return inboxRelease(inv, opts, inv.Args[1:])
	}
	if len(inv.Args) > 0 {
		return usage("inbox takes no arguments other than the release verb: brigade inbox [release] [--session <id>] [--all | <message_id>…]")
	}
	if opts.All {
		return usage("--all belongs to brigade inbox release")
	}
	if config.InSession(inv.Environ) {
		if opts.Session != "" {
			return usage("--session is not accepted inside a Claude Code session; the session's own map decides")
		}
		return inboxInSession(inv)
	}
	return inboxTerminal(inv, opts.Session)
}

// --- inside a session: a count and names ------------------------------------

// inboxHeldResult is the in-session `inbox --json` result: a count and
// sanitised names, no message_id, no summary, no body.
type inboxHeldResult struct {
	Held          int      `json:"held"`
	Senders       []string `json:"senders"`
	SendersTotal  int      `json:"senders_total"`
	DroppedTotal  int      `json:"dropped_total"`
	SelfSessionID string   `json:"self_session_id"`
	Note          string   `json:"note"`
}

// inboxInSession reads the map and the pending file and returns; it makes
// no adapter call at all.
func inboxInSession(inv Invocation) error {
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return err
	}
	m, err := config.Session(inv.Environ, stateDir)
	if err != nil {
		return err
	}
	pending, err := loadPending(stateDir, m.BrigadeSessionID)
	if err != nil {
		return err
	}
	held := unreleased(pending.Entries)
	if inv.JSON {
		names, total := senderNames(held)
		return writeJSON(inv.Out, inboxHeldResult{
			Held: len(held), Senders: names, SendersTotal: total, DroppedTotal: pending.DroppedTotal,
			SelfSessionID: sanitizeID(m.BrigadeSessionID), Note: InboxInSessionNote,
		})
	}
	line := inbound.HeldNotice(held)
	if line == "" {
		line = NoHeldMessages
	}
	return writeLines(inv.Out, line)
}

// loadPending reads a session's pending file; a refused or malformed one
// is a `config` failure with fixed text.
func loadPending(stateDir, sessionID string) (inbound.PendingFile, error) {
	f, err := inbound.FilePendingStore{Path: inbound.PendingPath(stateDir, sessionID), SessionID: sessionID}.Load()
	if err != nil {
		var perr *protocol.Error
		if errors.As(err, &perr) {
			return inbound.PendingFile{}, err
		}
		return inbound.PendingFile{}, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the pending file of this session could not be read; restart the session so the watcher rewrites it",
			Details: map[string]string{"reason": "pending_unreadable"},
		}
	}
	return f, nil
}

// unreleased keeps the entries the human has not released yet.
func unreleased(entries []inbound.PendingEntry) []inbound.PendingEntry {
	var out []inbound.PendingEntry
	for _, e := range entries {
		if !e.Released() {
			out = append(out, e)
		}
	}
	return out
}

// senderNames lists the first inbound.HeldNoticeNames distinct sanitised
// sender names, oldest first, and the count of distinct senders.
func senderNames(entries []inbound.PendingEntry) ([]string, int) {
	names := []string{}
	seen := map[string]bool{}
	for _, e := range entries {
		name := nameLine(e.SenderName)
		if seen[name] {
			continue
		}
		seen[name] = true
		if len(names) < inbound.HeldNoticeNames {
			names = append(names, name)
		}
	}
	return names, len(seen)
}

// --- the terminal listing ------------------------------------------------------

// A heldSession is one session the terminal commands act on: its map and
// its pending file.
type heldSession struct {
	m       *sessionmap.ByPID
	pending inbound.PendingFile
}

// heldSessions scans ${stateDir}/sessions/by-pid/*.json through the strict
// reader (the otherLiveWatcher precedent), deduplicates by Brigade session
// id (two pids can name one session across a resume; the most recently
// updated map wins), narrows to `only` when set, and keeps the sessions
// whose policy is not accept (3.6: a refusing session's waiting messages
// are listed too) or whose pending file holds entries (a hold→accept flip
// is not a release, and the entries stay until one). A map or pending
// file that cannot be read is skipped with one Warn, never a failure. The
// result is sorted by session id, so the output is stable.
func (inv Invocation) heldSessions(stateDir, only string) []heldSession {
	logger := inv.logger()
	store := sessionmap.Store{StateDir: stateDir}
	entries, err := os.ReadDir(store.ByPIDDir())
	if err != nil {
		return nil
	}
	byID := map[string]*sessionmap.ByPID{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		pid, perr := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if perr != nil || pid <= 0 {
			continue
		}
		m, rerr := store.ReadByPID(pid)
		if rerr != nil {
			logger.Warn("a session map was skipped", log.Err(rerr))
			continue
		}
		if only != "" && m.BrigadeSessionID != only {
			continue
		}
		if prev, ok := byID[m.BrigadeSessionID]; ok && !m.UpdatedAt.After(prev.UpdatedAt) {
			continue
		}
		byID[m.BrigadeSessionID] = m
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var out []heldSession
	for _, id := range ids {
		m := byID[id]
		pending, perr := inbound.FilePendingStore{Path: inbound.PendingPath(stateDir, id), SessionID: id}.Load()
		if perr != nil {
			logger.Warn("a pending file was skipped", log.Err(perr))
			pending = inbound.PendingFile{}
		}
		if m.Inbound == protocol.InboundAccept && len(pending.Entries) == 0 {
			continue
		}
		out = append(out, heldSession{m: m, pending: pending})
	}
	return out
}

// inboxMessage is one listed message in the terminal `--json` form.
type inboxMessage struct {
	MessageID       string    `json:"message_id"`
	SenderSessionID string    `json:"sender_session_id"`
	SenderName      string    `json:"sender_name"`
	SenderPrincipal string    `json:"sender_principal"`
	HumanLabel      string    `json:"human_label,omitzero"`
	Summary         string    `json:"summary,omitzero"`
	Body            string    `json:"body,omitzero"`
	ReceivedAt      time.Time `json:"received_at,omitzero"`
	ReleasedAt      time.Time `json:"released_at,omitzero"`
	// Recorded is false for a message the server holds that the pending
	// file does not know; OnServer is false for a pending entry whose
	// body the server no longer has.
	Recorded bool `json:"recorded"`
	OnServer bool `json:"on_server"`
}

// inboxSessionResult is one session of the terminal `--json` form.
type inboxSessionResult struct {
	SessionID    string         `json:"session_id"`
	SessionName  string         `json:"session_name"`
	TeamName     string         `json:"team_name"`
	Inbound      string         `json:"inbound"`
	Held         int            `json:"held"`
	DroppedTotal int            `json:"dropped_total"`
	Messages     []inboxMessage `json:"messages"`
	// ReceiveError is the code of a failed `message receive`, "" when it
	// succeeded; the pending entries are listed without bodies then.
	ReceiveError string `json:"receive_error,omitzero"`
}

// inboxListResult is the terminal `inbox --json` result.
type inboxListResult struct {
	Sessions []inboxSessionResult `json:"sessions"`
	Note     string               `json:"note"`
}

// sessionsStateDir is the state directory the sessions live under: ALWAYS
// the XDG default, as the hooks resolve it inside a session (E0-7: a
// BRIGADE_STATE_DIR must not be able to move the by-pid maps), so the
// terminal commands find the maps the hooks wrote whatever the shell's
// BRIGADE_STATE_DIR says.
func sessionsStateDir(environ []string) (string, error) {
	return adapterkit.StateDir(config.Strip(environ))
}

// inboxTerminal lists the held messages of every selected session, the
// bodies read through `message receive` and stored nowhere.
func inboxTerminal(inv Invocation, only string) error {
	stateDir, err := sessionsStateDir(inv.Environ)
	if err != nil {
		return err
	}
	sessions := inv.heldSessions(stateDir, only)
	now := inv.Deps.now()
	var results []inboxSessionResult
	var lines []string
	for _, hs := range sessions {
		r := inv.listSession(hs, stateDir)
		if r.Held == 0 && r.DroppedTotal == 0 {
			continue
		}
		results = append(results, r)
		lines = append(lines, renderSession(r, hs.pending, now)...)
	}
	if inv.JSON {
		if results == nil {
			results = []inboxSessionResult{}
		}
		return writeJSON(inv.Out, inboxListResult{Sessions: results, Note: InboxNote})
	}
	if len(lines) == 0 {
		return writeLines(inv.Out, NoHeldMessages)
	}
	return writeLines(inv.Out, lines...)
}

// listSession fetches one session's waiting messages through its own
// adapter and profile (from its map, exactly as sessionTarget builds a
// client) and reconciles them with its pending file. `message receive`
// acknowledges nothing and nothing is written to disk.
func (inv Invocation) listSession(hs heldSession, stateDir string) inboxSessionResult {
	m := hs.m
	r := inboxSessionResult{
		SessionID: sanitizeID(m.BrigadeSessionID), SessionName: protocol.SanitizeName(m.SessionName),
		TeamName: protocol.SanitizeName(m.TeamName), Inbound: protocol.SanitizeAttribute(m.Inbound),
		DroppedTotal: hs.pending.DroppedTotal, Messages: []inboxMessage{},
	}
	received, rerr := inv.receiveFor(m, stateDir)
	if rerr != nil {
		code, _ := codeAndReason(rerr)
		r.ReceiveError = string(code)
		inv.logger().Warn("message receive failed; bodies not shown", log.Err(rerr))
	}
	byID := make(map[string]*protocol.MessageEnvelope, len(received))
	for i := range received {
		byID[received[i].MessageID] = &received[i]
	}
	recorded := make(map[string]bool, len(hs.pending.Entries))
	for _, e := range hs.pending.Entries {
		recorded[e.MessageID] = true
		msg := inboxMessage{
			MessageID: sanitizeID(e.MessageID), SenderSessionID: sanitizeID(e.SenderSessionID),
			SenderName: protocol.SanitizeName(e.SenderName), SenderPrincipal: sanitizeID(e.SenderPrincipal),
			Summary: protocol.SanitizeSummary(e.Summary), ReceivedAt: e.ReceivedAt, ReleasedAt: e.ReleasedAt, Recorded: true,
		}
		if env, ok := byID[e.MessageID]; ok {
			msg.OnServer = true
			msg.HumanLabel = protocol.SanitizeLabel(env.Sender.HumanLabel)
			msg.Summary = protocol.SanitizeSummary(env.Summary)
			msg.Body = protocol.SanitizeBody(env.Body)
		}
		r.Messages = append(r.Messages, msg)
	}
	for i := range received {
		env := &received[i]
		if recorded[env.MessageID] {
			continue
		}
		r.Messages = append(r.Messages, inboxMessage{
			MessageID: sanitizeID(env.MessageID), SenderSessionID: sanitizeID(env.Sender.SessionID),
			SenderName: protocol.SanitizeName(env.Sender.SessionName), SenderPrincipal: sanitizeID(env.Sender.PrincipalRef),
			HumanLabel: protocol.SanitizeLabel(env.Sender.HumanLabel), Summary: protocol.SanitizeSummary(env.Summary),
			Body: protocol.SanitizeBody(env.Body), ReceivedAt: env.CreatedAt, OnServer: true,
		})
	}
	r.Held = len(r.Messages)
	return r
}

// receiveFor runs `message receive --limit 100` for the session a map
// describes, through that session's adapter and profile, after the cached
// describe (the protocol check).
func (inv Invocation) receiveFor(m *sessionmap.ByPID, stateDir string) ([]protocol.MessageEnvelope, error) {
	adapter, err := config.AdapterFromArgv(m.AdapterCommand)
	if err != nil {
		return nil, err
	}
	t := &target{session: m, profile: m.TeamKey, configDir: m.ConfigDir, stateDir: stateDir, adapter: adapter}
	t.client = inv.client(t)
	if _, err := t.probe(); err != nil {
		return nil, err
	}
	res, err := call(func(ctx context.Context) (*adapterclient.ReceiveResult, error) {
		return t.client.Receive(ctx, m.BrigadeSessionID, inboxReceiveLimit)
	})
	if err != nil {
		return nil, err
	}
	return res.Messages, nil
}

// renderSession renders one session's block of the human listing.
func renderSession(r inboxSessionResult, pending inbound.PendingFile, now time.Time) []string {
	lines := []string{columns(
		"session "+idLine(r.SessionID)+" \""+nameLine(r.SessionName)+"\"",
		"team \""+nameLine(r.TeamName)+"\"",
		"inbound="+enumLine(r.Inbound),
		strconv.Itoa(r.Held)+" held",
	)}
	if r.ReceiveError != "" {
		lines = append(lines, "  (the adapter could not be reached: "+enumLine(r.ReceiveError)+"; bodies are not shown)")
	}
	for _, msg := range r.Messages {
		age := "held " + ago(msg.ReceivedAt, now)
		switch {
		case !msg.Recorded:
			age = "held " + ago(msg.ReceivedAt, now) + " (not recorded locally)"
		case !msg.ReleasedAt.IsZero():
			age = "released " + ago(msg.ReleasedAt, now) + ", waiting for the watcher"
		}
		fields := []string{"  " + idLine(msg.MessageID), "from " + nameLine(msg.SenderName), "principal=" + idLine(msg.SenderPrincipal)}
		if msg.OnServer {
			fields = append(fields, labelLine(msg.HumanLabel))
		}
		lines = append(lines, columns(append(fields, age)...))
		if s := oneLine(msg.Summary); s != "" {
			lines = append(lines, "    summary: "+s)
		}
		switch {
		case !msg.OnServer:
			lines = append(lines, "    "+BodyGoneLine)
		case r.ReceiveError == "":
			for _, l := range strings.Split(msg.Body, "\n") {
				lines = append(lines, "    "+l)
			}
		}
	}
	if pending.DroppedTotal > 0 {
		lines = append(lines, "  ("+strconv.Itoa(pending.DroppedTotal)+" older held messages were not recorded locally; they are still on the server)")
	}
	return lines
}

// ago renders "<n>s ago" from the local clock; a zero time is "at an
// unknown time".
func ago(at, now time.Time) string {
	if at.IsZero() {
		return "at an unknown time"
	}
	secs := int(now.Sub(at).Seconds())
	if secs < 0 {
		secs = 0
	}
	return strconv.Itoa(secs) + "s ago"
}

// codeAndReason classifies an error for the listing.
func codeAndReason(err error) (protocol.Code, string) {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr.Code, perr.Details["reason"]
	}
	return protocol.CodeInternal, ""
}

// --- release --------------------------------------------------------------------

// inboxReleased is one session of the `inbox release --json` result.
type inboxReleased struct {
	SessionID      string   `json:"session_id"`
	MessageIDs     []string `json:"message_ids"`
	WatcherRunning bool     `json:"watcher_running"`
}

// inboxReleaseResult is the `inbox release --json` result.
type inboxReleaseResult struct {
	Released []inboxReleased `json:"released"`
	Note     string          `json:"note"`
}

// inboxRelease writes the release file of every selected session. The
// in-session refusal is the FIRST thing it does — before any flag beyond
// the verb is looked at, before any file is read: a session must not learn
// from the error which sessions hold messages. It is all-or-nothing: an
// id no selected session holds, or a selected session whose policy is
// refuse, fails before anything is written, so a typo cannot half-release.
func inboxRelease(inv Invocation, opts InboxOptions, ids []string) error {
	if config.InSession(inv.Environ) {
		return refuseReleaseInSession()
	}
	switch {
	case opts.All && len(ids) > 0:
		return usage("name message ids or --all, not both")
	case !opts.All && len(ids) == 0:
		return usage("name message ids or --all")
	}
	for _, id := range ids {
		if id == "" || strings.HasPrefix(id, "-") {
			return usage("a message id must be a bare word")
		}
	}
	stateDir, err := sessionsStateDir(inv.Environ)
	if err != nil {
		return err
	}
	sessions := inv.heldSessions(stateDir, opts.Session)
	plan, err := planRelease(sessions, opts.All, ids)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		if inv.JSON {
			return writeJSON(inv.Out, inboxReleaseResult{Released: []inboxReleased{}, Note: InboxReleaseNote})
		}
		return writeLines(inv.Out, NoHeldMessages)
	}
	now := inv.Deps.now()
	logger := inv.logger()
	var results []inboxReleased
	var lines []string
	for _, p := range plan {
		_, disregarded, werr := inbound.MergeRelease(inbound.ReleasePath(stateDir, p.sessionID), p.sessionID, p.ids, now)
		if werr != nil {
			return &protocol.Error{
				Code:    protocol.CodeConfig,
				Message: "the release file could not be written under the state directory",
				Details: map[string]string{"reason": "release_not_written"},
			}
		}
		if disregarded != "" {
			logger.Warn("an existing release file was replaced", slog.String("reason", disregarded))
		}
		running := inv.watcherRunning(stateDir, p.sessionID)
		results = append(results, inboxReleased{SessionID: sanitizeID(p.sessionID), MessageIDs: sanitizeIDs(p.ids), WatcherRunning: running})
		lines = append(lines, "released "+plural(len(p.ids), "message")+" for session "+idLine(p.sessionID)+"; the watcher delivers them within a few seconds")
		if !running {
			lines = append(lines, "(no watcher is running for that session; the messages are delivered when it next starts)")
		}
	}
	if inv.JSON {
		return writeJSON(inv.Out, inboxReleaseResult{Released: results, Note: InboxReleaseNote})
	}
	return writeLines(inv.Out, lines...)
}

// A releasePlan is the ids to release for one session.
type releasePlan struct {
	sessionID string
	ids       []string
}

// planRelease decides what each session releases, or fails with nothing
// written: `--all` takes every held id of every selected session (an id
// already released is included again harmlessly; a session with nothing
// held is skipped); explicit ids are looked up in the selected sessions'
// pending files, and an id held nowhere is `invalid_input`/not_held. A
// selected session whose policy is refuse is `config`/inbound_refuse (3.6):
// releasing into a session Claude Code will not deliver to would throw the
// message away and tell its sender it arrived.
func planRelease(sessions []heldSession, all bool, ids []string) ([]releasePlan, error) {
	var plan []releasePlan
	if all {
		for _, hs := range sessions {
			var held []string
			for _, e := range hs.pending.Entries {
				held = append(held, e.MessageID)
			}
			if len(held) == 0 {
				continue
			}
			if hs.m.Inbound == protocol.InboundRefuse {
				return nil, refuseRelease()
			}
			plan = append(plan, releasePlan{sessionID: hs.m.BrigadeSessionID, ids: held})
		}
		return plan, nil
	}
	byID := map[string]*heldSession{}
	for i := range sessions {
		for _, e := range sessions[i].pending.Entries {
			if _, dup := byID[e.MessageID]; !dup {
				byID[e.MessageID] = &sessions[i]
			}
		}
	}
	perSession := map[string][]string{}
	var order []string
	missing := 0
	for _, id := range ids {
		hs, ok := byID[id]
		if !ok {
			missing++
			continue
		}
		if hs.m.Inbound == protocol.InboundRefuse {
			return nil, refuseRelease()
		}
		sid := hs.m.BrigadeSessionID
		if _, seen := perSession[sid]; !seen {
			order = append(order, sid)
		}
		if !slices.Contains(perSession[sid], id) {
			perSession[sid] = append(perSession[sid], id)
		}
	}
	if missing > 0 {
		return nil, &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "some of the named message ids are not held by any selected session; nothing was released (run `brigade inbox` to see what is held)",
			Details: map[string]string{"reason": ReasonNotHeld, "count": strconv.Itoa(missing)},
		}
	}
	for _, sid := range order {
		plan = append(plan, releasePlan{sessionID: sid, ids: perSession[sid]})
	}
	return plan, nil
}

// refuseRelease is the `config` failure for a session under refuse. The
// by-pid map records the effective policy, not which settings file the
// session-start scan found the native value in, so the line names both
// sources the user can change.
func refuseRelease() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "that session refuses inbound messages (inbound=refuse): its team_inbound option is refuse, or Claude Code's crossSessionInbound setting is hold or refuse in a settings file (the session-start context line names the file); nothing was released",
		Details: map[string]string{"reason": ReasonInboundRefuse},
	}
}

// watcherRunning reports whether a live watcher pidfile names sessionID
// (the otherLiveWatcher precedent over ${stateDir}/watchers).
func (inv Invocation) watcherRunning(stateDir, sessionID string) bool {
	lookup := inv.Deps.Lookup
	if lookup == nil {
		lookup = procutil.Lookup
	}
	dir := filepath.Dir(pidfile.Path(stateDir, 1))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		v, cerr := pidfile.Check(filepath.Join(dir, name), lookup)
		if cerr == nil && v.Found && v.Alive && v.Entry.BrigadeSessionID == sessionID {
			return true
		}
	}
	return false
}

// sanitizeIDs sanitises a list of opaque ids for the --json form.
func sanitizeIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, sanitizeID(id))
	}
	return out
}

// plural is the notice-style count: "1 message", "2 messages".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
