//go:build windows

package main

import (
	"os"
	"path/filepath"
)

// main is the only place in this program that names the process streams and the only one that exits:
// everything else returns an exit status.
func main() {
	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		_, _ = os.Stderr.WriteString("brigade: cannot resolve the launcher's own path: " + err.Error() + "\n")
		os.Exit(exitConfig)
	}
	environ := os.Environ()
	l := &Launcher{
		Self:    self,
		BinDir:  filepath.Dir(self),
		Env:     environMap(environ),
		Environ: environ,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Arch:    nativeArch,
		Fetch:   curlFetch(environ),
		Run:     runWith(os.Stdin, os.Stdout, os.Stderr),
		Detach:  detach,
	}
	os.Exit(l.Main(os.Args[1:]))
}
