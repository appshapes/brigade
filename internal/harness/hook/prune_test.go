package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

func TestCachedVersionParsing(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"brigade-0.0.0-darwin-arm64":     "0.0.0",
		"brigade-0.1.0-rc1-linux-amd64":  "0.1.0-rc1",
		"brigade-1.2.3-4-linux-arm64":    "1.2.3-4",
		"brigade-0.0.0":                  "",
		"brigade-0.0.0-darwin":           "",
		".brigade-0.0.0.tmpABC":          "",
		"notes.txt":                      "",
		"brigade--darwin-arm64":          "",
		"brigade-":                       "",
		"brigade-0.0.0-darwin-arm64.bak": "0.0.0", // the last two parts read as os/arch; harmless: only the version matters
	} {
		got, ok := cachedVersion(name)
		if (want != "") != ok || got != want {
			t.Errorf("cachedVersion(%q) = %q %v, want %q", name, got, ok, want)
		}
	}
}

// TestWeeklyPrune: three cached versions with mtimes; only a non-current
// version older than seven days goes; the stamp keeps the next
// SessionStart from pruning again inside seven days; nothing else in the
// directory is touched.
func TestWeeklyPrune(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	dataHome := filepath.Join(f.dirs.Root, "xdg", "data")
	cache := filepath.Join(dataHome, "brigade", "bin")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	old := fixedTime.Add(-30 * 24 * time.Hour)
	recent := fixedTime.Add(-24 * time.Hour)
	files := map[string]time.Time{
		"brigade-0.0.0-darwin-arm64": old,    // the current version: never removed
		"brigade-0.0.1-darwin-arm64": old,    // old and not current: removed
		"brigade-0.0.2-linux-amd64":  recent, // not current but recent: kept
		"notes.txt":                  old,    // not a cached binary: kept
		".brigade-0.0.1.tmpXYZ":      old,    // the bootstrap's temp file: kept
	}
	for name, mtime := range files {
		p := filepath.Join(cache, name)
		if err := os.WriteFile(p, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(cache, "brigade-9.9.9-darwin-arm64")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	env := []string{"XDG_DATA_HOME=" + dataHome}
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup"), env...); exit != 0 || !strings.Contains(errOut, "cache prune: removed") {
		t.Fatalf("exit %d err %q", exit, errOut)
	}
	assertNames(t, cache, "brigade-0.0.0-darwin-arm64", "brigade-0.0.2-linux-amd64", "notes.txt", ".brigade-0.0.1.tmpXYZ", "brigade-9.9.9-darwin-arm64")
	stamp := filepath.Join(f.stateDir, "state", pruneStamp)
	fi, err := os.Stat(stamp)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stamp %v %v", fi, err)
	}
	if data, _ := os.ReadFile(stamp); strings.TrimSpace(string(data)) != fixedTime.Format(time.RFC3339) {
		t.Fatalf("stamp content %q", data)
	}
	// A day later, 0.0.2 is now eligible by age but the stamp is fresh.
	if err := os.Chtimes(filepath.Join(cache, "brigade-0.0.2-linux-amd64"), old, old); err != nil {
		t.Fatal(err)
	}
	f.now = fixedTime.Add(24 * time.Hour)
	if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup"), env...); exit != 0 {
		t.Fatal(exit)
	}
	assertNames(t, cache, "brigade-0.0.0-darwin-arm64", "brigade-0.0.2-linux-amd64", "notes.txt", ".brigade-0.0.1.tmpXYZ", "brigade-9.9.9-darwin-arm64")
	// Eight days later the prune runs again and 0.0.2 goes.
	f.now = fixedTime.Add(8 * 24 * time.Hour)
	if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup"), env...); exit != 0 {
		t.Fatal(exit)
	}
	assertNames(t, cache, "brigade-0.0.0-darwin-arm64", "notes.txt", ".brigade-0.0.1.tmpXYZ", "brigade-9.9.9-darwin-arm64")
	if data, _ := os.ReadFile(stamp); strings.TrimSpace(string(data)) != f.now.Format(time.RFC3339) {
		t.Fatalf("stamp not refreshed: %q", data)
	}
}

// TestPruneNeedsTheVersionAndTheDataDir: without a readable VERSION nothing
// is removed and no stamp is written (the next start tries again); a
// relative XDG_DATA_HOME falls back to HOME/.local/share.
func TestPruneNeedsTheVersionAndTheDataDir(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	cache := filepath.Join(f.dirs.Home, ".local", "share", "brigade", "bin")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	old := fixedTime.Add(-30 * 24 * time.Hour)
	victim := filepath.Join(cache, "brigade-0.0.1-darwin-arm64")
	if err := os.WriteFile(victim, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(victim, old, old); err != nil {
		t.Fatal(err)
	}
	version := filepath.Join(f.dirs.PluginRoot, "bin", "VERSION")
	if err := os.Remove(version); err != nil {
		t.Fatal(err)
	}
	if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup"), "XDG_DATA_HOME=relative/data"); exit != 0 {
		t.Fatal(exit)
	}
	if _, err := os.Lstat(victim); err != nil {
		t.Fatal("pruned without knowing the current version")
	}
	if _, err := os.Lstat(filepath.Join(f.stateDir, "state", pruneStamp)); err == nil {
		t.Fatal("a stamp was written without a prune")
	}
	if err := os.WriteFile(version, []byte("0.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup"), "XDG_DATA_HOME=relative/data"); exit != 0 {
		t.Fatal(exit)
	}
	if _, err := os.Lstat(victim); err == nil {
		t.Fatal("the HOME/.local/share fallback was not pruned")
	}
}

func assertNames(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name())
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing from %q", w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("dir holds %q, want %q", got, want)
	}
}
