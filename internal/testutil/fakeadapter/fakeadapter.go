// Package fakeadapter is a scripted BAP/1 adapter used as a test fixture
// (plan 9.5; brief section 2.10). It is a REAL executable — built as the
// dev-only main package cmd/brigade-fake-adapter and installed on the
// testscript PATH by cmd/brigade/main_test.go — so the error paths the fs
// adapter cannot produce (a rate_limited on the third send, an
// unavailable for the first N calls, a protocol_version "2" describe, a
// runaway stdout, a signal death, a watch stream with an unknown event and
// an over-long line) are reproducible across a real process boundary.
//
// It is scripted by a JSON [Script] whose path is a LEADING fixed
// argument, --script <abs path> — the adapter_command JSON-array mechanism,
// exactly like the fs adapter's --root — because the harness builds the
// child's environment from scratch and would drop a BRIGADE_FAKE_ADAPTER_SCRIPT
// variable; that variable is honoured only when no --script is given (a
// human running the binary by hand). It reads no environment beyond the
// script path and dumps the whole environment it received to a file the
// script names, so adapterclient's isolation tests can prove exactly what
// crossed the fork.
//
// Nothing here ships: it lives under internal/testutil and is linked only
// into the dev-only cmd/brigade-fake-adapter and into the test binary.
package fakeadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// ScriptEnvVar is the environment variable the fake reads its script path
// from when no --script argument is given. Under a live session the
// harness builds the child environment from scratch and this never
// arrives, which is why --script is the supported path (brief 2.10).
const ScriptEnvVar = "BRIGADE_FAKE_ADAPTER_SCRIPT"

// A Script drives one fake-adapter binary across every process the
// harness spawns from it. It is written to a file by a test and named
// with --script.
type Script struct {
	// Describe is the raw DescribeResult JSON returned for `describe`.
	// Empty means a default valid describe (protocol_version "1", the
	// standard capability set) — see [DescribeJSON].
	Describe jsontext.Value `json:"describe,omitzero"`
	// Responses maps "<group> <verb>" (e.g. "message send", "session
	// register") to an ordered list of responses. One is consumed per
	// matching invocation across processes; when the list is exhausted
	// the last entry repeats. This is how "rate_limited on the third
	// send" or "unavailable for the first N calls" are expressed.
	Responses map[string][]Response `json:"responses,omitzero"`
	// Watch scripts `message watch`.
	Watch *WatchScript `json:"watch,omitzero"`
	// DumpFile, when set, receives one [Invocation] JSON object per line
	// (NDJSON) recording the argv, stdin size and full environment each
	// process received.
	DumpFile string `json:"dump_file,omitzero"`
}

// A Response is one scripted answer to one verb invocation.
type Response struct {
	// Result is the raw `result` object returned in a success envelope.
	Result jsontext.Value `json:"result,omitzero"`
	// Error, when set, returns a failing envelope carrying it and the
	// process exits with the code's 4.6 status.
	Error *protocol.ErrorObject `json:"error,omitzero"`
	// StdoutBytes writes this many 'x' bytes to stdout INSTEAD of an
	// envelope (a runaway or non-JSON stdout; over 4 MiB triggers the
	// harness's overflow cap).
	StdoutBytes int `json:"stdout_bytes,omitzero"`
	// Stderr is written to the process stderr before responding, so a
	// test can prove raw adapter stderr never reaches a returned error
	// (U-24).
	Stderr string `json:"stderr,omitzero"`
	// ExitCode overrides the process exit status. A value >= 128 is a
	// SIGNAL death: the process raises signal (ExitCode-128) on itself,
	// so 137 models a SIGKILLed child (exit 137 = 128+SIGKILL) whose
	// death the harness maps to unavailable with the signal.
	ExitCode *int `json:"exit_code,omitzero"`
	// SleepMS sleeps this many milliseconds before responding, to let a
	// caller's deadline fire (the harness maps it to unavailable/timeout).
	SleepMS int `json:"sleep_ms,omitzero"`
}

// A WatchScript replays `message watch` output and then honours stdin
// commands (ack, heartbeat, close).
type WatchScript struct {
	// Lines are NDJSON event lines emitted in order before the stdin
	// command loop begins.
	Lines []WatchLine `json:"lines,omitzero"`
}

// A WatchLine is one emitted watch line.
type WatchLine struct {
	// Raw is a JSON event object emitted as one line (compacted). Exactly
	// one of Raw and Fill is set.
	Raw jsontext.Value `json:"raw,omitzero"`
	// Fill, when > 0, emits this many 'x' bytes as the line instead of
	// Raw — an over-long line (> 1 MiB) the reader must drop and continue
	// past (B-6).
	Fill int `json:"fill,omitzero"`
	// DelayMS delays this line's emission.
	DelayMS int `json:"delay_ms,omitzero"`
}

// An Invocation is one dumped record: what a child process received. It
// is emitted as one NDJSON line to the script's DumpFile.
type Invocation struct {
	// Argv is the process argv after the program name.
	Argv []string `json:"argv"`
	// Group and Verb are the parsed BAP/1 command.
	Group string `json:"group"`
	Verb  string `json:"verb"`
	// Args are the arguments left after the leading flags, group and verb.
	Args []string `json:"args"`
	// StdinBytes is how many bytes were read from stdin (0 for `message
	// watch`, whose stdin is a command stream, and for the no-input verbs).
	StdinBytes int `json:"stdin_bytes"`
	// Cwd is the working directory the parent gave the child.
	Cwd string `json:"cwd"`
	// Env is the FULL environment the process received, as a map, so a
	// test can assert it is exactly the allow-list plus the four computed
	// BRIGADE_* and nothing else.
	Env map[string]string `json:"env"`
}

// DescribeJSON builds a valid DescribeResult with the given protocol
// version and capabilities, marshalled compactly. A test uses it to make
// a describe document one line of JSON: DescribeJSON("2") for the
// protocol-mismatch path, DescribeJSON("1", "message.watch.push") for a
// capability check.
func DescribeJSON(version string, capabilities ...string) jsontext.Value {
	if capabilities == nil {
		capabilities = defaultCapabilities()
	}
	d := protocol.DescribeResult{
		ProtocolVersion: version,
		Adapter:         protocol.AdapterInfo{Name: AdapterName, Version: AdapterVersion},
		Delivery: protocol.DeliveryInfo{
			Guarantee: protocol.GuaranteeAtLeastOnce,
			Ordering:  "none",
			AckState:  protocol.AckStateInjected,
		},
		Capabilities: capabilities,
		Limits:       protocol.DefaultLimits(),
		Lease:        protocol.DefaultLease(),
		Retention:    protocol.DefaultRetention(),
		Profile:      protocol.ProfileInfo{Name: "default", State: protocol.ProfileStateUnconfigured},
	}
	b, err := json.Marshal(&d)
	if err != nil { // unreachable: DescribeResult always marshals
		panic(err)
	}
	return b
}

// AdapterName and AdapterVersion identify the fake in its describe result.
const (
	AdapterName    = "fake-adapter"
	AdapterVersion = "0.0.0-fake"
)

// defaultCapabilities is the standard set a scripted describe advertises
// when the script names none — everything the bundled Supabase adapter
// does, so a capability check passes by default and a test opts OUT by
// naming a smaller set.
func defaultCapabilities() []string {
	return []string{
		"team.create", "team.join", "team.roster", "message.receive",
		"message.watch.push", "message.watch.stdin_commands",
		"session.description", "session.resume",
		"session.workspace_label", "session.inbound",
	}
}

// Main runs one fake-adapter invocation and returns its exit status. Argv,
// the three streams and the environment are parameters so the same logic
// serves the cmd/brigade-fake-adapter binary and the in-process testscript
// command.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	inv, scriptPath, err := parseArgs(args, environ)
	if err != nil {
		return writeUsage(stdout, stderr, err.Error())
	}
	script, err := loadScript(scriptPath)
	if err != nil {
		return writeUsage(stdout, stderr, err.Error())
	}
	inv.Env = envMap(environ)
	inv.Cwd, _ = os.Getwd()

	if inv.Group == groupMessage && inv.Verb == verbWatch {
		// The watch stdin is a command stream, not a document; record no
		// stdin bytes and dump before the (possibly long) replay.
		dump(script.DumpFile, inv)
		return runWatch(script, inv, stdin, stdout)
	}

	// Every other verb reads its one input document (nil stdin from the
	// harness is /dev/null → EOF at once), then answers.
	body, _ := io.ReadAll(stdin)
	inv.StdinBytes = len(body)
	dump(script.DumpFile, inv)

	if inv.Group == groupDescribe {
		return respondDescribe(script, stdout)
	}
	return respond(script, scriptPath, inv, stdout, stderr)
}

// The command groups and the one special verb, mirroring 4.1/4.2.
const (
	groupDescribe = "describe"
	groupMessage  = "message"
	verbWatch     = "watch"
)

// parseArgs consumes the leading flags the harness prepends (--script is
// the fake's own fixed argument; --profile and --log-level are accepted
// and ignored) and then the group and optional verb.
func parseArgs(args []string, environ []string) (Invocation, string, error) {
	inv := Invocation{Argv: append([]string{}, args...)}
	scriptPath := ""
	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "" || arg[0] != '-' {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		switch name {
		case "script", "profile", "log-level":
			if !hasValue {
				if i+1 >= len(args) {
					return inv, "", errors.New("fake-adapter: a leading flag is missing its value")
				}
				i++
				value = args[i]
			}
			if name == "script" {
				scriptPath = value
			}
		default:
			return inv, "", errors.New("fake-adapter: unknown leading flag " + arg)
		}
		i++
	}
	rest := args[i:]
	if len(rest) == 0 {
		return inv, "", errors.New("fake-adapter: no command group given")
	}
	inv.Group, rest = rest[0], rest[1:]
	if inv.Group != groupDescribe {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return inv, "", errors.New("fake-adapter: the command group needs a verb")
		}
		inv.Verb, rest = rest[0], rest[1:]
	}
	inv.Args = append([]string{}, rest...)
	if scriptPath == "" {
		scriptPath = adapterkit.Getenv(environ, ScriptEnvVar)
	}
	if scriptPath == "" {
		return inv, "", errors.New("fake-adapter: no --script and no " + ScriptEnvVar)
	}
	return inv, scriptPath, nil
}

// loadScript reads and parses the script JSON.
func loadScript(path string) (*Script, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a test fixture reads a test-controlled path
	if err != nil {
		return nil, errors.New("fake-adapter: cannot read script: " + err.Error())
	}
	var s Script
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, errors.New("fake-adapter: script is not valid JSON")
	}
	return &s, nil
}

// respondDescribe answers `describe` with the scripted document, or a
// default valid one.
func respondDescribe(script *Script, stdout io.Writer) int {
	result := script.Describe
	if len(result) == 0 {
		result = DescribeJSON(protocol.ProtocolVersion)
	}
	return writeSuccess(stdout, result)
}

// respond answers any non-watch, non-describe verb from the ordered
// response list for its "<group> <verb>" key.
func respond(script *Script, scriptPath string, inv Invocation, stdout, stderr io.Writer) int {
	key := inv.Group + " " + inv.Verb
	list := script.Responses[key]
	if len(list) == 0 {
		return writeError(stdout, &protocol.ErrorObject{
			Code:    protocol.CodeInternal,
			Message: "fake-adapter: no scripted response for " + key,
		})
	}
	resp := pickResponse(scriptPath, key, list)

	if resp.Stderr != "" {
		_, _ = io.WriteString(stderr, resp.Stderr+"\n")
	}
	if resp.SleepMS > 0 {
		time.Sleep(time.Duration(resp.SleepMS) * time.Millisecond)
	}
	if resp.ExitCode != nil && *resp.ExitCode >= 128 {
		// A signal death: raise (ExitCode-128) on ourselves and wait to
		// be killed. 137 -> SIGKILL(9), the SIGKILLed-child model.
		raiseSignal(*resp.ExitCode - 128)
		select {} //nolint:staticcheck // unreachable once the signal lands
	}
	if resp.StdoutBytes > 0 {
		_, _ = stdout.Write(bytes.Repeat([]byte{'x'}, resp.StdoutBytes))
		if resp.ExitCode != nil {
			return clampExit(*resp.ExitCode)
		}
		return protocol.ExitOK
	}
	if resp.Error != nil {
		code := writeError(stdout, resp.Error)
		if resp.ExitCode != nil {
			return clampExit(*resp.ExitCode)
		}
		return code
	}
	code := writeSuccess(stdout, resp.Result)
	if resp.ExitCode != nil {
		return clampExit(*resp.ExitCode)
	}
	return code
}

// pickResponse consumes the next response for key across processes: it
// keeps a per-key count in a sidecar of the script file, held under an
// advisory flock so concurrent children serialise, and clamps the index
// to the last entry once the list is exhausted.
func pickResponse(scriptPath, key string, list []Response) Response {
	lock, err := adapterkit.LockFile(scriptPath+".lock", adapterkit.DefaultLockTimeout)
	if err != nil {
		return list[0]
	}
	defer func() { _ = lock.Unlock() }()

	statePath := scriptPath + ".state"
	counts := readCounts(statePath)
	idx := counts[key]
	counts[key] = idx + 1
	writeCounts(statePath, counts)
	if idx >= len(list) {
		idx = len(list) - 1
	}
	return list[idx]
}

// readCounts loads the per-key invocation counts; a missing or unreadable
// file is an empty map.
func readCounts(path string) map[string]int {
	counts := map[string]int{}
	data, err := os.ReadFile(path) //nolint:gosec // a test-controlled sidecar
	if err != nil {
		return counts
	}
	_ = json.Unmarshal(data, &counts)
	return counts
}

// writeCounts persists the counts, best effort.
func writeCounts(path string, counts map[string]int) {
	if data, err := json.Marshal(counts); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

// dump appends one Invocation record to the script's DumpFile as NDJSON.
func dump(path string, inv Invocation) {
	if path == "" {
		return
	}
	data, err := json.Marshal(inv)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // a test-controlled sidecar
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(data, '\n'))
}

// envMap turns an os.Environ slice into a map; the last occurrence wins,
// matching os/exec's dedupe.
func envMap(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, entry := range environ {
		if name, value, ok := strings.Cut(entry, "="); ok {
			out[name] = value
		}
	}
	return out
}

// writeSuccess writes one success envelope carrying result and returns 0.
func writeSuccess(w io.Writer, result jsontext.Value) int {
	if len(result) == 0 {
		result = jsontext.Value("{}")
	}
	env := protocol.Envelope{OK: true, ProtocolVersion: protocol.ProtocolVersion, Result: result}
	return writeEnvelope(w, &env, protocol.ExitOK)
}

// writeError writes one failing envelope and returns the code's exit
// status. The error object is emitted VERBATIM — including the adapter's
// own retryable flag — so the harness can be shown to recompute
// retryability from the code rather than trusting the wire.
func writeError(w io.Writer, obj *protocol.ErrorObject) int {
	env := protocol.Envelope{OK: false, ProtocolVersion: protocol.ProtocolVersion, Error: obj}
	return writeEnvelope(w, &env, obj.Code.Exit())
}

// writeEnvelope marshals env as one line and returns exit.
func writeEnvelope(w io.Writer, env *protocol.Envelope, exit int) int {
	data, err := json.Marshal(env)
	if err != nil {
		return protocol.CodeInternal.Exit()
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return protocol.CodeInternal.Exit()
	}
	return exit
}

// writeUsage writes a usage failure: the envelope on stdout, a line on
// stderr, exit 2 — the shape 4.6 gives a usage refusal.
func writeUsage(stdout, stderr io.Writer, message string) int {
	_, _ = io.WriteString(stderr, message+"\n")
	return writeError(stdout, &protocol.ErrorObject{Code: protocol.CodeUsage, Message: "fake-adapter usage error"})
}

// clampExit keeps a scripted exit code inside the 0..12 range the fake is
// documented to use for normal exits (a signal death is handled before
// this).
func clampExit(code int) int {
	if code < 0 || code > 12 {
		return protocol.CodeInternal.Exit()
	}
	return code
}

// runWatch replays the scripted lines and then honours stdin ack,
// heartbeat and close commands, exiting 0 on close, on stdin EOF or on
// SIGTERM (4.4.9, C-38, C-41).
func runWatch(script *Script, inv Invocation, stdin io.Reader, stdout io.Writer) int {
	ctx, stop := notifyTerm()
	defer stop()

	sessionID := flagValue(inv.Args, "session")
	if script.Watch != nil {
		for _, line := range script.Watch.Lines {
			if line.DelayMS > 0 {
				select {
				case <-ctx.Done():
					return protocol.ExitOK
				case <-time.After(time.Duration(line.DelayMS) * time.Millisecond):
				}
			}
			if line.Fill > 0 {
				_, _ = stdout.Write(bytes.Repeat([]byte{'x'}, line.Fill))
				_, _ = io.WriteString(stdout, "\n")
				continue
			}
			_, _ = stdout.Write(bytes.TrimSpace(line.Raw))
			_, _ = io.WriteString(stdout, "\n")
		}
	}
	return watchCommands(ctx, sessionID, stdin, stdout)
}

// watchCommands reads NDJSON commands and answers them until EOF, close or
// ctx cancellation (SIGTERM).
func watchCommands(ctx context.Context, sessionID string, stdin io.Reader, stdout io.Writer) int {
	lines := make(chan []byte)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdin)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			b := append([]byte{}, scanner.Bytes()...)
			lines <- b
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return protocol.ExitOK
		case line, ok := <-lines:
			if !ok {
				return protocol.ExitOK
			}
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var cmd protocol.WatchCommand
			if err := json.Unmarshal(line, &cmd); err != nil {
				continue
			}
			switch cmd.Type {
			case protocol.CommandAck:
				ids := cmd.MessageIDs
				if ids == nil {
					ids = []string{}
				}
				emit(stdout, &protocol.WatchAcked{Event: protocol.EventAcked, MessageIDs: ids, Unknown: []string{}})
			case protocol.CommandHeartbeat:
				now := time.Now().UTC()
				emit(stdout, &protocol.WatchHeartbeatOK{
					Event: protocol.EventHeartbeatOK, SessionID: sessionID,
					State: protocol.SessionStateActive, LeaseUntil: now.Add(90 * time.Second), ServerTime: now,
				})
			case protocol.CommandClose:
				return protocol.ExitOK
			default:
				// Unknown command type: ignored (4.4.9, B-5).
			}
		}
	}
}

// emit writes one watch event as one line.
func emit(w io.Writer, event any) {
	if data, err := json.Marshal(event); err == nil {
		_, _ = w.Write(append(data, '\n'))
	}
}

// flagValue returns the value of a leading `--name value` (or `--name=value`)
// in args, or "".
func flagValue(args []string, name string) string {
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimLeft(args[i], "-")
		if key, value, ok := strings.Cut(trimmed, "="); ok && key == name {
			return value
		}
		if trimmed == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
