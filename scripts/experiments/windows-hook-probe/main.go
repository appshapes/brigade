// Command windows-hook-probe is step 0 of the native Windows plan (Trello card 133,
// .context/plans/native-windows.md). Copied to plugin/bin/brigade.exe beside the POSIX bootstrap, with
// plugin/hooks/hooks.json left exactly as it ships, it records every invocation Claude Code makes of it, so one
// `claude -p` run on a Windows runner (.github/workflows/experiment-windows-hooks.yml) answers the question the
// whole plan rests on: does an exec-form hook whose `command` is `${CLAUDE_PLUGIN_ROOT}/bin/brigade`, with no
// extension, resolve to the brigade.exe beside it on Windows? Node's spawner tries `.com` and `.exe` for an
// extensionless path; whether Claude Code's does is what this measures.
//
// Every invocation appends one block to hook-probe.log under RUNNER_TEMP (TEMP, then the OS temp dir, when
// unset): the time, argv, the working directory, the executable's own path, what arrived on stdin (the hook
// input JSON), the environment with every value whose name looks secret redacted, the Claude Code session
// registry's file names, and, from the UserPromptSubmit hook, the result of writing the auth frame and one user
// frame into CLAUDE_CODE_MESSAGING_SOCKET (a named pipe on Windows), which measures delivery. It prints nothing
// to stdout: a hook's stdout is Claude Code's, and the probe has nothing to say there. It always exits 0.
//
// Measured 2026-10-27 (docs/experiments/E11-windows-hook-resolution.md): GO, and every post was delivered as a
// new user turn, which fired the prompt hook again, which posted again, for 19 minutes. The probe now posts only
// from a prompt that does not carry its own marker.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	stdinLimit = 64 << 10
	logName    = "hook-probe.log"
	socketVar  = "CLAUDE_CODE_MESSAGING_SOCKET"
	tokenVar   = "CLAUDE_CODE_MESSAGING_TOKEN" //nolint:gosec // G101: the NAME of the variable, not a credential
	marker     = "PROBE-DELIVERED"
)

var secretName = regexp.MustCompile(`(?i)token|secret|key|password|auth|credential`)

func main() {
	env := environ()
	var b strings.Builder
	fmt.Fprintf(&b, "---- %s\n", time.Now().UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(&b, "argv: %s\n", strings.Join(os.Args, " "))
	if wd, err := os.Getwd(); err == nil {
		fmt.Fprintf(&b, "cwd: %s\n", wd)
	}
	if exe, err := os.Executable(); err == nil {
		fmt.Fprintf(&b, "executable: %s\n", exe)
	}
	stdin := readStdin()
	fmt.Fprintf(&b, "stdin: %s\n", stdin)
	writeEnv(&b, env)
	listSessions(&b, env)
	if sock := env[socketVar]; sock != "" {
		if fi, err := os.Lstat(sock); err == nil {
			fmt.Fprintf(&b, "socket stat: mode=%s size=%d\n", fi.Mode(), fi.Size())
		} else {
			fmt.Fprintf(&b, "socket stat: %v\n", err)
		}
		switch {
		case !isPromptHook():
		case strings.Contains(stdin, marker):
			b.WriteString("post: skipped (this prompt is the probe's own delivered frame)\n")
		default:
			fmt.Fprintf(&b, "post: %s\n", post(sock, env[tokenVar]))
		}
	} else {
		fmt.Fprintf(&b, "socket: %s unset\n", socketVar)
	}
	b.WriteString("\n")
	appendLog(logDir(env), b.String())
}

// environ is os.Environ as a map (os.Getenv is forbidden by the repository's lint rules, which this
// experiment program keeps to).
func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

func logDir(env map[string]string) string {
	for _, name := range []string{"RUNNER_TEMP", "TEMP", "TMP"} {
		if d := env[name]; d != "" {
			return d
		}
	}
	return os.TempDir()
}

func appendLog(dir, block string) {
	f, err := os.OpenFile(filepath.Join(dir, logName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: the experiment's own log under the runner's temp dir
	if err != nil {
		fmt.Fprintf(os.Stderr, "windows-hook-probe: %v\n", err)
		return
	}
	if _, err := io.WriteString(f, block); err != nil {
		fmt.Fprintf(os.Stderr, "windows-hook-probe: %v\n", err)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "windows-hook-probe: %v\n", err)
	}
}

// readStdin returns what a hook was given, or a note when stdin is a terminal (the Bash tool case), so a
// probe run by hand never blocks.
func readStdin() string {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return "(stat: " + err.Error() + ")"
	}
	if fi.Mode()&os.ModeCharDevice != 0 {
		return "(terminal, not read)"
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, stdinLimit))
	if err != nil {
		return "(read: " + err.Error() + ")"
	}
	return strings.TrimSpace(string(data))
}

func writeEnv(b *strings.Builder, env map[string]string) {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	b.WriteString("env:\n")
	for _, k := range names {
		v := env[k]
		if secretName.MatchString(k) {
			v = fmt.Sprintf("<redacted, %d bytes>", len(v))
		}
		fmt.Fprintf(b, "  %s=%s\n", k, v)
	}
}

// listSessions names the files of Claude Code's session registry (CLAUDE_CONFIG_DIR/sessions, or
// ~/.claude/sessions), which the harness reads best-effort on macOS and Linux (invariants.md).
func listSessions(b *strings.Builder, env map[string]string) {
	dir := env["CLAUDE_CONFIG_DIR"]
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(b, "sessions: no home: %v\n", err)
			return
		}
		dir = filepath.Join(home, ".claude")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "sessions"))
	if err != nil {
		fmt.Fprintf(b, "sessions: %v\n", err)
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	fmt.Fprintf(b, "sessions: %s\n", strings.Join(names, " "))
}

func isPromptHook() bool {
	return len(os.Args) >= 3 && os.Args[1] == "hook" && os.Args[2] == "prompt"
}

// post writes the two frames A-verified-facts §A.2 documents, the auth line first (required on Windows), and
// reports what the write did. A session that then sees "PROBE-DELIVERED" in its own turn proves the pipe.
func post(path, token string) string {
	f, err := os.OpenFile(path, os.O_WRONLY, 0) //nolint:gosec // G304: the session's own socket, named by its environment
	if err != nil {
		return "open: " + err.Error()
	}
	auth, err := json.Marshal(map[string]string{"type": "auth", "token": token})
	if err != nil {
		return "marshal: " + err.Error()
	}
	user, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]string{"role": "user", "content": marker + " " + time.Now().UTC().Format(time.RFC3339)},
	})
	if err != nil {
		return "marshal: " + err.Error()
	}
	frames := append(append(auth, '\n'), append(user, '\n')...)
	_, werr := f.Write(frames)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		return "write: " + err.Error()
	}
	return fmt.Sprintf("wrote %d bytes (auth line then user line)", len(frames))
}
