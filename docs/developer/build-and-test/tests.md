# Run the tests

```sh
make test                                  # unit + testscript + harness + conformance(fs) + conformance(rest), -race, coverage profile
make supabase-start supabase-env && make test-all   # + pgTAP, advisor lints, integration, conformance(supabase), e2e
```

`make test` is Docker-free and stack-free. The live Supabase tests are opt-in behind `BRIGADE_TEST_LIVE=1`, which
only `make test-integration-adapter` sets: `make test-integration` runs it, then `make test-integration-conformance`
(CI's `supabase` job runs the first, its `e2e` job the second); `make test-db` runs the pgTAP tests in
`supabase/tests`; `make advisor-lints` mirrors Supabase's Security Advisor lints with the database container's
`psql`.

## The real Syncthing

The Syncthing adapter's tests use a fake REST server and a fake `syncthing` script launched as `/bin/sh <script>`.
Two tests run the real daemon on `PATH` and are skipped unless `BRIGADE_TEST_SYNCTHING=1`, so `make test` starts no
daemon and opens no port:

```sh
BRIGADE_TEST_SYNCTHING=1 go test -run TestRealSyncthingIntegration ./internal/syncadapters/syncthing
BRIGADE_TEST_SYNCTHING=1 go test -count=1 -timeout 15m -run TestTwoSessionsSyncAFolderThroughSyncthing ./internal/harness/e2e/ -v
```

The second is the two-session smoke: two personas, two instances, a file each way, a conflict, the stop with the
last session; a few minutes.

## The conformance suite

`brigade-conformance` is the arbiter for an adapter: 49 cases, three principals in two teams, an environment built
from scratch. `make test` runs it against the fs adapter and, through `bin/brigade-rest-stub` (a stand-in server
for [the backend REST contract](../../backend-rest-v1.md) that runs the fs adapter in-process), against the
bundled `rest` adapter; `make conformance adapter=<executable> [args="-- fixed args"]` runs it against any adapter, with `--shared-env <VAR>` when every principal must share a directory and
`--setup <cmd>` when principals are provisioned out of band ([Write an adapter](../change/adapters.md)). Its own
positive control is four deliberately broken builds of the reference adapter, each of which must fail exactly its
own cases.

## The server

```sh
make server-check        # rustfmt and clippy on server/, warnings denied
make server-test         # cargo test, then the conformance suite against a running brigade-server
```

`server/` is a Rust workspace outside `make test` ([Change the server](../change/server.md)). `make server-test`
builds the Go binaries, runs the workspace's tests, and then runs `brigade-conformance` through `bin/brigade`'s
`rest` adapter against a server it starts: on the file log, and with `--slow` on the Cavalry feed. It needs
`cargo`, Linux or macOS, and 201 MiB of room where Cavalry's driver directory goes (`/dev/shm` on Linux).

## The drift tests in scripts/ci

`go test ./scripts/ci` runs the Go tests that prove each CI script can fail, against fixture trees: the plugin
check, the secret scan, the checksums check, the release verify, the keep-alive, the release-notes lint, the setup
copies join (`setup_docs_test.go`: the same commands in the skill and the member pages) and the documentation gate
(`docs_check_test.go`). They run in `make test`, so they bite before a push.
