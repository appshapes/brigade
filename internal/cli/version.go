package cli

import (
	"io"
	"runtime"
	"strconv"

	"github.com/appshapes/brigade/internal/buildinfo"
)

// versionResult is the `result` object of `brigade version --json`.
type versionResult struct {
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// runVersion prints the version. The human form is the bare version string
// and nothing else: `bin/brigade version` is asserted byte-for-byte by the
// Makefile's acceptance check and by CI, so build details belong in the
// --json form only.
func runVersion(cx *Context, args []string) error {
	if len(args) > 0 {
		return usagef("version", "version takes no arguments, got "+strconv.Quote(args[0]))
	}
	if cx.JSON {
		return WriteResult(cx.Out, versionResult{
			Version:   buildinfo.String(),
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
		})
	}
	_, err := io.WriteString(cx.Out, buildinfo.String()+"\n")
	return err
}

// runHelp prints the top-level usage block, or one command's.
func runHelp(cx *Context, args []string) error {
	all := cx.Bool("all")
	switch len(args) {
	case 0:
		writeUsage(cx.Streams, cx.JSON, usageAll(all))
		return nil
	case 1:
		cmd, ok := Lookup(args[0])
		if !ok {
			return usagef("help", "unknown command "+strconv.Quote(args[0])+"; run `"+Program+" help` for the command list")
		}
		writeUsage(cx.Streams, cx.JSON, usageCommand(cmd))
		return nil
	default:
		return usagef("help", "help takes at most one command name")
	}
}
