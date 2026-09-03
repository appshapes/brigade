// Package fakesock is the fake inbox socket of plan 9.5: a unix-domain
// server standing in for a Claude Code session's
// `CLAUDE_CODE_MESSAGING_SOCKET`, so the socket poster (6.7, U-17, U-19,
// U-20) and later the watcher can be tested against a real socket with
// no Claude process anywhere near.
//
// It records, per accepted connection, every physical line it read: the
// auth lines separately from the user frames, plus the lines that were
// neither (A.2 says the real harness silently drops those, so a test can
// prove nothing malformed was ever sent). It can Stall — accept and never
// read, so a write deadline fires — CloseAtOnce, or Abandon its listener
// while leaving the socket file in place so a dial gets ECONNREFUSED.
//
// The socket lives under os.MkdirTemp("/tmp", "bsk"), never t.TempDir():
// macOS caps sun_path at 103 bytes and a t.TempDir path is already about
// 91 of them (7.3). The socket file is chmodded to 0600 like the real
// one. Everything is torn down in a Cleanup: held and live connections
// are closed first, otherwise the reader wait would hang the package.
package fakesock

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// SocketName is the file name inside the server's private directory.
const SocketName = "inbox.sock"

// A Server is one fake inbox socket.
type Server struct {
	tb   testing.TB
	dir  string
	path string
	ln   *net.UnixListener

	mu          sync.Mutex
	stall       bool
	closeAtOnce bool
	abandoned   bool
	closed      bool
	accepted    int
	records     []*record
	held        []net.Conn
	live        map[net.Conn]struct{}

	wg sync.WaitGroup
}

// record is the mutable per-connection state behind a Connection.
type record struct {
	auth    []string
	frames  []string
	invalid []string
	lines   int
	held    bool
	done    bool
}

// A Connection is the snapshot of one accepted connection.
type Connection struct {
	// Auth holds the token of every valid auth line, in order.
	Auth []string
	// Frames holds the content of every valid user line, in order.
	Frames []string
	// Invalid holds every line that was neither, verbatim (an over-long
	// line is recorded as OverlongLine).
	Invalid []string
	// Lines is how many physical lines were read, valid or not.
	Lines int
	// Held reports a connection accepted while stalled: never read.
	Held bool
	// Done reports that the peer closed and the reader finished.
	Done bool
}

// OverlongLine is recorded in Invalid for a line the reader dropped as
// longer than protocol.MaxLineBytes.
const OverlongLine = "<line dropped: over 1 MiB>"

// wireLine is the loose shape of both A.2 lines.
type wireLine struct {
	Type    string `json:"type"`
	Token   string `json:"token"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

// New starts a server and registers its teardown with tb.Cleanup.
func New(tb testing.TB) *Server {
	tb.Helper()
	// Not tb.TempDir(): unix sockets must live under /tmp, because macOS
	// caps sun_path at 103 bytes and a tb.TempDir() path is already about
	// 91 of them (plan 7.3, verified A.7).
	dir, err := os.MkdirTemp("/tmp", "bsk") //nolint:usetesting // sun_path length, see above
	if err != nil {
		tb.Fatalf("fakesock: mkdirtemp: %v", err)
	}
	path := filepath.Join(dir, SocketName)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		_ = os.RemoveAll(dir)
		tb.Fatalf("fakesock: listen %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
		tb.Fatalf("fakesock: chmod: %v", err)
	}
	s := &Server{
		tb:   tb,
		dir:  dir,
		path: path,
		ln:   ln,
		live: map[net.Conn]struct{}{},
	}
	s.wg.Add(1)
	go s.acceptLoop()
	tb.Cleanup(s.Close)
	return s
}

// Path is the socket path.
func (s *Server) Path() string { return s.path }

// Dir is the private directory holding the socket.
func (s *Server) Dir() string { return s.dir }

// Stall makes every connection accepted from now on be held and never
// read. Connections already being read are unaffected; held connections
// stay held until Close.
func (s *Server) Stall(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stall = on
}

// CloseAtOnce makes every connection accepted from now on be closed
// immediately, before anything is read.
func (s *Server) CloseAtOnce(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeAtOnce = on
}

// Abandon closes the listener but leaves the socket file in place, so a
// dial finds a socket nobody listens on: ECONNREFUSED.
func (s *Server) Abandon() {
	s.mu.Lock()
	if s.abandoned || s.closed {
		s.mu.Unlock()
		return
	}
	s.abandoned = true
	s.mu.Unlock()
	s.ln.SetUnlinkOnClose(false)
	_ = s.ln.Close()
}

// Accepted is how many connections the listener has accepted so far,
// held and closed-at-once ones included. A refused pre-check shows as
// zero (U-19).
func (s *Server) Accepted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

// Connections snapshots every accepted connection in acceptance order.
func (s *Server) Connections() []Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Connection, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, Connection{
			Auth:    append([]string(nil), r.auth...),
			Frames:  append([]string(nil), r.frames...),
			Invalid: append([]string(nil), r.invalid...),
			Lines:   r.lines,
			Held:    r.held,
			Done:    r.done,
		})
	}
	return out
}

// Frames is every user-line content recorded so far, across connections,
// in arrival order.
func (s *Server) Frames() []string {
	var out []string
	for _, c := range s.Connections() {
		out = append(out, c.Frames...)
	}
	return out
}

// WaitFrames polls until at least n frames have been recorded and
// returns them. timeout is a hang catcher, never a performance bound:
// on expiry it fails tb and returns what it has.
func (s *Server) WaitFrames(n int, timeout time.Duration) []string {
	s.tb.Helper()
	s.waitFor(timeout, func() bool { return len(s.Frames()) >= n })
	return s.Frames()
}

// WaitAccepted polls until at least n connections have been accepted.
func (s *Server) WaitAccepted(n int, timeout time.Duration) {
	s.tb.Helper()
	s.waitFor(timeout, func() bool { return s.Accepted() >= n })
}

// WaitDone polls until at least n connections have been read to EOF.
func (s *Server) WaitDone(n int, timeout time.Duration) {
	s.tb.Helper()
	s.waitFor(timeout, func() bool {
		done := 0
		for _, c := range s.Connections() {
			if c.Done {
				done++
			}
		}
		return done >= n
	})
}

// waitFor is the poll loop behind the Wait accessors: testutil.Eventually
// on a 5 ms interval (plan 7.3: no sleeps in assertions; the timeout is a
// hang catcher that fails tb).
func (s *Server) waitFor(timeout time.Duration, cond func() bool) {
	s.tb.Helper()
	testutil.Eventually(s.tb, timeout, 5*time.Millisecond, cond)
}

// Close stops the listener, closes every held and live connection, waits
// for the readers, and removes the private directory. Safe to call more
// than once; New registers it as a Cleanup.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	held := s.held
	s.held = nil
	live := make([]net.Conn, 0, len(s.live))
	for c := range s.live {
		live = append(live, c)
	}
	s.mu.Unlock()

	_ = s.ln.Close()
	for _, c := range held {
		_ = c.Close()
	}
	for _, c := range live {
		_ = c.Close()
	}
	s.wg.Wait()
	_ = os.RemoveAll(s.dir)
}

// acceptLoop runs until the listener is closed. It holds the WaitGroup
// count while it runs, so the per-connection Adds inside it can never
// race a Wait that started at zero.
func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.accepted++
		r := &record{}
		s.records = append(s.records, r)
		switch {
		case s.closed:
			s.mu.Unlock()
			_ = conn.Close()
			return
		case s.stall:
			r.held = true
			s.held = append(s.held, conn)
			s.mu.Unlock()
			continue
		case s.closeAtOnce:
			r.done = true
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.live[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn, r)
	}
}

// serve reads one connection line by line until the peer closes.
func (s *Server) serve(conn net.Conn, r *record) {
	defer s.wg.Done()
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		delete(s.live, conn)
		r.done = true
		s.mu.Unlock()
	}()
	lr := protocol.NewLineReader(conn)
	for {
		line, err := lr.Next()
		switch {
		case err == nil:
			s.mu.Lock()
			s.classify(r, line)
			s.mu.Unlock()
		case errors.Is(err, protocol.ErrLineTooLong):
			s.mu.Lock()
			r.lines++
			r.invalid = append(r.invalid, OverlongLine)
			s.mu.Unlock()
		case errors.Is(err, io.EOF):
			return
		default:
			return
		}
	}
}

// classify records one physical line under the caller's lock.
func (*Server) classify(r *record, line []byte) {
	r.lines++
	var v wireLine
	if err := json.Unmarshal(line, &v); err != nil {
		r.invalid = append(r.invalid, string(line))
		return
	}
	switch {
	case v.Type == "auth" && v.Token != "":
		r.auth = append(r.auth, v.Token)
	case v.Type == "user" && v.Message.Role == "user":
		r.frames = append(r.frames, v.Message.Content)
	default:
		r.invalid = append(r.invalid, string(line))
	}
}
