package watch

import (
	"context"
	"os"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/socketpost"
)

// A delivery is how a frame is put in front of one host's model (work item
// 381): one implementation for each way a host takes one, in a file of
// its own, chosen from the host table by its kind (config.Host.Delivery).
// The injector, the liveness check and the watcher's start and end ask it
// and name no host.
type delivery interface {
	// bind says, at the watcher's start, whether the session's map and
	// the sink the watcher was given are ones this delivery may serve.
	bind(rc *runConfig, m *sessionmap.ByPID, d Deps) error
	// ready refuses one injection when the receiver is no longer the one
	// this watcher serves; nil to go on.
	ready(w *watcher) error
	// openSink opens the sink file for one append, as its reader needs it.
	openSink(path string) (*os.File, error)
	// direct delivers one item where the watcher has no sink file.
	direct(ctx context.Context, in *injector, item inbound.Item) error
	// alive is this delivery's own part of the liveness check: a stop
	// reason, or "" to go on to the shared checks.
	alive(w *watcher) string
	// retire runs as the watcher ends, for what only this delivery left.
	retire(w *watcher)
}

// deliveries holds the implementations, each under the kind the table
// names it by. A kind that is not here is the socket: what Claude Code
// has, and what a session of a host the table does not hold is given.
var deliveries = map[config.DeliveryKind]delivery{
	config.DeliverSocket:       socket{},
	config.DeliverFollowedSink: followedSink{},
	config.DeliverQueue:        queue{},
	config.DeliverPagedSink:    pagedSink{},
	config.DeliverNextCall:     followedSink{},
}

// deliveryOf is the delivery of a host by its registration's word.
func deliveryOf(harness string) delivery {
	if d := deliveries[config.Host(harness).Delivery()]; d != nil {
		return d
	}
	return socket{}
}

// delivery is the watcher's own: its session's host's.
func (w *watcher) delivery() delivery { return deliveryOf(w.harness) }

// socket is Claude Code's delivery (config.DeliverSocket): a post on the
// inbox socket the session's process owns (6.6). It is also the plain
// form the others start from: nothing to bind, nothing to check before an
// injection, a sink file that is appended to, nothing of its own to watch
// or to leave behind.
type socket struct{}

func (socket) bind(*runConfig, *sessionmap.ByPID, Deps) error { return nil }
func (socket) ready(*watcher) error                           { return nil }
func (socket) alive(*watcher) string                          { return "" }
func (socket) retire(*watcher)                                {}

// openSink opens the sink for an append and nothing else: the watcher is
// its one writer, and the file's reader follows it as it grows.
func (socket) openSink(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // 0600; the path is the test harness's argv flag
}

func (socket) direct(ctx context.Context, in *injector, item inbound.Item) error {
	target := in.w.state.snapshot().target
	if target.Path == "" {
		return &socketpost.Error{Kind: socketpost.KindPrecheck, Reason: socketpost.ReasonEmpty}
	}
	opts := in.w.deps.PostOptions
	if opts.Logger == nil {
		opts.Logger = in.w.log
	}
	return in.w.deps.Post(ctx, target, item.Content, opts)
}
