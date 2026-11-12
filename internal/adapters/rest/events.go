package rest

import (
	"log/slog"

	"github.com/appshapes/brigade/internal/protocol"
)

// The watch's event helpers (4.4.9): every event of one watch goes through
// ONE protocol.LineWriter (C-39), a fatal failure is ONE `error` event
// with `retryable: false` and the code's exit status, never a 4.3
// envelope, a missing --session included (C-37's positive control), and a
// rejected stdin command is an `error` event with `retryable: true` after
// which the watch continues.

// watchFatal emits the ONE `error` event a fatal failure produces, with
// `retryable` present and false, and returns the failure's 4.6 status.
func (c *command) watchFatal(events *protocol.LineWriter, err error) int {
	perr := asProtocolError(err)
	object := perr.Object()
	object.Retryable = false
	_ = events.WriteLine(&protocol.WatchError{Event: protocol.EventError, Error: *object})
	c.log.Debug("watch ended", slog.String("code", string(perr.Code)))
	return perr.Code.Exit()
}

// watchRetryable reports a failure the watch survives. 4.4.9 keys the
// watch's survival on the flag, not on the code: `retryable: true` means
// informational, and the loop goes on.
func (c *command) watchRetryable(events *protocol.LineWriter, err error) {
	perr := asProtocolError(err)
	object := perr.Object()
	object.Retryable = true
	_ = events.WriteLine(&protocol.WatchError{Event: protocol.EventError, Error: *object})
	c.log.Debug("watch continues after a failure", slog.String("code", string(perr.Code)))
}
