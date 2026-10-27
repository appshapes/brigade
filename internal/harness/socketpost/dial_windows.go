//go:build windows

package socketpost

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// dial opens the named pipe as a client, write-only, which is how the
// probe of E11 posted and was delivered. A pipe whose every instance is
// busy answers ERROR_PIPE_BUSY; the open is retried until the dial
// timeout, as WaitNamedPipe would, and the timeout is reported as a
// dial timeout like a unix dialer's.
func dial(ctx context.Context, path string, timeout time.Duration) (conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(path, os.O_WRONLY, 0) //nolint:gosec // G304: the session's own pipe, named by its environment or the registry
		if err == nil {
			return &pipeConn{f: f}, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, dialTimeout{}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// dialTimeout is a net.Error whose Timeout is true, so classifyDial
// reports it as the unix dialer's own timeout.
type dialTimeout struct{}

func (dialTimeout) Error() string   { return "socketpost: the pipe stayed busy past the dial timeout" }
func (dialTimeout) Timeout() bool   { return true }
func (dialTimeout) Temporary() bool { return true }

// pipeConn is the open pipe handle with the write deadline Post sets.
// Go's os.File supports a deadline on a pipe handle only when it was
// opened for overlapped I/O, which os.OpenFile does not do, so when
// SetWriteDeadline is refused the bound is kept another way: a timer
// closes the handle, which fails the pending write, and the write then
// reports os.ErrDeadlineExceeded as a deadline would.
type pipeConn struct {
	f        *os.File
	deadline time.Time
	native   bool
	mu       sync.Mutex
	closed   bool
	timedOut bool
}

func (c *pipeConn) SetWriteDeadline(t time.Time) error {
	if err := c.f.SetWriteDeadline(t); err == nil {
		c.native = true
		return nil
	} else if !errors.Is(err, os.ErrNoDeadline) {
		return err
	}
	c.deadline = t
	return nil
}

func (c *pipeConn) Write(p []byte) (int, error) {
	if c.native || c.deadline.IsZero() {
		return c.f.Write(p)
	}
	timer := time.AfterFunc(time.Until(c.deadline), func() {
		c.mu.Lock()
		c.timedOut = true
		c.mu.Unlock()
		_ = c.Close()
	})
	n, err := c.f.Write(p)
	timer.Stop()
	c.mu.Lock()
	timedOut := c.timedOut
	c.mu.Unlock()
	if err != nil && timedOut {
		return n, os.ErrDeadlineExceeded
	}
	return n, err
}

func (c *pipeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.f.Close()
}

// isBrokenPipe: the server end is gone (ERROR_BROKEN_PIPE) or closing
// (ERROR_NO_DATA), the two shapes of EPIPE on a named pipe.
func isBrokenPipe(err error) bool {
	return errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA)
}

// isReset: a pipe that was disconnected underneath the client.
func isReset(err error) bool { return errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) }

// isTransportError reports whether a write failed on the handle (a
// *fs.PathError from the file) rather than in the encoder.
func isTransportError(err error) bool {
	var pathErr *fs.PathError
	return errors.As(err, &pathErr)
}
