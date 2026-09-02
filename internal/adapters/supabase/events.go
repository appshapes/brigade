package supabase

import (
	"errors"
	"log/slog"

	"github.com/appshapes/brigade/internal/protocol"
)

// The watch's event helpers (4.4.9), shared by the watch lane and by the
// pre-watch stub: every event of one watch goes through ONE
// protocol.LineWriter (C-39), a pre-catch-up failure is ONE `error` event
// with `retryable: false` and the code's exit status — never a 4.3
// envelope, a missing --session included (C-37's positive control) — and
// a rejected stdin command is an `error` event with `retryable: true`
// after which the watch continues (BAP/1.x (a)).

// watchFatal emits the ONE `error` event a fatal failure produces —
// `retryable` present and false — and returns the failure's 4.6 status.
func (c *command) watchFatal(events *protocol.LineWriter, err error) int {
	perr := asProtocolError(err)
	object := perr.Object()
	object.Retryable = false
	_ = events.WriteLine(&protocol.WatchError{Event: protocol.EventError, Error: *object})
	c.log.Debug("watch ended", slog.String("code", string(perr.Code)))
	return perr.Code.Exit()
}

// watchRetryable reports a non-fatal command failure. 4.4.9 keys the
// watch's survival on the flag, not on the code: `retryable: true` means
// informational, and the loop goes on.
func (c *command) watchRetryable(events *protocol.LineWriter, err error) int {
	perr := asProtocolError(err)
	object := perr.Object()
	object.Retryable = true
	_ = events.WriteLine(&protocol.WatchError{Event: protocol.EventError, Error: *object})
	c.log.Debug("watch command refused", slog.String("code", string(perr.Code)))
	return protocol.ExitOK
}

// emit writes one event; a write failure ends the watch, because a watch
// whose stdout is gone has nothing left to say.
func (c *command) emit(events *protocol.LineWriter, event any) (int, bool) {
	if err := events.WriteLine(event); err != nil {
		c.log.Debug("watch stdout write failed")
		return protocol.CodeInternal.Exit(), true
	}
	return protocol.ExitOK, false
}

// asProtocolError is the *protocol.Error in err's chain, or `internal`
// with the fixed message: an unclassified error can carry raw server text
// and never reaches stdout.
func asProtocolError(err error) *protocol.Error {
	var typed *protocol.Error
	if errors.As(err, &typed) && typed != nil {
		return typed
	}
	return &protocol.Error{Code: protocol.CodeInternal, Message: "internal error"}
}
