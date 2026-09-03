package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/appshapes/brigade/internal/app"
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
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
// `fake-adapter` is the scripted BAP/1 adapter of P3-2
// (internal/testutil/fakeadapter): the P1-1 measuring instrument that used
// to answer here is gone. The third name the scripts need,
// `brigade-adapter-fs`, cannot be installed this way: it lives in its own
// main package and its entrypoint is unexported, so there is nothing for
// this package to reference. TestScript builds it and puts it on PATH
// instead — which is closer to the truth anyway, since that is the
// executable `make build` produces.
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

// fakeAdapterMain is the `fake-adapter` command on the scripts' PATH: the
// scripted BAP/1 adapter of P3-2. It speaks the protocol on argv/stdin/
// stdout, scripted by the JSON file named with a leading `--script`, and
// can dump the argv, stdin size and environment a child received so a
// script can prove what crossed the fork. The scripting format and the
// behaviour live in internal/testutil/fakeadapter.
func fakeAdapterMain() {
	//nolint:forbidigo // this is a main(); naming the process streams is the point (7.3)
	os.Exit(fakeadapter.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
}
