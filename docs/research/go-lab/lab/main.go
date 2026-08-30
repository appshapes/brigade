//go:build unix

// Lab: small experiments for the Brigade Go toolchain digest
// (2026-08-30, Go 1.27.0, macOS arm64; the linux/arm64 build runs in Docker).
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/invopop/jsonschema"
	stjson "github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lab <cmd> [args]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "json2":
		err = cmdJSON2()
	case "ndjson":
		err = cmdNDJSON()
	case "unix":
		err = cmdUnix()
	case "exec-cap":
		err = cmdExecCap()
	case "exec-timeout":
		err = cmdExecTimeout()
	case "detach":
		err = cmdDetach(args)
	case "child-report":
		err = cmdChildReport()
	case "kill0":
		err = cmdKill0(args)
	case "lstart":
		err = cmdLstart(args)
	case "flock":
		err = cmdFlock(args)
	case "atomic":
		err = cmdAtomic()
	case "slog":
		err = cmdSlog()
	case "xdg":
		err = cmdXDG()
	case "signals":
		err = cmdSignals()
	case "sigwait":
		err = cmdSigwait()
	case "schema":
		err = cmdSchema()
	case "stdin":
		err = cmdStdin()
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// ---------- encoding/json v1 vs v2 ----------

type wire struct {
	Body    string    `json:"body"`
	Summary *string   `json:"summary,omitzero"`
	Count   int       `json:"count,omitzero"`
	When    time.Time `json:"when,omitzero"`
}

func cmdJSON2() error {
	inputs := []string{
		`{"body":"hi","unknown":1}`,
		`{"Body":"case"}`,
		`{"body":"a","body":"b"}`,
		`{"body":"x","summary":null}`,
		`{"body":"x","count":"7"}`,
	}
	for _, in := range inputs {
		var v1, v2 wire
		e1 := json.Unmarshal([]byte(in), &v1)
		e2 := jsonv2.Unmarshal([]byte(in), &v2)
		fmt.Printf("input %s\n  v1: %+v err=%v\n  v2: %+v err=%v\n", in, v1, e1, v2, e2)
	}
	var v wire
	err := jsonv2.Unmarshal([]byte(`{"body":"hi","unknown":1}`), &v, jsonv2.RejectUnknownMembers(true))
	fmt.Println("v2 RejectUnknownMembers:", err)
	b1, _ := json.Marshal(wire{Body: "m"})
	b2, _ := jsonv2.Marshal(wire{Body: "m"})
	fmt.Printf("marshal v1: %s\nmarshal v2: %s\n", b1, b2)
	for _, in := range []string{"{\"body\":\"t\"}\n", "{\"body\":\"t\"} {\"body\":\"u\"}", "{\"body\":\"t\"} junk", ""} {
		var w wire
		err := jsonv2.UnmarshalRead(strings.NewReader(in), &w)
		fmt.Printf("v2 UnmarshalRead %q -> %+v err=%v\n", in, w, err)
	}
	for _, in := range []string{"{\"body\":\"t\"}\n", "{\"body\":\"t\"} junk", ""} {
		var w wire
		dec := json.NewDecoder(strings.NewReader(in))
		err := dec.Decode(&w)
		_, terr := dec.Token()
		fmt.Printf("v1 Decode %q -> err=%v trailing-token err=%v (io.EOF means clean)\n", in, err, terr)
	}
	// HTML escaping and U+2028: v1 escapes <>& and U+2028/2029 by default; v2 does not escape HTML.
	s := "<x> &  "
	b1, _ = json.Marshal(s)
	b2, _ = jsonv2.Marshal(s)
	fmt.Printf("string v1: %s\nstring v2: %s\n", b1, b2)
	return nil
}

// ---------- NDJSON reader with a 1 MiB line cap that DROPS overlong lines ----------

const maxLine = 1 << 20

// readNDJSON calls fn for every complete line of at most maxLine bytes
// (excluding the newline). Longer lines are skipped and counted; the reader
// keeps going, unlike bufio.Scanner, which stops permanently on ErrTooLong.
func readNDJSON(r io.Reader, fn func(line []byte)) (accepted, dropped int, err error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var buf []byte
	overflow := false
	for {
		chunk, rerr := br.ReadSlice('\n')
		if len(chunk) > 0 && !overflow {
			if len(buf)+len(chunk) > maxLine+1 {
				overflow = true
				buf = buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch {
		case errors.Is(rerr, bufio.ErrBufferFull):
			continue
		case rerr == nil:
			if overflow {
				dropped++
				overflow = false
			} else if line := bytes.TrimRight(buf, "\r\n"); len(line) > 0 {
				accepted++
				fn(line)
			}
			buf = buf[:0]
		case errors.Is(rerr, io.EOF):
			if overflow {
				dropped++
			} else if line := bytes.TrimRight(buf, "\r\n"); len(bytes.TrimSpace(line)) > 0 {
				accepted++
				fn(line)
			}
			return accepted, dropped, nil
		default:
			return accepted, dropped, rerr
		}
	}
}

func cmdNDJSON() error {
	var in bytes.Buffer
	in.WriteString(`{"n":1}` + "\n")
	in.WriteString(strings.Repeat("x", 2<<20) + "\n")       // 2 MiB: dropped
	in.WriteString(`{"n":2}` + "\n")                          //
	in.WriteString(strings.Repeat("y", (1<<20)-1) + "\n")     // 1 MiB - 1: kept
	in.WriteString(`{"n":3}` + "\n")                          //
	in.WriteString(strings.Repeat("z", (1<<20)+1) + "\n")     // 1 MiB + 1: dropped
	in.WriteString(strings.Repeat("w", 1<<20) + "\n")         // exactly 1 MiB: kept
	in.WriteString(`{"n":4}`)                                 // no trailing newline: kept
	data := in.Bytes()

	acc, drop, err := readNDJSON(bytes.NewReader(data), func(line []byte) {
		if len(line) < 40 {
			fmt.Printf("  line: %s\n", line)
		} else {
			fmt.Printf("  line: %d bytes\n", len(line))
		}
	})
	fmt.Printf("bufio.Reader variant: accepted=%d dropped=%d err=%v\n", acc, drop, err)

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	n := 0
	for sc.Scan() {
		n++
	}
	fmt.Printf("bufio.Scanner variant: scanned=%d err=%v (stops permanently at the first overlong line)\n", n, sc.Err())
	return nil
}

// ---------- unix domain socket client (the Claude Code inbox socket shape) ----------

func cmdUnix() error {
	dir, err := os.MkdirTemp("", "brigade-sock")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer ln.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	got := make(chan []string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- []string{"accept: " + err.Error()}
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		var lines []string
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		got <- lines
	}()
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	fmt.Printf("lstat: socket=%v symlink=%v mode=%o uid=%d (self %d)\n",
		st.Mode()&os.ModeSocket != 0, st.Mode()&os.ModeSymlink != 0, st.Mode().Perm(), st.Sys().(*syscall.Stat_t).Uid, os.Getuid())
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(context.Background(), "unix", path)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	auth, _ := json.Marshal(map[string]any{"type": "auth", "token": "t0k3n"})
	user, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hello\nworld   <x>"}})
	frame := append(append(auth, '\n'), append(user, '\n')...)
	if _, err := conn.Write(frame); err != nil {
		return err
	}
	if err := conn.Close(); err != nil {
		return err
	}
	lines := <-got
	fmt.Printf("server received %d lines:\n", len(lines))
	for _, l := range lines {
		fmt.Println("  ", l)
	}
	_, err = d.Dial("unix", filepath.Join(dir, "missing.sock"))
	fmt.Println("dial missing:", err, "| ENOENT?", errors.Is(err, syscall.ENOENT))
	return nil
}

// ---------- spawning the adapter: no shell, stdin JSON, capped stdout, context timeout ----------

var errOverflow = errors.New("child stdout exceeded cap")

type capBuf struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (c *capBuf) Write(p []byte) (int, error) {
	if c.overflow {
		return 0, errOverflow
	}
	if c.buf.Len()+len(p) > c.limit {
		c.overflow = true
		c.cancel() // kill the child: nothing more is wanted from it
		return 0, errOverflow
	}
	return c.buf.Write(p)
}

type runResult struct {
	Out      []byte
	Err      error
	Overflow bool
	Elapsed  time.Duration
	Exit     int
	Signaled bool
}

func runChild(timeout time.Duration, limit int, stdin []byte, name string, args ...string) runResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cb := &capBuf{limit: limit, cancel: cancel}
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Stdout = cb
	cmd.Stderr = io.Discard
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME")}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 1 * time.Second
	start := time.Now()
	err := cmd.Run()
	r := runResult{Out: cb.buf.Bytes(), Err: err, Overflow: cb.overflow, Elapsed: time.Since(start)}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.Exit = ee.ExitCode()
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			r.Signaled = ws.Signaled()
		}
	}
	return r
}

func cmdExecCap() error {
	r := runChild(10*time.Second, 4<<20, nil, "head", "-c", "100", "/dev/zero")
	fmt.Printf("small: len=%d overflow=%v err=%v elapsed=%s\n", len(r.Out), r.Overflow, r.Err, r.Elapsed.Round(time.Millisecond))
	r = runChild(10*time.Second, 4<<20, nil, "head", "-c", "60000000", "/dev/zero")
	fmt.Printf("60MB: len=%d overflow=%v err=%v exit=%d signaled=%v elapsed=%s\n", len(r.Out), r.Overflow, r.Err, r.Exit, r.Signaled, r.Elapsed.Round(time.Millisecond))
	r = runChild(10*time.Second, 4<<20, []byte(`{"x":1}`+"\n"), "cat")
	fmt.Printf("stdin echo: %q err=%v\n", r.Out, r.Err)
	r = runChild(10*time.Second, 4<<20, nil, "/nonexistent/adapter")
	fmt.Printf("missing executable: err=%v ENOENT=%v\n", r.Err, errors.Is(r.Err, os.ErrNotExist))
	r = runChild(10*time.Second, 4<<20, nil, "sh", "-c", "exit 8")
	fmt.Printf("exit 8: err=%v exit=%d\n", r.Err, r.Exit)
	return nil
}

func cmdExecTimeout() error {
	r := runChild(500*time.Millisecond, 4<<20, nil, "sleep", "30")
	fmt.Printf("sleep 30 / 500ms: err=%v exit=%d signaled=%v elapsed=%s deadline?=%v\n",
		r.Err, r.Exit, r.Signaled, r.Elapsed.Round(time.Millisecond), errors.Is(r.Err, context.DeadlineExceeded))
	r = runChild(500*time.Millisecond, 4<<20, nil, "sh", "-c", `trap "" TERM; sleep 30`)
	fmt.Printf("TERM-ignoring child / 500ms + WaitDelay 1s: err=%v exit=%d signaled=%v elapsed=%s\n",
		r.Err, r.Exit, r.Signaled, r.Elapsed.Round(time.Millisecond))
	r = runChild(5*time.Second, 4<<20, nil, "sh", "-c", `sleep 3 & echo parent-done; exit 0`)
	fmt.Printf("grandchild holds stdout open / WaitDelay 1s: out=%q err=%v ErrWaitDelay=%v elapsed=%s\n",
		r.Out, r.Err, errors.Is(r.Err, exec.ErrWaitDelay), r.Elapsed.Round(time.Millisecond))
	return nil
}

// ---------- detaching the watcher ----------

func cmdDetach(args []string) error {
	if len(args) < 1 {
		return errors.New("detach <logfile>")
	}
	logPath := args[0]
	self, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	extra, err := os.Open("/etc/hosts") // must NOT be inherited: Go opens with O_CLOEXEC
	if err != nil {
		return err
	}
	defer extra.Close()
	cmd := exec.Command(self, "child-report")
	cmd.Stdin = nil // /dev/null
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.Dir = os.Getenv("HOME")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME"), "LAB_TOKEN=secret-in-env-only"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return err
	}
	fmt.Printf("spawned detached child pid=%d (parent pid=%d, ppid=%d, exits now)\n", pid, os.Getpid(), os.Getppid())
	return nil
}

func cmdChildReport() error {
	report := func(tag string) {
		sid, _ := unix.Getsid(0)
		fds, _ := os.ReadDir("/dev/fd")
		names := make([]string, 0, len(fds))
		for _, e := range fds {
			names = append(names, e.Name())
		}
		cwd, _ := os.Getwd()
		fmt.Printf("%s pid=%d ppid=%d pgid=%d sid=%d fds=%v cwd=%s LAB_TOKEN=%q\n",
			tag, os.Getpid(), os.Getppid(), unix.Getpgrp(), sid, names, cwd, os.Getenv("LAB_TOKEN"))
	}
	report("start")
	time.Sleep(3 * time.Second)
	report("after-3s")
	return nil
}

// ---------- PID liveness and start-time (PID reuse guard) ----------

func cmdKill0(args []string) error {
	for _, a := range args {
		pid, err := strconv.Atoi(a)
		if err != nil {
			return err
		}
		err = syscall.Kill(pid, 0)
		var state string
		switch {
		case err == nil:
			state = "alive (signalable)"
		case errors.Is(err, syscall.ESRCH):
			state = "no such process (ESRCH)"
		case errors.Is(err, syscall.EPERM):
			state = "alive, not ours (EPERM)"
		default:
			state = "error: " + err.Error()
		}
		fmt.Printf("kill(%d, 0): %s\n", pid, state)
	}
	return nil
}

func psStart(pid int) (time.Time, string, error) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, "", err
	}
	raw := strings.TrimSpace(string(out))
	t, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", raw, time.Local)
	return t, raw, err
}

func cmdLstart(args []string) error {
	for _, a := range args {
		pid, err := strconv.Atoi(a)
		if err != nil {
			return err
		}
		t, raw, err := psStart(pid)
		fmt.Printf("pid %d: ps lstart=%q parsed=%v err=%v\n", pid, raw, t.Format(time.RFC3339), err)
		fmt.Printf("pid %d: procfs=%s\n", pid, procStart(pid))
	}
	return nil
}

// ---------- flock for the shared credential file ----------

func cmdFlock(args []string) error {
	if len(args) < 2 {
		return errors.New("flock <path> hold <secs>|try|wait")
	}
	f, err := os.OpenFile(args[0], os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	switch args[1] {
	case "hold":
		secs, _ := strconv.Atoi(args[2])
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			return err
		}
		fmt.Printf("hold: LOCK_EX acquired by pid %d for %ds\n", os.Getpid(), secs)
		time.Sleep(time.Duration(secs) * time.Second)
		return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	case "try":
		start := time.Now()
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		fmt.Printf("try: LOCK_EX|LOCK_NB err=%v EWOULDBLOCK=%v (%s)\n", err, errors.Is(err, syscall.EWOULDBLOCK), time.Since(start).Round(time.Millisecond))
		return nil
	case "wait":
		start := time.Now()
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		fmt.Printf("wait: blocking LOCK_EX acquired after %s err=%v\n", time.Since(start).Round(time.Millisecond), err)
		return nil
	}
	return errors.New("bad mode")
}

// ---------- atomic 0600 writes and O_EXCL pidfiles ----------

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*") // O_EXCL, mode 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func cmdAtomic() error {
	dir, err := os.MkdirTemp("", "brigade-atomic")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	syscall.Umask(0o022)
	p := filepath.Join(dir, "session.json")
	if err := writeFileAtomic(p, []byte(`{"v":1}`), 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(p, []byte(`{"v":2}`), 0o600); err != nil {
		return err
	}
	st, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	entries, _ := os.ReadDir(dir)
	fmt.Printf("atomic: mode=%o content=%s entries=%d\n", st.Mode().Perm(), b, len(entries))
	pf := filepath.Join(dir, "watcher.json")
	f1, err := os.OpenFile(pf, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_ = f1.Close()
	_, err = os.OpenFile(pf, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	fmt.Printf("pidfile second O_EXCL create: err=%v ErrExist=%v\n", err, errors.Is(err, os.ErrExist))
	_ = os.Chmod(p, 0o644)
	st, _ = os.Stat(p)
	fmt.Printf("world-readable check: mode=%o refuse=%v\n", st.Mode().Perm(), st.Mode().Perm()&0o077 != 0)
	if err := os.MkdirAll(filepath.Join(dir, "profiles", "default"), 0o700); err != nil {
		return err
	}
	st, _ = os.Stat(filepath.Join(dir, "profiles", "default"))
	fmt.Printf("profile dir mode=%o\n", st.Mode().Perm())
	return nil
}

// ---------- log/slog redacting handler ----------

var (
	reJWT      = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`)
	reJoin     = regexp.MustCompile(`brg1\.[0-9a-fA-F-]{36}\.[0-9a-f]{32}`)
	reSecret   = regexp.MustCompile(`sb_secret_[A-Za-z0-9_-]+`)
	reBearer   = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._-]+`)
	secretKeys = map[string]bool{"token": true, "secret": true, "authorization": true, "apikey": true,
		"access_token": true, "refresh_token": true, "join_secret": true, "password": true}
)

func redactString(s string, exact []string) string {
	for _, e := range exact {
		if e != "" {
			s = strings.ReplaceAll(s, e, "[redacted]")
		}
	}
	s = reJWT.ReplaceAllString(s, "[redacted-jwt]")
	s = reJoin.ReplaceAllString(s, "brg1.[redacted]")
	s = reSecret.ReplaceAllString(s, "sb_secret_[redacted]")
	s = reBearer.ReplaceAllString(s, "${1}[redacted]")
	return s
}

func newRedactingHandler(w io.Writer, level slog.Leveler, exact []string) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if secretKeys[strings.ToLower(a.Key)] {
				return slog.String(a.Key, "[redacted]")
			}
			a.Value = a.Value.Resolve()
			switch a.Value.Kind() {
			case slog.KindString:
				a.Value = slog.StringValue(redactString(a.Value.String(), exact))
			case slog.KindAny:
				if err, ok := a.Value.Any().(error); ok {
					a.Value = slog.StringValue(redactString(err.Error(), exact))
				}
			}
			return a
		},
	})
}

func cmdSlog() error {
	exact := []string{os.Getenv("CLAUDE_CODE_MESSAGING_TOKEN"), "my-exact-token-value"}
	log := slog.New(newRedactingHandler(os.Stdout, slog.LevelDebug, exact))
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	log.Info("refresh ok", "token", jwt, "expires_in", 3600)
	log.Warn("server said: invalid token " + jwt)
	log.Error("join failed", "err", errors.New("secret brg1.0d9c2b1e-1111-2222-3333-444444444444.0123456789abcdef0123456789abcdef rejected"))
	log.Debug("headers", "authorization", "Bearer abc.def", "apikey", "sb_secret_XYZ123", "note", "key sb_secret_abc in text", "exact", "value my-exact-token-value here")
	log.Info("group", slog.Group("http", slog.String("Authorization", "Bearer zzz"), slog.Int("status", 200)))
	log.Info("PITFALL: struct values are JSON-encoded, ReplaceAttr never sees their fields", "req", struct{ Token string }{Token: jwt})
	return nil
}

// ---------- XDG directories ----------

func xdgDir(override, xdgVar, fallback string) string {
	if v := os.Getenv(override); v != "" && filepath.IsAbs(v) {
		return v
	}
	if v := os.Getenv(xdgVar); v != "" && filepath.IsAbs(v) { // the XDG spec says relative values must be ignored
		return filepath.Join(v, "brigade")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback, "brigade")
}

func cmdXDG() error {
	cfg, err := os.UserConfigDir()
	fmt.Println("os.UserConfigDir:", cfg, err)
	cache, err := os.UserCacheDir()
	fmt.Println("os.UserCacheDir:", cache, err)
	home, _ := os.UserHomeDir()
	fmt.Println("os.UserHomeDir:", home)
	fmt.Printf("XDG_CONFIG_HOME=%q XDG_STATE_HOME=%q\n", os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_STATE_HOME"))
	fmt.Println("brigade config dir:", xdgDir("BRIGADE_CONFIG_DIR", "XDG_CONFIG_HOME", ".config"))
	fmt.Println("brigade state dir: ", xdgDir("BRIGADE_STATE_DIR", "XDG_STATE_HOME", filepath.Join(".local", "state")))
	return nil
}

// ---------- signals ----------

func cmdSigwait() error {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	fmt.Println("sigwait: ready", os.Getpid())
	select {
	case s := <-ch:
		fmt.Println("sigwait: got", s, "-> clean shutdown")
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("no signal")
	}
}

func cmdSignals() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "sigwait")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	_ = cmd.Process.Signal(syscall.SIGTERM)
	err = cmd.Wait()
	fmt.Printf("handled SIGTERM: err=%v after %s; child output:\n%s", err, time.Since(start).Round(time.Millisecond), out.String())
	cmd2 := exec.Command("sleep", "5")
	_ = cmd2.Start()
	_ = cmd2.Process.Signal(syscall.SIGTERM)
	err = cmd2.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		fmt.Printf("unhandled SIGTERM: ExitCode=%d Signaled=%v err=%v\n", ee.ExitCode(), ee.Sys().(syscall.WaitStatus).Signaled(), err)
	}
	return nil
}

// ---------- JSON Schema export (invopop) and validation (santhosh-tekuri) ----------

type SendRequest struct {
	SenderSessionID    string  `json:"sender_session_id" jsonschema:"required,minLength=1,maxLength=200,description=A session owned by the profile's principal"`
	RecipientSessionID string  `json:"recipient_session_id" jsonschema:"required,minLength=1,maxLength=200"`
	Body               string  `json:"body" jsonschema:"required,minLength=1,description=UTF-8 text of at most 16384 bytes (byte length is enforced by the adapter)"`
	Summary            *string `json:"summary,omitzero" jsonschema:"maxLength=200"`
	ReplyTo            *string `json:"reply_to,omitzero" jsonschema:"maxLength=200"`
	IdempotencyKey     string  `json:"idempotency_key" jsonschema:"required,minLength=1,maxLength=128"`
}

type ErrorBody struct {
	Code         string         `json:"code" jsonschema:"required,enum=usage,enum=invalid_input,enum=unauthenticated,enum=unauthorized,enum=not_found,enum=conflict,enum=rate_limited,enum=unavailable,enum=protocol_mismatch,enum=config,enum=loop_detected,enum=internal"`
	Message      string         `json:"message" jsonschema:"required"`
	Retryable    bool           `json:"retryable" jsonschema:"required"`
	RetryAfterMS *int           `json:"retry_after_ms,omitzero" jsonschema:"minimum=0"`
	Details      map[string]any `json:"details,omitzero"`
}

type Result struct {
	OK              bool       `json:"ok" jsonschema:"required"`
	ProtocolVersion string     `json:"protocol_version" jsonschema:"required,enum=1"`
	Result          any        `json:"result,omitzero"`
	Error           *ErrorBody `json:"error,omitzero"`
}

func cmdSchema() error {
	r := &jsonschema.Reflector{RequiredFromJSONSchemaTags: true, AllowAdditionalProperties: true}
	root := &jsonschema.Schema{Version: jsonschema.Version, ID: "https://github.com/appshapes/brigade/docs/protocol-v1.schema.json", Definitions: jsonschema.Definitions{}}
	for _, v := range []any{&Result{}, &SendRequest{}} {
		s := r.Reflect(v)
		for k, d := range s.Definitions {
			root.Definitions[k] = d
		}
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))

	c := stjson.NewCompiler()
	doc, err := stjson.UnmarshalJSON(bytes.NewReader(out))
	if err != nil {
		return err
	}
	if err := c.AddResource("protocol.json", doc); err != nil {
		return err
	}
	sch, err := c.Compile("protocol.json#/$defs/SendRequest")
	if err != nil {
		return err
	}
	for _, inst := range []string{
		`{"sender_session_id":"a","recipient_session_id":"b","body":"hi","idempotency_key":"k","extra":1}`,
		`{"sender_session_id":"a","body":"hi"}`,
		`{"sender_session_id":"a","recipient_session_id":"b","body":"","idempotency_key":"k"}`,
	} {
		v, err := stjson.UnmarshalJSON(strings.NewReader(inst))
		if err != nil {
			return err
		}
		err = sch.Validate(v)
		fmt.Printf("validate %s -> %v\n", inst, err)
	}
	return nil
}

// ---------- bounded stdin JSON document ----------

func cmdStdin() error {
	const capBytes = 1 << 20
	if _, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), ioctlTermios); err == nil {
		fmt.Println("stdin is a terminal: a command that takes input would print usage instead of blocking")
		return nil
	}
	if st, err := os.Stdin.Stat(); err == nil {
		fmt.Printf("stdin mode=%v chardev=%v (note: /dev/null is a char device, so ModeCharDevice alone is not a TTY test)\n",
			st.Mode(), st.Mode()&os.ModeCharDevice != 0)
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, capBytes+1))
	if err != nil {
		return err
	}
	if len(data) > capBytes {
		fmt.Printf("invalid_input: stdin exceeds 1 MiB (stopped after %d bytes)\n", len(data))
		return nil
	}
	var v map[string]any
	if err := jsonv2.Unmarshal(bytes.TrimSpace(data), &v); err != nil {
		fmt.Println("invalid_input:", err)
		return nil
	}
	fmt.Println("ok:", v)
	return nil
}
