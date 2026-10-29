// stop.go is the Stop hook of the manager digest (card 162,
// .context/plans/manager-digest.md §5.3): at the end of a turn, in a
// project that shares a state folder, the hook refreshes the session's
// entry and — when a condition it can see itself holds — asks the model
// once for its end-of-turn state, through the hook's `decision: block`
// answer, whose reason is the model's next instruction. Three gates, each
// its own sentence of the ask: the turn made a commit and no note
// followed (a note), the doing line is older than ten minutes after a
// turn that ran longer than a minute (a refreshed line), and anyone is
// subscribed to the project's digest after such a turn (a decision, if
// the model waits on one; card 167). The cost rules of ruling 8 hold whatever fires: at
// most one ask per ten minutes, never twice in a row, never on the Stop
// that follows an ask (stop_hook_active), never in a quiet session, never
// where the permission rules would make the call a dialog (the doing
// line's own gate), never with the `status` option off, never in a
// non-interactive session. Where a harness has no Stop hook (Muse Code,
// Codex CLI) the prompt hook prints the same ask as a context line at the
// next prompt, under the same gates and the same stamp.

package hook

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/state"
)

// The Stop hook's budgets and thresholds.
const (
	// stopBudget keeps the hook under the 5 s timeout hooks.json declares.
	stopBudget = promptBudget
	// stopAskInterval is the floor between two asks in one conversation.
	stopAskInterval = 10 * time.Minute
	// stopLongTurn is how long a turn must have run for the doing-line and
	// the decision gates to open.
	stopLongTurn = time.Minute
	// stopDoingStale is how old the last published doing line must be for
	// its gate to open; no line ever published counts as stale.
	stopDoingStale = 10 * time.Minute
	// stopMinBudget is what must remain of the budget before an ask is
	// stamped and written (the P5-18 shape, as the doing reminder's).
	stopMinBudget = time.Second
)

// The two verdicts a stop stamp records.
const (
	stopAsked = "asked"
	stopQuiet = "quiet"
)

// The three sentences of the ask, and its frame. Constants: no session
// text, nothing to sanitise, the bare `brigade` only (the F1 rule of
// startLine), and the closing says a refusal is final.
const (
	stopAskOpen     = "Brigade state (one ask, then finish): "
	stopAskNote     = "this turn made a commit and no note followed; record one line with `brigade note <<'EOF'`, the line, `EOF`, starting Done: (what shipped), Next: (what comes next) or Blocked: (what you wait on)."
	stopAskDoing    = "your roster line is older than ten minutes and this turn ran long; if the work changed, set it with `brigade doing <<'EOF'`, one short sentence, `EOF`."
	stopAskDecision = "if you are waiting on a person for a decision, record it with `brigade decision --to <their roster label> --options \"a | b\" <<'EOF'`, the question, `EOF`, so it reaches them."
	stopAskClose    = " No secrets, local paths or customer names. Subagents leave it alone. If a command is refused, do not run it another way; the work is done."
)

// stopStampPath is the Stop hook's ask stamp for a process:
// ${stateDir}/state/<pid>.stop-ask, `<RFC3339Nano> <claude_session_id>
// <asked|quiet>`: when Brigade last asked in that conversation, and
// whether the last Stop it judged asked.
func stopStampPath(stateDir string, pid int) string {
	return filepath.Join(stateDir, "state", strconv.Itoa(pid)+".stop-ask")
}

// A stopStamp is the parsed stamp: ours when it names the conversation,
// at when Brigade last asked (zero for never), lastAsked whether the
// previous judged Stop asked.
type stopStamp struct {
	ours      bool
	at        time.Time
	lastAsked bool
}

// readStopStamp reads the stamp under the strict private-file rules;
// every failure is the missing state.
func readStopStamp(path, sessionID string) stopStamp {
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		return stopStamp{}
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 || fields[1] != sessionID {
		return stopStamp{}
	}
	at, err := time.Parse(time.RFC3339Nano, fields[0])
	if err != nil {
		return stopStamp{}
	}
	return stopStamp{ours: true, at: at, lastAsked: fields[2] == stopAsked}
}

// writeStopStamp writes the stamp the retry stamp's way. False, with one
// debug line, when it cannot be written — the caller then asks nothing.
func (r *run) writeStopStamp(path string, at time.Time, sessionID, verdict string) bool {
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		r.log.Debug("stop: state directory not created; no ask", log.Err(err))
		return false
	}
	if err := adapterkit.WriteAtomic(path, []byte(at.UTC().Format(time.RFC3339Nano)+" "+sessionID+" "+verdict+"\n")); err != nil {
		r.log.Debug("stop: ask stamp not written; no ask", log.Err(err))
		return false
	}
	return true
}

// stop is `brigade hook stop` on Claude Code (card 162): the entry's
// git facts refreshed, then the ask when its gates hold. Exit 0 whatever
// happens; the one stdout document is the block answer.
func (r *run) stop() int {
	in, err := r.readInput()
	if err != nil {
		r.log.Warn("stop: bad stdin", log.Err(err))
		return 0
	}
	f, err := r.facts()
	if err != nil {
		r.log.Warn("stop: session facts", log.Err(err))
		return 0
	}
	ctx, cancel := r.hookContext(stopBudget)
	defer cancel()
	m, err := sessionmap.Store{StateDir: f.stateDir}.ReadByPID(f.pid)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.log.Warn("stop: the session map cannot be trusted; nothing done", log.Err(err))
		}
		return 0
	}
	if in.PermissionMode != "" {
		m.PermissionMode = in.PermissionMode
	}
	e := r.stateStop(ctx, f, m, r.deps.Now())
	if reason := r.stopAsk(ctx, f, in, m, e); reason != "" {
		r.emitBlock(reason)
	}
	return 0
}

// emitBlock writes the Stop hook's block answer: the one JSON document on
// stdout, which Claude Code reads as the reason to continue.
func (r *run) emitBlock(reason string) {
	if r.streams.Out == nil {
		return
	}
	b, err := marshalBlock(reason)
	if err != nil {
		return
	}
	_, _ = io.WriteString(r.streams.Out, string(b)+"\n")
}

// stopAsk judges the gates and answers the ask, or "". It reads the
// options for `status`, the stamp for the cadence, the entry for the
// facts and reports.json for the subscribers; it writes the stamp on every
// judged Stop — asked or quiet — so "never twice in a row" has a record
// to read, and only when it could be written is an ask answered.
func (r *run) stopAsk(ctx context.Context, f facts, in input, m *sessionmap.ByPID, e *state.Entry) string {
	if e == nil || in.stopActive() || in.SessionID == "" || m.ClaudeSessionID != in.SessionID || m.NonInteractive || f.entrypoint == entrypointSDK {
		return ""
	}
	if m.InboundRequest != "" {
		return "" // a quiet session (the quiet skill's hold or refuse)
	}
	if !doingModePrints(m.DoingMode) || !doingLineEligible(m.DoingMode, m.PermissionMode) {
		return ""
	}
	opts, err := r.options()
	if err != nil || !opts.Status {
		return ""
	}
	now := r.deps.Now()
	path := stopStampPath(f.stateDir, f.pid)
	stamp := readStopStamp(path, in.SessionID)
	if stamp.ours && (stamp.lastAsked || now.Sub(stamp.at) < stopAskInterval) {
		r.writeStopStamp(path, stamp.at, in.SessionID, stopQuiet)
		return ""
	}
	sentences := stopSentences(e, m, stamp, now)
	if len(sentences) == 0 {
		r.writeStopStamp(path, stamp.at, in.SessionID, stopQuiet)
		return ""
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < stopMinBudget {
		r.log.Debug("stop: too little budget left for the ask")
		return ""
	}
	if !r.writeStopStamp(path, now, in.SessionID, stopAsked) {
		return ""
	}
	r.log.Info("stop: state asked", slog.Int("sentences", len(sentences)))
	return stopAskOpen + strings.Join(sentences, " ") + stopAskClose
}

// stopSentences is the three gates, each its sentence when it holds: a
// commit newer than the last ask with no note after it; after a turn
// longer than stopLongTurn, a doing line older than stopDoingStale (or
// none ever) and, with anyone subscribed to the project's digest, the
// decision.
func stopSentences(e *state.Entry, m *sessionmap.ByPID, stamp stopStamp, now time.Time) []string {
	var out []string
	if len(e.Commits) > 0 {
		newest := e.Commits[0].At
		var lastNote time.Time
		if n := len(e.Notes); n > 0 {
			lastNote = e.Notes[n-1].At
		}
		if newest.After(lastNote) && newest.After(stamp.at) {
			out = append(out, stopAskNote)
		}
	}
	longTurn := !e.LastPromptAt.IsZero() && now.Sub(e.LastPromptAt) >= stopLongTurn
	if longTurn {
		var lastDoing time.Time
		if n := len(e.Doing); n > 0 {
			lastDoing = e.Doing[n-1].At
		}
		if lastDoing.IsZero() || now.Sub(lastDoing) >= stopDoingStale {
			out = append(out, stopAskDoing)
		}
		if reports, err := state.ReadReports(m.StateFolder); err == nil && len(reports.Subscribers) > 0 {
			out = append(out, stopAskDecision)
		}
	}
	return out
}

// stopActive reports the document's stop_hook_active in either spelling
// (Claude Code's snake case; Grok's camel case, merged by decodeGrok).
func (in input) stopActive() bool { return in.StopHookActive || in.StopHookActiveSnake }

// stateStop refreshes the entry's git facts at the end of a turn — the
// commit the turn made is what the note gate reads — and answers the
// entry, or nil for a session that shares no state or has no entry.
func (r *run) stateStop(ctx context.Context, f facts, m *sessionmap.ByPID, now time.Time) *state.Entry {
	s, ok := stateStore(f, m)
	if !ok {
		return nil
	}
	e, err := s.Load(m.BrigadeSessionID)
	if err != nil {
		return nil
	}
	e.Inbound = m.Inbound
	r.stateRefreshGit(ctx, m, e, stateGitBudgetPrompt)
	e.UpdatedAt = now
	r.stateSave(s, e)
	return e
}

// promptStateAsk is the prompt hook's half of the parity rule: on a host
// with no Stop hook (Muse Code, Codex CLI; Claude Code and Grok Build run
// one) the same ask, under the same gates and stamp, is printed as a
// context line at the next prompt.
func (r *run) promptStateAsk(ctx context.Context, f facts, in input, m *sessionmap.ByPID, e *state.Entry) {
	if hostHasStop(f.host) || e == nil {
		return
	}
	reason := r.stopAsk(ctx, f, in, m, e)
	if reason == "" || !r.fits(reason) {
		return
	}
	r.say(reason)
}

// hostHasStop reports whether the host runs a Stop hook of its own, so
// the prompt hook need not carry the ask.
func hostHasStop(h config.Host) bool { return h == config.HostClaude || h == config.HostGrok }

// marshalBlock is the block answer's JSON: {"decision":"block","reason":…}.
func marshalBlock(reason string) ([]byte, error) {
	return json.Marshal(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{"block", reason})
}
