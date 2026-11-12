// Package rest is the bundled Brigade adapter for a server that implements
// the backend REST contract (docs/backend-rest-v1.md): BAP/1
// (docs/protocol-v1.md) carried over HTTP. It is reached as `brigade
// adapter rest <group> <verb> [flags]`, a hidden multi-call entry of the
// one shipped binary; internal/harness never imports it (depguard) and
// talks to it only as a child process speaking BAP/1.
//
// The split is the contract's: `describe`, `profile init`, `profile
// status` and the local binding rules are answered from this adapter's own
// files and never touch the network; every other command is one POST to
// `<base>/v1/…` whose body is the command's input and whose answer is the
// command's result envelope; `message watch` is a loop over
// `/v1/message/receive` (the catch-up, long polls behind a cursor, a
// periodic drain) with the stdin commands as requests of their own.
//
// Files in the profile directory ${BRIGADE_CONFIG_DIR}/teams/<name>/:
//
//	team.json        adapterkit.Profile with adapter "rest", the server's
//	                 base url (configuration, never a secret) and the team
//	                 binding
//	credential.json  the credential the server issued, with the principal
//	                 it names and last_team_ref, mode 0600, written
//	                 atomically. It is never printed, never logged and
//	                 never on argv.
//
// What the adapter does with an answer (the contract's section 1): a body
// that is a valid result envelope is the answer, whatever the HTTP status;
// anything else is `unavailable` when the status is 5xx or the request
// failed in transport (refused, timed out, TLS), and `internal` otherwise.
// A result is decoded into its typed shape and validated before it is
// printed, so a server that answers a malformed result is `internal` and
// an unknown member never reaches stdout.
//
// Environment: team.json is the configuration. BRIGADE_REST_URL is
// honoured by `team create` and `team join` only, and only when the
// profile names no backend yet (4.1: a BRIGADE_<ADAPTER>_* variable is for
// a human's shell and for the conformance suite; under a live session it
// never arrives). The url is then written to team.json as `profile init`
// would write it. BRIGADE_TEST_OFFLINE=1 makes every dial fail loudly
// (C-01, C-07), and the proxy variables, SSL_CERT_FILE and SSL_CERT_DIR
// are honoured as the Supabase adapter honours them.
//
// The package mirrors internal/adapters/supabase in structure (the same
// dispatch and flag rules, the same poison flag, the same order of checks:
// argv, profile, stdin, then the server) without importing it. The
// stand-in server the tests and the conformance run use is the subpackage
// stub.
package rest
