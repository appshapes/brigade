// Package conformance is the BAP/1 conformance suite of plan 9.2: the
// library behind the dev binary cmd/brigade-conformance and behind the
// `go test` subtests of suite_test.go (TestConformanceFS/C-25), so that
// `go test ./...` stays a complete gate on its own. The cases themselves
// live in the sibling package cases (one file per plan case, C-01..C-43
// plus C-03b, C-19b and C-29b); this package never imports it, and the
// case authors code against the T API of t.go and the WatchProc API of
// watch.go, nothing else.
//
// # What a run is
//
// Run resolves the adapter under test (a bare name is looked up on PATH,
// exactly as the harness does), creates one run directory with
// os.MkdirTemp("", "brigade-conformance-"), runs `describe` once against
// a scratch principal to learn the adapter's name, version, capabilities,
// limits, lease range and retention floors, selects the cases, runs them
// one after another in id order — they share a fixture and the adapter's
// rate budgets, so they are never parallel inside one run — and writes
// the report: the human table on stderr in every mode, and with --json
// the machine-readable document on stdout. stdout carries that document
// and nothing else. The run directory is removed at the end unless
// --keep-temp, in which case its path is printed on stderr.
//
// --shuffle <seed> runs the same cases in the permutation that seed names
// instead of in id order (0, the default, is id order). The cases share a
// fixture, a store and the adapter's rate budgets, so id order is one
// order out of many and a case that quietly depends on running after
// another one passes in it forever: the P1-6 verifier found two such cases
// with a throwaway tool the binary did not have. A non-zero seed is
// printed on the human summary's first line and carried in the JSON
// report's `shuffle`, and `results` is in execution order, so a failure is
// replayed with the same seed. TestConformanceFSWholeRun runs shuffled by
// default, with a seed taken from the clock and logged.
//
// A selection that matches NO case is a usage error (exit 2), not a pass:
// `--tags cap:nosuch` selecting nothing and reporting "0 passed, 0 failed,
// 0 skipped" with exit 0 reads exactly like a clean run. The refusal names
// the selectors responsible and happens before the adapter is launched.
// Note that a case reported SKIP is still selected — `--tags slow` without
// --slow selects C-14 and exits 0.
//
// Every adapter process gets an environment built from scratch (never
// os.Environ wholesale): PATH and TMPDIR copied from the suite's own
// environment, HOME, BRIGADE_CONFIG_DIR and BRIGADE_STATE_DIR under the
// principal's directory, BRIGADE_PROFILE=default, BRIGADE_LOG_LEVEL=debug
// (so C-05's "no secret at debug" check is meaningful), the --env pairs
// and, with --shared-env NAME, NAME=<run>/shared. Nothing else: an adapter
// that needs another variable fails C-01 with a clear message, and that is
// a test (4.1, Environment). The launcher applies four global checks to
// every spawn and attributes a violation to the running case: the exit
// status is in 0..12 (B-11); stdout is exactly one JSON object followed by
// one newline and that object is a valid 4.3 envelope (4.1; a watch's
// stdout is NDJSON with an `event` on every line instead); no join secret
// the run knows, and no join-secret-shaped `brg1.<x>.<y>` token (the fixed
// protocol text `expected brg1.<team_ref>.<secret>` names the format, not
// a secret, and is not a hit) outside `team create`'s own stdout, appears
// on stdout or stderr (C-05); and at the end of the run the whole
// run directory — profile files, logs, the fs store — is scanned the same
// way, any hit being a C-05 failure.
//
// # The fixture
//
// Three principals: A and B in team T1, C in team T2, each with its own
// HOME, BRIGADE_CONFIG_DIR and BRIGADE_STATE_DIR under
// <run>/principals/<name>/, and one registered session each (fixture-a,
// fixture-b, fixture-c). It is built lazily on the first case that asks
// for it, so `--only C-36` builds it and a case on scratch principals does
// not. With --setup <cmd> the command runs once per principal in that
// principal's environment (argv split on whitespace, no shell) and must
// leave the three profiles joined; otherwise the suite provisions them
// through `team create` and `team join`, which requires those two
// capabilities. Cases that need more principals than the fixture offers
// (C-28, C-40) join their own with T.JoinPrincipal, because the frozen
// principal_send_rate is 60 per minute and the whole fs run takes seconds:
// a case that sent 60 messages from A would drain A's budget for every case
// after it.
//
// # Tags and selection
//
// Every case carries `core`; a case that needs a capability carries
// `cap:<capability>` and is reported SKIP ("capability not advertised")
// when describe does not list it; a case tagged `slow` runs only with
// --slow and is reported SKIP otherwise. --only and --skip take
// comma-separated case ids, case-insensitively, and an unknown id is a
// usage error (exit 2). --tags is any-of. The exit status is 0 when every
// selected case passed (skips allowed), 1 on any failure, 2 on a usage
// error and 3 on a launcher error: adapter not found, describe not ok or
// unparseable or a protocol major other than "1", --setup failing, the
// fixture failing to build.
//
// # The mutants
//
// The suite's own positive control is mutants_test.go: four deliberately
// broken builds of the fs adapter, each a build-tagged twin inside
// internal/adapters/fs, must fail EXACTLY the cases named here and no
// other, and the normal build must fail none.
//
//	mutant_noack        the ack routine reports `acked` and moves nothing
//	                    (4.5.3)  ->  C-29b, C-30, C-36, C-41
//	mutant_teamleak     `session list` walks every team, not the profile's
//	                    (4.5.6)  ->  C-12, C-26
//	mutant_trustsender  the forbidden members of 4.4.6 are accepted and a
//	                    forged sender is trusted (4.5.5, 4.5.7)  ->  C-23, C-24
//	mutant_caporder     the two unacknowledged caps of 4.5.12 are checked
//	                    in the wrong order — the recipient-wide one before
//	                    the per-pair one  ->  C-28
//
// The fourth exists because that order was the decisive untested defect in
// both P1-5 and P1-6: every other property of the caps survives the swap,
// and only a case that puts BOTH caps at their limit at once can see it.
// An expected set is never widened to fit a case that turns out to depend
// on a mutation the table does not foresee; that is reported instead.
//
// # Deadlines
//
// Every wait has one. A request/response command gets --timeout (20 s by
// default); a watch runs until the case closes it or the runner kills it at
// the end of the case. T.PushDeadline is 5 s for an adapter advertising
// message.watch.push, and also 5 s for a polling adapter: 4.4.9 says "two
// poll intervals" for those, but BAP/1 carries no poll-interval member in
// describe, so the suite has nothing to read — the driver has recorded
// that as a BAP/1.x question and the suite does not invent a member. The
// exit deadlines are the spec's own: 5 s after stdin EOF, a close command
// or SIGTERM (C-38, C-41), 10 s for the C-37 error exit.
package conformance
