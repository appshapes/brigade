// Package adapterkit is the shared plumbing every Brigade adapter uses
// (plan P1-3): the bounded stdin document reader with its TTY refusal, the
// result printer (the only place under internal/ that may write protocol
// output to os.Stdout, plan 7.3), the XDG directory resolution of 3.2, the
// atomic 0600 file writer and the strict 0600 reader (U-10), the advisory
// flock helper on a sidecar file with its 10 s bound (5.1, corrected by
// E0-6), the O_EXCL pidfile helper with compare-then-delete removal
// (E0-5), and the profile file schema of 5.2.
//
// Everything here maps failures onto internal/protocol's 4.6 taxonomy: a
// helper that refuses something returns a *protocol.Error whose Code
// selects the exit status, and the caller hands it to WriteError or
// PrintError. Nothing here re-declares codes, exits or envelope shapes.
//
// Two adapterkit deliverables live elsewhere in this package's plan row
// and are NOT in this file set: the child spawn helper (spawn.go) and the
// redacting slog handler (the adapterkit/log subpackage).
//
// stdin discipline (4.1): a command that takes an input document calls
// ReadInput exactly once; a command that takes no input MUST NOT read
// stdin at all — the harness spawns such commands with stdin ignored, and
// a read would block forever when a human runs the adapter by hand.
package adapterkit
