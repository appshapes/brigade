package watch

import (
	"context"
	"log/slog"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/protocol"
)

// heartbeat renews the lease with the current activity, name and inbound
// policy (6.6): a `heartbeat` command on the child's stdin when the
// adapter advertises message.watch.stdin_commands, else one `session
// heartbeat` call with the 3 s budget. The answer arrives as a
// heartbeat_ok event (or the call's result) and is logged; a rejected
// command comes back as an `error` event flagged retryable and the watch
// goes on. It runs on the session's command writer, never on the event
// loop, and never once the exit path has begun (U-21: the writer checks
// heartbeatsStopped first).
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
	w.log.Debug("heartbeat",
		slog.String("activity", activity), slog.String("inbound", inbound),
		slog.Bool("named", name != nil), slog.Bool("stdin", s.stdinCommands))
	if s.stdinCommands {
		err := s.watch.Heartbeat(protocol.WatchCommand{
			Activity: &activity, SessionName: name, Inbound: &inbound, LeaseSeconds: s.lease,
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
	})
	if err != nil {
		w.log.Warn("session heartbeat failed", adlog.Err(err))
		return
	}
	w.log.Debug("heartbeat_ok", slog.String("state", res.State))
}
