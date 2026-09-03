package socketpost

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// fakeInfo is a FileInfo whose mode and owner the test chooses, for the
// rows a real filesystem cannot produce without a second user.
type fakeInfo struct {
	mode fs.FileMode
	sys  any
}

func (f fakeInfo) Name() string       { return "inbox.sock" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return f.sys }

// countingStat wraps a stat function and counts its calls.
func countingStat(inner func(string) (fs.FileInfo, error)) (func(string) (fs.FileInfo, error), *atomic.Int32) {
	var calls atomic.Int32
	return func(p string) (fs.FileInfo, error) {
		calls.Add(1)
		return inner(p)
	}, &calls
}

// TestPrecheckRefusals is U-19: each row is refused before any dial —
// the fake server counts zero accepted connections — with the classified
// error and reason.
func TestPrecheckRefusals(t *testing.T) {
	t.Parallel()
	uid := os.Getuid()
	longPath := "/" + strings.Repeat("p", MaxSocketPath) // MaxSocketPath+1 bytes

	type row struct {
		name      string
		path      func(t *testing.T, srv *fakesock.Server) string
		stat      func(string) (fs.FileInfo, error) // nil: os.Lstat
		sentinel  error
		reason    string
		statCalls int32
	}
	rows := []row{
		{
			name: "symlink to a real socket",
			path: func(t *testing.T, srv *fakesock.Server) string {
				t.Helper()
				link := filepath.Join(srv.Dir(), "link.sock")
				if err := os.Symlink(srv.Path(), link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			sentinel: ErrPrecheck, reason: ReasonSymlink, statCalls: 1,
		},
		{
			name: "foreign uid through the injected stat",
			path: func(_ *testing.T, srv *fakesock.Server) string { return srv.Path() },
			stat: func(string) (fs.FileInfo, error) {
				return fakeInfo{mode: fs.ModeSocket | 0o600, sys: &syscall.Stat_t{Uid: uint32(uid + 1)}}, nil //nolint:gosec // test uid arithmetic
			},
			sentinel: ErrPrecheck, reason: ReasonForeignUID, statCalls: 1,
		},
		{
			name: "mode 0644",
			path: func(t *testing.T, srv *fakesock.Server) string {
				t.Helper()
				if err := os.Chmod(srv.Path(), 0o644); err != nil { //nolint:gosec // the wrong mode is the point
					t.Fatal(err)
				}
				return srv.Path()
			},
			sentinel: ErrPrecheck, reason: ReasonMode, statCalls: 1,
		},
		{
			name: "mode 0700",
			path: func(t *testing.T, srv *fakesock.Server) string {
				t.Helper()
				if err := os.Chmod(srv.Path(), 0o700); err != nil { //nolint:gosec // the wrong mode is the point
					t.Fatal(err)
				}
				return srv.Path()
			},
			sentinel: ErrPrecheck, reason: ReasonMode, statCalls: 1,
		},
		{
			name:     "path one byte past sun_path",
			path:     func(*testing.T, *fakesock.Server) string { return longPath },
			sentinel: ErrPrecheck, reason: ReasonPathTooLong, statCalls: 0,
		},
		{
			name: "regular file",
			path: func(t *testing.T, srv *fakesock.Server) string {
				t.Helper()
				p := filepath.Join(srv.Dir(), "file.sock")
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			sentinel: ErrPrecheck, reason: ReasonNotSocket, statCalls: 1,
		},
		{
			name:     "directory",
			path:     func(_ *testing.T, srv *fakesock.Server) string { return srv.Dir() },
			sentinel: ErrPrecheck, reason: ReasonNotSocket, statCalls: 1,
		},
		{
			name:     "missing path",
			path:     func(_ *testing.T, srv *fakesock.Server) string { return filepath.Join(srv.Dir(), "gone.sock") },
			sentinel: ErrSocketGone, reason: ReasonMissing, statCalls: 1,
		},
		{
			name:     "relative path",
			path:     func(*testing.T, *fakesock.Server) string { return "tmp/cc-socks/1.sock" },
			sentinel: ErrPrecheck, reason: ReasonRelative, statCalls: 0,
		},
		{
			name:     "empty path",
			path:     func(*testing.T, *fakesock.Server) string { return "" },
			sentinel: ErrPrecheck, reason: ReasonEmpty, statCalls: 0,
		},
		{
			name: "owner unknown (no Stat_t)",
			path: func(_ *testing.T, srv *fakesock.Server) string { return srv.Path() },
			stat: func(string) (fs.FileInfo, error) {
				return fakeInfo{mode: fs.ModeSocket | 0o600, sys: nil}, nil
			},
			sentinel: ErrPrecheck, reason: ReasonOwnerUnknown, statCalls: 1,
		},
		{
			name: "stat fails otherwise",
			path: func(_ *testing.T, srv *fakesock.Server) string { return srv.Path() },
			stat: func(string) (fs.FileInfo, error) {
				return nil, &fs.PathError{Op: "lstat", Path: "x", Err: syscall.EACCES}
			},
			sentinel: ErrPrecheck, reason: ReasonStat, statCalls: 1,
		},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := fakesock.New(t)
			inner := tc.stat
			if inner == nil {
				inner = os.Lstat
			}
			stat, calls := countingStat(inner)
			err := Post(t.Context(), Target{Path: tc.path(t, srv), Token: "tok"}, "content", Options{Stat: stat})
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("err %v, want %v", err, tc.sentinel)
			}
			var e *Error
			if !errors.As(err, &e) || e.Reason != tc.reason {
				t.Fatalf("err %v, want reason %s", err, tc.reason)
			}
			if got := calls.Load(); got != tc.statCalls {
				t.Errorf("stat called %d times, want %d", got, tc.statCalls)
			}
			if n := srv.Accepted(); n != 0 {
				t.Errorf("server accepted %d connections, want 0 (a refused path is never dialled)", n)
			}
		})
	}
}

// TestPrecheckAcceptsTheRealSocket is the positive control of U-19: the
// fake server's own socket — 0600, this uid, not a link — passes, the
// dial happens exactly once, and the post arrives.
func TestPrecheckAcceptsTheRealSocket(t *testing.T) {
	t.Parallel()
	srv := fakesock.New(t)
	if err := precheck(srv.Path(), lstat, os.Getuid()); err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if err := Post(t.Context(), Target{Path: srv.Path(), Token: "tok"}, "hello", Options{}); err != nil {
		t.Fatalf("post: %v", err)
	}
	srv.WaitFrames(1, 10*time.Second)
	if n := srv.Accepted(); n != 1 {
		t.Errorf("accepted %d, want 1", n)
	}
}

// TestDefaultStatIsLstat: with no injected stat, a symlink to a valid
// socket is refused — the default does not follow links.
func TestDefaultStatIsLstat(t *testing.T) {
	t.Parallel()
	srv := fakesock.New(t)
	link := filepath.Join(srv.Dir(), "link.sock")
	if err := os.Symlink(srv.Path(), link); err != nil {
		t.Fatal(err)
	}
	// os.Stat would see a socket through the link; the default must not.
	if info, err := os.Stat(link); err != nil || info.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("control: os.Stat through the link should see a socket: %v %v", info, err)
	}
	err := Post(t.Context(), Target{Path: link, Token: "tok"}, "x", Options{})
	if !errors.Is(err, ErrPrecheck) {
		t.Fatalf("err %v, want ErrPrecheck", err)
	}
	if srv.Accepted() != 0 {
		t.Error("the link was dialled")
	}
}

// TestMaxSocketPathMatchesPlatform pins the constant to the kernel's
// sockaddr_un: 103 on darwin, 107 on linux (D33: nothing else builds).
// A path of exactly MaxSocketPath bytes passes the length check (it
// reaches stat); one byte more does not (stat never runs).
func TestMaxSocketPathMatchesPlatform(t *testing.T) {
	t.Parallel()
	want := map[string]int{"darwin": 103, "linux": 107}[runtime.GOOS]
	if want == 0 {
		t.Fatalf("unsupported GOOS %s", runtime.GOOS)
	}
	if MaxSocketPath != want {
		t.Fatalf("MaxSocketPath = %d, want %d on %s", MaxSocketPath, want, runtime.GOOS)
	}
	exact := "/" + strings.Repeat("p", MaxSocketPath-1)
	stat, calls := countingStat(func(string) (fs.FileInfo, error) {
		return nil, &fs.PathError{Op: "lstat", Path: "x", Err: syscall.ENOENT}
	})
	err := precheck(exact, stat, os.Getuid())
	if err == nil || err.Reason != ReasonMissing || calls.Load() != 1 {
		t.Errorf("exact-length path: err %v, stat calls %d; want missing after one stat", err, calls.Load())
	}
	err = precheck(exact+"p", stat, os.Getuid())
	if err == nil || err.Reason != ReasonPathTooLong || calls.Load() != 1 {
		t.Errorf("over-length path: err %v, stat calls %d; want path_too_long with no stat", err, calls.Load())
	}
}

// TestOwnerUID reads the uid from a real Lstat and refuses a fake.
func TestOwnerUID(t *testing.T) {
	t.Parallel()
	srv := fakesock.New(t)
	info, err := os.Lstat(srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	uid, ok := ownerUID(info)
	if !ok || uid != os.Getuid() {
		t.Errorf("ownerUID = %d, %v; want %d, true", uid, ok, os.Getuid())
	}
	if _, ok := ownerUID(fakeInfo{sys: "not a stat"}); ok {
		t.Error("a non-Stat_t Sys was accepted")
	}
	if _, ok := ownerUID(fakeInfo{sys: (*syscall.Stat_t)(nil)}); ok {
		t.Error("a nil Stat_t was accepted")
	}
}
