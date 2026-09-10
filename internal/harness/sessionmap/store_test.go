package sessionmap_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

func newStore(t *testing.T) sessionmap.Store {
	t.Helper()
	return sessionmap.Store{StateDir: filepath.Join(t.TempDir(), "state")}
}

// sysInfo is a real FileInfo with its Sys replaced, so an owner other than
// the current user can be reported for a file this test wrote.
type sysInfo struct {
	fs.FileInfo
	sys any
}

func (i sysInfo) Sys() any { return i.sys }

// statWithUIDDelta lstat's the path and reports the real owner uid plus
// delta. delta 0 is the positive control: the injected path with the true
// owner must pass, so the refusal below is the owner check and not the
// injection itself.
func statWithUIDDelta(delta uint32) func(string) (fs.FileInfo, error) {
	return func(p string) (fs.FileInfo, error) {
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, errors.New("no Stat_t")
		}
		copied := *st
		copied.Uid += delta
		return sysInfo{FileInfo: fi, sys: &copied}, nil
	}
}

func mustWriteByPID(t *testing.T, s sessionmap.Store, m sessionmap.ByPID) string {
	t.Helper()
	if err := s.WriteByPID(&m); err != nil {
		t.Fatalf("WriteByPID: %v", err)
	}
	path, err := s.ByPIDPath(m.ClaudePID)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStoreByPIDRoundTripModesAndDirs(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	want := validByPID()
	path := mustWriteByPID(t, s, want)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("map mode = %o, want 0600", fi.Mode().Perm())
	}
	for _, dir := range []string{s.ByPIDDir(), filepath.Dir(s.ByPIDDir()), s.StateDir} {
		di, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %o, want 0700", dir, di.Mode().Perm())
		}
	}
	got, err := s.ReadByPID(want.ClaudePID)
	if err != nil {
		t.Fatalf("ReadByPID: %v", err)
	}
	if !got.RegisteredAt.Equal(want.RegisteredAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("times: got %v/%v want %v/%v", got.RegisteredAt, got.UpdatedAt, want.RegisteredAt, want.UpdatedAt)
	}
	if !equalByPID(*got, want) {
		t.Fatalf("round trip changed the map:\n got %+v\nwant %+v", *got, want)
	}
}

func TestStoreWriteByPIDNilArgvIsAnEmptyArray(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	m := validByPID()
	m.AdapterCommand = nil
	path := mustWriteByPID(t, s, m)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"adapter_command":[]`) {
		t.Fatalf("file lacks an empty adapter_command array (bundled):\n%s", raw)
	}
	got, err := s.ReadByPID(m.ClaudePID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AdapterCommand == nil || len(got.AdapterCommand) != 0 {
		t.Fatalf("AdapterCommand = %#v, want a non-nil empty slice", got.AdapterCommand)
	}
}

func TestStoreWriteByPIDOverwritesOnEveryWrite(t *testing.T) {
	t.Parallel()
	// SessionStart re-fires on /clear (E0-8) and rewrites the map with the
	// new native id; the previous file must simply be replaced.
	s := newStore(t)
	first := validByPID()
	mustWriteByPID(t, s, first)
	second := first
	second.ClaudeSessionID = "d2c72366-0000-4000-8000-000000000002"
	second.PermissionMode = "plan"
	mustWriteByPID(t, s, second)
	got, err := s.ReadByPID(first.ClaudePID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaudeSessionID != second.ClaudeSessionID || got.PermissionMode != "plan" {
		t.Fatalf("second write did not win: %+v", *got)
	}
	entries, err := os.ReadDir(s.ByPIDDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("by-pid dir holds %d entries, want 1 (no temp file left behind)", len(entries))
	}
}

func TestStoreWriteByPIDRefusesAnInvalidMapAndWritesNothing(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	m := validByPID()
	m.ConfigDir = "relative/" + evilMarker
	details := assertConfig(t, s.WriteByPID(&m), sessionmap.ReasonMapInvalid)
	if details["field"] != "config_dir" {
		t.Fatalf("field = %q", details["field"])
	}
	if _, err := os.Stat(s.ByPIDDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("by-pid dir exists after a refused write (stat err %v)", err)
	}
}

func TestStoreReadByPIDMissingIsNotExist(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	_, err := s.ReadByPID(4242)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadByPID(missing) = %v, want fs.ErrNotExist", err)
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		t.Fatalf("a missing map is the caller's to classify, not a protocol error: %v", err)
	}
}

func TestStoreReadByPIDModeTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode os.FileMode
		ok   bool
	}{
		{0o600, true},
		{0o400, true},
		{0o644, false},
		{0o640, false},
		{0o604, false},
		{0o660, false},
		{0o666, false},
		{0o601, false},
		{0o610, false},
	}
	for _, tc := range cases {
		t.Run(tc.mode.String(), func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			path := mustWriteByPID(t, s, validByPID())
			//nolint:gosec // G302: a group- or world-readable file is the PRECONDITION this test refuses
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			_, err := s.ReadByPID(4242)
			if tc.ok {
				if err != nil {
					t.Fatalf("mode %o refused: %v", tc.mode, err)
				}
				return
			}
			details := assertConfig(t, err, sessionmap.ReasonMapNotPrivate)
			if details["check"] != "insecure_mode" {
				t.Fatalf("check = %q, want insecure_mode", details["check"])
			}
		})
	}
}

func TestStoreReadByPIDForeignOwnerThroughInjectedStat(t *testing.T) {
	t.Parallel()
	// A file owned by another uid cannot be created without privileges,
	// so the owner refusal is driven through Store.Stat. The delta-0 arm is
	// the control: the same injection with the true owner passes, so what
	// refuses below is the uid comparison and nothing about the injection.
	base := newStore(t)
	mustWriteByPID(t, base, validByPID())

	control := base
	control.Stat = statWithUIDDelta(0)
	if _, err := control.ReadByPID(4242); err != nil {
		t.Fatalf("control (injected stat, true owner) refused: %v", err)
	}

	foreign := base
	foreign.Stat = statWithUIDDelta(1)
	_, err := foreign.ReadByPID(4242)
	details := assertConfig(t, err, sessionmap.ReasonMapNotPrivate)
	if details["check"] != "foreign_owner" {
		t.Fatalf("check = %q, want foreign_owner", details["check"])
	}
}

func TestStoreReadByPIDRefusesASymlink(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	genuine := newStore(t)
	genuinePath := mustWriteByPID(t, genuine, validByPID())
	if err := os.MkdirAll(s.ByPIDDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := s.ByPIDPath(4242)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(genuinePath, path); err != nil {
		t.Fatal(err)
	}
	_, err = s.ReadByPID(4242)
	details := assertConfig(t, err, sessionmap.ReasonMapNotPrivate)
	if details["check"] != "symlink" {
		t.Fatalf("check = %q, want symlink", details["check"])
	}
}

func TestStoreReadByPIDRefusesADirectory(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	path, err := s.ByPIDPath(4242)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = s.ReadByPID(4242)
	details := assertConfig(t, err, sessionmap.ReasonMapNotPrivate)
	if details["check"] != "not_regular" {
		t.Fatalf("check = %q, want not_regular", details["check"])
	}
}

// TestStoreReadByPIDRefusesAFIFOWithoutBlocking: a named pipe planted at
// the map path is refused as not_regular, and the open does not block
// waiting for a writer (a blocking open would hang every session-bound
// command). The 30 s bound is a hang catcher, never a performance bound.
func TestStoreReadByPIDRefusesAFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	path, err := s.ByPIDPath(4242)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.ReadByPID(4242)
		done <- err
	}()
	select {
	case err := <-done:
		details := assertConfig(t, err, sessionmap.ReasonMapNotPrivate)
		if details["check"] != "not_regular" {
			t.Fatalf("check = %q, want not_regular", details["check"])
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ReadByPID blocked on a FIFO with no writer")
	}
}

func writeRawByPID(t *testing.T, s sessionmap.Store, pid int, content string) {
	t.Helper()
	path, err := s.ByPIDPath(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G703: the path is under this test's own state directory
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStoreReadByPIDMalformedAndInvalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		reason  string
		check   string
		field   string
	}{
		{name: "not json", content: "not json " + evilMarker, reason: sessionmap.ReasonMapMalformed, check: "not_json"},
		{name: "a json array", content: "[]", reason: sessionmap.ReasonMapMalformed, check: "not_json"},
		{name: "empty file", content: "", reason: sessionmap.ReasonMapMalformed, check: "not_json"},
		{name: "duplicate member", content: `{"claude_pid":4242,"claude_pid":4242}`, reason: sessionmap.ReasonMapMalformed, check: "not_json"},
		{name: "empty object fails validation", content: "{}", reason: sessionmap.ReasonMapInvalid, field: "claude_pid"},
		{name: "relative adapter in a planted map", content: `{"claude_pid":4242,"brigade_session_id":"x","team_key":"default","config_dir":"/c","inbound":"accept","adapter_command":["bin/` + evilMarker + `"]}`, reason: sessionmap.ReasonMapInvalid, field: "adapter_command"},
		{name: "relative transcript path in a planted map", content: `{"claude_pid":4242,"brigade_session_id":"x","team_key":"default","config_dir":"/c","inbound":"accept","frame_level":"open","transcript_path":"` + evilMarker + `.jsonl","adapter_command":[]}`, reason: sessionmap.ReasonMapInvalid, field: "transcript_path"},
		{name: "too large", content: "{" + strings.Repeat(" ", sessionmap.MaxMapBytes) + "}", reason: sessionmap.ReasonMapMalformed, check: "too_large"},
		// P5-12: a hand-written frame_text of 64 KiB pushes the file past MaxMapBytes and the reader's size
		// guard refuses it before any JSON is parsed; one of 60 KiB fits under the file cap and is refused by
		// Validate instead (frame_text over frame.MaxCustomBytes) — two layers, both closed.
		{name: "a 64 KiB frame_text is refused by the reader's size guard", content: `{"claude_pid":4242,"brigade_session_id":"x","team_key":"default","config_dir":"/c","inbound":"accept","frame_level":"custom","frame_text":"` + strings.Repeat("x", 64<<10) + ` ","adapter_command":[]}`, reason: sessionmap.ReasonMapMalformed, check: "too_large"},
		{name: "a 60 KiB frame_text fits the file cap and is refused by Validate", content: `{"claude_pid":4242,"brigade_session_id":"x","team_key":"default","config_dir":"/c","inbound":"accept","frame_level":"custom","frame_text":"` + strings.Repeat("x", 60<<10) + ` ","adapter_command":[]}`, reason: sessionmap.ReasonMapInvalid, field: "frame_text"},
		{name: "a planted frame level outside the set", content: `{"claude_pid":4242,"brigade_session_id":"x","team_key":"default","config_dir":"/c","inbound":"accept","frame_level":"` + evilMarker + `","adapter_command":[]}`, reason: sessionmap.ReasonMapInvalid, field: "frame_level"},
		{name: "a planted map from before P5-12 (no frame_level)", content: `{"claude_pid":4242,"brigade_session_id":"x","team_key":"default","config_dir":"/c","inbound":"accept","adapter_command":[]}`, reason: sessionmap.ReasonMapInvalid, field: "frame_level"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			writeRawByPID(t, s, 4242, tc.content)
			_, err := s.ReadByPID(4242)
			details := assertConfig(t, err, tc.reason)
			if tc.check != "" && details["check"] != tc.check {
				t.Fatalf("check = %q, want %q", details["check"], tc.check)
			}
			if tc.field != "" && details["field"] != tc.field {
				t.Fatalf("field = %q, want %q", details["field"], tc.field)
			}
		})
	}
}

func TestStoreReadByPIDAtTheSizeCapIsAccepted(t *testing.T) {
	t.Parallel()
	// The positive control for the too_large row: a map padded to exactly
	// MaxMapBytes still reads.
	s := newStore(t)
	body := `{"claude_pid":4242,"brigade_session_id":"x","team_key":"default","config_dir":"/c","inbound":"accept","frame_level":"open","adapter_command":[]}`
	content := body + strings.Repeat(" ", sessionmap.MaxMapBytes-len(body))
	if len(content) != sessionmap.MaxMapBytes {
		t.Fatalf("padding arithmetic: %d", len(content))
	}
	writeRawByPID(t, s, 4242, content)
	if _, err := s.ReadByPID(4242); err != nil {
		t.Fatalf("a map of exactly MaxMapBytes was refused: %v", err)
	}
}

func TestStoreReadByPIDRefusesAPIDMismatch(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	m := validByPID()
	m.ClaudePID = 4243
	data := mustWriteByPID(t, s, m)
	// Copy 4243's file to 4242's name: a planted or stale copy.
	raw, err := os.ReadFile(data)
	if err != nil {
		t.Fatal(err)
	}
	writeRawByPID(t, s, 4242, string(raw))
	_, err = s.ReadByPID(4242)
	assertConfig(t, err, sessionmap.ReasonMapMismatch)
	if _, err := s.ReadByPID(4243); err != nil {
		t.Fatalf("control: the genuine file refused: %v", err)
	}
}

func TestStoreDeleteByPID(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	mustWriteByPID(t, s, validByPID())
	if err := s.DeleteByPID(4242); err != nil {
		t.Fatalf("DeleteByPID: %v", err)
	}
	if _, err := s.ReadByPID(4242); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("after delete: %v, want not exist", err)
	}
	if err := s.DeleteByPID(4242); err != nil {
		t.Fatalf("second DeleteByPID: %v, want nil (missing is not an error)", err)
	}
}

func TestStoreByPIDForAnotherPIDIsJustAFile(t *testing.T) {
	t.Parallel()
	// No liveness or process-ownership check is claimable for a by-pid
	// map: beyond the file's mode and owner there is nothing to verify
	// (E0-7, the map is an unauthenticated trust boundary). A map for a
	// pid that is not ours — here one no process is likely to hold —
	// reads like any other.
	s := newStore(t)
	m := validByPID()
	m.ClaudePID = 999999
	mustWriteByPID(t, s, m)
	got, err := s.ReadByPID(999999)
	if err != nil {
		t.Fatalf("ReadByPID(foreign pid): %v", err)
	}
	if got.ClaudePID != 999999 {
		t.Fatalf("pid = %d", got.ClaudePID)
	}
}

func TestStoreByNativeOverwriteToleratesARecurringID(t *testing.T) {
	t.Parallel()
	// E0-5 item 6: 99a48c02 → /clear → d2c72366 → /resume → 99a48c02.
	s := newStore(t)
	const a, b = "99a48c02-0000-4000-8000-000000000001", "d2c72366-0000-4000-8000-000000000002"
	write := func(id, sid string) {
		t.Helper()
		if err := s.WriteByNative(id, &sessionmap.ByNative{BrigadeSessionID: sid, TeamRef: "t", SessionName: "n", UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	write(a, "brigade-1")
	write(b, "brigade-1")
	write(a, "brigade-2")
	got, err := s.ReadByNative(a)
	if err != nil {
		t.Fatal(err)
	}
	if got.BrigadeSessionID != "brigade-2" {
		t.Fatalf("recurring id did not take the last write: %+v", *got)
	}
	entries, err := os.ReadDir(s.ByNativeDir())
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if want := []string{a + ".json", b + ".json"}; !slices.Equal(names, want) {
		t.Fatalf("by-native dir = %v, want %v", names, want)
	}
	fi, err := os.Stat(filepath.Join(s.ByNativeDir(), a+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", fi.Mode().Perm())
	}
}

func TestStoreByNativeRefusesAHostileIDBeforeItBecomesAPath(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	for _, id := range []string{"../../" + evilMarker, "a/b", "", strings.Repeat("a", 81), "x.json"} {
		if _, err := s.ByNativePath(id); err == nil {
			t.Fatalf("ByNativePath(%q) accepted", id)
		}
		err := s.WriteByNative(id, &sessionmap.ByNative{BrigadeSessionID: "x"})
		assertConfig(t, err, sessionmap.ReasonMapInvalid)
		if _, err := s.ReadByNative(id); err == nil {
			t.Fatalf("ReadByNative(%q) accepted", id)
		}
	}
	if _, err := os.Stat(s.StateDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused id created state (stat err %v)", err)
	}
	// Positive control: a valid id lands inside the by-native directory.
	path, err := s.ByNativePath("beee3690-1111-4222-8333-444455556666")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != s.ByNativeDir() {
		t.Fatalf("path %q is not directly under %q", path, s.ByNativeDir())
	}
}

func TestStoreReadByNativeStrictAndMissing(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	const id = "beee3690-1111-4222-8333-444455556666"
	if _, err := s.ReadByNative(id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v, want not exist", err)
	}
	if err := s.WriteByNative(id, &sessionmap.ByNative{BrigadeSessionID: "x"}); err != nil {
		t.Fatal(err)
	}
	path, err := s.ByNativePath(id)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G302: a world-readable file is the PRECONDITION this test refuses
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = s.ReadByNative(id)
	assertConfig(t, err, sessionmap.ReasonMapNotPrivate)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = s.ReadByNative(id)
	assertConfig(t, err, sessionmap.ReasonMapMalformed)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = s.ReadByNative(id)
	assertConfig(t, err, sessionmap.ReasonMapInvalid)
}

func TestStoreRefusesARelativeOrEmptyStateDir(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"", "relative/state", "./state"} {
		s := sessionmap.Store{StateDir: dir}
		if _, err := s.ByPIDPath(4242); err == nil {
			t.Fatalf("StateDir %q accepted", dir)
		} else {
			assertConfig(t, err, sessionmap.ReasonStateDirRelative)
		}
		if _, err := s.ByNativePath("abc"); err == nil {
			t.Fatalf("StateDir %q accepted for by-native", dir)
		}
		m := validByPID()
		if err := s.WriteByPID(&m); err == nil {
			t.Fatalf("StateDir %q accepted for a write", dir)
		}
	}
}

func TestStoreByPIDPathRefusesANonPositivePID(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	for _, pid := range []int{0, -1} {
		if _, err := s.ByPIDPath(pid); err == nil {
			t.Fatalf("pid %d accepted", pid)
		}
		if _, err := s.ReadByPID(pid); err == nil {
			t.Fatalf("ReadByPID(%d) accepted", pid)
		}
	}
}
