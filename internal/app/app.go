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

	"github.com/appshapes/brigade/internal/cli"
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
	// command table because each will need process-level machinery the
	// table has no place for: a hook must never fail its session (4.6), the
	// watcher detaches with Setsid (6.6), and an adapter speaks BAP/1 on
	// stdio rather than human output (4.1).
	if len(args) > 0 {
		if cmd, ok := cli.LookupMultiCall(args[0]); ok {
			return cli.NotImplemented(cmd, args, streams)
		}
	}

	return cli.Dispatch(args, streams, environ)
}
