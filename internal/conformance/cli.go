package conformance

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// Main is the binary's entry point: it parses os.Args, runs the suite with
// the process's own streams and environment, and exits with the status
// Run returns. It is the only file in this package that may name
// os.Stdout and os.Exit (plan 7.3; the lint config exempts it).
func Main(cases []Case) {
	opts, err := ParseArgs(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		Usage(os.Stdout)
		os.Exit(ExitPass)
	}
	if err != nil {
		_, _ = io.WriteString(os.Stderr, "brigade-conformance: "+err.Error()+"\n")
		Usage(os.Stderr)
		os.Exit(ExitUsage)
	}
	// A consumer that closes the pipe early (`brigade-conformance -v | head`)
	// must not kill the process with SIGPIPE before the run directory is
	// removed: with the signal ignored the write fails with EPIPE, which
	// every writer here discards, and the run finishes and cleans up.
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := Run(ctx, opts, cases, os.Environ(), os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
