// Package syncthing is Brigade's bundled sync adapter (plan folder-sync
// 4.4): `brigade sync-adapter syncthing <verb>`, one JSON request on stdin,
// one 4.3 result envelope on stdout, the verbs of the sync-adapter protocol
// (plan 4.3: describe, attach, apply, status, detach).
//
// It drives a DEDICATED Syncthing instance per machine, never the user's
// own: its home is <state_dir>/sync/syncthing (0700), holding Syncthing's
// config.xml, certificate and index beside Brigade's book-keeping —
// daemon.pid, port, lock, syncthing.log and one refs/<session_id> file per
// attached session. The first attach starts `syncthing serve` detached in
// its own session; the last detach shuts it down, so sync runs only while
// a session is active (plan section 1). Everything else — discovery,
// relays, transfer, conflicts, deletions, the trash can — is Syncthing's,
// with its defaults as they are (the owner's ruling of 2026-09-22: no
// options added).
//
// This package is the ONE place Brigade starts a long-running daemon, which
// is why it has its own exec carve-out in .golangci.yml. The daemon is
// spawned as an argv array with the allow-listed environment of
// adapterkit.ChildEnv; its REST API is bound to 127.0.0.1 on a port Brigade
// picks, and the API key is read from config.xml on every call — never put
// on argv, never logged. Syncthing's response bodies are never logged
// either.
//
// Nothing here imports internal/harness or internal/adapters: the harness
// reaches this package only across a process boundary, through the
// sync-adapter protocol (plan 4.3), exactly as it reaches an external
// `brigade-sync-<name>` adapter.
package syncthing
