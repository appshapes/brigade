// Command brigade-adapter-fs is the Brigade filesystem adapter: a second
// BAP/1 implementation whose backend is a directory on disk (plan P1-5).
//
// It exists so the conformance suite and the harness tests can run a
// complete adapter in seconds, and so the plugin can be proved to carry no
// Supabase assumption (D3). It is INSECURE and TEST-ONLY — any process
// that can read the root can read every team's messages — and it is a
// development binary: `make build` produces it, nothing ships it.
//
// The implementation lives in internal/adapters/fs; see its README.md for
// the store layout, the root resolution and the three build-tagged
// mutants.
package main

import "github.com/appshapes/brigade/internal/adapters/fs"

func main() { fs.Main() }
