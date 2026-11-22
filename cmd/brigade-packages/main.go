// Command brigade-packages is the one check of the harness packages (work
// item 382): `brigade-packages check <harness id> [arguments]` holds the
// package of that harness in docs/harnesses.json to its rules, and
// `brigade-packages check` with no id holds every package. `make
// plugin-check`, `muse-plugin-check`, `codex-plugin-check` and
// `grok-plugin-check` are names over it. It is a development tool and is
// never shipped: it reads the working tree it is run in, runs git, sh and
// shellcheck where they are, and changes nothing.
package main

import (
	"fmt"
	"os"

	"github.com/appshapes/brigade/internal/docscheck"
	"github.com/appshapes/brigade/internal/packagecheck"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(os.Stderr, "usage: brigade-packages check [<harness id> [arguments]]")
		return 2
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "brigade-packages: the working directory cannot be told:", err)
		return 2
	}
	opts := packagecheck.Options{Root: root, Environ: os.Environ(), Stdout: os.Stdout, Stderr: os.Stderr}
	if len(args) > 1 {
		opts.Args = args[2:]
		return packagecheck.Run(args[1], opts)
	}
	// Every harness the registry lists: a harness added there with no
	// check is refused here, so a package cannot go unchecked.
	reg, err := docscheck.LoadRegistry(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brigade-packages:", err)
		return 2
	}
	for _, h := range reg.Harnesses {
		if status := packagecheck.Run(h.ID, opts); status != 0 {
			return status
		}
	}
	return 0
}
