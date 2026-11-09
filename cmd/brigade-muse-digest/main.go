// Command brigade-muse-digest computes the package digest of the Muse Code
// package and keeps marketplace.json's copy of it (cards 115 and 114):
//
//	brigade-muse-digest print [<package directory>]   print the digest (`sha256:<64 hex>`) of muse/brigade/,
//	                                                  or of any Muse package directory
//	brigade-muse-digest write                         rewrite marketplace.json's `integrity.digest`, and its
//	                                                  `version` from muse/brigade/bin/VERSION
//	brigade-muse-digest check                         hold marketplace.json against what `write` would write
//	                                                  (the digest and the version); exit 1 on a difference
//
// Muse installs a plugin from a marketplace only when the catalog entry's
// digest equals the one it computes over the package tree. internal/musedigest
// computes the same value without Muse, so `make muse-digest` (write),
// `make muse-plugin-check` (check), scripts/release-prep.sh (write) and
// release.yml (check) run this with `go run` from the repository root, on a
// machine with or without Muse Code.
//
// The command is DEV-ONLY and never shipped; `make build` does not build it.
package main

import (
	"io"
	"os"

	"github.com/appshapes/brigade/internal/musedigest"
)

const usage = "usage: brigade-muse-digest print [<package directory>] | write | check\n"

func main() {
	root, err := os.Getwd()
	if err != nil {
		os.Exit(fail(os.Stderr, err))
	}
	os.Exit(run(os.Args[1:], root, os.Stdout, os.Stderr))
}

// run is the command with its streams and its directory handed in: root is the repository root, where
// marketplace.json and muse/brigade/ are.
func run(args []string, root string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	switch {
	case args[0] == "print" && len(args) == 2:
		digest, err := musedigest.SumDir(args[1])
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = io.WriteString(stdout, digest+"\n")
	case args[0] == "print" && len(args) == 1:
		digest, err := musedigest.Package(root)
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = io.WriteString(stdout, digest+"\n")
	case args[0] == "write" && len(args) == 1:
		version, digest, err := musedigest.Write(root)
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = io.WriteString(stdout, "muse-digest: wrote "+musedigest.CatalogFile+": version "+version+", digest "+digest+"\n")
	case args[0] == "check" && len(args) == 1:
		digest, err := musedigest.Check(root)
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = io.WriteString(stdout, "muse-digest: ok: "+musedigest.CatalogFile+"'s digest is the package digest of "+musedigest.PackageDir+"/ ("+digest+")\n")
	default:
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	return 0
}

// fail prints the error and returns exit code 1: a stale catalog, a tree
// that is no package and an unreadable file all fail the same way.
func fail(stderr io.Writer, err error) int {
	_, _ = io.WriteString(stderr, "muse-digest: "+err.Error()+"\n")
	return 1
}
