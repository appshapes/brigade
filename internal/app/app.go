// Package app is the multi-call dispatch seam of the shipped `brigade`
// binary: one process image serving the human command table, the Claude
// Code lifecycle hooks, the detached watcher and the bundled adapters
// (D35, 7.1).
//
// Run takes its streams and its environment as arguments so that the whole
// binary can be driven from a test without a subprocess, and so that
// nothing under internal/ has to name os.Stdout, os.Exit or os.Getenv
// (7.3). There is deliberately no app.Main() wrapper: it would have to name
// os.Stdout, and the stdout discipline puts that in package main only.
package app

import (
	"io"

	"github.com/appshapes/brigade/internal/adapters/supabase"
	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/hook"
	"github.com/appshapes/brigade/internal/harness/watch"
)

// Run executes one invocation and returns the process exit status.
//
// args excludes the program name: pass os.Args[1:]. argv[0] is deliberately
// not consulted, because the bootstrap ends in `exec "$target" "$@"` and so
// argv[0] is the version-stamped cache path on every legitimate call (6.4).
//
// environ is the process environment in os.Environ() form; commands read
// configuration from it rather than from os.Getenv, which is the static
// half of the environment-isolation rule of 3.2.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	streams := cli.Streams{In: stdin, Out: stdout, Err: stderr}

	// The multi-call branches. They are intercepted before the human
	// command table because each needs process-level machinery the table
	// has no place for: a hook must never fail its session (6.3: every
	// subcommand exits 0 on every failure, with its context on stdout and
	// its diagnostic on stderr), the watcher detaches with Setsid and
	// writes nothing to its stdout (6.6), and an adapter speaks BAP/1 on
	// stdio rather than human output (4.1). Each gets the REAL process
	// streams and the environment as given, and its production
	// dependencies: the hook's clock, watcher spawner, registry, settings
	// reader and PATH search; the watcher's process facts, signals,
	// socket poster and schedules.
	if len(args) > 0 {
		if cmd, ok := cli.LookupMultiCall(args[0]); ok {
			switch cmd.Name {
			case adapterEntry:
				return runAdapter(cmd, args[1:], stdin, stdout, stderr, environ)
			case hookEntry:
				return hook.Run(args[1:], streams, environ, hook.RealDeps())
			case watchEntry:
				return watch.Run(args[1:], streams, environ, watch.RealDeps())
			}
			return cli.NotImplemented(cmd, args, streams)
		}
	}

	return cli.Dispatch(args, streams, environ)
}

// The multi-call words (6.4): the hidden entrypoints hooks.json, the
// SessionStart hook and the adapter_command resolution use.
const (
	// adapterEntry is the hidden multi-call word of D26: `brigade adapter
	// <name> <group> <verb> …`.
	adapterEntry = "adapter"
	// hookEntry is `brigade hook session-start|prompt|session-end` (6.3).
	hookEntry = "hook"
	// watchEntry is `brigade watch [--sink <file>]`, the detached watcher
	// the SessionStart and prompt hooks start (6.6).
	watchEntry = "watch"
)

// bundledSupabase is the one bundled adapter name of v1.
const bundledSupabase = "supabase"

// runAdapter hands `brigade adapter supabase …` to the bundled adapter's
// Run seam with the REAL process streams — stdin included, because
// adapterkit.ReadInput's terminal refusal and the team commands' --prompt
// path both test the descriptor — and the environment as given. The
// adapter speaks BAP/1 on stdout from here on: its own poison scan, flag
// rules and 4.3 envelopes apply, not the human command table's. A
// missing or unknown adapter name is `usage` through the table's own
// reporter, so nothing here ever names os.Stdout.
func runAdapter(cmd cli.Command, rest []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	if len(rest) > 0 && rest[0] == bundledSupabase {
		return supabase.Run(rest[1:], stdin, stdout, stderr, environ)
	}
	streams := cli.Streams{In: stdin, Out: stdout, Err: stderr}
	if len(rest) == 0 {
		return cli.Usage(cmd, rest, streams, "adapter needs a name; the bundled adapter is `supabase`")
	}
	return cli.Usage(cmd, rest, streams, "unknown adapter name; the bundled adapter is `supabase`")
}
