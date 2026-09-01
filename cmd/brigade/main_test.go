package main

import (
	"bytes"
	"encoding/json/v2"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/appshapes/brigade/internal/app"
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/tscmd"
)

// scriptVersion is what `brigade version` prints inside a testscript.
//
// TestMain assigns it to buildinfo.Version, which is the same variable the
// release stamps with
//
//	-ldflags "-X github.com/appshapes/brigade/internal/buildinfo.Version=…"
//
// and the only way to reach it in a binary `go test` linked. Without it the
// scripts would have to assert whatever debug.ReadBuildInfo happens to say
// about a test binary, which is a fact about the toolchain and not about
// Brigade. The stamp is deliberately not a plausible release number, so
// that a value read out of a script can never be mistaken for one.
//
// TestBuiltBinaryReportsTheStampedVersion covers the other half: a real
// `go build` of ./cmd/brigade, stamped by real ldflags, printing the real
// version. Between them the ldflags target is asserted end to end.
const scriptVersion = "0.0.0-testscript"

// TestMain installs `brigade` and `fake-adapter` on the scripts' PATH as
// copies of this test binary (plan 9.1). `exec brigade …` in a script is
// then a genuine fork and exec: argv, stdin, stdout, stderr and the exit
// status all cross a real process boundary, which is the only place the
// stdout discipline and the 4.6 exit codes can actually be observed.
//
// The third name the scripts need, `brigade-adapter-fs`, cannot be
// installed this way: it lives in its own main package and its entrypoint
// is unexported, so there is nothing for this package to reference.
// TestScript builds it and puts it on PATH instead — which is closer to the
// truth anyway, since that is the executable `make build` produces.
func TestMain(m *testing.M) {
	buildinfo.Version = scriptVersion
	testscript.Main(m, map[string]func(){
		"brigade":      brigadeMain,
		"fake-adapter": fakeAdapterMain,
	})
}

// brigadeMain is the shipped binary's entrypoint. It repeats the one line
// of main() rather than calling it, because main() is not callable; the
// built-binary test below is what keeps the real main() honest.
func brigadeMain() {
	//nolint:forbidigo // this is a main(); naming the process streams is the point (7.3)
	os.Exit(app.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
}

// TestScript runs every scenario in testdata/script.
// updateScripts rewrites the committed txtar goldens. Plan 7.3 specifies goldens
// "with -update", and the distinction matters: an environment variable read here
// would let an ambient UPDATE_SCRIPTS=1 disarm every byte-for-byte assertion in
// the suite -- a broken `brigade version` would PASS and silently rewrite the
// golden in the repository to match its own wrong output. A test flag cannot be
// set by inheritance, so the escape hatch stays deliberate.
var updateScripts = flag.Bool("update", false, "rewrite the testdata/script golden files")

func TestScript(t *testing.T) {
	t.Parallel()

	// The dev-only adapter binary, built exactly as `make build` builds it,
	// on a PATH entry of its own.
	adapterDir := filepath.Dir(testutil.Build(t, "./cmd/brigade-adapter-fs"))

	testscript.Run(t, testscript.Params{
		Dir:                 filepath.Join("testdata", "script"),
		Cmds:                tscmd.Commands(),
		RequireExplicitExec: true,
		RequireUniqueNames:  true,
		UpdateScripts:       *updateScripts,
		Setup: func(env *testscript.Env) error {
			// Every directory Brigade or Claude Code would otherwise take
			// from the developer's account is redirected under $WORK. HOME
			// is left at testscript's own /no-home so that a stray ~ fails
			// loudly instead of resolving somewhere real.
			dirs := testutil.NewDirs(filepath.Join(env.WorkDir, ".brigade"))
			if err := dirs.Mkdir(); err != nil {
				return err
			}
			env.Vars = append(env.Vars, dirs.Vars()...)
			env.Vars = append(env.Vars,
				"PATH="+adapterDir+string(os.PathListSeparator)+env.Getenv("PATH"))
			return nil
		},
	})
}

// TestBuiltBinaryReportsTheStampedVersion builds ./cmd/brigade the way the
// release does and runs it as a child process.
//
// It is the test that pins the shared build contract: the ldflags target is
// internal/buildinfo.Version, `brigade version` prints exactly that string
// and nothing else, and it prints it on stdout with stderr silent. The
// version is freshly generated on every run, so the check cannot pass on a
// stale binary or on a value compiled in somewhere else.
func TestBuiltBinaryReportsTheStampedVersion(t *testing.T) {
	t.Parallel()

	want := "0.0.0-" + testutil.RunID()
	binary := testutil.BuildStamped(t, "./cmd/brigade", want)

	//nolint:gosec // G204: the argv is a binary this test just built; no shell is involved
	cmd := exec.CommandContext(t.Context(), binary, "version")
	cmd.Env = testutil.Env(t)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s version: %v (stderr %q)", binary, err, stderr.String())
	}
	if got := stdout.String(); got != want+"\n" {
		t.Errorf("stdout = %q, want %q", got, want+"\n")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// reportedEnv are the variables fake-adapter echoes back. They are the ones
// a test must be able to prove a child received, because each of them
// points at a directory the developer also owns a real copy of.
var reportedEnv = []string{
	"HOME",
	"CLAUDE_CONFIG_DIR",
	"CLAUDE_PLUGIN_ROOT",
	"XDG_CONFIG_HOME",
	"XDG_STATE_HOME",
	"BRIGADE_CONFIG_DIR",
	"BRIGADE_STATE_DIR",
	"BRIGADE_FS_ROOT",
}

// fakeAdapterReport is the single JSON line fake-adapter writes to stdout.
type fakeAdapterReport struct {
	// Argv is os.Args[1:] exactly as the process received it.
	Argv []string `json:"argv"`
	// Args is what is left after fake-adapter's own flags.
	Args []string `json:"args"`
	// Cwd is the working directory the parent gave the child.
	Cwd string `json:"cwd"`
	// StdinBytes is how many bytes were read from stdin before EOF.
	StdinBytes int `json:"stdin_bytes"`
	// Env holds the reportedEnv variables, "" when unset.
	Env map[string]string `json:"env"`
	// Alive answers -alive: whether that pid exists. Absent without it.
	Alive *bool `json:"alive,omitzero"`
	// Exit is the status this process is about to exit with.
	Exit int `json:"exit"`
}

// fakeAdapterMain is the `fake-adapter` command on the scripts' PATH.
//
// P1-1 scope, stated plainly: this is a measuring instrument, not an
// adapter. It speaks no BAP/1 verb and pretends to none — the protocol
// types arrive in P1-2 and the real scripted fake in P3-2, and a stub that
// invented an envelope shape now would be a fixture that lies. What it does
// do is report, as one JSON line on stdout, exactly what a child process
// received: its argv, how many bytes came down stdin, the directories its
// environment points at, and whether a pid handed to -alive exists. It
// writes one human line to stderr and exits with the status -exit names.
//
// That is enough for a script to prove the plumbing that P1-1 is actually
// responsible for: argv reaches the child, stdin reaches the child, the
// environment is the isolated one and not the developer's, the two output
// streams stay apart, and an exit status survives.
func fakeAdapterMain() {
	fs := flag.NewFlagSet("fake-adapter", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exit := fs.Int("exit", 0, "exit with this status")
	alive := fs.Int("alive", 0, "report whether this pid exists")
	stdinOut := fs.String("stdin-out", "", "write everything read from stdin to this file")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fakeAdapterFail("bad arguments: " + err.Error())
	}

	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fakeAdapterFail("reading stdin: " + err.Error())
	}
	if *stdinOut != "" {
		if err := os.WriteFile(*stdinOut, body, 0o600); err != nil {
			fakeAdapterFail("writing " + *stdinOut + ": " + err.Error())
		}
	}

	report := fakeAdapterReport{
		Argv:       os.Args[1:],
		Args:       fs.Args(),
		StdinBytes: len(body),
		Env:        map[string]string{},
		Exit:       *exit,
	}
	if report.Argv == nil {
		report.Argv = []string{}
	}
	if report.Args == nil {
		report.Args = []string{}
	}
	if cwd, err := os.Getwd(); err == nil {
		report.Cwd = cwd
	}
	for _, name := range reportedEnv {
		report.Env[name] = os.Getenv(name)
	}
	if *alive != 0 {
		live := pidExists(*alive)
		report.Alive = &live
	}

	line, err := json.Marshal(report)
	if err != nil {
		fakeAdapterFail("marshalling the report: " + err.Error())
	}
	//nolint:forbidigo // this is a main(); the report is this program's protocol output
	if _, err := os.Stdout.Write(append(line, '\n')); err != nil {
		fakeAdapterFail("writing the report: " + err.Error())
	}
	_, _ = io.WriteString(os.Stderr, "fake-adapter: reported "+strings.Join(os.Args[1:], " ")+"\n")
	os.Exit(*exit)
}

// pidExists reports whether a process with this pid exists: the kill(pid,
// 0) probe of 6.6, which is what the watcher's liveness check is built on.
// A nil os.Signal will not do — os.Process.Signal refuses it as an
// unsupported type and every pid would read as dead.
func pidExists(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// fakeAdapterFail ends the fake adapter on a usage error: nothing on
// stdout, one line on stderr, exit 2, which is the same shape 4.6 gives a
// `usage` failure.
func fakeAdapterFail(message string) {
	_, _ = io.WriteString(os.Stderr, "fake-adapter: "+message+"\n")
	os.Exit(2)
}
