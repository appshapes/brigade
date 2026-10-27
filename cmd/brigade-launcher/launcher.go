// Command brigade-launcher is the Windows counterpart of the POSIX bootstrap plugin/bin/brigade (card 133,
// .context/plans/native-windows.md §3.1). Built once, reproducibly, and committed as plugin/bin/brigade.exe
// beside that script, it is what Claude Code on Windows runs for the hooks file's extensionless command
// `${CLAUDE_PLUGIN_ROOT}/bin/brigade` (E11 measured the resolution), for the Bash tool's bare `brigade` (the
// bootstrap execs it under Git Bash), and for a human's terminal.
//
// It does what the bootstrap does, step for step: read the committed VERSION and checksums.txt beside itself,
// resolve the native CPU, honour the developer pointer in the user's own configuration directory, run the
// cached binary when it is there, and otherwise download the pinned release asset with the curl.exe Windows
// ships (an argv array, no shell, no TLS stack of its own), verify its sha256 against the committed line,
// install it atomically and run it with stdin, stdout, stderr and the exit code passed through. The
// cold-cache rules are the bootstrap's (E0-8 correction 1, P5-18): the SessionStart hook starts the download
// in a detached worker and returns at once; `hook prompt` and `hook stop` never download and report the
// worker's final failure once, from the marker it leaves; `hook session-end` stays silent; everything else
// downloads synchronously. Exit codes are the bootstrap's: 9 unavailable (network), 11 configuration (pins,
// platform, integrity).
//
// Where the bootstrap reads HOME and the XDG variables, this reads the Windows profile directories: the
// pointer lives at %APPDATA%\brigade\dev-binary and the cache at %LOCALAPPDATA%\brigade\bin, the same roots
// the brigade binary itself uses on Windows (internal/adapterkit/env.go).
//
// This file is platform-neutral so its tests run everywhere; main.go and platform_windows.go carry the
// Windows glue. It reads nothing through os.Getenv and spawns nothing through a shell.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The bootstrap's exit codes.
const (
	exitOK          = 0
	exitUnavailable = 9  // the network
	exitConfig      = 11 // pins, platform, integrity
)

// The fixed names shared with the bootstrap and the release.
const (
	versionFile   = "VERSION"
	checksumsFile = "checksums.txt"
	pointerFile   = "dev-binary"
	defaultBase   = "https://github.com/appshapes/brigade/releases/download"
	baseVar       = "BRIGADE_RELEASE_BASE_URL"
	workerFlag    = "--install-worker"
	goos          = "windows"
)

// Launcher is one invocation's dependencies. Every function field is a seam the tests fill with a fake and
// platform_windows.go fills with the real thing.
type Launcher struct {
	// Self is this executable's absolute path (the detached worker is this program again).
	Self string
	// BinDir holds VERSION and checksums.txt: the directory Self lives in.
	BinDir string
	// Env is the process environment as a map (os.Getenv is forbidden by the repository's rules).
	Env map[string]string
	// Environ is the process environment as the child receives it, untouched.
	Environ []string
	// Stdout and Stderr are the process streams; the child inherits the real ones through Run.
	Stdout io.Writer
	Stderr io.Writer
	// Arch reports the native CPU, "amd64" or "arm64" (IsWow64Process2, so an amd64 launcher running under
	// Windows' x64 emulation on an ARM machine still downloads the arm64 binary).
	Arch func() (string, error)
	// Fetch downloads url into the file at tmp with curl.exe, or returns what curl said.
	Fetch func(url, tmp string, proto string) error
	// Run starts path with args, the process streams inherited (main.go hands them in), waits, and returns
	// the exit status.
	Run func(path string, args []string, environ []string) (int, error)
	// Detach starts path with args as a detached, windowless process whose streams are NUL, and returns at
	// once.
	Detach func(path string, args []string, environ []string) error
}

// Main is the launcher: it returns the exit status, and never writes to Stdout except the bootstrap's own
// SessionStart line.
func (l *Launcher) Main(args []string) int {
	p, code := l.plan()
	if code != exitOK {
		return code
	}
	if len(args) >= 1 && args[0] == workerFlag {
		return l.worker(p)
	}
	if p.dev != "" {
		return l.run(p.dev, args)
	}
	if isRegular(p.target) {
		return l.run(p.target, args)
	}
	if len(args) >= 2 && args[0] == "hook" {
		switch args[1] {
		case "session-start":
			_ = os.Remove(p.failed) // a new session start is a new attempt: the previous verdict is void
			if err := l.Detach(l.Self, []string{workerFlag}, l.Environ); err != nil {
				return l.fail(exitConfig, "cannot start the download worker: "+err.Error())
			}
			fmt.Fprintln(l.Stdout, "Brigade: installing the brigade binary in the background; team messaging becomes available on your next prompt")
			return exitOK
		case "prompt", "stop":
			return l.reportMarker(p.failed)
		case "session-end":
			return exitOK
		}
	}
	fmt.Fprintf(l.Stderr, "brigade: first use: downloading brigade %s for %s/%s from %s\n", p.version, goos, p.arch, p.base)
	if code := l.install(p); code != exitOK {
		return code
	}
	return l.run(p.target, args)
}

// plan is everything the launcher derives from the committed pins and the environment before any file is
// touched: a failure here is a configuration failure for every invocation.
type plan struct {
	version  string
	arch     string
	dev      string // the developer pointer's executable, or ""
	cacheDir string
	target   string
	failed   string
	asset    string
	expected string
	base     string
	proto    string
	url      string
}

func (l *Launcher) plan() (plan, int) {
	var p plan
	version, err := readVersion(filepath.Join(l.BinDir, versionFile))
	if err != nil {
		return p, l.fail(exitConfig, err.Error())
	}
	p.version = version
	checksums := filepath.Join(l.BinDir, checksumsFile)
	if !isRegular(checksums) {
		return p, l.fail(exitConfig, "missing "+checksums)
	}
	arch, err := l.Arch()
	if err != nil {
		return p, l.fail(exitConfig, err.Error())
	}
	p.arch = arch
	configBase, dataBase, err := profileDirs(l.Env)
	if err != nil {
		return p, l.fail(exitConfig, err.Error())
	}
	dev, err := devPointer(filepath.Join(configBase, "brigade", pointerFile))
	if err != nil {
		return p, l.fail(exitConfig, err.Error())
	}
	p.dev = dev
	p.cacheDir = filepath.Join(dataBase, "brigade", "bin")
	p.target = filepath.Join(p.cacheDir, "brigade-"+version+"-"+goos+"-"+arch+".exe")
	p.failed = p.target + ".failed"
	p.asset = "brigade_" + version + "_" + goos + "_" + arch + ".exe"
	expected, err := expectedSum(checksums, p.asset)
	if err != nil {
		return p, l.fail(exitConfig, err.Error())
	}
	if expected == "" {
		return p, l.fail(exitConfig, fmt.Sprintf("no sha256 for %s in %s: plugin version %s ships no binary for %s/%s (developers: make plugin-dev)", p.asset, checksums, version, goos, arch))
	}
	p.expected = expected
	base, proto, err := releaseBase(l.Env[baseVar])
	if err != nil {
		return p, l.fail(exitConfig, err.Error())
	}
	p.base, p.proto = base, proto
	p.url = base + "/v" + version + "/" + p.asset
	return p, exitOK
}

// worker is the detached SessionStart download (E0-8 correction 1): a failure goes to the marker, not to a
// stderr nobody reads, as `<exit code> <reason>`, written whole and renamed into place after the temporary
// file is gone.
func (l *Launcher) worker(p plan) int {
	if isRegular(p.target) {
		return exitOK
	}
	var sink strings.Builder
	w := &Launcher{Stderr: &sink, Fetch: l.Fetch}
	code := w.install(p)
	if code == exitOK {
		return exitOK
	}
	reason := strings.TrimPrefix(strings.TrimSpace(sink.String()), "brigade: ")
	tmp := p.failed + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d %s\n", code, reason)), 0o600); err == nil {
		_ = os.Rename(tmp, p.failed)
	}
	return code
}

// reportMarker is P5-18's prompt-time report: the worker's final verdict once, on stderr with its exit code,
// then the marker is gone; a warming cache costs a prompt nothing.
func (l *Launcher) reportMarker(failed string) int {
	data, err := os.ReadFile(failed) //nolint:gosec // G304: the marker beside the cache target, named by this program
	if err != nil {
		return exitOK
	}
	_ = os.Remove(failed)
	code := exitUnavailable
	reason := "unknown reason"
	line := strings.TrimSpace(string(data))
	if c, r, ok := strings.Cut(line, " "); ok {
		switch c {
		case "9":
			code = exitUnavailable
		case "11":
			code = exitConfig
		}
		if r = strings.TrimSpace(r); r != "" {
			reason = r
		}
	}
	fmt.Fprintf(l.Stderr, "Brigade: not installed: %s. Team messaging is off in this session; /clear or a new session retries the install\n", reason)
	return code
}

// install downloads, verifies and renames into place. A concurrent first run that won the rename is
// harmless: the file in place is verified against the same line.
func (l *Launcher) install(p plan) int {
	if err := os.MkdirAll(p.cacheDir, 0o700); err != nil {
		return l.fail(exitConfig, "cannot create "+p.cacheDir)
	}
	f, err := os.CreateTemp(p.cacheDir, ".brigade-"+p.version+".*.tmp")
	if err != nil {
		return l.fail(exitConfig, "cannot create a temporary file in "+p.cacheDir)
	}
	tmp := f.Name()
	_ = f.Close()
	defer os.Remove(tmp)
	if err := l.Fetch(p.url, tmp, p.proto); err != nil {
		return l.fail(exitUnavailable, fmt.Sprintf("download failed: %s (%s). Check network access and any HTTPS_PROXY setting, or download the file yourself, verify sha256 %s, and install it as %s", p.url, err.Error(), p.expected, p.target))
	}
	actual, err := fileSum(tmp)
	if err != nil {
		return l.fail(exitUnavailable, "download interrupted before it could be verified")
	}
	if actual != p.expected {
		return l.fail(exitConfig, fmt.Sprintf("checksum mismatch for %s: expected %s, got %s; refusing to install", p.asset, p.expected, actual))
	}
	if err := os.Rename(tmp, p.target); err != nil {
		// Windows refuses to replace a file another process is running. If that file is the verified binary,
		// the other first run won and this one uses it.
		if sum, serr := fileSum(p.target); serr != nil || sum != p.expected {
			return l.fail(exitConfig, "cannot install "+p.target+": "+err.Error())
		}
	}
	_ = os.Remove(p.failed)
	return exitOK
}

func (l *Launcher) run(path string, args []string) int {
	code, err := l.Run(path, args, l.Environ)
	if err != nil {
		return l.fail(exitConfig, "cannot run "+path+": "+err.Error())
	}
	return code
}

func (l *Launcher) fail(code int, msg string) int {
	fmt.Fprintf(l.Stderr, "brigade: %s\n", msg)
	return code
}

// readVersion is the bootstrap's VERSION rule: one token of [0-9A-Za-z.+-].
func readVersion(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the committed pin beside this executable
	if err != nil {
		return "", errors.New("missing " + path)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	version := strings.TrimSpace(line)
	if version == "" || strings.ContainsFunc(version, func(r rune) bool { return !versionRune(r) }) {
		return "", errors.New("invalid version in " + path)
	}
	return version, nil
}

func versionRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '.' || r == '+' || r == '-'
}

// expectedSum returns the sha256 the checksums file carries for asset ("" when it carries none): the first
// line whose second field is the asset, as the bootstrap's awk reads it.
func expectedSum(checksums, asset string) (string, error) {
	data, err := os.ReadFile(checksums) //nolint:gosec // G304: the committed pin beside this executable
	if err != nil {
		return "", errors.New("missing " + checksums)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", nil
}

// profileDirs is the Windows reading of the bootstrap's "absolute values only" rule for HOME and XDG:
// %APPDATA% and %LOCALAPPDATA%, each absolute or refused.
func profileDirs(env map[string]string) (configBase, dataBase string, err error) {
	configBase = env["APPDATA"]
	dataBase = env["LOCALAPPDATA"]
	if !filepath.IsAbs(configBase) {
		return "", "", errors.New("APPDATA is unset or not an absolute path")
	}
	if !filepath.IsAbs(dataBase) {
		return "", "", errors.New("LOCALAPPDATA is unset or not an absolute path")
	}
	return configBase, dataBase, nil
}

// devPointer reads the developer override: a file in the user's own configuration directory naming an
// executable by absolute path, never an environment variable (a trusted repository's settings can set those).
func devPointer(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the user's own pointer file, at a fixed name
	if err != nil {
		return "", nil //nolint:nilerr // an absent pointer is the normal case
	}
	line, _, _ := strings.Cut(string(data), "\n")
	dev := strings.TrimSpace(line)
	if filepath.IsAbs(dev) && isRegular(dev) {
		return dev, nil
	}
	return "", fmt.Errorf("%s does not name an executable absolute path: '%s'", path, dev)
}

// releaseBase is the bootstrap's rule: https anywhere, http only for a loopback host spelled out whole (bare,
// with a port, or with a path), never a prefix match.
func releaseBase(value string) (base, proto string, err error) {
	base = value
	if base == "" {
		base = defaultBase
	}
	switch {
	case strings.HasPrefix(base, "https://"):
		return base, "=https", nil
	case loopback(base, "http://127.0.0.1"), loopback(base, "http://localhost"):
		return base, "=http", nil
	}
	return "", "", errors.New("release base must be https (http only for loopback): " + base)
}

func loopback(base, host string) bool {
	return base == host || strings.HasPrefix(base, host+":") || strings.HasPrefix(base, host+"/")
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the download in the cache directory, or the installed target
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isRegular(path string) bool {
	fi, err := os.Stat(path) //nolint:gosec // G703: the cache target, the pointer's path or the committed pins, every one named by this program
	return err == nil && fi.Mode().IsRegular()
}

// environMap is os.Environ as a map: the last value wins, as Windows' own lookup does.
func environMap(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			env[k] = v
		}
	}
	return env
}
