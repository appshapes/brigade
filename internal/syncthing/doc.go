// Package syncthing is the one Syncthing instance Brigade runs on a machine
// (plan folder-sync 4.4; card 141): its home under the Brigade state
// directory, the lifecycle of the `syncthing serve` daemon (attach, detach,
// the per-session references, the pidfile, the GUI and listen ports) and
// the REST client the two adapters that drive it share — the sync adapter
// (internal/syncadapters/syncthing), which keeps a project's folders in
// step, and the message adapter (internal/adapters/syncthing), whose
// backend is a team folder the same instance replicates.
//
// This package is the ONE place Brigade starts a long-running daemon, which
// is why engine.go has its own exec carve-out in .golangci.yml. The daemon
// is spawned as an argv array with the allow-listed environment of
// adapterkit.ChildEnv; its REST API is bound to 127.0.0.1 on a port
// Brigade picks, and the API key is read from config.xml on every call —
// never put on argv, never logged. Syncthing's response bodies are never
// logged either.
//
// Nothing here imports internal/harness or internal/adapters: the harness
// reaches the engine only across a process boundary, through the two
// adapters' protocols.
package syncthing
