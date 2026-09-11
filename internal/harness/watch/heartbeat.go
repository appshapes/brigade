package watch

import (
	"context"
	"log/slog"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/transcript"
	"github.com/appshapes/brigade/internal/protocol"
)

// heartbeat renews the lease with the current activity, name, inbound
// policy and — when the map names a transcript — the model and context
// facts read from it (4.4.4; capabilities session.model and
// session.context_used_tokens, which an adapter without simply ignores):
// a `heartbeat` command on the child's stdin when the adapter advertises
// message.watch.stdin_commands, else one `session heartbeat` call with
// the 3 s budget. The answer arrives as a heartbeat_ok event (or the
// call's result) and is logged; a rejected command comes back as an
// `error` event flagged retryable and the watch goes on. It runs on the
// session's command writer, never on the event loop, and never once the
// exit path has begun (U-21: the writer checks heartbeatsStopped first).
func (w *watcher) heartbeat(s *session) {
	if s.heartbeatsStopped.Load() {
		return
	}
	snap := w.state.snapshot()
	activity := snap.activity
	inbound := snap.inbound
	var name *string
	if snap.name != "" {
		n := snap.name
		name = &n
	}
	model, tokens := w.transcriptFacts(snap.transcriptPath)
	w.log.Debug("heartbeat",
		slog.String("activity", activity), slog.String("inbound", inbound),
		slog.Bool("named", name != nil), slog.Bool("model", model != nil), slog.Bool("context", tokens != nil),
		slog.Bool("stdin", s.stdinCommands))
	if s.stdinCommands {
		err := s.watch.Heartbeat(protocol.WatchCommand{
			Activity: &activity, SessionName: name, Inbound: &inbound, LeaseSeconds: s.lease,
			Model: model, ContextUsedTokens: tokens,
		})
		if err != nil {
			w.log.Warn("heartbeat command not written", adlog.Err(err))
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), adapterclient.WatchRequestTimeout)
	defer cancel()
	res, err := w.client.Heartbeat(ctx, w.sessionID, &protocol.HeartbeatRequest{
		Activity: &activity, SessionName: name, Inbound: &inbound, LeaseSeconds: s.lease,
		Model: model, ContextUsedTokens: tokens,
	})
	if err != nil {
		if code, details := errorParts(err); sessionGone(code, details) {
			w.log.Info("the session was closed under the watcher; re-opening", slog.String("code", string(code)))
			w.reopen(s)
			return
		}
		w.log.Warn("session heartbeat failed", adlog.Err(err))
		return
	}
	w.log.Debug("heartbeat_ok", slog.String("state", res.State))
}

// transcriptFacts refreshes the transcript reader for path right before a
// heartbeat and returns the two members the heartbeat carries: `model`
// when the transcript names one (sanitised and folded to one line), and
// `context_used_tokens` when usage has been seen and the count is one the
// wire carries — 0..MaxContextUsedTokens; the reader never yields a
// negative one and only a corrupt transcript sums past 2^53 - 1, and such
// a count is left absent rather than sent to be refused, which would fail
// the whole heartbeat, lease renewal included. An absent member means
// "unchanged" on the wire (4.4.4), which is also what a refresh failure
// yields — the reader keeps the last facts and the failure is a debug
// line of fixed text. An empty path (the map names no transcript) drops
// the reader and sends neither; a changed path (the hook rewrote the map
// on /clear) starts a fresh reader on the new file. The path is never
// logged (T10); the model value is, once per change.
func (w *watcher) transcriptFacts(path string) (model *string, tokens *int) {
	w.transcriptMu.Lock()
	defer w.transcriptMu.Unlock()
	if path == "" {
		w.transcript = nil
		return nil, nil
	}
	if w.transcript == nil || w.transcript.Path() != path {
		w.transcript = transcript.NewReader(path)
	}
	facts, err := w.transcript.Refresh()
	if err != nil {
		w.log.Debug("transcript not refreshed; the last facts stand", adlog.Err(err))
	}
	if m := oneLineModel(facts.Model); m != "" {
		if m != w.lastModel {
			w.log.Info("model updated from the transcript", slog.String("model", m))
			w.lastModel = m
		}
		model = &m
	}
	if n := facts.ContextUsedTokens; facts.HasContext && n >= 0 && n <= protocol.MaxContextUsedTokens {
		tokens = &n
	}
	return model, tokens
}
