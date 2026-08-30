// Package fakesock is a stand-in for Claude Code's inbox socket: a unix-domain listener that records
// the NDJSON frames it receives (auth line + user line), never answers, and can be told to stall
// (accept but never read) so a poster's write timeout can be tested.
package fakesock

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type Frame struct {
	Type    string          `json:"type"`
	Token   string          `json:"token,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`
}

type Server struct {
	Path string
	ln   net.Listener
	mu   sync.Mutex
	conns []Connection
	stall bool
	held  []net.Conn // stalled connections, closed on shutdown
	wg   sync.WaitGroup
}

// Connection is everything one client connection sent, in order.
type Connection struct {
	Frames []Frame
	Bad    []string // lines that were not valid frames (the real server drops them silently)
}

// New listens on a short path under /tmp (macOS sun_path is 103 bytes; t.TempDir() is too long)
// with the same modes Claude Code uses (dir 0700, socket 0600) and removes it at test end.
func New(t *testing.T) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bsk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "1.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{Path: path, ln: ln}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(func() {
		ln.Close()
		s.mu.Lock()
		for _, c := range s.held {
			c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}

// Stall makes every later connection hang without reading, like a wedged harness (U-20).
func (s *Server) Stall(on bool) { s.mu.Lock(); s.stall = on; s.mu.Unlock() }

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		stall := s.stall
		s.mu.Unlock()
		if stall {
			// hold the connection open and read nothing; it is closed at shutdown
			s.mu.Lock()
			s.held = append(s.held, c)
			s.mu.Unlock()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer c.Close()
			var conn Connection
			sc := bufio.NewScanner(c)
			sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
			for sc.Scan() {
				var f Frame
				if err := json.Unmarshal(sc.Bytes(), &f); err != nil || f.Type == "" {
					conn.Bad = append(conn.Bad, sc.Text())
					continue
				}
				conn.Frames = append(conn.Frames, f)
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
		}()
	}
}

// Connections returns a copy of every completed connection so far.
func (s *Server) Connections() []Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Connection(nil), s.conns...)
}
