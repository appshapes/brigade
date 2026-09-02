package supabase

import (
	"io"
	"time"
)

// Run executes one adapter command and returns its 4.6 exit status. It is
// the seam internal/app dispatches `brigade adapter supabase …` to and the
// one every test drives in-process: argv (after the adapter name), the
// three streams and the environment are all parameters, so no test ever
// reads the developer's own environment or writes their own state
// directories. Nothing here exits: the dispatcher flushes stdout before
// the process does (4.1), and only package main names os.Exit (7.3).
//
// stdin must be the process's real os.Stdin when the adapter is run from
// an entry point: adapterkit.ReadInput's terminal refusal and the
// --prompt path of the team commands both test the descriptor.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	return run(args, stdin, stdout, stderr, environ, time.Now)
}
