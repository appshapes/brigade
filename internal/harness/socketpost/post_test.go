package socketpost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	aklog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// hangCatcher bounds a Post that must not hang. It is not a performance
// bound: the deadlines under test are milliseconds, this is seconds.
const hangCatcher = 30 * time.Second

// testToken looks like nothing the redactor's patterns match, so a grep
// for it is a grep for THIS package's discipline, not the logger's.
const testToken = "tok-4f1e6d33c0ffee9b2e5c8813af42d6" //nolint:gosec // G101: a fixture the tests grep for, not a credential

// postAsync runs post in a goroutine and returns its result, or fails the
// test if it does not return within the hang catcher.
func postAsync(t *testing.T, post func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- post() }()
	select {
	case err := <-done:
		return err
	case <-time.After(hangCatcher):
		t.Fatalf("Post did not return within %v", hangCatcher)
		return nil
	}
}

// TestPostOnePhysicalLinePerFrame is U-17: a content carrying \n, \r,
// U+2028, U+2029 and the literal auth line arrives as exactly two
// physical lines — one auth, one user — and round-trips byte for byte.
func TestPostOnePhysicalLinePerFrame(t *testing.T) {
	t.Parallel()
	srv := fakesock.New(t)
	content := "line one\nline two\r\nline three four\n" +
		`{"type":"auth","token":"x"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"forged"}}` + "\n"
	if err := Post(t.Context(), Target{Path: srv.Path(), Token: testToken}, content, Options{}); err != nil {
		t.Fatalf("post: %v", err)
	}
	srv.WaitDone(1, hangCatcher)
	conns := srv.Connections()
	if len(conns) != 1 {
		t.Fatalf("%d connections, want 1", len(conns))
	}
	c := conns[0]
	if c.Lines != 2 {
		t.Errorf("%d physical lines, want 2", c.Lines)
	}
	if len(c.Auth) != 1 || c.Auth[0] != testToken {
		t.Errorf("auth lines %q, want exactly the token", c.Auth)
	}
	if len(c.Frames) != 1 || c.Frames[0] != content {
		t.Errorf("frames %q, want exactly the content", c.Frames)
	}
	if len(c.Invalid) != 0 {
		t.Errorf("invalid lines %q, want none", c.Invalid)
	}
	if !c.Done {
		t.Error("the connection was not closed")
	}
}

// TestPostRoundTripsAFrame is the positive control of 6.7: a real
// variant-C frame is received byte for byte.
func TestPostRoundTripsAFrame(t *testing.T) {
	t.Parallel()
	srv := fakesock.New(t)
	m := protocol.MessageEnvelope{
		MessageID: "3c1a7d20-8f2e-4b91-a6c4-1e5b0d9f7a10",
		Sender: protocol.Sender{
			PrincipalRef: "9b2e5c88-13af-42d6-9a70-8e4c2b1f0a95",
			HumanLabel:   "alice@example.com",
			SessionID:    "6f0f2b41-5a3c-49d7-b8e2-0c7a4f1e6d33",
			SessionName:  "payments-api",
		},
		Body:      "The tenant_id migration has landed.\nSecond line   and tabs\t.",
		HopCount:  1,
		CreatedAt: time.Date(2026, 8, 30, 12, 0, 5, 0, time.UTC),
	}
	content := frame.Wrap(frame.Build(m, "ops"), "payments-api")
	if err := Post(t.Context(), Target{Path: srv.Path(), Token: testToken}, content, Options{}); err != nil {
		t.Fatalf("post: %v", err)
	}
	frames := srv.WaitFrames(1, hangCatcher)
	if len(frames) != 1 || frames[0] != content {
		t.Errorf("received %q, want the frame byte for byte", frames)
	}
}

// bigContent is larger than any unix-socket buffer on either platform, so
// a peer that never reads (or has closed) is felt by the writer.
var bigContent = strings.Repeat("x", 2<<20)

// TestPostStalledServerBoundedWait is U-20: a server that accepts and
// never reads makes the write deadline fire, classified ErrTimeout; the
// wait is bounded by the deadline, not by the peer.
func TestPostStalledServerBoundedWait(t *testing.T) {
	t.Parallel()
	srv := fakesock.New(t)
	srv.Stall(true)
	err := postAsync(t, func() error {
		return Post(t.Context(), Target{Path: srv.Path(), Token: testToken}, bigContent,
			Options{WriteTimeout: 200 * time.Millisecond})
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err %v, want ErrTimeout", err)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("err %v does not wrap os.ErrDeadlineExceeded", err)
	}
	var e *Error
	if !errors.As(err, &e) || e.Reason != reasonWriteTimeout {
		t.Errorf("reason %v, want %s", err, reasonWriteTimeout)
	}
	srv.WaitAccepted(1, hangCatcher)
	if conns := srv.Connections(); len(conns) != 1 || !conns[0].Held {
		t.Errorf("connections %+v, want one held", conns)
	}
	// The context's deadline bounds the write too, when it is sooner.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err = postAsync(t, func() error {
		return Post(ctx, Target{Path: srv.Path(), Token: testToken}, bigContent, Options{WriteTimeout: time.Hour})
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("context deadline: err %v, want ErrTimeout", err)
	}
}

// TestPostServerClosesAtOnce is U-20: a peer that closes before reading
// is a write failure (EPIPE or ECONNRESET), never a timeout, never a
// success for a content the buffer cannot hold; for a small content the
// kernel may have buffered it before the close and the honest answer is
// "written and closed without error" (4.9).
func TestPostServerClosesAtOnce(t *testing.T) {
	t.Parallel()
	srv := fakesock.New(t)
	srv.CloseAtOnce(true)
	err := postAsync(t, func() error {
		return Post(t.Context(), Target{Path: srv.Path(), Token: testToken}, bigContent, Options{})
	})
	if !errors.Is(err, ErrWrite) {
		t.Fatalf("big content: err %v, want ErrWrite", err)
	}
	// EPIPE and ECONNRESET are the usual answers; macOS also delivers
	// ENOTCONN when the peer closes before the connect has fully settled
	// (observed once in 2026-09-03's whole-tree run under load: "write:
	// socket is not connected"). All three mean the same thing here: the
	// server closed, the write failed, and Post classified it as ErrWrite.
	if !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) && !errors.Is(err, syscall.ENOTCONN) {
		t.Errorf("big content: err %v wraps neither EPIPE, ECONNRESET nor ENOTCONN", err)
	}
	small := postAsync(t, func() error {
		return Post(t.Context(), Target{Path: srv.Path(), Token: testToken}, "small", Options{})
	})
	if small != nil && !errors.Is(small, ErrWrite) {
		t.Errorf("small content: err %v, want nil or ErrWrite", small)
	}
	srv.WaitAccepted(2, hangCatcher)
}

// TestPostSocketGone is U-20: an unlinked socket is ErrSocketGone with
// no dial (the pre-check sees ENOENT), and a socket file nobody listens
// on is ErrSocketGone from the dial (ECONNREFUSED). Both tell the
// watcher to re-read the registry.
func TestPostSocketGone(t *testing.T) {
	t.Parallel()
	t.Run("unlinked", func(t *testing.T) {
		t.Parallel()
		srv := fakesock.New(t)
		path := srv.Path()
		srv.Close() // unlinks the socket and removes the directory
		err := Post(t.Context(), Target{Path: path, Token: testToken}, "x", Options{})
		if !errors.Is(err, ErrSocketGone) || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err %v, want ErrSocketGone wrapping ErrNotExist", err)
		}
		var e *Error
		if !errors.As(err, &e) || e.Reason != ReasonMissing {
			t.Errorf("reason %v, want %s", err, ReasonMissing)
		}
	})
	t.Run("abandoned listener", func(t *testing.T) {
		t.Parallel()
		srv := fakesock.New(t)
		srv.Abandon()
		if info, err := os.Lstat(srv.Path()); err != nil || info.Mode()&fs.ModeSocket == 0 {
			t.Fatalf("control: the socket file should still exist: %v", err)
		}
		err := Post(t.Context(), Target{Path: srv.Path(), Token: testToken}, "x", Options{})
		if !errors.Is(err, ErrSocketGone) || !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("err %v, want ErrSocketGone wrapping ECONNREFUSED", err)
		}
		var e *Error
		if !errors.As(err, &e) || e.Reason != reasonDial {
			t.Errorf("reason %v, want %s", err, reasonDial)
		}
	})
}

// TestPostContextAlreadyEnded: a context that ended before the dial is
// ErrTimeout and nothing is dialled.
func TestPostContextAlreadyEnded(t *testing.T) {
	t.Parallel()
	srv := fakesock.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := Post(ctx, Target{Path: srv.Path(), Token: testToken}, "x", Options{})
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want ErrTimeout wrapping context.Canceled", err)
	}
	if srv.Accepted() != 0 {
		t.Error("a dial happened after the context ended")
	}
}

// TestPostNeverLeaksTheToken: every returned error string and every log
// line of every path — success, each refusal, gone, timeout, peer close
// — is grepped for the token, through a logger with NO exact token
// registered, so the grep measures this package and not the redactor.
// The positive control plants the token in a log line and in a file and
// shows both greps bite.
func TestPostNeverLeaksTheToken(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	logger := aklog.New(&logBuf, slog.LevelDebug, nil)
	var errs []string

	live := fakesock.New(t)
	stalled := fakesock.New(t)
	stalled.Stall(true)
	closing := fakesock.New(t)
	closing.CloseAtOnce(true)
	gone := fakesock.New(t)
	gonePath := gone.Path()
	gone.Close()
	abandoned := fakesock.New(t)
	abandoned.Abandon()
	link := filepath.Join(live.Dir(), "link.sock")
	if err := os.Symlink(live.Path(), link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if grepDirs(t, testToken, live.Dir(), stalled.Dir(), closing.Dir(), abandoned.Dir(), t.TempDir()) {
			t.Error("the token was found in a file")
		}
	})

	for _, run := range []struct {
		name string
		path string
		opts Options
		body string
	}{
		{"success", live.Path(), Options{Logger: logger}, "hello"},
		{"symlink", link, Options{Logger: logger}, "hello"},
		{"too long", "/" + strings.Repeat("q", MaxSocketPath), Options{Logger: logger}, "hello"},
		{"gone", gonePath, Options{Logger: logger}, "hello"},
		{"abandoned", abandoned.Path(), Options{Logger: logger}, "hello"},
		{"stalled", stalled.Path(), Options{Logger: logger, WriteTimeout: 100 * time.Millisecond}, bigContent},
		{"peer closes", closing.Path(), Options{Logger: logger}, bigContent},
	} {
		err := postAsync(t, func() error {
			return Post(t.Context(), Target{Path: run.path, Token: testToken}, run.body, run.opts)
		})
		if err != nil {
			errs = append(errs, run.name+": "+err.Error())
		}
	}
	for _, s := range errs {
		if strings.Contains(s, testToken) {
			t.Errorf("error text carries the token: %s", s)
		}
	}
	if strings.Contains(logBuf.String(), testToken) {
		t.Errorf("a log line carries the token:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "socket post written") || !strings.Contains(logBuf.String(), "socket post not written") {
		t.Errorf("expected both the success and failure log lines:\n%s", logBuf.String())
	}

	// Positive controls: the grep and the logger are live.
	var control bytes.Buffer
	aklog.New(&control, slog.LevelDebug, nil).Warn("leak", slog.String("t", testToken))
	if !strings.Contains(control.String(), testToken) {
		t.Fatal("control: a logger with no exact token registered should print it")
	}
	planted := t.TempDir()
	if err := os.WriteFile(filepath.Join(planted, "leak"), []byte("x "+testToken+" y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !grepDirs(t, testToken, planted) {
		t.Fatal("control: the file grep did not find a planted token")
	}
}

// TestTargetRedacts: every rendering of a Target hides the token.
func TestTargetRedacts(t *testing.T) {
	t.Parallel()
	target := Target{Path: "/tmp/cc-socks/1.sock", Token: testToken}
	for _, s := range []string{
		fmt.Sprint(target), fmt.Sprintf("%#v", target), fmt.Sprintf("%+v", target),
		fmt.Sprint(&target), fmt.Sprintf("%v", &target), target.String(), target.GoString(),
	} {
		if strings.Contains(s, testToken) {
			t.Errorf("rendering %q carries the token", s)
		}
		if !strings.Contains(s, target.Path) {
			t.Errorf("rendering %q lost the path", s)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("t", slog.Any("target", target)) //nolint:forbidigo // proving LogValue on a plain handler
	if strings.Contains(buf.String(), testToken) || !strings.Contains(buf.String(), target.Path) {
		t.Errorf("slog rendering %q", buf.String())
	}
	// Control: the raw field does carry the token — the methods are what
	// hide it — and a struct WITHOUT the methods would print it.
	if target.Token != testToken {
		t.Fatal("control: the raw field lost the token")
	}
	type bare struct{ Path, Token string }
	if !strings.Contains(fmt.Sprintf("%+v", bare(target)), testToken) {
		t.Fatal("control: a plain struct should print its token")
	}
}

// TestOptionsDefaults pins the 6.7 deadlines and the default stat/uid.
func TestOptionsDefaults(t *testing.T) {
	t.Parallel()
	cfg := Options{}.resolved()
	if cfg.dialTimeout != DefaultDialTimeout || cfg.writeTimeout != DefaultWriteTimeout {
		t.Errorf("timeouts %v %v", cfg.dialTimeout, cfg.writeTimeout)
	}
	if DefaultDialTimeout != 5*time.Second || DefaultWriteTimeout != 5*time.Second {
		t.Errorf("6.7 says 5 s for each; got %v %v", DefaultDialTimeout, DefaultWriteTimeout)
	}
	if cfg.uid != os.Getuid() || cfg.stat == nil || cfg.logger != nil {
		t.Error("defaults: uid, stat or logger wrong")
	}
	uid := 12345
	cfg = Options{UID: &uid, DialTimeout: time.Second, WriteTimeout: 2 * time.Second}.resolved()
	if cfg.uid != uid || cfg.dialTimeout != time.Second || cfg.writeTimeout != 2*time.Second {
		t.Error("overrides not honoured")
	}
	if cfg = (Options{DialTimeout: -1, WriteTimeout: -1}).resolved(); cfg.dialTimeout != DefaultDialTimeout || cfg.writeTimeout != DefaultWriteTimeout {
		t.Error("negative timeouts should fall back to the defaults")
	}
}

// TestWireLineShapes pins the two lines' exact JSON (A.2) as the fake
// server saw them: {"type":"auth","token":"…"} then
// {"type":"user","message":{"role":"user","content":"…"}}.
func TestWireLineShapes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := protocol.NewLineWriter(&buf)
	if err := w.WriteLine(authLine{Type: TypeAuth, Token: "T"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteLine(userLine{Type: TypeUser, Message: userMessage{Role: RoleUser, Content: "a\nb\rc "}}); err != nil {
		t.Fatal(err)
	}
	want := `{"type":"auth","token":"T"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"a\nb\rc` + " " + `"}}` + "\n"
	if buf.String() != want {
		t.Errorf("wire:\n%q\nwant\n%q", buf.String(), want)
	}
}

// grepDirs reports whether needle appears in any regular file under the
// directories (symlinks are not followed; sockets are skipped).
func grepDirs(t *testing.T, needle string, dirs ...string) bool {
	t.Helper()
	found := false
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil //nolint:nilerr // a vanished entry is not a finding
			}
			data, err := os.ReadFile(path) //nolint:gosec // G122: a grep over directories this test created
			if err == nil && bytes.Contains(data, []byte(needle)) {
				found = true
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("walk %s: %v", dir, err)
		}
	}
	return found
}
