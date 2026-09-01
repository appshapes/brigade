// Package buildinfo carries the version string of the running binary.
//
// The release path stamps [Version] at link time:
//
//	-X github.com/appshapes/brigade/internal/buildinfo.Version=0.1.0
//
// `go install github.com/appshapes/brigade/cmd/brigade@v0.1.0` applies no
// ldflags, so Version is empty there and [String] falls back to the module
// version recorded by the toolchain in the binary's build info.
package buildinfo

import "runtime/debug"

// Version is set at link time by the -X flag above. It is deliberately a
// plain var and not a const: a const would be inlined and -X could not
// reach it.
var Version string

// Unknown is reported when neither the link-time stamp nor the embedded
// build info names a version — a plain `go build` with -buildvcs=false, or
// `go run`.
const Unknown = "unknown"

// devel is what the toolchain records in BuildInfo.Main.Version for a
// binary built from a module checkout rather than from a module version.
const devel = "(devel)"

// String reports the binary's version. It never returns the empty string.
func String() string { return resolve(Version, debug.ReadBuildInfo) }

// resolve is the testable core of String. read is injected so the fallback
// branch — the one `go install` takes, which no test can reach by building
// itself — can be exercised directly.
func resolve(stamped string, read func() (*debug.BuildInfo, bool)) string {
	if stamped != "" {
		return stamped
	}
	if info, ok := read(); ok && info != nil {
		if v := info.Main.Version; v != "" && v != devel {
			return v
		}
	}
	return Unknown
}
