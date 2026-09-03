package fakesock

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// hang is the hang catcher for the Wait accessors; not a performance bound.
const hang = 30 * time.Second

func dial(t *testing.T, path string) net.Conn {
	t.Helper()
	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func TestNewCreatesAPrivateSocketUnderTmp(t *testing.T) {
	t.Parallel()
	srv := New(t)
	if !strings.HasPrefix(srv.Path(), "/tmp/bsk") {
		t.Errorf("path %q is not under /tmp/bsk*", srv.Path())
	}
	if len(srv.Path()) > 103 {
		t.Errorf("path %q is longer than darwin's sun_path", srv.Path())
	}
	info, err := os.Lstat(srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSocket == 0 || info.Mode()&fs.ModeSymlink != 0 {
		t.Errorf("mode %v is not a socket", info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm %v, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(srv.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("dir perm %v, want 0700", dirInfo.Mode().Perm())
	}
}

func TestRecordsLinesPerConnection(t *testing.T) {
	t.Parallel()
	srv := New(t)
	conn := dial(t, srv.Path())
	lines := []string{
		`{"type":"auth","token":"tok-1"}`,
		`{"type":"user","message":{"role":"user","content":"first\nframe"}}`,
		`not json at all`,
		`{"type":"user","message":{"role":"assistant","content":"wrong role"}}`,
		`{"type":"auth"}`,
		`{"type":"user","message":{"role":"user","content":"second"}}`,
		`{"type":"auth","token":"tok-2"}`,
	}
	if _, err := io.WriteString(conn, strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	srv.WaitDone(1, hang)
	conns := srv.Connections()
	if len(conns) != 1 {
		t.Fatalf("%d connections", len(conns))
	}
	c := conns[0]
	if c.Lines != len(lines) {
		t.Errorf("Lines %d, want %d", c.Lines, len(lines))
	}
	if want := []string{"tok-1", "tok-2"}; strings.Join(c.Auth, ",") != strings.Join(want, ",") {
		t.Errorf("Auth %q, want %q", c.Auth, want)
	}
	if want := []string{"first\nframe", "second"}; strings.Join(c.Frames, "|") != strings.Join(want, "|") {
		t.Errorf("Frames %q, want %q", c.Frames, want)
	}
	if want := []string{lines[2], lines[3], lines[4]}; strings.Join(c.Invalid, "|") != strings.Join(want, "|") {
		t.Errorf("Invalid %q, want %q", c.Invalid, want)
	}
	if !c.Done || c.Held {
		t.Errorf("Done %v Held %v", c.Done, c.Held)
	}
	if got := srv.Frames(); len(got) != 2 {
		t.Errorf("Frames() %q", got)
	}
	if srv.Accepted() != 1 {
		t.Errorf("Accepted %d", srv.Accepted())
	}
}

func TestSecondConnectionIsRecordedSeparately(t *testing.T) {
	t.Parallel()
	srv := New(t)
	for i, content := range []string{"one", "two"} {
		conn := dial(t, srv.Path())
		if _, err := io.WriteString(conn, `{"type":"user","message":{"role":"user","content":"`+content+`"}}`+"\n"); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		srv.WaitDone(i+1, hang)
	}
	conns := srv.Connections()
	if len(conns) != 2 || len(conns[0].Frames) != 1 || len(conns[1].Frames) != 1 {
		t.Fatalf("connections %+v", conns)
	}
	if got := srv.WaitFrames(2, hang); strings.Join(got, ",") != "one,two" {
		t.Errorf("frames %q", got)
	}
}

func TestOverlongLineIsRecordedAsInvalid(t *testing.T) {
	t.Parallel()
	srv := New(t)
	conn := dial(t, srv.Path())
	go func() {
		_, _ = io.WriteString(conn, `{"type":"auth","token":"a"}`+"\n")
		_, _ = io.WriteString(conn, strings.Repeat("y", 1<<20+1)+"\n")
		_, _ = io.WriteString(conn, `{"type":"user","message":{"role":"user","content":"after"}}`+"\n")
		_ = conn.Close()
	}()
	srv.WaitDone(1, hang)
	c := srv.Connections()[0]
	if c.Lines != 3 || len(c.Invalid) != 1 || c.Invalid[0] != OverlongLine {
		t.Errorf("Lines %d Invalid %q", c.Lines, c.Invalid)
	}
	if len(c.Frames) != 1 || c.Frames[0] != "after" {
		t.Errorf("reading did not continue past the over-long line: %q", c.Frames)
	}
}

func TestStallHoldsConnectionsAndCloseReleasesThem(t *testing.T) {
	t.Parallel()
	srv := New(t)
	srv.Stall(true)
	conn := dial(t, srv.Path())
	if _, err := io.WriteString(conn, "{\"type\":\"auth\",\"token\":\"t\"}\n"); err != nil {
		t.Fatal(err)
	}
	srv.WaitAccepted(1, hang)
	c := srv.Connections()[0]
	if !c.Held || c.Lines != 0 || c.Done {
		t.Errorf("held connection %+v", c)
	}
	// A read on the client blocks until the server closes it at Close.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	}()
	srv.Stall(false)
	conn2 := dial(t, srv.Path())
	if _, err := io.WriteString(conn2, `{"type":"user","message":{"role":"user","content":"live"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn2.Close()
	srv.WaitFrames(1, hang)
	srv.Close() // must not hang on the held connection
	wg.Wait()
	_ = conn.Close()
}

func TestCloseAtOnce(t *testing.T) {
	t.Parallel()
	srv := New(t)
	srv.CloseAtOnce(true)
	conn := dial(t, srv.Path())
	srv.WaitAccepted(1, hang)
	buf := make([]byte, 1)
	_ = conn.SetReadDeadline(time.Now().Add(hang))
	if _, err := conn.Read(buf); !errors.Is(err, io.EOF) {
		t.Errorf("read after the server's close: %v, want EOF", err)
	}
	_ = conn.Close()
	if c := srv.Connections()[0]; !c.Done || c.Lines != 0 {
		t.Errorf("connection %+v", c)
	}
}

func TestAbandonLeavesTheFileRefusingDials(t *testing.T) {
	t.Parallel()
	srv := New(t)
	srv.Abandon()
	srv.Abandon() // idempotent
	info, err := os.Lstat(srv.Path())
	if err != nil || info.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("socket file should remain: %v", err)
	}
	var d net.Dialer
	ctx, cancel := context.WithTimeout(t.Context(), hang)
	defer cancel()
	_, err = d.DialContext(ctx, "unix", srv.Path())
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("dial: %v, want ECONNREFUSED", err)
	}
	srv.Close()
	if _, err := os.Stat(srv.Dir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Close after Abandon left the directory: %v", err)
	}
}

func TestCloseRemovesTheDirectoryAndIsIdempotent(t *testing.T) {
	t.Parallel()
	srv := New(t)
	dir := srv.Dir()
	srv.Close()
	srv.Close()
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("directory survived Close: %v", err)
	}
	var d net.Dialer
	if _, err := d.DialContext(t.Context(), "unix", srv.Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dial after Close: %v, want ENOENT", err)
	}
	if srv.Accepted() != 0 || len(srv.Connections()) != 0 {
		t.Error("a closed server recorded a connection")
	}
}

// recordingTB captures Fatalf so the Wait accessors' give-up path can be
// exercised without failing this test.
type recordingTB struct {
	testing.TB
	mu    sync.Mutex
	fatal []string
}

func (r *recordingTB) Helper() {}
func (r *recordingTB) Fatalf(format string, _ ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fatal = append(r.fatal, format)
}

func TestWaitAccessorsGiveUp(t *testing.T) {
	t.Parallel()
	rec := &recordingTB{TB: t}
	srv := New(rec)
	got := srv.WaitFrames(1, 20*time.Millisecond)
	srv.WaitAccepted(1, 20*time.Millisecond)
	srv.WaitDone(1, 20*time.Millisecond)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.fatal) != 3 {
		t.Errorf("%d Fatalf calls, want 3", len(rec.fatal))
	}
	if len(got) != 0 {
		t.Errorf("WaitFrames returned %q", got)
	}
	// Positive control: with a frame present the same accessor returns
	// without failing.
	rec2 := &recordingTB{TB: t}
	srv2 := New(rec2)
	conn := dial(t, srv2.Path())
	_, _ = io.WriteString(conn, `{"type":"user","message":{"role":"user","content":"c"}}`+"\n")
	_ = conn.Close()
	if got := srv2.WaitFrames(1, hang); len(got) != 1 || len(rec2.fatal) != 0 {
		t.Errorf("WaitFrames %q, fatal %v", got, rec2.fatal)
	}
}
