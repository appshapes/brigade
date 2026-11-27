package hook

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"hash/fnv"
	"io"
	"slices"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// injectSteps is Antigravity CLI's dialect (config.DialectInjectSteps;
// work item 532, ADR 0039). agy hands a hook a camelCase document and takes
// one JSON document back:
//
//   - SessionStart and PreInvocation may answer {"injectSteps": [...]}, steps
//     the hook adds to the conversation before the model is called. What the
//     shared start and prompt logic says is one such step, and the team
//     messages another, both user messages: on the other harnesses both
//     ride on the user's turn, a teammate's message is never put where the
//     model reads the system's own words, and Brigade's own lines can carry
//     a name a teammate chose.
//   - PreInvocation runs before EVERY model call. The first call of a turn
//     (invocationNum 0) is the prompt hook's moment; at a later one only the
//     messages that arrived since are handed over.
//   - Stop may answer {"decision": "continue", "reason": ...}, which goes on
//     with the reason as a system message: so it carries one fixed line,
//     that a message arrived while the turn ran, and nothing a teammate
//     wrote. The message itself is handed over by the next PreInvocation.
//
// Messages wait in the session's sink (config.DeliverNextCall), which the
// watcher writes, and are taken from it once (inbound.DrainNew). There is
// no session-end hook: the watcher ends the session when the agy process,
// kept for liveness, is gone. The session is keyed by its conversation, as
// a Codex thread is: one agy process can hold one conversation after
// another (/new), and `agy --conversation <id>` takes one up again in a new
// process.
type injectSteps struct{ plugin }

func (injectSteps) native() bool { return true }

func (injectSteps) run(r *run) int { return r.runAntigravity() }

func (injectSteps) decode(_ *run, data []byte, in *input) error {
	var d struct {
		ConversationID     string   `json:"conversationId"`
		HookEventName      string   `json:"hookEventName"`
		ModelName          string   `json:"modelName"`
		ContinuationActive bool     `json:"continuationActive"`
		TerminationReason  string   `json:"terminationReason"`
		Error              string   `json:"error"`
		WorkspacePaths     []string `json:"workspacePaths"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return errors.New("invalid Antigravity hook document")
	}
	in.SessionID = d.ConversationID
	in.HookEventName = d.HookEventName
	in.Model = d.ModelName
	// agy's transcript is not Claude Code's: the watcher must not read it
	// as one. Work item 534 reads it.
	in.TranscriptPath = ""
	in.StopHookActive = d.ContinuationActive
	in.StopReason, in.StopError = d.TerminationReason, d.Error
	// The hooks page lists workspacePaths among the common fields and no
	// cwd (1.5.18 sent both): the first workspace stands in for it.
	if in.Cwd == "" && len(d.WorkspacePaths) > 0 {
		in.Cwd = d.WorkspacePaths[0]
	}
	return nil
}

// adopt: agy gives its hooks the conversation's id under the marker. When
// the environment lacks it, the document's id is adopted, as Codex's is;
// when the two disagree, the hook serves nothing.
func (injectSteps) adopt(r *run, in input) error {
	variable := r.spokenHost().NativeIDVar()
	have := adapterkit.Getenv(r.environ, variable)
	if have != "" && in.SessionID != "" && have != in.SessionID {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the Antigravity hook document names another conversation than its environment",
			Details: map[string]string{"reason": config.ReasonInvalidNativeID, "variable": variable},
		}
	}
	if have != "" {
		return nil
	}
	if err := sessionmap.CheckNativeID(in.SessionID); err != nil {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the Antigravity hook document carries no usable conversation id",
			Details: map[string]string{"reason": config.ReasonInvalidNativeID, "variable": variable},
		}
	}
	r.environ = append(slices.Clone(r.environ), variable+"="+in.SessionID)
	return nil
}

// key: the conversation is the session; the agy process, each hook's
// parent (measured on 1.5.18), is kept for liveness.
func (injectSteps) key(nativeID string, parent int) (pid, hostProcess int) {
	return conversationKey(nativeID), parent
}

// conversationKey is the session's key from its conversation's id: a
// number above every real pid, as codexKey is, in a range of its own, so a
// Codex thread and an agy conversation never share a map.
func conversationKey(conversation string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(conversation))
	return 1<<30 | 1<<29 | int(h.Sum32()&(1<<29-1))
}

// competingRefusal is the line a start says when the session's environment
// also carries another host's session marker: agy was started from that
// host's shell, so which session this is cannot be told, and Brigade does
// nothing here.
const competingRefusal = "Brigade: this Antigravity session's environment also carries another harness's session marker, so its identity cannot be proven and Brigade is off here. Start agy from a plain terminal."

// runAntigravity runs one agy hook and prints its one JSON answer, "{}"
// when it has nothing to say. Every failure is that empty answer: a hook
// never stops agy's turn. Waiting messages are taken only when the hook's
// document is this session's, and the drain's cursor moves past them only
// once the answer that carries them is written.
func (r *run) runAntigravity() int {
	out := r.streams.Out
	answer := []byte("{}")
	var batch *inbound.Batch
	defer func() {
		if batch != nil {
			batch.Release()
		}
	}()
	if _, err := config.NativeID(r.spokenHost(), r.environ); err != nil {
		if _, reason := codeOf(err); reason == config.ReasonAmbiguousHost {
			r.log.Warn("Antigravity hook refused: competing host markers")
			if r.args.sub == SubSessionStart {
				answer = injectAnswer(competingRefusal, "")
			}
			r.streams.In = nil
		}
	}
	if r.streams.In != nil {
		answer, batch = r.antigravityAnswer(out)
	}
	if out == nil {
		return 0
	}
	if _, err := out.Write(append(answer, '\n')); err != nil {
		r.log.Warn("Antigravity hook: the answer could not be written; the messages wait", adlog.Err(err))
		return 0
	}
	if batch != nil {
		if err := batch.Commit(); err != nil {
			r.log.Warn("Antigravity hook: the inbox cursor could not be moved", adlog.Err(err))
		}
		batch = nil
	}
	return 0
}

// antigravityAnswer runs the hook's logic with its output captured and is
// the answer, with the batch of messages it carries, nil for none.
func (r *run) antigravityAnswer(out io.Writer) ([]byte, *inbound.Batch) {
	data, err := io.ReadAll(io.LimitReader(r.streams.In, maxStdinBytes+1))
	if err != nil || len(data) > maxStdinBytes {
		r.log.Warn("Antigravity hook: stdin could not be read")
		return []byte("{}"), nil
	}
	var probe struct {
		InvocationNum *int `json:"invocationNum"`
	}
	_ = json.Unmarshal(data, &probe)
	again := func() { r.streams.In = bytes.NewReader(data) }
	var text bytes.Buffer
	r.streams.Out = &text
	defer func() { r.streams.Out = out }()
	// valid reports whether the document is one this session's hook may act
	// on: read again, as the shared logic reads it, with no refusal.
	valid := func() bool {
		again()
		_, err := r.readInput()
		return err == nil
	}
	again()
	switch r.args.sub {
	case SubSessionStart:
		r.sessionStart()
	case SubPrompt:
		if probe.InvocationNum == nil || *probe.InvocationNum == 0 {
			r.prompt()
		}
	case SubStop:
		return r.antigravityStop(), nil
	default:
		// agy has no session-end hook; a hooks.json that names one gets
		// the shared end and the empty answer.
		r.sessionEnd()
		return []byte("{}"), nil
	}
	if !valid() {
		r.log.Warn("Antigravity hook: the document is not this session's; no message is taken")
		return injectAnswer(text.String(), ""), nil
	}
	batch := r.takeNextCall()
	if batch == nil {
		return injectAnswer(text.String(), ""), nil
	}
	return injectAnswer(text.String(), batch.Text), batch
}

// takeNextCall is the batch of messages that wait in this session's sink,
// under the drain's lock; nil when there are none, the session is not
// registered, or the sink cannot be read.
func (r *run) takeNextCall() *inbound.Batch {
	stateDir, session := r.sinkSession()
	if session == "" {
		return nil
	}
	batch, err := inbound.TakeNew(stateDir, session)
	if err != nil {
		r.log.Warn("Antigravity hook: the inbox sink could not be read", adlog.Err(err))
		return nil
	}
	if batch.Text == "" {
		// Lines that are no record: move past them now, or the Stop hook
		// would find them new at every turn's end.
		if batch.Moved() {
			if err := batch.Commit(); err != nil {
				r.log.Warn("Antigravity hook: the inbox cursor could not be moved", adlog.Err(err))
			}
		} else {
			batch.Release()
		}
		return nil
	}
	return batch
}

// sinkSession is the state directory and the Brigade session whose sink
// this session's messages wait in, "" for a session that is not
// registered.
func (r *run) sinkSession() (string, string) {
	f, err := r.facts()
	if err != nil {
		return "", ""
	}
	m, err := sessionmap.Store{StateDir: f.stateDir}.ReadByPID(f.pid)
	if err != nil || m.BrigadeSessionID == "" {
		return "", ""
	}
	return f.stateDir, m.BrigadeSessionID
}

// messagesWaitLine is what a Stop answer says of messages that arrived
// while the turn ran: a system message carries Brigade's own words only,
// so the messages follow at the model call the answer starts.
const messagesWaitLine = "Brigade: a team message arrived during this turn. It follows now, as a message your user did not type."

// endedByItself are agy's words for a turn that ended because the model
// was done (`terminationReason`): NO_TOOL_CALL measured on 1.5.18 (work
// item 531), model_stop the hooks page's example. After any other end (an
// error, max_steps_exceeded, a cancel) a Stop does not make the turn go on.
var endedByItself = map[string]bool{"no_tool_call": true, "model_stop": true}

// antigravityStop is the Stop answer: one fixed line that says a message
// arrived while the turn ran, as a "continue" whose reason agy hands the
// model as a system message; "{}" otherwise. Only after a turn that ended
// by itself with no error, and not on one a Stop answer already continued:
// that turn's model calls hand the message over. The shared end-of-turn
// ask is not said here: agy's documents carry no permission mode, which
// its gate wants, and a Stop reason is a system message. The entry's git
// facts are refreshed as at any turn's end.
func (r *run) antigravityStop() []byte {
	in, err := r.readInput()
	if err != nil {
		r.log.Warn("Antigravity Stop skipped", adlog.Err(err))
		return []byte("{}")
	}
	f, err := r.facts()
	if err != nil {
		return []byte("{}")
	}
	m, err := sessionmap.Store{StateDir: f.stateDir}.ReadByPID(f.pid)
	if err != nil {
		return []byte("{}")
	}
	ctx, cancel := r.hookContext(r.deps.StopBudget)
	defer cancel()
	r.stateStop(ctx, f, m, r.deps.Now())
	if in.stopActive() || in.StopError != "" || !endedByItself[strings.ToLower(in.StopReason)] {
		return []byte("{}")
	}
	stateDir, session := r.sinkSession()
	if session == "" || !inbound.HasNew(stateDir, session) {
		return []byte("{}")
	}
	b, err := json.Marshal(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{"continue", messagesWaitLine})
	if err != nil {
		return []byte("{}")
	}
	return b
}

// injectAnswer is the answer that adds Brigade's own text and then the
// team messages to the conversation, each as a user message and only when
// there is one; "{}" for neither. Nothing a hook adds is a system message:
// on the other harnesses Brigade's lines ride on the user's turn, and some
// of them carry a name a teammate chose (work item 532, the owner's review).
func injectAnswer(own, messages string) []byte {
	type step struct {
		UserMessage string `json:"userMessage"`
	}
	var steps []step
	if own = strings.TrimSpace(own); own != "" {
		steps = append(steps, step{UserMessage: own})
	}
	if messages = strings.TrimSpace(messages); messages != "" {
		steps = append(steps, step{UserMessage: messages})
	}
	if len(steps) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(struct {
		InjectSteps []step `json:"injectSteps"`
	}{steps})
	if err != nil {
		return []byte("{}")
	}
	return b
}
