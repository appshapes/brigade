// Package syncthing is Brigade's bundled sync adapter (plan folder-sync
// 4.4): `brigade sync-adapter syncthing <verb>`, one JSON request on stdin,
// one 4.3 result envelope on stdout, the verbs of the sync-adapter protocol
// (plan 4.3: describe, attach, apply, status, detach).
//
// It drives a DEDICATED Syncthing instance per machine, never the user's
// own — the engine of internal/syncthing, whose home is
// <state_dir>/sync/syncthing (0700). The first attach starts
// `syncthing serve` detached in its own session; the last detach shuts it
// down, so sync runs only while a session is active (plan section 1).
// Everything else — discovery, relays, transfer, conflicts, deletions, the
// trash can — is Syncthing's, with its defaults as they are (the owner's
// ruling of 2026-09-22: no options added) but one: the instance listens
// for peers on a port of its own (listen-port) instead of 22000, so it
// never shares a port with a Syncthing the person runs themselves.
//
// This package holds the sync adapter's POLICY over that engine (apply.go):
// which devices and folders an apply posts, what it keeps, moves, pauses
// and reports. Nothing here imports internal/harness or internal/adapters:
// the harness reaches this package only across a process boundary, through
// the sync-adapter protocol (plan 4.3), exactly as it reaches an external
// `brigade-sync-<name>` adapter.
package syncthing
