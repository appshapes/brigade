package rest

import (
	"context"
	"io"
	"time"
)

// Run executes one adapter command and returns its 4.6 exit status. It is
// the seam internal/app dispatches `brigade adapter rest …` to and the one
// every test drives in-process: argv (after the adapter name), the three
// streams and the environment are all parameters, so no test ever reads
// the developer's own environment or writes their own state directories.
// Nothing here exits: the dispatcher flushes stdout before the process
// does (4.1), and only package main names os.Exit.
//
// stdin must be the process's real os.Stdin when the adapter is run from
// an entry point: adapterkit.ReadInput's terminal refusal and the --prompt
// path of the team commands both test the descriptor.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	return run(args, stdin, stdout, stderr, environ, options{})
}

// options are the two things a test replaces: the clock, and the watch's
// intervals and the context its stop signal is derived from. The zero
// value is production: time.Now, defaultTiming and context.Background.
type options struct {
	now    func() time.Time
	timing *timing
	base   context.Context
}

func (o options) clock() func() time.Time {
	if o.now != nil {
		return o.now
	}
	return time.Now
}

func (o options) intervals() timing {
	if o.timing != nil {
		return *o.timing
	}
	return defaultTiming()
}

func (o options) context() context.Context {
	if o.base != nil {
		return o.base
	}
	return context.Background()
}
