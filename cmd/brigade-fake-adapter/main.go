// Command brigade-fake-adapter is a scripted BAP/1 adapter used only as a
// test fixture (plan 9.5; brief section 2.10). It is a dev-only binary:
// `make build` produces it beside brigade-adapter-fs, `testutil.Build`
// compiles it for the harness tests, and nothing ships it.
//
// It speaks the protocol on argv/stdin/stdout, scripted by a JSON file
// named with a leading `--script <path>`, so the error paths the fs
// adapter cannot produce — a rate_limited on the third send, a
// protocol_version "2" describe, a runaway stdout, a signal death, a watch
// stream with an unknown event and an over-long line — are reproducible
// across a real process boundary. The scripting format and behaviour live
// in internal/testutil/fakeadapter.
package main

import (
	"os"

	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// main is the only place this dev binary names the process streams and the
// only one that exits.
func main() {
	//nolint:forbidigo // this is a main(); naming the process streams is the point (7.3)
	os.Exit(fakeadapter.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
}
