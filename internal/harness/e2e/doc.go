// Package e2e holds the end-to-end harness tests of plan 9.5 (the
// integrator's half of P3-3/P3-4/P3-5, E2E-01's fs precursor): the built
// `brigade` binary and the built fs adapter, driven as real processes —
// the terminal pass-through that creates and joins a team, the three
// lifecycle hooks with their stdin documents and session environments, the
// REAL detached watcher the SessionStart hook spawns, `brigade send` from
// a second principal's own session, the frame arriving at a fake inbox
// socket, and the token appearing in no file and on no argv afterwards
// (U-25's whole-tree grep).
//
// The package has no non-test code: nothing here is linked into the
// shipped binary, and every process a test starts is SIGTERMed and reaped
// in its Cleanup, the detached watcher from its pidfile.
package e2e
