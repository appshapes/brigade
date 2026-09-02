// Package supabase is the bundled Brigade adapter for a Supabase backend
// (plan section 5, protocol docs/protocol-v1.md): one anonymous GoTrue
// principal per profile, every write through the `brigade.*` RPCs of the
// finished migrations over PostgREST, and a Phoenix channel over
// coder/websocket for the watch. It is reached as `brigade adapter
// supabase <group> <verb> [flags]`, a hidden multi-call entry of the one
// shipped binary (D26, D35); internal/harness never imports it (depguard)
// and talks to it only as a child process speaking BAP/1.
//
// Files in the profile directory ${BRIGADE_CONFIG_DIR}/profiles/<name>/:
//
//	profile.json       adapterkit.Profile with adapter "supabase", the backend
//	                   url and publishable_key (configuration, never a
//	                   secret), and the team binding (5.2)
//	session.json       the GoTrue session — access_token, refresh_token,
//	                   expires_at, user — plus last_team_ref, mode 0600,
//	                   written atomically; the credential (5.1)
//	session.json.lock  the flock sidecar every read-refresh-write holds
//
// The error mapping, in one sentence: a PostgREST error body is parsed
// first whatever the HTTP status, a `brigade:<code>[:<reason>]` message
// names the 4.6 code directly, otherwise the SQLSTATE decides (28000 → 4,
// 42501 → 5 or 4 without a JWT, PT404 and P0002 → 6, 23505 → 7, 57014 → 9,
// PGRST202/PGRST205 → 1 "migration drift", PGRST301/PGRST303 → one refresh
// and one retry then 4), and a transport, DNS, TLS or non-PostgREST 5xx
// failure is 9; every error.message is this package's own fixed text and
// the server's goes to stderr at debug through the redacting logger.
//
// Environment: the adapter reads no BRIGADE_* configuration of its own —
// profile.json is the configuration — with one exception for a human
// shell (4.1): BRIGADE_SUPABASE_URL and BRIGADE_SUPABASE_PUBLISHABLE_KEY
// are honoured by `team create` and `team join` only, and only when the
// profile names no backend yet (the pair is then written to profile.json
// as `profile init` would write it; a configured profile is never
// overridden). This is how the conformance suite provisions its own
// principals without a --setup hook. The rest are test-only switches:
// BRIGADE_TEST_OFFLINE=1 makes every dial fail loudly (C-01, C-07),
// BRIGADE_SUPABASE_REALTIME_VSN selects the Phoenix serializer, and
// SSL_CERT_FILE/SSL_CERT_DIR are honoured for the trust store.
//
// The package mirrors internal/adapters/fs in structure — the same dispatch
// and flag rules, the same poison flag, the same logger wiring, the same
// order of checks (profile, then stdin, then the backend) — without
// importing it. run.go, client.go, gotrue.go, postgrest.go, credentials.go,
// errors.go, profile.go, describe.go and events.go are the core (P2-6);
// team.go (P2-7), session.go (P2-8), message.go (P2-9), and watch.go with
// realtime.go (P2-10, the drain over fetch_inbox with the private Phoenix
// channel as a hint) carry the verbs.
package supabase
