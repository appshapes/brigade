// Package cli holds the `brigade` command table, the interspersed-flag
// parser of plan 7.3, the usage and help output, and the mapping from a
// command failure to a process exit status (4.6).
//
// Nothing in this package touches os.Stdout, os.Stderr, os.Exit or
// os.Getenv. Streams arrive as io.Writer values and the environment arrives
// as a slice, which is both the stdout discipline of 7.3 and what makes
// every command testable without a subprocess.
package cli

import (
	"errors"
	"flag"
	"io"
	"strconv"
	"strings"

	harnesscmd "github.com/appshapes/brigade/internal/harness/commands"
)

// Streams are the three process streams a command may use.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// A Context carries everything a command may read: its streams, the
// environment it was given, and the global flags.
type Context struct {
	Streams
	// Environ is the process environment in os.Environ() form. Commands
	// read configuration from here, never from os.Getenv, so a test can
	// hand a command an environment without mutating its own (3.2, 7.3).
	Environ []string
	// JSON is the global --json flag: the protocol envelope on stdout
	// instead of human output.
	JSON bool
	// LogLevel is the global --log-level flag, validated against LogLevels.
	LogLevel string
	// Command is the resolved command name.
	Command string
	// Flags is the command's own parsed flag set.
	Flags *flag.FlagSet
}

// Getenv returns the value of name in the context's environment, or "" when
// it is unset. This is the only environment accessor commands may use.
func (cx *Context) Getenv(name string) string {
	prefix := name + "="
	// Last occurrence wins. This deliberately does NOT match os.Getenv: Go and libc both resolve the
	// FIRST occurrence (syscall/env_unix.go records "first mention of key" and blanks later duplicates,
	// calling unshadowing "a security problem"). It matches os/exec's dedupEnv, which keeps the last, so
	// a value a caller appends when building a child environment overrides an earlier one -- which is
	// exactly how testutil.Env is documented to work.
	for i := len(cx.Environ) - 1; i >= 0; i-- {
		if strings.HasPrefix(cx.Environ[i], prefix) {
			return cx.Environ[i][len(prefix):]
		}
	}
	return ""
}

// Bool reports the value of a boolean flag registered by the command.
func (cx *Context) Bool(name string) bool {
	if cx.Flags == nil {
		return false
	}
	f := cx.Flags.Lookup(name)
	if f == nil {
		return false
	}
	v, ok := f.Value.(interface{ IsBoolFlag() bool })
	if !ok || !v.IsBoolFlag() {
		return false
	}
	b, err := strconv.ParseBool(f.Value.String())
	return err == nil && b
}

// Dispatch runs one `brigade` invocation and returns its exit status. args
// excludes the program name.
func Dispatch(args []string, s Streams, environ []string) int {
	// The poison scan runs before any parsing, so that a join secret on
	// argv cannot reach an error message through a route the flag sets do
	// not cover — after a `--` separator, for instance (4.5.14, C-05).
	if scanPoison(args) {
		return report(s, scanJSONFlag(args), usagef("", poisonMessage))
	}

	// --json selects the OUTPUT STREAM, so it has to be known even when
	// parsing fails before the flag set reaches it. stdlib flag aborts at the
	// first offending argument, so `brigade version --bad-flag --json` leaves
	// g.json false and a machine caller would get a human line on stderr and
	// an empty stdout instead of the 4.3 envelope it asked for.
	argvJSON := scanJSONFlag(args)

	// Pass one: the global flags that appear before the command word.
	// stdlib flag stops at the first non-flag argument, which is exactly
	// what is wanted here.
	var g globals
	fs := newFlagSet(Program)
	g.register(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeUsage(s, g.json || argvJSON, usageAll(false))
			return ExitOK
		}
		return report(s, g.json || argvJSON, usagef("", "invalid arguments: "+err.Error()))
	}
	if lerr := validateLogLevel("", g.logLevel); lerr != nil {
		return report(s, g.json, lerr)
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return report(s, g.json, usagef("", "no command given; run `"+Program+" help` for the command list"))
	}

	name := rest[0]
	cmd, ok := Lookup(name)
	if !ok {
		// No flag set can be built for a command that does not exist, so
		// the stream for this one error is chosen by a literal scan.
		jsonMode := g.json || argvJSON
		return report(s, jsonMode, usagef("", "unknown command "+strconv.Quote(name)+"; run `"+Program+" help` for the command list"))
	}

	if cmd.Raw {
		return dispatchRaw(cmd, rest[1:], s, environ, g)
	}

	// Pass two: the command's own flags, plus the same globals sharing the
	// same variables, so the two parses cannot disagree.
	cfs := newFlagSet(Program + " " + name)
	g.register(cfs)
	if cmd.Flags != nil {
		cmd.Flags(cfs)
	}
	positional, err := parseInterspersed(cfs, rest[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeUsage(s, g.json || argvJSON, usageCommand(cmd))
			return ExitOK
		}
		if g.poison {
			// Never print err: the flag package formats the offending
			// value into its message.
			return report(s, g.json || argvJSON, usagef(name, poisonMessage))
		}
		return report(s, g.json || argvJSON, usagef(name, "invalid arguments: "+err.Error()))
	}
	if lerr := validateLogLevel(name, g.logLevel); lerr != nil {
		return report(s, g.json, lerr)
	}
	if !cmd.Implemented() {
		return report(s, g.json, notImplementedError(cmd))
	}

	cx := &Context{
		Streams:  s,
		Environ:  environ,
		JSON:     g.json,
		LogLevel: g.logLevel,
		Command:  name,
		Flags:    cfs,
	}
	return finish(s, g.json, name, cmd.Run(cx, positional))
}

// dispatchRaw runs a Raw command: no second parse. --json is honoured
// wherever it appears (it selects the error stream, and the command
// consumes it from the raw arguments itself), -h/--help as the FIRST raw
// argument prints the command's usage, and everything else — the verb,
// --profile, the adapter's own flags — reaches Run verbatim.
func dispatchRaw(cmd Command, raw []string, s Streams, environ []string, g globals) int {
	jsonMode := g.json || scanJSONFlag(raw)
	if len(raw) > 0 && (raw[0] == "-h" || raw[0] == "--help") {
		writeUsage(s, jsonMode, usageCommand(cmd))
		return ExitOK
	}
	cx := &Context{
		Streams:  s,
		Environ:  environ,
		JSON:     jsonMode,
		LogLevel: g.logLevel,
		Command:  cmd.Name,
	}
	return finish(s, jsonMode, cmd.Name, cmd.Run(cx, raw))
}

// finish maps a command's return to the process exit status: nil is
// success; an ExitStatus is an adapter's own status forwarded by a
// pass-through (its envelope is already on stdout, so nothing is
// printed); anything else is reported on the right stream with its code.
func finish(s Streams, jsonMode bool, name string, rerr error) int {
	if rerr == nil {
		return ExitOK
	}
	var exit harnesscmd.ExitStatus
	if errors.As(rerr, &exit) {
		return int(exit)
	}
	return report(s, jsonMode, asError(name, rerr))
}

// NotImplemented reports a recognised multi-call entrypoint that internal/app
// intercepts but cannot run yet. It exists so that `brigade hook …` and
// `brigade watch …` fail with the plan task that builds them rather than
// as an unknown command.
func NotImplemented(cmd Command, args []string, s Streams) int {
	return multiCallReport(cmd, args, s, notImplementedError(cmd))
}

// Usage reports a `usage` refusal of a multi-call entrypoint that
// internal/app dispatches itself (the adapter name after `brigade
// adapter`), through the same reporter the table uses. message is fixed
// text and never carries an argument: argv can hold a secret (4.5.14).
func Usage(cmd Command, args []string, s Streams, message string) int {
	return multiCallReport(cmd, args, s, usagef(cmd.Name, message))
}

// multiCallReport reports err for a multi-call entrypoint: --json is
// honoured wherever it appears, and a join secret on argv wins over any
// other answer (C-05).
func multiCallReport(cmd Command, args []string, s Streams, err *Error) int {
	jsonMode := scanJSONFlag(args)
	if scanPoison(args) {
		return report(s, jsonMode, usagef(cmd.Name, poisonMessage))
	}
	return report(s, jsonMode, err)
}

// notImplementedError is the failure a placeholder table entry produces. It
// is `internal` (exit 1) and not `usage` (exit 2) on purpose: the command
// name and arguments were correct, the binary is simply incomplete, and a
// caller that distinguishes the two learns something true.
func notImplementedError(c Command) *Error {
	return &Error{
		Code:    CodeInternal,
		Message: Program + " " + c.Name + " is not implemented yet; it arrives with plan task " + c.Task,
		Command: c.Name,
		Details: map[string]string{"reason": "not_implemented", "task": c.Task},
	}
}
