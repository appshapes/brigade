// Package bootstrap holds no Go code that ships. It exists for exactly one
// thing: the test of plugin/bin/brigade, the POSIX-sh bootstrap the Claude
// Code plugin puts on the Bash tool's PATH (plan 6.2).
//
// The bootstrap is shell, not Go, because it must run before any Go binary
// exists on the machine — it is the thing that fetches one. So its test
// drives the real file as a subprocess against an httptest server standing
// in for the GitHub release, with HOME and the XDG directories pointed at
// a temp tree and BRIGADE_RELEASE_BASE_URL pointed at the server: nothing
// here ever reads the developer's own ~/.local/share cache, their
// ~/.config/brigade/dev-binary pointer, or the network.
//
// This package is the macOS/dash leg of the three-way matrix P1-8 owes
// plan 6.2. The busybox leg (ash + wget + sha256sum) is
// scripts/ci/bootstrap-alpine.sh, run locally under docker; the Ubuntu
// leg is this same test in the CI `fast` job.
package bootstrap
