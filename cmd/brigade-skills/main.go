// Command brigade-skills writes the four packages' skills from their one
// source each, and checks that none is stale (work item 393;
// internal/skillgen). `make skills-generate` and `make skills-check` run it.
//
//	brigade-skills write [--out <dir>]   write every copy (into <dir>, to look at)
//	brigade-skills check                 exit 1 when a copy is not what its source renders
package main

import (
	"fmt"
	"os"

	"github.com/appshapes/brigade/internal/skillgen"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "brigade-skills:", err)
		return 2
	}
	switch {
	case len(args) == 1 && args[0] == "check":
		stale, err := skillgen.Check(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "brigade-skills:", err)
			return 2
		}
		for _, s := range stale {
			fmt.Fprintf(os.Stderr, "brigade-skills: %s\n", s)
		}
		if len(stale) > 0 {
			return 1
		}
		names, _ := skillgen.Sources(root)
		fmt.Fprintf(os.Stdout, "skills-check: %d skills, %d copies, ok\n", len(names), len(names)*len(skillgen.Harnesses))
		return 0
	case len(args) >= 1 && args[0] == "write":
		out := ""
		switch {
		case len(args) == 3 && args[1] == "--out":
			out = args[2]
		case len(args) != 1:
			fmt.Fprintln(os.Stderr, "usage: brigade-skills write [--out <dir>] | check")
			return 2
		}
		changed, err := skillgen.Write(root, out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "brigade-skills:", err)
			return 2
		}
		for _, p := range changed {
			fmt.Fprintln(os.Stdout, "wrote", p)
		}
		fmt.Fprintf(os.Stdout, "skills-generate: %d written\n", len(changed))
		return 0
	}
	fmt.Fprintln(os.Stderr, "usage: brigade-skills write [--out <dir>] | check")
	return 2
}
