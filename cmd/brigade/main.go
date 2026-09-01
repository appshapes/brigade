// Command brigade is the one shipped Brigade binary: the human and model
// command surface, the Claude Code lifecycle hooks, the detached watcher
// and the bundled adapters, all in one multi-call image (D35).
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
