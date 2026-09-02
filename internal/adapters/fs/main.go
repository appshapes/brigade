package fs

import "os"

// This file is the adapter's entry point and the ONE file in the package
// that may name os.Exit and os.Stdout (the .golangci.yml exclusions are
// anchored to this exact path, plan 7.3). Everything else takes streams as
// parameters, so nothing else can write to the process's stdout by
// accident and nothing else can exit before stdout is flushed.

// Main runs the adapter with the process's own argv, streams and
// environment and exits with the 4.6 status of the command (4.1: the
// dispatcher flushes stdout before the process exits, so a result is never
// truncated by an early exit).
func Main() {
	//nolint:forbidigo // an entry point must name the process's real streams (7.3)
	os.Exit(Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
}
