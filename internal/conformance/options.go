package conformance

import (
	"errors"
	"flag"
	"io"
	"strings"
	"time"
)

// Exit statuses of the binary and of Run (plan 9.2).
const (
	// ExitPass: every selected case passed (skips allowed).
	ExitPass = 0
	// ExitFail: at least one case failed.
	ExitFail = 1
	// ExitUsage: bad argv, or an unknown case id in --only/--skip.
	ExitUsage = 2
	// ExitLauncher: adapter not found, describe not ok/unparseable/wrong
	// major, --setup failed, the fixture could not be built, or the run
	// outlived the fixture's lease (SuiteWallClockBudget).
	ExitLauncher = 3
)

// DefaultTimeout is the per-command timeout when --timeout is not given
// (4.1 applies 20 s to every request/response command).
const DefaultTimeout = 20 * time.Second

// Options is the 9.2 command line, parsed. The zero value is not usable:
// Adapter is required and Run applies DefaultTimeout when Timeout is zero.
type Options struct {
	// Adapter is the executable to test; a bare name is looked up on PATH.
	Adapter string
	// Env holds extra K=V pairs for every adapter process (--env).
	Env []string
	// SharedEnv names a variable exported as <run>/shared to every
	// principal (--shared-env; BRIGADE_FS_ROOT for the fs adapter).
	SharedEnv string
	// Setup is a command run once per fixture principal, in that
	// principal's environment, before any protocol command (--setup).
	Setup string
	// Rebind is a command that rebinds a profile to the team_ref given on
	// stdin as {"team_ref": "…"} (--rebind); empty means the default
	// team.json rewrite.
	Rebind string
	// Tags, Only and Skip select cases (--tags, --only, --skip).
	Tags []string
	Only []string
	Skip []string
	// Slow includes the cases tagged slow (--slow).
	Slow bool
	// Shuffle is the seed of the permutation the selected cases run in
	// (--shuffle); 0 runs them in id order. A non-zero seed is reported on
	// the human summary's first line and in the JSON report, so a failure
	// found in a shuffled run is reproducible with the same seed.
	Shuffle int64
	// Timeout bounds every request/response command (--timeout).
	Timeout time.Duration
	// KeepTemp keeps the run directory and prints its path (--keep-temp).
	KeepTemp bool
	// JSON writes the machine-readable report on stdout (--json).
	JSON bool
	// Verbose logs every spawn to stderr (-v).
	Verbose bool
	// FixedArgs is everything after `--`, prepended to every invocation.
	FixedArgs []string
}

// repeatable collects a flag that may appear more than once (--env).
type repeatable []string

func (r *repeatable) String() string { return strings.Join(*r, ",") }

func (r *repeatable) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// newFlagSet registers the 9.2 flag set — exactly the names the P1-1 stub
// registered, because the Makefile's test recipes and CI were written
// against them — onto o.
func newFlagSet(o *Options, env *repeatable, tags, only, skip, timeout *string) *flag.FlagSet {
	fs := flag.NewFlagSet("brigade-conformance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.StringVar(&o.Adapter, "adapter", "", "executable to test (PATH lookup for a bare name)")
	fs.Var(env, "env", "extra environment for every adapter process, K=V (repeatable)")
	fs.StringVar(&o.SharedEnv, "shared-env", "", "export NAME=<run temp dir>/shared to every principal")
	fs.StringVar(&o.Setup, "setup", "", "command run once per principal before any protocol command")
	fs.StringVar(&o.Rebind, "rebind", "", "command that rebinds a profile to a team_ref given on stdin")
	fs.StringVar(tags, "tags", "", "run only cases carrying one of these tags (core, cap:<capability>, slow)")
	fs.StringVar(only, "only", "", "run only these case ids (comma-separated)")
	fs.StringVar(skip, "skip", "", "skip these case ids (comma-separated)")
	fs.BoolVar(&o.Slow, "slow", false, "include cases tagged slow")
	fs.Int64Var(&o.Shuffle, "shuffle", 0, "run the selected cases in the permutation this seed names (0: id order)")
	fs.StringVar(timeout, "timeout", DefaultTimeout.String(), "per-command timeout")
	fs.BoolVar(&o.KeepTemp, "keep-temp", false, "keep the run directory for inspection")
	fs.BoolVar(&o.JSON, "json", false, "machine-readable report on stdout (human table on stderr)")
	fs.BoolVar(&o.Verbose, "v", false, "show every command, environment, stdin, stdout and stderr")
	return fs
}

// ParseArgs parses the 9.2 command line (os.Args[1:]). It returns
// flag.ErrHelp for -h/--help; any other error is a usage error (exit 2)
// whose text is safe to print. Whether the ids in --only/--skip exist is
// checked by Run, which has the case list.
func ParseArgs(args []string) (Options, error) {
	var (
		o                        Options
		env                      repeatable
		tags, only, skip, tmoStr string
	)
	fs := newFlagSet(&o, &env, &tags, &only, &skip, &tmoStr)
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	o.Env = []string(env)
	o.Tags = splitList(tags)
	o.Only = splitList(only)
	o.Skip = splitList(skip)
	o.FixedArgs = fs.Args()
	tmo, err := time.ParseDuration(tmoStr)
	if err != nil || tmo <= 0 {
		return Options{}, errors.New("--timeout must be a positive duration such as 20s")
	}
	o.Timeout = tmo
	if o.Adapter == "" {
		return Options{}, errors.New("--adapter <cmd> is required")
	}
	for _, kv := range o.Env {
		if name, _, ok := strings.Cut(kv, "="); !ok || name == "" {
			return Options{}, errors.New("--env takes NAME=value")
		}
	}
	if o.SharedEnv != "" && strings.ContainsAny(o.SharedEnv, "= \t") {
		return Options{}, errors.New("--shared-env takes a variable NAME")
	}
	return o, nil
}

// splitList splits a comma-separated flag value, trimming and dropping
// empty items.
func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Usage writes the 9.2 command line to w.
func Usage(w io.Writer) {
	var o Options
	var env repeatable
	var tags, only, skip, tmo string
	fs := newFlagSet(&o, &env, &tags, &only, &skip, &tmo)
	_, _ = io.WriteString(w, "Usage: brigade-conformance [flags] --adapter <cmd> [-- <fixed args>...]\n\nFlags:\n")
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	_, _ = io.WriteString(w, "\nExit: 0 all selected cases passed  1 a case failed  2 usage  3 launcher error\n")
}
