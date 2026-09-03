package pidfile_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/protocol"
)

// fakeMessagingTok stands in for a messaging token in these tests. It is
// never allowed to reach a file: every test that touches it registers
// assertTokenNeverStored on its state dir.
const fakeMessagingTok = "cc-messaging-token-DO-NOT-STORE-4f9c1a7e"

// entry is a well-formed Entry with the token hashed.
func entry(pid int, startTok string) pidfile.Entry {
	return pidfile.Entry{
		PID:              pid,
		StartToken:       startTok,
		BrigadeSessionID: "bs_0123456789abcdef",
		SocketPath:       "/tmp/cc-socks/4242.sock",
		TokenSHA256:      pidfile.TokenSHA256(fakeMessagingTok),
	}
}

// filesContaining walks dir and returns every regular file whose content
// contains needle, as absolute paths. It is the U-25-style grep of
// acceptance item 5. The walk is root-scoped (os.Root) so a symlink cannot
// lead it out of dir.
func filesContaining(tb testing.TB, dir, needle string) []string {
	tb.Helper()
	rootDir, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		tb.Fatalf("open root %s: %v", dir, err)
	}
	defer func() { _ = rootDir.Close() }()
	fsys := rootDir.FS()
	var hits []string
	err = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(needle)) {
			hits = append(hits, filepath.Join(dir, filepath.FromSlash(path)))
		}
		return nil
	})
	if err != nil {
		tb.Fatalf("walk %s: %v", dir, err)
	}
	return hits
}

// assertTokenNeverStored fails the test at cleanup if the token string
// appears in any file under dir (acceptance 5; 3.2: only its SHA-256 is
// ever written).
func assertTokenNeverStored(tb testing.TB, dir string) {
	tb.Helper()
	tb.Cleanup(func() {
		if hits := filesContaining(tb, dir, fakeMessagingTok); len(hits) != 0 {
			tb.Errorf("the messaging token was written to %v", hits)
		}
	})
}

// stateDir is a fresh state dir with the token grep armed.
func stateDir(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	assertTokenNeverStored(tb, dir)
	return dir
}

func TestTokenGrepBites(t *testing.T) {
	t.Parallel()
	// Positive control for assertTokenNeverStored: a file that DOES hold
	// the token is found, in a nested directory, so the cleanup checks of
	// every other test are not vacuous.
	dir := t.TempDir()
	nested := filepath.Join(dir, "watchers", "deep")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	leak := filepath.Join(nested, "leak.json")
	if err := os.WriteFile(leak, []byte(`{"token":"`+fakeMessagingTok+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clean.json"), []byte(`{"token_sha256":"`+pidfile.TokenSHA256(fakeMessagingTok)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hits := filesContaining(t, dir, fakeMessagingTok)
	if len(hits) != 1 || hits[0] != leak {
		t.Fatalf("filesContaining = %v, want exactly [%s]", hits, leak)
	}
	// And a missing directory is simply empty, never a crash in Cleanup.
	if hits := filesContaining(t, filepath.Join(dir, "absent"), fakeMessagingTok); len(hits) != 0 {
		t.Fatalf("filesContaining on a missing dir = %v", hits)
	}
}

func TestPath(t *testing.T) {
	t.Parallel()
	got := pidfile.Path("/state", 4242)
	if want := filepath.Join("/state", "watchers", "4242.json"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestEncodeIsOneFixedShape(t *testing.T) {
	t.Parallel()
	e := entry(4242, "1788394706.376379")
	got := string(pidfile.Encode(e))
	want := `{"pid":4242,"start_token":"1788394706.376379","brigade_session_id":"bs_0123456789abcdef",` +
		`"socket_path":"/tmp/cc-socks/4242.sock","token_sha256":"` + pidfile.TokenSHA256(fakeMessagingTok) + `"}` + "\n"
	if got != want {
		t.Fatalf("Encode =\n%s\nwant\n%s", got, want)
	}
	// Every member is present even when empty: the shape never varies.
	empty := string(pidfile.Encode(pidfile.Entry{PID: 1, StartToken: "t"}))
	for _, member := range []string{`"pid"`, `"start_token"`, `"brigade_session_id"`, `"socket_path"`, `"token_sha256"`} {
		if !strings.Contains(empty, member) {
			t.Errorf("Encode of a sparse entry lacks %s: %s", member, empty)
		}
	}
	if !bytes.Equal(pidfile.Encode(e), pidfile.Encode(e)) {
		t.Fatal("Encode is not deterministic")
	}
}

func TestCreateWrites0600InA0700DirAndReadRoundTrips(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := pidfile.Path(dir, 4242)
	e := entry(4242, "1788394706.376379")
	if err := pidfile.Create(path, e); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("watchers dir mode = %o, want 0700", di.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, pidfile.Encode(e)) {
		t.Fatalf("file content %q != Encode %q", raw, pidfile.Encode(e))
	}
	got, err := pidfile.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != e {
		t.Fatalf("Read = %+v, want %+v", got, e)
	}
}

func TestCreateRefusesAnExistingFileWithErrExist(t *testing.T) {
	t.Parallel()
	path := pidfile.Path(stateDir(t), 4242)
	first := entry(4242, "1.000001")
	if err := pidfile.Create(path, first); err != nil {
		t.Fatal(err)
	}
	err := pidfile.Create(path, entry(4343, "2.000002"))
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second Create err = %v, want fs.ErrExist", err)
	}
	got, err := pidfile.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("the refused create clobbered the file: %+v", got)
	}
}

func TestCreateRefusesAMalformedEntry(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	for name, e := range map[string]pidfile.Entry{
		"pid zero":     entry(0, "1.000001"),
		"pid negative": entry(-1, "1.000001"),
		"empty token":  entry(4242, ""),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, name+".json")
			err := pidfile.Create(path, e)
			if !errors.Is(err, pidfile.ErrMalformed) {
				t.Fatalf("Create(%+v) err = %v, want ErrMalformed", e, err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a refused Create left a file behind: %v", err)
			}
		})
	}
}

func TestReadMissingIsErrNotExist(t *testing.T) {
	t.Parallel()
	_, err := pidfile.Read(pidfile.Path(stateDir(t), 1))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestReadRefusals(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	good := entry(4242, "1.000001")
	goodPath := filepath.Join(dir, "good.json")
	if err := pidfile.Create(goodPath, good); err != nil {
		t.Fatal(err)
	}
	// Positive control: the well-formed file reads.
	if got, err := pidfile.Read(goodPath); err != nil || got != good {
		t.Fatalf("control Read = %+v, %v", got, err)
	}

	write := func(name string, data []byte, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	symlink := filepath.Join(dir, "link.json")
	if err := os.Symlink(goodPath, symlink); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "dir.json")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		want error
		code protocol.Code
	}{
		{"symlink to a good file", symlink, pidfile.ErrMalformed, ""},
		{"a directory", subdir, pidfile.ErrMalformed, ""},
		{"mode 0644", write("0644.json", pidfile.Encode(good), 0o644), nil, protocol.CodeConfig},
		{"mode 0660", write("0660.json", pidfile.Encode(good), 0o660), nil, protocol.CodeConfig},
		{"not JSON", write("garbage.json", []byte("not json\n"), 0o600), pidfile.ErrMalformed, ""},
		{"JSON array", write("array.json", []byte("[1,2]\n"), 0o600), pidfile.ErrMalformed, ""},
		{"duplicate member", write("dup.json", []byte(`{"pid":1,"pid":2,"start_token":"t"}`), 0o600), pidfile.ErrMalformed, ""},
		{"pid zero", write("pid0.json", []byte(`{"pid":0,"start_token":"t"}`), 0o600), pidfile.ErrMalformed, ""},
		{"pid negative", write("pidneg.json", []byte(`{"pid":-4,"start_token":"t"}`), 0o600), pidfile.ErrMalformed, ""},
		{"pid a string", write("pidstr.json", []byte(`{"pid":"4242","start_token":"t"}`), 0o600), pidfile.ErrMalformed, ""},
		{"empty token", write("notoken.json", []byte(`{"pid":4242,"start_token":""}`), 0o600), pidfile.ErrMalformed, ""},
		{"missing token", write("nokey.json", []byte(`{"pid":4242}`), 0o600), pidfile.ErrMalformed, ""},
		{"oversized", write("big.json", append(pidfile.Encode(good), bytes.Repeat([]byte(" "), 4096)...), 0o600), pidfile.ErrMalformed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := pidfile.Read(tc.path)
			if err == nil {
				t.Fatalf("Read = %+v, want a refusal", got)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.code != "" {
				var pe *protocol.Error
				if !errors.As(err, &pe) || pe.Code != tc.code {
					t.Fatalf("err = %v, want a *protocol.Error with code %s", err, tc.code)
				}
			}
			if got != (pidfile.Entry{}) {
				t.Fatalf("a refusal returned content: %+v", got)
			}
		})
	}
}

func TestRemoveIsCompareThenDelete(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := pidfile.Path(dir, 4242)
	mine := entry(4242, "1.000001")
	if err := pidfile.Create(path, mine); err != nil {
		t.Fatal(err)
	}

	// The file changed under us — a replacement watcher wrote its own
	// entry — so our cleanup must leave it alone (E0-5 item 3).
	theirs := entry(4343, "2.000002")
	if err := os.WriteFile(path, pidfile.Encode(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := pidfile.Remove(path, mine)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("Remove reported success on a file holding someone else's entry")
	}
	if got, err := pidfile.Read(path); err != nil || got != theirs {
		t.Fatalf("the replacement's file was disturbed: %+v, %v", got, err)
	}

	// A one-field difference is a difference: same pid, other token.
	almost := mine
	almost.StartToken = "1.000002"
	if err := os.WriteFile(path, pidfile.Encode(almost), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := pidfile.Remove(path, mine); err != nil || removed {
		t.Fatalf("Remove on a one-field difference = %v, %v; want false, nil", removed, err)
	}

	// Positive control: our own content IS removed.
	if err := os.WriteFile(path, pidfile.Encode(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err = pidfile.Remove(path, mine)
	if err != nil || !removed {
		t.Fatalf("Remove on our own content = %v, %v; want true, nil", removed, err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file still there after Remove: %v", err)
	}

	// Missing: nothing to act on, not an error.
	if removed, err := pidfile.Remove(path, mine); err != nil || removed {
		t.Fatalf("Remove on a missing file = %v, %v; want false, nil", removed, err)
	}
}

func TestReplaceLeavesExactlyTheNewContent(t *testing.T) {
	t.Parallel()
	path := pidfile.Path(stateDir(t), 4242)
	stale := entry(4242, "1.000001")
	if err := pidfile.Create(path, stale); err != nil {
		t.Fatal(err)
	}
	fresh := entry(4343, "2.000002")
	if err := pidfile.Replace(path, stale, fresh); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, pidfile.Encode(fresh)) {
		t.Fatalf("after Replace the file holds %q, want exactly %q", raw, pidfile.Encode(fresh))
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("after Replace mode = %v", fi.Mode())
	}
}

func TestReplaceRefusesWhenSuperseded(t *testing.T) {
	t.Parallel()
	path := pidfile.Path(stateDir(t), 4242)
	stale := entry(4242, "1.000001")
	if err := pidfile.Create(path, stale); err != nil {
		t.Fatal(err)
	}
	// Another hook replaced the file between our Read and our Replace.
	theirs := entry(5555, "3.000003")
	if err := os.WriteFile(path, pidfile.Encode(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	err := pidfile.Replace(path, stale, entry(4343, "2.000002"))
	if !errors.Is(err, pidfile.ErrSuperseded) {
		t.Fatalf("Replace err = %v, want ErrSuperseded", err)
	}
	if got, err := pidfile.Read(path); err != nil || got != theirs {
		t.Fatalf("Replace disturbed the other holder's file: %+v, %v", got, err)
	}
}

func TestReplaceToleratesAVanishedFile(t *testing.T) {
	t.Parallel()
	path := pidfile.Path(stateDir(t), 4242)
	// The stale holder cleaned up on its own between our Read and now.
	fresh := entry(4343, "2.000002")
	if err := pidfile.Replace(path, entry(4242, "1.000001"), fresh); err != nil {
		t.Fatal(err)
	}
	if got, err := pidfile.Read(path); err != nil || got != fresh {
		t.Fatalf("Read after Replace = %+v, %v", got, err)
	}
}
