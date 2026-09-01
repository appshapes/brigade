// Command brigade-adapter-fs is the filesystem reference adapter: a second
// BAP/1 implementation used by the conformance suite and the harness tests
// to prove the plugin carries no Supabase assumptions (D3). It is a
// development binary — `make build` produces it, nothing ships it.
//
// P1-1 state: the adapter itself arrives with plan task P1-5. This binary
// exists now so that every Makefile target and the conformance launcher
// have a real executable to resolve from the first commit. It answers every
// invocation with the 4.3 error envelope on stdout and the `usage` exit
// status of 4.6, which is what an adapter does with a verb it does not
// implement (C-02).
package main

import (
	"io"
	"os"

	"github.com/appshapes/brigade/internal/cli"
)

// implTask is the plan task that builds the real adapter.
const implTask = "P1-5"

func main() { os.Exit(run(os.Stdout, os.Stderr)) }

func run(stdout, stderr io.Writer) int {
	err := &cli.Error{
		Code: cli.CodeUsage,
		Message: "brigade-adapter-fs is not implemented yet; it arrives with plan task " + implTask +
			". No BAP/1 verb is served.",
		Details: map[string]string{"reason": "not_implemented", "task": implTask},
	}
	// stdout carries the protocol envelope and nothing else (4.1); the
	// human line on stderr is what a person sees when they run it by hand.
	if werr := cli.WriteError(stdout, err); werr != nil {
		_, _ = io.WriteString(stderr, "brigade-adapter-fs failed (internal): "+werr.Error()+"\n")
		return cli.CodeInternal.Exit()
	}
	_, _ = io.WriteString(stderr, "brigade-adapter-fs failed (usage): "+err.Message+"\n")
	return err.Code.Exit()
}
