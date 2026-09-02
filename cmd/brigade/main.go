// Command brigade is the one shipped Brigade binary: the human and model
// command surface, the Claude Code lifecycle hooks, the detached watcher
// and the bundled adapters, all in one multi-call image (D35).
//
// x509usefallbackroots=1 makes the Mozilla root bundle linked by
// golang.org/x/crypto/x509roots/fallback (imported by the Supabase
// adapter) the verifier's roots whenever the system roots cannot be
// loaded: Go's darwin verifier cannot reach Security.framework inside the
// Bash sandbox and minimal Linux containers ship no CA bundle (plan 5.1).
// The directive is honoured only in package main, which is why it lives
// here and not beside the import.
//
//go:debug x509usefallbackroots=1
package main

import (
	"os"

	"github.com/appshapes/brigade/internal/app"
)

// main is the only place in this binary that names the process streams and
// the only one that exits: everything below returns an exit status, so a
// half-written envelope can never be truncated by an os.Exit (7.3).
func main() {
	os.Exit(app.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
}
