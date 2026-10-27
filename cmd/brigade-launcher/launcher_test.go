package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testVersion = "0.41.0"

// rig is one launcher over a scratch plugin bin directory and scratch profile directories, with every seam a
// recording fake.
type rig struct {
	t        *testing.T
	l        *Launcher
	bin      string
	appdata  string
	local    string
	asset    []byte
	stdout   strings.Builder
	stderr   strings.Builder
	ran      [][]string
	detached [][]string
	fetched  []string
	fetchErr error
	fetchBad bool
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{t: t, bin: filepath.Join(root, "plugin", "bin"), appdata: filepath.Join(root, "Roaming"), local: filepath.Join(root, "Local")}
	must(t, os.MkdirAll(r.bin, 0o700))
	must(t, os.MkdirAll(r.appdata, 0o700))
	must(t, os.MkdirAll(r.local, 0o700))
	r.asset = []byte("the windows binary " + testVersion)
	sum := sha256.Sum256(r.asset)
	must(t, os.WriteFile(filepath.Join(r.bin, versionFile), []byte(testVersion+"\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(r.bin, checksumsFile), []byte(
		"0000000000000000000000000000000000000000000000000000000000000000  brigade_"+testVersion+"_darwin_arm64\n"+
			hex.EncodeToString(sum[:])+"  brigade_"+testVersion+"_windows_amd64.exe\n"), 0o600))
	r.l = &Launcher{
		Self:    filepath.Join(r.bin, "brigade.exe"),
		BinDir:  r.bin,
		Env:     map[string]string{"APPDATA": r.appdata, "LOCALAPPDATA": r.local},
		Environ: []string{"APPDATA=" + r.appdata, "LOCALAPPDATA=" + r.local},
		Stdout:  &r.stdout,
		Stderr:  &r.stderr,
		Arch:    func() (string, error) { return "amd64", nil },
		Fetch: func(url, tmp, proto string) error {
			r.fetched = append(r.fetched, url+" "+proto)
			if r.fetchErr != nil {
				return r.fetchErr
			}
			body := r.asset
			if r.fetchBad {
				body = []byte("not the binary")
			}
			return os.WriteFile(tmp, body, 0o600)
		},
		Run: func(path string, args []string, _ []string) (int, error) {
			r.ran = append(r.ran, append([]string{path}, args...))
			return 42, nil
		},
		Detach: func(path string, args []string, _ []string) error {
			r.detached = append(r.detached, append([]string{path}, args...))
			return nil
		},
	}
	return r
}

func (r *rig) target() string {
	return filepath.Join(r.local, "brigade", "bin", "brigade-"+testVersion+"-windows-amd64.exe")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestWarmCacheRunsTheCachedBinary(t *testing.T) {
	r := newRig(t)
	must(t, os.MkdirAll(filepath.Dir(r.target()), 0o700))
	must(t, os.WriteFile(r.target(), r.asset, 0o600))
	if code := r.l.Main([]string{"sessions", "--all"}); code != 42 {
		t.Fatalf("exit %d, want the child's 42; stderr %q", code, r.stderr.String())
	}
	if len(r.ran) != 1 || r.ran[0][0] != r.target() || strings.Join(r.ran[0][1:], " ") != "sessions --all" {
		t.Fatalf("ran %v", r.ran)
	}
	if len(r.fetched) != 0 || len(r.detached) != 0 || r.stdout.Len() != 0 {
		t.Fatalf("a warm cache downloaded (%v), detached (%v) or printed (%q)", r.fetched, r.detached, r.stdout.String())
	}
}

func TestDeveloperPointerWins(t *testing.T) {
	r := newRig(t)
	dev := filepath.Join(t.TempDir(), "brigade-dev.exe")
	must(t, os.WriteFile(dev, []byte("dev build"), 0o600))
	must(t, os.MkdirAll(filepath.Join(r.appdata, "brigade"), 0o700))
	must(t, os.WriteFile(filepath.Join(r.appdata, "brigade", pointerFile), []byte(dev+"\n"), 0o600))
	if code := r.l.Main([]string{"whoami"}); code != 42 {
		t.Fatalf("exit %d; stderr %q", code, r.stderr.String())
	}
	if len(r.ran) != 1 || r.ran[0][0] != dev {
		t.Fatalf("ran %v, want the pointer's %s", r.ran, dev)
	}
}

func TestDeveloperPointerMustBeAbsoluteAndPresent(t *testing.T) {
	r := newRig(t)
	must(t, os.MkdirAll(filepath.Join(r.appdata, "brigade"), 0o700))
	must(t, os.WriteFile(filepath.Join(r.appdata, "brigade", pointerFile), []byte("bin\\brigade.exe\n"), 0o600))
	if code := r.l.Main([]string{"whoami"}); code != exitConfig {
		t.Fatalf("exit %d, want %d", code, exitConfig)
	}
	if !strings.Contains(r.stderr.String(), "does not name an executable absolute path") || len(r.ran) != 0 {
		t.Fatalf("stderr %q, ran %v", r.stderr.String(), r.ran)
	}
}

func TestSessionStartOnAColdCacheDetachesTheWorkerAndReturnsAtOnce(t *testing.T) {
	r := newRig(t)
	must(t, os.MkdirAll(filepath.Dir(r.target()), 0o700))
	must(t, os.WriteFile(r.target()+".failed", []byte("9 stale verdict\n"), 0o600))
	if code := r.l.Main([]string{"hook", "session-start"}); code != exitOK {
		t.Fatalf("exit %d; stderr %q", code, r.stderr.String())
	}
	if len(r.detached) != 1 || r.detached[0][0] != r.l.Self || r.detached[0][1] != workerFlag {
		t.Fatalf("detached %v", r.detached)
	}
	if len(r.fetched) != 0 || len(r.ran) != 0 {
		t.Fatalf("the hook itself fetched (%v) or ran (%v)", r.fetched, r.ran)
	}
	if !strings.HasPrefix(r.stdout.String(), "Brigade: installing the brigade binary in the background") {
		t.Fatalf("stdout %q", r.stdout.String())
	}
	if _, err := os.Stat(r.target() + ".failed"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stale marker survived a new session start: %v", err)
	}
}

func TestTheWorkerInstallsTheVerifiedAsset(t *testing.T) {
	r := newRig(t)
	if code := r.l.Main([]string{workerFlag}); code != exitOK {
		t.Fatalf("exit %d; stderr %q", code, r.stderr.String())
	}
	if len(r.fetched) != 1 || r.fetched[0] != defaultBase+"/v"+testVersion+"/brigade_"+testVersion+"_windows_amd64.exe =https" {
		t.Fatalf("fetched %v", r.fetched)
	}
	got, err := os.ReadFile(r.target())
	if err != nil || string(got) != string(r.asset) {
		t.Fatalf("installed %q, %v", got, err)
	}
	if len(r.ran) != 0 || r.stdout.Len() != 0 || r.stderr.Len() != 0 {
		t.Fatalf("the worker ran (%v) or spoke (%q, %q)", r.ran, r.stdout.String(), r.stderr.String())
	}
	entries, _ := os.ReadDir(filepath.Dir(r.target()))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestTheWorkerLeavesAMarkerThatThePromptHookReportsOnce(t *testing.T) {
	r := newRig(t)
	r.fetchErr = errors.New("curl: (6) Could not resolve host")
	if code := r.l.Main([]string{workerFlag}); code != exitUnavailable {
		t.Fatalf("worker exit %d, want %d", code, exitUnavailable)
	}
	marker, err := os.ReadFile(r.target() + ".failed")
	if err != nil || !strings.HasPrefix(string(marker), "9 download failed: ") || !strings.Contains(string(marker), "Could not resolve host") {
		t.Fatalf("marker %q, %v", marker, err)
	}
	if code := r.l.Main([]string{"hook", "prompt"}); code != exitUnavailable {
		t.Fatalf("prompt exit %d, want %d", code, exitUnavailable)
	}
	if !strings.HasPrefix(r.stderr.String(), "Brigade: not installed: download failed: ") || !strings.Contains(r.stderr.String(), "/clear or a new session retries the install") {
		t.Fatalf("stderr %q", r.stderr.String())
	}
	r.stderr.Reset()
	if code := r.l.Main([]string{"hook", "prompt"}); code != exitOK || r.stderr.Len() != 0 {
		t.Fatalf("second prompt: exit %d, stderr %q; the marker must be reported once", code, r.stderr.String())
	}
	if len(r.fetched) != 1 {
		t.Fatalf("a prompt hook downloaded: %v", r.fetched)
	}
}

func TestPromptAndSessionEndNeverDownloadOnAColdCache(t *testing.T) {
	for _, sub := range []string{"prompt", "stop", "session-end"} {
		r := newRig(t)
		if code := r.l.Main([]string{"hook", sub}); code != exitOK {
			t.Fatalf("%s: exit %d; stderr %q", sub, code, r.stderr.String())
		}
		if len(r.fetched) != 0 || len(r.detached) != 0 || len(r.ran) != 0 || r.stdout.Len() != 0 || r.stderr.Len() != 0 {
			t.Fatalf("%s on a cold cache did something: %v %v %v %q %q", sub, r.fetched, r.detached, r.ran, r.stdout.String(), r.stderr.String())
		}
	}
}

func TestAHumanOrTheBashToolDownloadsSynchronouslyThenRuns(t *testing.T) {
	r := newRig(t)
	if code := r.l.Main([]string{"sessions"}); code != 42 {
		t.Fatalf("exit %d; stderr %q", code, r.stderr.String())
	}
	if !strings.HasPrefix(r.stderr.String(), "brigade: first use: downloading brigade "+testVersion+" for windows/amd64 from "+defaultBase) {
		t.Fatalf("stderr %q", r.stderr.String())
	}
	if len(r.ran) != 1 || r.ran[0][0] != r.target() || r.ran[0][1] != "sessions" {
		t.Fatalf("ran %v", r.ran)
	}
}

func TestAChecksumMismatchRefusesToInstall(t *testing.T) {
	r := newRig(t)
	r.fetchBad = true
	if code := r.l.Main([]string{"sessions"}); code != exitConfig {
		t.Fatalf("exit %d, want %d", code, exitConfig)
	}
	if !strings.Contains(r.stderr.String(), "checksum mismatch for brigade_"+testVersion+"_windows_amd64.exe") || !strings.Contains(r.stderr.String(), "refusing to install") {
		t.Fatalf("stderr %q", r.stderr.String())
	}
	if _, err := os.Stat(r.target()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a mismatched download was installed: %v", err)
	}
	if len(r.ran) != 0 {
		t.Fatalf("ran %v after a mismatch", r.ran)
	}
}

func TestAPlatformWithoutALineIsAConfigurationFailure(t *testing.T) {
	r := newRig(t)
	r.l.Arch = func() (string, error) { return "arm64", nil }
	if code := r.l.Main([]string{"sessions"}); code != exitConfig {
		t.Fatalf("exit %d, want %d", code, exitConfig)
	}
	if !strings.Contains(r.stderr.String(), "ships no binary for windows/arm64") {
		t.Fatalf("stderr %q", r.stderr.String())
	}
}

func TestTheReleaseBaseRule(t *testing.T) {
	cases := map[string]string{
		"":                                   "=https",
		"https://mirror.example/releases":    "=https",
		"http://127.0.0.1":                   "=http",
		"http://127.0.0.1:8080/x":            "=http",
		"http://localhost/releases":          "=http",
		"http://localhost.attacker.net":      "",
		"http://127.0.0.1.evil.example/x":    "",
		"http://mirror.example/releases":     "",
		"ftp://mirror.example/releases":      "",
		"https://":                           "=https",
		"http://localhost:1234":              "=http",
		"http://localhostx":                  "",
		"http://127.0.0.10":                  "",
		"http://127.0.0.1x/":                 "",
		"HTTPS://mirror.example":             "",
		"https://github.com/appshapes/x/y/z": "=https",
	}
	for value, want := range cases {
		_, proto, err := releaseBase(value)
		switch {
		case want == "" && err == nil:
			t.Errorf("%q: accepted, want refusal", value)
		case want != "" && (err != nil || proto != want):
			t.Errorf("%q: proto %q err %v, want %q", value, proto, err, want)
		}
	}
	r := newRig(t)
	r.l.Env[baseVar] = "http://mirror.example"
	if code := r.l.Main([]string{"sessions"}); code != exitConfig || !strings.Contains(r.stderr.String(), "release base must be https") {
		t.Fatalf("exit %d, stderr %q", code, r.stderr.String())
	}
}

func TestTheVersionAndChecksumPins(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"", "\n", "0.41.0 rc\n", "0.41.0/x\n", "ver$ion\n"} {
		must(t, os.WriteFile(filepath.Join(dir, versionFile), []byte(bad), 0o600))
		if _, err := readVersion(filepath.Join(dir, versionFile)); err == nil {
			t.Errorf("VERSION %q accepted", bad)
		}
	}
	must(t, os.WriteFile(filepath.Join(dir, versionFile), []byte("1.2.3-rc.1+build\n"), 0o600))
	if v, err := readVersion(filepath.Join(dir, versionFile)); err != nil || v != "1.2.3-rc.1+build" {
		t.Errorf("version %q, %v", v, err)
	}
	if _, err := readVersion(filepath.Join(dir, "absent")); err == nil {
		t.Error("a missing VERSION was accepted")
	}
	must(t, os.WriteFile(filepath.Join(dir, checksumsFile), []byte("ABCDEF  brigade_1_windows_amd64.exe\n\nmalformed\n123456  brigade_1_windows_amd64.exe\n"), 0o600))
	if sum, err := expectedSum(filepath.Join(dir, checksumsFile), "brigade_1_windows_amd64.exe"); err != nil || sum != "abcdef" {
		t.Errorf("sum %q, %v: the first line wins, lower-cased", sum, err)
	}
	if sum, err := expectedSum(filepath.Join(dir, checksumsFile), "brigade_1_windows_arm64.exe"); err != nil || sum != "" {
		t.Errorf("absent asset: sum %q, %v", sum, err)
	}
	if _, err := expectedSum(filepath.Join(dir, "absent"), "x"); err == nil {
		t.Error("a missing checksums file was accepted")
	}
}

func TestProfileDirectoriesMustBeAbsolute(t *testing.T) {
	r := newRig(t)
	r.l.Env["APPDATA"] = "Roaming"
	if code := r.l.Main([]string{"sessions"}); code != exitConfig || !strings.Contains(r.stderr.String(), "APPDATA") {
		t.Fatalf("exit %d, stderr %q", code, r.stderr.String())
	}
	r = newRig(t)
	delete(r.l.Env, "LOCALAPPDATA")
	if code := r.l.Main([]string{"sessions"}); code != exitConfig || !strings.Contains(r.stderr.String(), "LOCALAPPDATA") {
		t.Fatalf("exit %d, stderr %q", code, r.stderr.String())
	}
}

func TestEnvironMapLastValueWins(t *testing.T) {
	env := environMap([]string{"A=1", "=ignored", "B", "A=2", "C=x=y"})
	if env["A"] != "2" || env["C"] != "x=y" || len(env) != 2 {
		t.Fatalf("env %v", env)
	}
}

func TestTheDeveloperPointerRunsOnAPlatformTheReleaseDoesNotShip(t *testing.T) {
	// Measured in the proof VM (Windows on ARM, a checksums file from before Windows): the launcher must honour
	// the pointer before it asks the checksums file anything, as the bootstrap does.
	r := newRig(t)
	r.l.Arch = func() (string, error) { return "arm64", nil }
	dev := filepath.Join(t.TempDir(), "brigade-dev.exe")
	must(t, os.WriteFile(dev, []byte("dev build"), 0o600))
	must(t, os.MkdirAll(filepath.Join(r.appdata, "brigade"), 0o700))
	must(t, os.WriteFile(filepath.Join(r.appdata, "brigade", pointerFile), []byte(dev+"\n"), 0o600))
	if code := r.l.Main([]string{"version"}); code != 42 {
		t.Fatalf("exit %d; stderr %q", code, r.stderr.String())
	}
	if len(r.ran) != 1 || r.ran[0][0] != dev {
		t.Fatalf("ran %v, want the pointer's %s", r.ran, dev)
	}
	// And the prompt hook on a cold cache stays silent there too: it never downloads, so no line to miss.
	r = newRig(t)
	r.l.Arch = func() (string, error) { return "arm64", nil }
	if code := r.l.Main([]string{"hook", "prompt"}); code != exitOK || r.stderr.Len() != 0 {
		t.Fatalf("prompt: exit %d, stderr %q", code, r.stderr.String())
	}
}
