package bootstrap_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/testutil"
)

// The bootstrap under test is plugin/bin/brigade (plan 6.2). Every case here copies that real file into a
// temp tree and runs it as a subprocess with an environment built from scratch: PATH, HOME, XDG_CONFIG_HOME,
// XDG_DATA_HOME, BRIGADE_RELEASE_BASE_URL and TMPDIR, and nothing else. The developer's own
// ~/.local/share/brigade cache, their ~/.config/brigade/dev-binary pointer and the real GitHub release are
// therefore unreachable from this file by construction, not by care.

const (
	// fixtureVersion is what the fixture plugin/bin/VERSION pins. It is deliberately not a real Brigade
	// version, so a case that somehow escaped the temp tree would still not collide with a released asset.
	fixtureVersion = "9.9.9"

	// fakeRelease is the "release binary" the test server hands out: it prints its argv on one line and then
	// copies stdin to stdout, so a single run witnesses the exec, the argument pass-through and the stdin
	// pass-through at once.
	fakeRelease = "#!/bin/sh\nprintf 'ARGV:'\nfor a in \"$@\"; do printf ' %s' \"$a\"; done\nprintf '\\n'\ncat\n"

	// wrongRelease is what the /bad route serves: different bytes under the same asset name, which is what a
	// substituted or corrupted download looks like to the script.
	wrongRelease = "#!/bin/sh\necho IMPOSTOR\n"

	// hookContextLine is the one line the SessionStart hook prints on a cold cache (E0-8 correction 1).
	hookContextLine = "Brigade: installing the brigade binary in the background; team messaging becomes available on your next prompt"
)

// assetName is the release asset for the host, in the layout 6.2 fixes: brigade_<version>_<os>_<arch>.
func assetName() string {
	return "brigade_" + fixtureVersion + "_" + runtime.GOOS + "_" + runtime.GOARCH
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------------------------------------
// the release server

// server stands in for https://github.com/appshapes/brigade/releases/download. It counts every request it
// receives, because "the second run makes no request" is only provable by counting.
type server struct {
	*httptest.Server
	mu sync.Mutex

	hits map[string]int
	// delay is how long the good route holds each response before answering. It exists so that "the
	// SessionStart hook returned before the download finished" is a measurement and not an inference: on a
	// loopback server that answers in microseconds, a synchronous first use and a detached one are
	// indistinguishable, and every timing assertion about the hook is vacuous.
	delay time.Duration
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{hits: make(map[string]int)}
	good := "/v" + fixtureVersion + "/" + assetName()
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		switch r.URL.Path {
		case good:
			if d := s.responseDelay(); d > 0 {
				time.Sleep(d)
			}
			_, _ = w.Write([]byte(fakeRelease))
		case "/bad" + good:
			_, _ = w.Write([]byte(wrongRelease))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// setDelay makes every later answer on the good route take at least d.
func (s *server) setDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

func (s *server) responseDelay() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delay
}

// requests is the total number of HTTP requests this server has answered, and the per-path breakdown, taken
// under the lock so that a failure message can print it without racing the handler.
func (s *server) requests() (int, map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	seen := make(map[string]int, len(s.hits))
	for k, v := range s.hits {
		n += v
		seen[k] = v
	}
	return n, seen
}

// hostPort is the server's "127.0.0.1:<port>", for building an https:// base against the same listener.
func (s *server) hostPort() string { return strings.TrimPrefix(s.URL, "http://") }

// ---------------------------------------------------------------------------------------------------------
// the fixture

type fixture struct {
	t      *testing.T
	root   string
	script string // <root>/plugin/bin/brigade -- the copy under test
	home   string
	config string
	data   string
	tmpdir string
	srv    *server
	base   string   // BRIGADE_RELEASE_BASE_URL
	path   string   // PATH handed to the child
	shell  string   // the shell that runs the script
	extra  []string // appended last, so it overrides anything above
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		t:      t,
		root:   root,
		script: filepath.Join(root, "plugin", "bin", "brigade"),
		home:   filepath.Join(root, "home"),
		config: filepath.Join(root, "config"),
		data:   filepath.Join(root, "data"),
		tmpdir: filepath.Join(root, "tmp"),
		path:   os.Getenv("PATH"),
		shell:  "/bin/sh",
	}
	for _, d := range []string{filepath.Dir(f.script), f.home, f.config, f.data, f.tmpdir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("fixture: mkdir %s: %v", d, err)
		}
	}
	src := filepath.Join(testutil.RepoRoot(t), "plugin", "bin", "brigade")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("fixture: reading %s: %v", src, err)
	}
	//nolint:gosec // G306: the file under test must be executable; 0700 keeps it owner-only
	if err := os.WriteFile(f.script, body, 0o700); err != nil {
		t.Fatalf("fixture: writing %s: %v", f.script, err)
	}
	f.srv = newServer(t)
	f.base = f.srv.URL
	f.writeVersion(fixtureVersion)
	f.writeChecksums(sha256hex(fakeRelease))
	return f
}

func (f *fixture) writeVersion(v string) {
	f.t.Helper()
	p := filepath.Join(f.root, "plugin", "bin", "VERSION")
	if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
		f.t.Fatalf("fixture: writing %s: %v", p, err)
	}
}

// writeChecksums writes the four-line file the release publishes. Only the host's line has to carry a real
// hash; the other three are what the script must ignore.
func (f *fixture) writeChecksums(hostSum string) {
	f.t.Helper()
	var b strings.Builder
	for _, tgt := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		sum := strings.Repeat("a", 64)
		if tgt == runtime.GOOS+"_"+runtime.GOARCH {
			sum = hostSum
		}
		fmt.Fprintf(&b, "%s  brigade_%s_%s\n", sum, fixtureVersion, tgt)
	}
	f.writeChecksumsRaw(b.String())
}

func (f *fixture) writeChecksumsRaw(s string) {
	f.t.Helper()
	p := filepath.Join(f.root, "plugin", "bin", "checksums.txt")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		f.t.Fatalf("fixture: writing %s: %v", p, err)
	}
}

// env is the child's whole environment. The test process's own environment is never copied: PATH is the one
// value carried over, explicitly, because nothing can be executed by name without it.
func (f *fixture) env() []string {
	return append([]string{
		"PATH=" + f.path,
		"HOME=" + f.home,
		"XDG_CONFIG_HOME=" + f.config,
		"XDG_DATA_HOME=" + f.data,
		"BRIGADE_RELEASE_BASE_URL=" + f.base,
		"TMPDIR=" + f.tmpdir,
	}, f.extra...)
}

// cacheDir is where the bootstrap installs, given the data directory it actually resolved.
func (f *fixture) cacheDir(dataHome string) string {
	return filepath.Join(dataHome, "brigade", "bin")
}

func (f *fixture) cachePath(dataHome string) string {
	return filepath.Join(f.cacheDir(dataHome), "brigade-"+fixtureVersion+"-"+runtime.GOOS+"-"+runtime.GOARCH)
}

type result struct {
	code   int
	stdout string
	stderr string
	took   time.Duration
}

func (f *fixture) run(stdin string, args ...string) result {
	f.t.Helper()
	return f.runAs(f.shell, f.script, stdin, args...)
}

func (f *fixture) runAs(shell, script, stdin string, args ...string) result {
	f.t.Helper()
	//nolint:gosec // G204: a fixed shell name and this test's own temp script path; no shell string is built
	cmd := exec.CommandContext(f.t.Context(), shell, append([]string{script}, args...)...)
	cmd.Env = f.env()
	cmd.Dir = f.root
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	start := time.Now()
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errb.String(), took: time.Since(start)}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	default:
		f.t.Fatalf("running %s %s: %v", shell, script, err)
	}
	return r
}

// ---------------------------------------------------------------------------------------------------------
// assertions

func wantCode(t *testing.T, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %q\nstderr: %q", r.code, code, r.stdout, r.stderr)
	}
}

func wantStderrContains(t *testing.T, r result, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(r.stderr, s) {
			t.Fatalf("stderr does not mention %q\nstderr: %q", s, r.stderr)
		}
	}
}

func wantArgv(t *testing.T, r result, args ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("ARGV:")
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(a)
	}
	want := b.String()
	got, _, _ := strings.Cut(r.stdout, "\n")
	if got != want {
		t.Fatalf("first stdout line is %q, want %q (full stdout %q, stderr %q)", got, want, r.stdout, r.stderr)
	}
}

// wantInstalled asserts the cache file is there with mode 0755 and that no download temp file survives.
func wantInstalled(t *testing.T, f *fixture, dataHome string) {
	t.Helper()
	p := f.cachePath(dataHome)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("no cached binary at %s: %v", p, err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Fatalf("cached binary %s has mode %#o, want 0755", p, got)
	}
	wantNoTempFile(t, f, dataHome)
}

func wantNoTempFile(t *testing.T, f *fixture, dataHome string) {
	t.Helper()
	entries, err := os.ReadDir(f.cacheDir(dataHome))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("reading the cache directory: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".brigade-") {
			t.Fatalf("a download temp file survived: %s", filepath.Join(f.cacheDir(dataHome), e.Name()))
		}
	}
}

func wantNothingInstalled(t *testing.T, f *fixture, dataHome string) {
	t.Helper()
	entries, err := os.ReadDir(f.cacheDir(dataHome))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("reading the cache directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the cache directory is not empty: %v", names)
	}
}

func wantRequests(t *testing.T, s *server, n int) {
	t.Helper()
	got, seen := s.requests()
	if got != n {
		t.Fatalf("the server answered %d request(s), want %d (%v)", got, n, seen)
	}
}

// writeExecutable drops a #!/bin/sh script at path and makes it runnable.
func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	//nolint:gosec // G306: an executable test fixture; 0700 keeps it owner-only
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------------------------------------
// the download path

func TestBootstrapDownload(t *testing.T) {
	t.Parallel()

	t.Run("first run downloads once, installs 0755 and execs", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		r := f.run("", "sessions", "--json")
		wantCode(t, r, 0)
		wantArgv(t, r, "sessions", "--json")
		wantRequests(t, f.srv, 1)
		wantInstalled(t, f, f.data)
	})

	t.Run("second run makes no request", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		wantCode(t, f.run("", "whoami"), 0)
		wantRequests(t, f.srv, 1)

		r := f.run("", "whoami")
		wantCode(t, r, 0)
		wantArgv(t, r, "whoami")
		wantRequests(t, f.srv, 1)
		if strings.Contains(r.stderr, "first use") {
			t.Fatalf("the warm-cache run announced a download: %q", r.stderr)
		}
	})

	t.Run("stdin passes through byte for byte", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		body := "line one\nline two\n\nline four with  spaces \t and a tab\n"
		r := f.run(body, "send", "peer-1")
		wantCode(t, r, 0)
		wantArgv(t, r, "send", "peer-1")
		_, rest, _ := strings.Cut(r.stdout, "\n")
		if rest != body {
			t.Fatalf("stdin arrived as %q, want %q", rest, body)
		}
	})

	t.Run("wrong bytes: exit 11 and nothing installed", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.base = f.srv.URL + "/bad" // the same asset name, different bytes
		r := f.run("", "whoami")
		wantCode(t, r, 11)
		wantStderrContains(t, r, "checksum mismatch")
		wantRequests(t, f.srv, 1)
		wantNothingInstalled(t, f, f.data)
	})

	t.Run("download failure: exit 9 naming the URL and the install path", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.base = f.srv.URL + "/404" // a route the server answers 404
		r := f.run("", "whoami")
		wantCode(t, r, 9)
		wantStderrContains(t, r,
			f.base+"/v"+fixtureVersion+"/"+assetName(),
			f.cachePath(f.data),
		)
		wantNothingInstalled(t, f, f.data)
	})

	t.Run("no curl and no wget: exit 11 naming the URL, the sha256 and the target", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.path = toolPath(t, f.root, nil)
		r := f.run("", "whoami")
		wantCode(t, r, 11)
		wantStderrContains(t, r,
			"curl or wget",
			f.base+"/v"+fixtureVersion+"/"+assetName(),
			sha256hex(fakeRelease),
			f.cachePath(f.data),
		)
		wantRequests(t, f.srv, 0)
		wantNothingInstalled(t, f, f.data)
	})
}

// toolPath builds a PATH directory holding symlinks to exactly the tools the bootstrap is allowed to need —
// and never curl or wget, so the "neither downloader is installed" branch can be reached on a host that has
// both. extra names additional tools to link.
func toolPath(t *testing.T, root string, extra []string) string {
	t.Helper()
	dir := filepath.Join(root, "restricted-bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	want := []string{"sh", "uname", "awk", "mktemp", "mkdir", "chmod", "mv", "rm", "readlink", "cat", "shasum", "sha256sum"}
	want = append(want, extra...)
	linked := 0
	for _, tool := range want {
		src, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if err := os.Symlink(src, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("symlinking %s: %v", tool, err)
		}
		linked++
	}
	if linked == 0 {
		t.Fatalf("no tool at all could be linked into %s", dir)
	}
	for _, hasher := range []string{"shasum", "sha256sum"} {
		if _, err := os.Lstat(filepath.Join(dir, hasher)); err == nil {
			return dir
		}
	}
	t.Skip("neither shasum nor sha256sum is on PATH: the download branch cannot be reached")
	return dir
}

// ---------------------------------------------------------------------------------------------------------
// the developer pointer file and the absolute-path rules

func TestBootstrapOverrides(t *testing.T) {
	t.Parallel()

	t.Run("the pointer file wins over any download", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		dev := filepath.Join(f.root, "dev", "brigade")
		writeExecutable(t, dev, "#!/bin/sh\necho DEV \"$@\"\n")
		writeExecutable(t, filepath.Join(f.config, "brigade", "dev-binary"), dev+"\n")

		r := f.run("", "whoami")
		wantCode(t, r, 0)
		if got := strings.TrimRight(r.stdout, "\n"); got != "DEV whoami" {
			t.Fatalf("stdout is %q, want %q", got, "DEV whoami")
		}
		wantRequests(t, f.srv, 0)
		wantNothingInstalled(t, f, f.data)
	})

	t.Run("a pointer naming a non-executable file is exit 11", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		plain := filepath.Join(f.root, "dev", "not-executable")
		if err := os.MkdirAll(filepath.Dir(plain), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(plain, []byte("not a binary\n"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", plain, err)
		}
		writeExecutable(t, filepath.Join(f.config, "brigade", "dev-binary"), plain+"\n")

		r := f.run("", "whoami")
		wantCode(t, r, 11)
		wantStderrContains(t, r, "does not name an executable absolute path")
		wantRequests(t, f.srv, 0)
	})

	t.Run("a pointer naming a relative path is exit 11", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		writeExecutable(t, filepath.Join(f.config, "brigade", "dev-binary"), "bin/brigade\n")

		r := f.run("", "whoami")
		wantCode(t, r, 11)
		wantStderrContains(t, r, "does not name an executable absolute path", "bin/brigade")
		wantRequests(t, f.srv, 0)
	})

	t.Run("a relative XDG_DATA_HOME is ignored in favour of $HOME/.local/share", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.extra = append(f.extra, "XDG_DATA_HOME=data-rel")

		r := f.run("", "whoami")
		wantCode(t, r, 0)
		wantArgv(t, r, "whoami")
		wantInstalled(t, f, filepath.Join(f.home, ".local", "share"))
		if _, err := os.Stat(filepath.Join(f.root, "data-rel")); err == nil {
			t.Fatalf("the relative XDG_DATA_HOME was honoured: %s exists", filepath.Join(f.root, "data-rel"))
		}
	})

	t.Run("a relative XDG_CONFIG_HOME is ignored for the pointer lookup", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.extra = append(f.extra, "XDG_CONFIG_HOME=config-rel")
		dev := filepath.Join(f.root, "dev", "brigade")
		writeExecutable(t, dev, "#!/bin/sh\necho DEV \"$@\"\n")
		// The pointer the script must find is the DEFAULT one under $HOME/.config, not one under the
		// relative value or under the fixture's own XDG_CONFIG_HOME.
		writeExecutable(t, filepath.Join(f.home, ".config", "brigade", "dev-binary"), dev+"\n")
		writeExecutable(t, filepath.Join(f.config, "brigade", "dev-binary"), "/nonexistent/decoy\n")

		r := f.run("", "whoami")
		wantCode(t, r, 0)
		if got := strings.TrimRight(r.stdout, "\n"); got != "DEV whoami" {
			t.Fatalf("stdout is %q, want %q", got, "DEV whoami")
		}
		wantRequests(t, f.srv, 0)
	})

	t.Run("a relative HOME becomes /nonexistent and fails with exit 11", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("running as root: /nonexistent would be creatable")
		}
		f := newFixture(t)
		f.extra = append(f.extra, "HOME=home-rel", "XDG_CONFIG_HOME=config-rel", "XDG_DATA_HOME=data-rel")

		r := f.run("", "whoami")
		wantCode(t, r, 11)
		wantStderrContains(t, r, "/nonexistent")
		wantRequests(t, f.srv, 0)
	})
}

// ---------------------------------------------------------------------------------------------------------
// the pins, the platform and the base URL

func TestBootstrapPins(t *testing.T) {
	t.Parallel()

	t.Run("the pre-release state exits 11 with the developer hint", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.writeVersion("0.0.0")
		f.writeChecksumsRaw("")

		r := f.run("", "whoami")
		wantCode(t, r, 11)
		wantStderrContains(t, r, "make plugin-dev", "0.0.0")
		wantRequests(t, f.srv, 0)
		wantNothingInstalled(t, f, f.data)
	})

	t.Run("an http base that is not loopback is refused", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.base = "http://mirror.example.com/brigade"

		r := f.run("", "whoami")
		wantCode(t, r, 11)
		wantStderrContains(t, r, "release base must be https", f.base)
		wantRequests(t, f.srv, 0)
	})

	// The loopback exemption is a whole-host rule, not a prefix one. Written as `http://127.0.0.1*` and
	// `http://localhost*` — which is what plan 6.2 prints — the case arms also match any host that merely
	// BEGINS with a loopback name, so an attacker-controlled `127.0.0.1.evil.example` was accepted as
	// loopback and its release binary fetched over plaintext http. Both hosts below carry the test server's
	// own port, so a script that accepts them really would reach out; the request counter is what proves it
	// does not.
	for _, host := range []string{"127.0.0.1.evil.example", "localhost.attacker.net"} {
		t.Run("an http base whose host only starts with a loopback name is refused: "+host, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			_, port, _ := strings.Cut(f.srv.hostPort(), ":")
			f.base = "http://" + host + ":" + port

			r := f.run("", "whoami")
			wantCode(t, r, 11)
			wantStderrContains(t, r, "release base must be https", f.base)
			wantRequests(t, f.srv, 0)
			wantNothingInstalled(t, f, f.data)
		})
	}

	t.Run("an https base against the plain-http server fails the download", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		// The same listener, addressed as https. What this pins down is that the base is carried into the
		// fetch verbatim and an https:// base is never silently downgraded to the plain-http server behind
		// it: the listener must answer nothing at all, and nothing may be installed. (It is NOT a test of
		// curl's `--proto '=https'`: removing that flag leaves this case green, because an https request to
		// a plain-http listener fails in the TLS handshake either way. The flag is what stops an https base
		// from being followed to an http redirect, which needs a real certificate to exercise.)
		f.base = "https://" + f.srv.hostPort()

		r := f.run("", "whoami")
		wantCode(t, r, 9)
		wantStderrContains(t, r, "download failed", f.base+"/v"+fixtureVersion+"/"+assetName())
		wantRequests(t, f.srv, 0)
		wantNothingInstalled(t, f, f.data)
	})

	t.Run("an unsupported OS is refused before anything else", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		fake := filepath.Join(f.root, "fake-bin")
		writeExecutable(t, filepath.Join(fake, "uname"), "#!/bin/sh\necho 'Windows_NT x86_64'\n")
		f.path = fake + string(os.PathListSeparator) + f.path

		r := f.run("", "whoami")
		wantCode(t, r, 11)
		wantStderrContains(t, r, "unsupported OS")
		wantRequests(t, f.srv, 0)
	})

	t.Run("a symlink resolves to the plugin's own VERSION", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		link := filepath.Join(f.root, "local", "bin", "brigade")
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Symlink(f.script, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		r := f.runAs(f.shell, link, "", "whoami")
		wantCode(t, r, 0)
		wantArgv(t, r, "whoami")
		// The cache name carries the version read through the symlink, so this is the assertion that the
		// script found the plugin's VERSION and not something beside the symlink.
		wantInstalled(t, f, f.data)
	})
}

// ---------------------------------------------------------------------------------------------------------
// the SessionStart hook: E0-8 correction 1

func TestBootstrapSessionStartHook(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// The whole point of the E0-8 correction is that the hook does NOT wait for the download, and against a
	// loopback server that answers in microseconds that is unobservable: a fully synchronous hook returns in
	// 20 ms too. So the server is made slow first. A synchronous first use cannot return in less than
	// hookDownloadDelay; a detached one returns at once and leaves the cache file missing on the way out.
	const hookDownloadDelay = 2 * time.Second
	f.srv.setDelay(hookDownloadDelay)

	p := f.cachePath(f.data)
	r := f.run("", "hook", "session-start")
	wantCode(t, r, 0)
	if got := strings.TrimRight(r.stdout, "\n"); got != hookContextLine {
		t.Fatalf("stdout is %q, want exactly the context line %q", got, hookContextLine)
	}
	if r.took >= hookDownloadDelay/2 {
		t.Fatalf("the hook took %s, but the server holds every answer for %s: the download was not detached",
			r.took, hookDownloadDelay)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatalf("%s already existed when the hook returned: the hook waited for the download", p)
	}

	// The install happens behind the hook. Poll rather than sleep: the point of the measurement is that the
	// hook returned before this finished, not how long this takes.
	deadline := time.Now().Add(hookDownloadDelay + 10*time.Second)
	for {
		if fi, err := os.Stat(p); err == nil {
			if got := fi.Mode().Perm(); got != 0o755 {
				t.Fatalf("cached binary %s has mode %#o, want 0755", p, got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the detached download did not install %s in time", p)
		}
		time.Sleep(20 * time.Millisecond)
	}
	wantNoTempFile(t, f, f.data)

	// The next SessionStart hook finds a warm cache and execs the binary itself, with no further request.
	second := f.run("", "hook", "session-start")
	wantCode(t, second, 0)
	wantArgv(t, second, "hook", "session-start")
	wantRequests(t, f.srv, 1)
}

// TestBootstrapSessionStartHookPreRelease pins the ordering the background branch depends on: every
// precondition is still checked synchronously, so `hook session-start` in the pre-release state fails loudly
// with exit 11 instead of returning 0 and losing the diagnostic inside a detached job.
func TestBootstrapSessionStartHookPreRelease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.writeVersion("0.0.0")
	f.writeChecksumsRaw("")

	r := f.run("", "hook", "session-start")
	wantCode(t, r, 11)
	wantStderrContains(t, r, "make plugin-dev")
	if r.stdout != "" {
		t.Fatalf("stdout is %q, want nothing: a failed hook must not print the context line", r.stdout)
	}
	wantRequests(t, f.srv, 0)
	wantNothingInstalled(t, f, f.data)
}

// ---------------------------------------------------------------------------------------------------------
// syntax, and the second shell

func TestBootstrapSyntax(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), "plugin", "bin", "brigade")

	for _, tc := range []struct {
		tool string
		args []string
	}{
		{"sh", []string{"-n"}},
		{"bash", []string{"-n"}},
		{"zsh", []string{"-n"}},
		{"dash", []string{"-n"}},
		{"shellcheck", []string{"-s", "sh"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			t.Parallel()
			bin, err := exec.LookPath(tc.tool)
			if err != nil {
				if tc.tool == "sh" {
					t.Fatalf("no sh on PATH: %v", err)
				}
				t.Skipf("%s is not installed on this host; CI enforces shellcheck through make plugin-check", tc.tool)
			}
			//nolint:gosec // G204: a looked-up tool name from the fixed table above and a fixed script path
			cmd := exec.CommandContext(t.Context(), bin, append(append([]string{}, tc.args...), script)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s %v %s failed: %v\n%s", tc.tool, tc.args, script, err, out)
			}
		})
	}
}

func TestBootstrapUnderDash(t *testing.T) {
	t.Parallel()
	dash, err := exec.LookPath("dash")
	if err != nil {
		t.Skip("dash is not installed on this host; the busybox ash leg is scripts/ci/bootstrap-alpine.sh")
	}
	f := newFixture(t)
	f.shell = dash

	r := f.run("", "sessions")
	wantCode(t, r, 0)
	wantArgv(t, r, "sessions")
	wantRequests(t, f.srv, 1)
	wantInstalled(t, f, f.data)
}
