// Command brigade-packages is the one check of the harness packages (work
// item 382): `brigade-packages check <harness id> [arguments]` holds the
// package of that harness in docs/harnesses.json to its rules, and
// `brigade-packages check` with no id holds every package. `make
// plugin-check`, `muse-plugin-check`, `codex-plugin-check` and
// `grok-plugin-check` are names over it. It is a development tool and is
// never shipped: it reads the working tree it is run in, runs git, sh and
// shellcheck where they are, and a check changes nothing.
//
// `brigade-packages pin` says which release the Codex CLI and the Grok
// Build package are pinned to in their marketplace files, and
// `brigade-packages pin <commit>` writes that commit into both (work item
// 424, ADR 0030). The second is the release sequence's
// (scripts/release-pin.sh): never run it by hand.
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
	if len(args) == 0 || (args[0] != "check" && args[0] != "pin") || (args[0] == "pin" && len(args) > 2) {
		fmt.Fprintln(os.Stderr, "usage: brigade-packages check [<harness id> [arguments]]\n       brigade-packages pin [<commit>]")
		return 2
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "brigade-packages: the working directory cannot be told:", err)
		return 2
	}
	opts := packagecheck.Options{Root: root, Environ: os.Environ(), Stdout: os.Stdout, Stderr: os.Stderr}
	if args[0] == "pin" {
		return pin(opts, args[1:])
	}
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

// pin writes the commit into both marketplace files when one is given, and
// says where the pin then stands: exit status 1 when the tree breaks one
// of its rules. A commit that would break one is refused with both files
// as they were.
func pin(opts packagecheck.Options, commit []string) int {
	var state packagecheck.Pin
	var err error
	if len(commit) == 1 {
		state, err = packagecheck.MovePin(opts, commit[0])
	} else {
		state, err = packagecheck.ReadPin(opts)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "packages-pin:", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "packages-pin: "+state.String())
	return 0
}
