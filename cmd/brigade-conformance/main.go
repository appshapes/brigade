// Command brigade-conformance runs the BAP/1 conformance suite (C-01..C-43,
// plan 9.2) against any adapter. It is a development binary: `make build`
// produces it and `make test` runs it, but it is never shipped.
//
// P1-1 state: the suite library (internal/conformance) arrives with plan
// task P1-6, so this binary selects zero cases and says so. It accepts the
// full 9.2 command line now, because `make test` and CI invoke it from the
// first commit and a stub that rejected their flags would be worse than no
// stub at all. Zero cases is reported as zero cases; it is never dressed up
// as a passing suite.
package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"strings"
)

// suiteTask is the plan task that replaces this stub.
const suiteTask = "P1-6"

// protocolVersion is the BAP/1 major this suite speaks (4.3).
const protocolVersion = "1"

// Exit statuses, from 9.2.
const (
	exitPass     = 0 // every selected case passed (skips allowed)
	exitFail     = 1 // at least one case failed
	exitUsage    = 2 // bad argv
	exitLauncher = 3 // adapter not found, describe failed, --setup failed
)

// repeatable collects a flag that may appear more than once (--env).
type repeatable []string

func (r *repeatable) String() string { return strings.Join(*r, ",") }

func (r *repeatable) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// options is the 9.2 command line.
type options struct {
	adapter    string
	env        repeatable
	sharedEnv  string
	setup      string
	rebind     string
	tags       string
	only       string
	skip       string
	slow       bool
	timeout    string
	keepTemp   bool
	jsonReport bool
	verbose    bool
	fixedArgs  []string
}

// summary is the `summary` object of the JSON report.
type summary struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// adapterInfo is the `adapter` object of the JSON report. Name and Version
// come from `describe`; at P1-1 no adapter is ever launched, so they are
// empty and Command records what would have been launched.
type adapterInfo struct {
	Command string `json:"command"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// report is the --json document of 9.2.
type report struct {
	Adapter         adapterInfo      `json:"adapter"`
	ProtocolVersion string           `json:"protocol_version"`
	Capabilities    []string         `json:"capabilities"`
	Results         []jsontext.Value `json:"results"`
	Summary         summary          `json:"summary"`
	Note            string           `json:"note"`
}

// register builds the 9.2 flag set. Every flag of the real suite is
// accepted so that a caller written against 9.2 — the Makefile's `test` and
// `test-integration` recipes, and CI — runs unchanged once P1-6 lands.
func register(fs *flag.FlagSet, o *options) {
	fs.StringVar(&o.adapter, "adapter", "", "executable to test (PATH lookup for a bare name)")
	fs.Var(&o.env, "env", "extra environment for every adapter process, K=V (repeatable)")
	fs.StringVar(&o.sharedEnv, "shared-env", "", "export NAME=<run temp dir>/shared to every principal")
	fs.StringVar(&o.setup, "setup", "", "command run once per principal before any protocol command")
	fs.StringVar(&o.rebind, "rebind", "", "command that rebinds a profile to a team_ref given on stdin")
	fs.StringVar(&o.tags, "tags", "", "run only cases carrying one of these tags")
	fs.StringVar(&o.only, "only", "", "run only these case ids")
	fs.StringVar(&o.skip, "skip", "", "skip these case ids")
	fs.BoolVar(&o.slow, "slow", false, "include cases tagged slow")
	fs.StringVar(&o.timeout, "timeout", "20s", "per-command timeout")
	fs.BoolVar(&o.keepTemp, "keep-temp", false, "keep the run directory for inspection")
	fs.BoolVar(&o.jsonReport, "json", false, "machine-readable report on stdout (human table on stderr)")
	fs.BoolVar(&o.verbose, "v", false, "show every command, stdin, stdout and stderr")
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	var o options
	fs := flag.NewFlagSet("brigade-conformance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	register(fs, &o)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeUsage(stdout, fs)
			return exitPass
		}
		_, _ = io.WriteString(stderr, "brigade-conformance: "+err.Error()+"\n")
		writeUsage(stderr, fs)
		return exitUsage
	}
	// Everything after `--` is prepended to every adapter invocation
	// (9.2). The stub records it; it never launches anything.
	o.fixedArgs = fs.Args()

	if o.adapter == "" {
		_, _ = io.WriteString(stderr, "brigade-conformance: --adapter <cmd> is required\n")
		writeUsage(stderr, fs)
		return exitUsage
	}
	resolved, err := exec.LookPath(o.adapter)
	if err != nil {
		_, _ = io.WriteString(stderr, "brigade-conformance: adapter not found: "+o.adapter+"\n")
		return exitLauncher
	}

	note := "the C-01..C-43 suite is not implemented yet; it arrives with plan task " + suiteTask +
		". This binary is the P1-1 stub: it resolves the adapter, selects zero cases and launches nothing."

	if o.jsonReport {
		rep := report{
			Adapter:         adapterInfo{Command: strings.Join(append([]string{resolved}, o.fixedArgs...), " ")},
			ProtocolVersion: protocolVersion,
			Capabilities:    []string{},
			Results:         []jsontext.Value{},
			Summary:         summary{},
			Note:            note,
		}
		b, merr := json.Marshal(rep, jsontext.WithIndent("  "))
		if merr != nil {
			_, _ = io.WriteString(stderr, "brigade-conformance: "+merr.Error()+"\n")
			return exitLauncher
		}
		if _, werr := stdout.Write(append(b, '\n')); werr != nil {
			return exitLauncher
		}
	}

	// The human summary goes to stderr in every mode (9.2), so that stdout
	// carries the report and nothing else.
	var b strings.Builder
	b.WriteString("brigade-conformance: 0 cases selected.\n")
	b.WriteString("  adapter:   " + resolved)
	if len(o.fixedArgs) > 0 {
		b.WriteString(" " + strings.Join(o.fixedArgs, " "))
	}
	b.WriteString(" (resolved, not launched)\n")
	b.WriteString("  " + note + "\n")
	b.WriteString("  0 passed, 0 failed, 0 skipped.\n")
	_, _ = io.WriteString(stderr, b.String())

	return exitPass
}

// writeUsage prints the 9.2 command line.
func writeUsage(w io.Writer, fs *flag.FlagSet) {
	_, _ = io.WriteString(w, "Usage: brigade-conformance [flags] --adapter <cmd> [-- <fixed args>...]\n\nFlags:\n")
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	_, _ = io.WriteString(w, "\nExit: 0 all selected cases passed  1 a case failed  2 usage  3 launcher error\n")
}
