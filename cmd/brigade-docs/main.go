// Command brigade-docs is the documentation gate's front end (card 110):
//
//	brigade-docs check      run every rule of internal/docscheck against the tree; exit 1 on a finding
//	brigade-docs generate   rewrite the generated reference pages: docs/developer/reference/make-targets.md
//	                        from the Makefile's help lines and docs/developer/reference/commands.md from
//	                        internal/cli's command table (card 112)
//
// `make docs-check` and `make docs-generate` run it with `go run` from the
// repository root. The same rules run in `make test` through
// scripts/ci/docs_check_test.go, which is what CI, `make push` and the release
// sequence execute; this command exists so a person can read the findings
// without a test harness around them.
//
// The command is DEV-ONLY and never shipped; `make build` does not build it.
package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/appshapes/brigade/internal/docscheck"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	root, err := os.Getwd()
	if err != nil {
		return internalError(stderr, err)
	}
	if len(args) != 1 {
		_, _ = io.WriteString(stderr, "usage: brigade-docs check | generate\n")
		return 2
	}
	switch args[0] {
	case "check":
		out, err := docscheck.Check(root)
		_, _ = io.WriteString(stdout, out)
		switch {
		case err == nil:
			return 0
		case errors.Is(err, docscheck.ErrFindings):
			return 1
		default:
			return internalError(stderr, err)
		}
	case "generate":
		targets, n, err := docscheck.GenerateMakeTargets(root)
		if err != nil {
			return internalError(stderr, err)
		}
		if err := writePage(root, docscheck.MakeTargetsPage, targets); err != nil {
			return internalError(stderr, err)
		}
		_, _ = io.WriteString(stdout, "brigade-docs: wrote "+docscheck.MakeTargetsPage+" ("+strconv.Itoa(n)+" targets)\n")
		commands, m := docscheck.GenerateCommands()
		if err := writePage(root, docscheck.CommandsPage, commands); err != nil {
			return internalError(stderr, err)
		}
		_, _ = io.WriteString(stdout, "brigade-docs: wrote "+docscheck.CommandsPage+" ("+strconv.Itoa(m)+" commands)\n")
		return 0
	default:
		_, _ = io.WriteString(stderr, "usage: brigade-docs check | generate\n")
		return 2
	}
}

// writePage writes one generated page under root, creating its directory.
func writePage(root, rel string, page []byte) error {
	target := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	//nolint:gosec // G306: a committed documentation page, not state; the mode is what git records for every page
	return os.WriteFile(target, page, 0o644)
}

func internalError(stderr io.Writer, err error) int {
	_, _ = io.WriteString(stderr, "brigade-docs failed (internal): "+err.Error()+"\n")
	return 1
}
