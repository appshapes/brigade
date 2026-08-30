// Fake `brigade` release binary for the bootstrap experiment: prints what it sees, echoes stdin bodies.

//go:debug x509usefallbackroots=1
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "golang.org/x/crypto/x509roots/fallback" // embedded Mozilla roots; with x509usefallbackroots=1 they replace Security.framework
)

var version = "0.1.0"

func claudeEnv() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "BRIGADE") || k == "PATH" || k == "HOME" || k == "XDG_DATA_HOME" || k == "XDG_STATE_HOME" {
			if k == "CLAUDE_CODE_MESSAGING_TOKEN" {
				v = fmt.Sprintf("<redacted len=%d>", len(v))
			}
			m[k] = v
		}
	}
	return m
}

func logRecord(rec map[string]any) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	dir = filepath.Join(dir, "brigade")
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "fake-brigade.ndjson"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(rec)
	f.Write(append(b, '\n'))
}

func readStdin(limit int64, timeout time.Duration) string {
	done := make(chan string, 1)
	go func() { b, _ := io.ReadAll(io.LimitReader(os.Stdin, limit)); done <- string(b) }()
	select {
	case s := <-done:
		return s
	case <-time.After(timeout):
		return "<stdin read timed out>"
	}
}

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	rec := map[string]any{"ts": time.Now().Format(time.RFC3339Nano), "cmd": cmd, "argv": os.Args, "exe": exe, "cwd": cwd, "pid": os.Getpid(), "ppid": os.Getppid(), "env": claudeEnv()}
	switch cmd {
	case "version", "--version":
		fmt.Printf("brigade %s (%s/%s, fake release build for the bootstrap experiment)\n", version, runtime.GOOS, runtime.GOARCH)
	case "whoami":
		keys := []string{}
		for k := range rec["env"].(map[string]string) {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("brigade whoami: exe=%s cwd=%s pid=%d ppid=%d\n", exe, cwd, os.Getpid(), os.Getppid())
		for _, k := range keys {
			if k == "PATH" {
				continue
			}
			fmt.Printf("  %s=%s\n", k, rec["env"].(map[string]string)[k])
		}
	case "send":
		// brigade send <session_id> [--summary s] [--reply-to id] [--json]; body on stdin
		var recipient, summary, replyTo string
		jsonOut := false
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--summary":
				i++
				if i < len(args) {
					summary = args[i]
				}
			case "--reply-to":
				i++
				if i < len(args) {
					replyTo = args[i]
				}
			case "--json":
				jsonOut = true
			default:
				if recipient == "" {
					recipient = args[i]
				}
			}
		}
		body := readStdin(1<<20, 3*time.Second)
		rec["recipient"] = recipient
		rec["summary"] = summary
		rec["reply_to"] = replyTo
		rec["body"] = body
		out := map[string]any{"status": "accepted", "message_id": "fake-msg-0001", "recipient_session_id": recipient, "summary": summary, "reply_to": replyTo, "body_bytes": len(body), "body": body}
		if jsonOut {
			b, _ := json.Marshal(out)
			fmt.Println(string(b))
		} else {
			fmt.Printf("accepted: message fake-msg-0001 to %s (%d bytes, summary=%q, reply_to=%q)\nbody as received:\n%s\n", recipient, len(body), summary, replyTo, body)
		}
	case "hook":
		stdin := readStdin(8192, 2*time.Second)
		rec["stdin"] = stdin
		if envFile := os.Getenv("CLAUDE_ENV_FILE"); envFile != "" && len(args) > 1 && args[1] == "session-start" {
			f, err := os.OpenFile(envFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err == nil {
				fmt.Fprintf(f, "export BRIGADE_ENVFILE_TEST=1\nexport BRIGADE_PLUGIN_DATA_FROM_HOOK=%s\n", os.Getenv("CLAUDE_PLUGIN_DATA"))
				f.Close()
				rec["env_file_written"] = envFile
			} else {
				rec["env_file_error"] = err.Error()
			}
		}
		if len(args) > 1 && args[1] == "session-start" {
			fmt.Printf("Brigade: fake session-start hook ran from %s (CLAUDE_PID=%s, CLAUDE_PLUGIN_DATA=%s, ENV_FILE=%s)\n", exe, os.Getenv("CLAUDE_PID"), os.Getenv("CLAUDE_PLUGIN_DATA"), os.Getenv("CLAUDE_ENV_FILE"))
		}
	case "net":
		// brigade net <url>: fetch with Go's default transport (honours HTTP(S)_PROXY) and report; then probe writes.
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "SSL_CERT_FILE", "TMPDIR"} {
			fmt.Printf("env %s=%q\n", k, os.Getenv(k))
		}
		if len(args) > 1 {
			c := &http.Client{Timeout: 10 * time.Second}
			t0 := time.Now()
			resp, err := c.Get(args[1])
			if err != nil {
				fmt.Printf("GET %s -> error after %s: %v\n", args[1], time.Since(t0).Round(time.Millisecond), err)
			} else {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
				resp.Body.Close()
				fmt.Printf("GET %s -> %s in %s; body[:80]=%q\n", args[1], resp.Status, time.Since(t0).Round(time.Millisecond), strings.TrimSpace(string(b)))
			}
		}
		home := os.Getenv("HOME")
		for _, p := range []string{filepath.Join(home, ".local", "state", "brigade", "sandbox-write-test.txt"), filepath.Join(home, ".config", "brigade", "sandbox-write-test.txt"), filepath.Join(os.TempDir(), "brigade-sandbox-write-test.txt"), filepath.Join(cwd, "brigade-sandbox-write-test.txt")} {
			_ = os.MkdirAll(filepath.Dir(p), 0o700)
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				fmt.Printf("write %s -> DENIED: %v\n", p, err)
			} else {
				fmt.Printf("write %s -> ok\n", p)
				os.Remove(p)
			}
		}
		if b, err := os.ReadFile(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "sessions", os.Getenv("CLAUDE_PID")+".json")); err != nil {
			fmt.Printf("read registry sessions/%s.json -> error: %v\n", os.Getenv("CLAUDE_PID"), err)
		} else {
			fmt.Printf("read registry sessions/%s.json -> ok (%d bytes)\n", os.Getenv("CLAUDE_PID"), len(b))
		}
	default:
		fmt.Fprintf(os.Stderr, "brigade: unknown command %q (fake build; commands: version whoami send hook)\n", cmd)
		logRecord(rec)
		os.Exit(2)
	}
	logRecord(rec)
}
