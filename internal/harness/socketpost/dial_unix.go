//go:build darwin || linux

package socketpost

import (
	"context"
	"errors"
	"net"
	"syscall"
	"time"
)

// dial connects to the unix socket with net.Dialer{Timeout}.DialContext.
func dial(ctx context.Context, path string, timeout time.Duration) (conn, error) {
	dialer := net.Dialer{Timeout: timeout}
	return dialer.DialContext(ctx, Network, path)
}

// isBrokenPipe and isReset name the two write failures the classifier
// reports by name (EPIPE, ECONNRESET).
func isBrokenPipe(err error) bool { return errors.Is(err, syscall.EPIPE) }

func isReset(err error) bool { return errors.Is(err, syscall.ECONNRESET) }

// isTransportError reports whether a write failed in the transport (a
// *net.OpError) rather than in the encoder.
func isTransportError(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr)
}
