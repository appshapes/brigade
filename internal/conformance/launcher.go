package conformance

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// Environment names the launcher sets on every adapter process (section 3
// of the P1-6 brief; 4.1 Environment).
const (
	envProfile   = "BRIGADE_PROFILE=default"
	envLogLevel  = "BRIGADE_LOG_LEVEL=debug"
	envConfigDir = "BRIGADE_CONFIG_DIR"
	envStateDir  = "BRIGADE_STATE_DIR"
)

// maxExit is the highest 4.6 exit status; anything outside 0..maxExit is
// a B-11 violation.
const maxExit = 12

// stdoutCap bounds what the launcher keeps of a request/response
// command's stdout, matching the harness's own 4 MiB cap.
const stdoutCap = 4 << 20

// waitDelay is how long a child may keep its pipes open after it exited or
// was killed before the launcher gives up on them.
const waitDelay = 2 * time.Second

// A Result is one finished adapter process: what was sent, what came back,
// and the launcher's reading of stdout.
type Result struct {
	// Args is the full argv that ran: the resolved adapter, the fixed
	// arguments and the command.
	Args []string
	// Stdin is the document that was fed (nil when stdin was the null
	// device or a held-open pipe).
	Stdin []byte
	// Stdout and Stderr are the captured streams.
	Stdout, Stderr []byte
	// Exit is the process exit status; -1 when the process was killed by a
	// signal, including the launcher's own kill on timeout.
	Exit int
	// Duration is wall time from start to reap.
	Duration time.Duration
	// Envelope is stdout parsed and validated as one 4.3 envelope; nil when
	// stdout is not exactly one valid envelope followed by one newline.
	Envelope *protocol.Envelope
	// Raw is the first JSON value on stdout parsed loosely; nil when stdout
	// does not start with a JSON object. It exists for key-presence checks
	// (B-2's `retryable`, C-17's unknown members, C-30's empty arrays).
	Raw map[string]any

	// command names the case's own arguments for failure reasons.
	command string
	// err is the launcher-side failure, if any (timeout, spawn error).
	err error
}

// name returns the command as the case gave it, for failure reasons.
func (r *Result) name() string { return r.command }

// launcher resolves and spawns the adapter under test and applies the
// global checks of brief section 5 to every spawn.
type launcher struct {
	adapter   string // resolved executable
	fixedArgs []string
	path      string // PATH copied from the suite's environment
	tmpdir    string // TMPDIR copied from the suite's environment
	envPairs  []string
	sharedEnv string
	runDir    string
	timeout   time.Duration
	verbose   bool

	logMu  sync.Mutex
	stderr io.Writer

	secMu   sync.Mutex
	secrets []string
}

// newLauncher builds the launcher from the options and the suite's own
// environment (never read through os.Getenv: Run takes environ).
func newLauncher(adapter string, opts Options, environ []string, runDir string, stderr io.Writer) *launcher {
	l := &launcher{
		adapter:   adapter,
		fixedArgs: slices.Clone(opts.FixedArgs),
		envPairs:  slices.Clone(opts.Env),
		sharedEnv: opts.SharedEnv,
		runDir:    runDir,
		timeout:   opts.Timeout,
		verbose:   opts.Verbose,
		stderr:    stderr,
	}
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		switch name {
		case "PATH":
			l.path = value
		case "TMPDIR":
			l.tmpdir = value
		}
	}
	return l
}

// sharedDir is <run>/shared, the directory --shared-env names; "" without
// the flag.
func (l *launcher) sharedDir() string {
	if l.sharedEnv == "" {
		return ""
	}
	return filepath.Join(l.runDir, "shared")
}

// env builds a principal's process environment from scratch, in the
// section-3 order, with extra appended last.
func (l *launcher) env(p *Principal, extra []string) []string {
	env := make([]string, 0, 8+len(l.envPairs)+len(extra))
	if l.path != "" {
		env = append(env, "PATH="+l.path)
	}
	if l.tmpdir != "" {
		env = append(env, "TMPDIR="+l.tmpdir)
	}
	env = append(env,
		"HOME="+p.Home,
		envConfigDir+"="+p.ConfigDir,
		envStateDir+"="+p.StateDir,
		envProfile,
		envLogLevel,
	)
	env = append(env, l.envPairs...)
	if l.sharedEnv != "" {
		env = append(env, l.sharedEnv+"="+l.sharedDir())
	}
	return append(env, extra...)
}

// argv is the adapter, the fixed arguments and the command.
func (l *launcher) argv(args []string) []string {
	argv := make([]string, 0, 1+len(l.fixedArgs)+len(args))
	argv = append(argv, l.adapter)
	argv = append(argv, l.fixedArgs...)
	return append(argv, args...)
}

// cappedBuffer keeps the first limit bytes of what is written to it and
// counts the rest.
type cappedBuffer struct {
	limit   int
	buf     bytes.Buffer
	dropped int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); len(p) > room {
		b.buf.Write(p[:room])
		b.dropped += len(p) - room
	} else {
		b.buf.Write(p)
	}
	return len(p), nil
}

// execute runs one process to completion under timeout: argv array, the
// given environment and nothing else, stdin from the document (nil: the
// null device) or, with open, from a pipe whose write end is held until
// the process exits (B-1). It never applies the protocol checks; spawn
// does.
func (l *launcher) execute(ctx context.Context, argv, env []string, stdin []byte, open bool, timeout time.Duration) *Result {
	r := &Result{Args: argv, Stdin: stdin, Exit: -1}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.WaitDelay = waitDelay
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = stdoutCap, stdoutCap
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	switch {
	case open:
		// The read end goes to the child as its stdin fd; the write end
		// stays here, unwritten and unclosed, until the child has exited.
		pr, pw, err := os.Pipe()
		if err != nil {
			r.err = err
			return r
		}
		cmd.Stdin = pr
		defer func() {
			_ = pw.Close()
			_ = pr.Close()
		}()
	case stdin != nil:
		cmd.Stdin = bytes.NewReader(stdin)
	}

	start := time.Now()
	runErr := cmd.Run()
	r.Duration = time.Since(start)
	r.Stdout, r.Stderr = stdout.buf.Bytes(), stderr.buf.Bytes()
	if stdout.dropped > 0 {
		r.err = errors.New("stdout exceeded the 4 MiB cap")
	}
	if cmd.ProcessState != nil {
		r.Exit = cmd.ProcessState.ExitCode()
	}

	switch {
	case runErr == nil:
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		r.err = errors.New("did not exit within " + timeout.String() + " (killed)")
	case ctx.Err() != nil:
		r.err = errors.New("run cancelled")
	default:
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			r.err = errors.New("spawn failed: " + runErr.Error())
		} else if r.Exit < 0 {
			r.err = errors.New("killed by a signal (" + cmd.ProcessState.String() + ")")
		}
	}
	return r
}

// spawn runs one adapter command for a case and applies the global checks
// of brief section 5, returning the violations for the caller to record
// against the running case. caseID is for the -v log only.
func (l *launcher) spawn(ctx context.Context, caseID string, p *Principal, extra []string, stdin []byte, open bool, args ...string) (*Result, []string) {
	argv := l.argv(args)
	env := l.env(p, extra)
	r := l.execute(ctx, argv, env, stdin, open, l.timeout)
	r.command = commandName(args)
	name := r.name()

	var violations []string
	if r.err != nil {
		violations = append(violations, name+": "+r.err.Error())
	} else if r.Exit < 0 || r.Exit > maxExit {
		violations = append(violations, name+": exit status "+strconv.Itoa(r.Exit)+" is outside 0..12 (B-11)")
	}

	envelope, msg := oneEnvelope(r.Stdout)
	if msg != "" {
		violations = append(violations, name+": "+msg+" (4.1 stdout)")
	}
	r.Envelope = envelope
	r.Raw = looseObject(r.Stdout)

	isCreate := len(args) >= 2 && args[0] == "team" && args[1] == "create"
	if isCreate && envelope != nil && envelope.OK {
		l.learnSecret(r.Raw)
	}
	if !isCreate {
		violations = append(violations, l.scanSecrets(name, "stdout", r.Stdout)...)
	}
	violations = append(violations, l.scanSecrets(name, "stderr", r.Stderr)...)

	l.logSpawn(caseID, argv, env, stdin, open, r)
	return r, violations
}

// commandName joins a case's arguments for failure reasons; an opaque id
// is left as is because it names the thing that failed.
func commandName(args []string) string {
	if len(args) == 0 {
		return "(no arguments)"
	}
	return strings.Join(args, " ")
}

// oneEnvelope applies the 4.1 stdout discipline: stdout is exactly one
// JSON object followed by exactly one "\n", and that object is a valid 4.3
// envelope. It returns the envelope, or "" and a reason.
func oneEnvelope(stdout []byte) (*protocol.Envelope, string) {
	if len(stdout) == 0 {
		return nil, "stdout is empty; want exactly one envelope"
	}
	if stdout[0] != '{' {
		return nil, "stdout does not start with a JSON object"
	}
	dec := jsontext.NewDecoder(bytes.NewReader(stdout))
	val, err := dec.ReadValue()
	if err != nil {
		return nil, "stdout is not a JSON document"
	}
	rest := stdout[dec.InputOffset():]
	switch {
	case len(rest) == 0:
		return nil, "stdout lacks the newline after the envelope"
	case !bytes.Equal(rest, []byte("\n")):
		return nil, "stdout carries " + strconv.Itoa(len(rest)) +
			" bytes after the envelope (a second document, a bare line or trailing text)"
	}
	var env protocol.Envelope
	if err := protocol.Unmarshal(val, &env); err != nil {
		return nil, "stdout does not parse as a 4.3 envelope"
	}
	if err := env.Validate(); err != nil {
		return nil, "stdout is not a valid 4.3 envelope: " + err.Error()
	}
	return &env, ""
}

// looseObject parses the first JSON value of data into a map; nil when it
// is not an object.
func looseObject(data []byte) map[string]any {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	dec := jsontext.NewDecoder(bytes.NewReader(trimmed))
	val, err := dec.ReadValue()
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := protocol.Unmarshal(val, &m); err != nil {
		return nil
	}
	return m
}

// learnSecret remembers the join_secret of a successful `team create` so
// every later spawn and the end-of-run directory scan look for it.
func (l *launcher) learnSecret(raw map[string]any) {
	result, _ := raw["result"].(map[string]any)
	if s, ok := result["join_secret"].(string); ok && s != "" {
		l.addSecret(s)
	}
}

// addSecret registers a join secret the run knows.
func (l *launcher) addSecret(s string) {
	if s == "" {
		return
	}
	l.secMu.Lock()
	defer l.secMu.Unlock()
	if !slices.Contains(l.secrets, s) {
		l.secrets = append(l.secrets, s)
	}
}

// knownSecrets returns a snapshot of the secrets the run knows.
func (l *launcher) knownSecrets() []string {
	l.secMu.Lock()
	defer l.secMu.Unlock()
	return slices.Clone(l.secrets)
}

// secretShaped matches a join-secret-shaped token: the `brg1.` prefix
// followed by at least two non-empty dot-separated components made of
// characters a real secret can carry (4.4.10: no whitespace, control or
// format characters; the team_ref may itself contain dots). Quotes, angle
// brackets, backslashes and backticks end a token, so the protocol's fixed
// invalid_input text `expected brg1.<team_ref>.<secret>` — which spells the
// FORMAT and carries no secret — is not a hit, while `brg1.t.s`, a hex or
// UUID-shaped secret, and a JSON-quoted one all are.
var secretShaped = regexp.MustCompile("brg1\\.[^\\s\"'<>\\\\`.]+(?:\\.[^\\s\"'<>\\\\`.]+)+")

// scanSecrets checks one stream for every known join secret and for any
// join-secret-shaped `brg1.` token. A hit is reported as a C-05 failure
// that never quotes the secret.
func (l *launcher) scanSecrets(name, stream string, data []byte) []string {
	var out []string
	for _, s := range l.knownSecrets() {
		if bytes.Contains(data, []byte(s)) {
			out = append(out, "C-05: "+stream+" of "+name+" contains a join secret the run knows")
			break
		}
	}
	if secretShaped.Match(data) {
		out = append(out, "C-05: "+stream+" of "+name+" contains the join-secret prefix "+protocol.JoinSecretPrefix)
	}
	return out
}

// scanDir walks the run directory at the end of the run and applies the
// secret scan to every regular file: profile files, logs, the fs store.
// The walk is rooted with os.Root so a symlink planted by an adapter
// cannot lead the scan outside the run directory.
func (l *launcher) scanDir() []string {
	root, err := os.OpenRoot(l.runDir)
	if err != nil {
		return []string{"run-directory scan: " + err.Error()}
	}
	defer func() { _ = root.Close() }()
	var out []string
	rootFS := root.FS()
	_ = fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is skipped, never fatal
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, rerr := fs.ReadFile(rootFS, path)
		if rerr != nil {
			return nil //nolint:nilerr // same: skip what cannot be read
		}
		out = append(out, l.scanSecrets("run directory file "+path, "content", data)...)
		return nil
	})
	return out
}

// logSpawn writes the -v record of one spawn to stderr: argv, the
// environment (values elided for names containing SECRET, TOKEN or KEY),
// stdin (known join secrets redacted), stdout, stderr, exit and duration.
func (l *launcher) logSpawn(caseID string, argv, env []string, stdin []byte, open bool, r *Result) {
	if !l.verbose {
		return
	}
	var b strings.Builder
	b.WriteString("[" + caseID + "] $ " + strings.Join(argv, " ") + "\n")
	b.WriteString("    env:")
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "SECRET") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "KEY") {
			b.WriteString(" " + name + "=[elided]")
		} else {
			b.WriteString(" " + kv)
		}
	}
	b.WriteString("\n")
	switch {
	case open:
		b.WriteString("    stdin: (pipe held open)\n")
	case stdin == nil:
		b.WriteString("    stdin: (none)\n")
	default:
		b.WriteString("    stdin (" + strconv.Itoa(len(stdin)) + " bytes): " + l.redact(preview(stdin)) + "\n")
	}
	b.WriteString("    exit " + strconv.Itoa(r.Exit) + " in " + r.Duration.Round(time.Millisecond).String())
	if r.err != nil {
		b.WriteString(" (" + r.err.Error() + ")")
	}
	b.WriteString("\n")
	b.WriteString("    stdout (" + strconv.Itoa(len(r.Stdout)) + " bytes): " + l.redact(preview(r.Stdout)) + "\n")
	b.WriteString("    stderr (" + strconv.Itoa(len(r.Stderr)) + " bytes): " + l.redact(preview(r.Stderr)) + "\n")
	l.write(b.String())
}

// logf writes one -v line.
func (l *launcher) logf(caseID, line string) {
	if !l.verbose {
		return
	}
	l.write("[" + caseID + "] " + line + "\n")
}

// write serialises stderr output.
func (l *launcher) write(s string) {
	l.logMu.Lock()
	defer l.logMu.Unlock()
	_, _ = io.WriteString(l.stderr, s)
}

// redact replaces every known join secret in s.
func (l *launcher) redact(s string) string {
	for _, secret := range l.knownSecrets() {
		s = strings.ReplaceAll(s, secret, "[join secret redacted]")
	}
	return s
}

// preview truncates a stream for the log; the whole thing is never needed
// and a 1 MiB stdin would drown the terminal.
func preview(b []byte) string {
	const limit = 2048
	s := strings.TrimRight(string(b), "\n")
	if len(s) > limit {
		return s[:limit] + "… [" + strconv.Itoa(len(s)-limit) + " more bytes]"
	}
	return s
}

// operator runs a --setup or --rebind command for a principal: argv split
// on whitespace, no shell, the principal's from-scratch environment, three
// times the per-command timeout because a backend bootstrap can be slower
// than one protocol command. The protocol checks are not applied — it is
// the operator's program, not the adapter — but the spawn is logged at -v.
func (l *launcher) operator(ctx context.Context, caseID string, p *Principal, command string, stdin []byte) (*Result, error) {
	argv := strings.Fields(command)
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	env := l.env(p, nil)
	r := l.execute(ctx, argv, env, stdin, false, 3*l.timeout)
	r.command = commandName(argv)
	l.logSpawn(caseID, argv, env, stdin, false, r)
	if r.err != nil {
		return r, r.err
	}
	return r, nil
}
