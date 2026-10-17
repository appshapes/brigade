# Run the tests

```sh
make test                                  # unit + testscript + harness + conformance(fs), -race, coverage profile
make supabase-start supabase-env && make test-all   # + pgTAP, advisor lints, integration, conformance(supabase), e2e
```

`make test` is Docker-free and stack-free. The live Supabase tests are opt-in behind `BRIGADE_TEST_LIVE=1`, which
only `make test-integration` (and CI's `supabase` job) sets; `make test-db` runs the pgTAP tests in
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
from scratch. `make test` runs it against the fs adapter; `make conformance adapter=<executable> [args="-- fixed
args"]` runs it against any adapter, with `--shared-env <VAR>` when every principal must share a directory and
`--setup <cmd>` when principals are provisioned out of band ([Write an adapter](../change/adapters.md)). Its own
positive control is four deliberately broken builds of the reference adapter, each of which must fail exactly its
own cases.

## The drift tests in scripts/ci

`go test ./scripts/ci` runs the Go tests that prove each CI script can fail, against fixture trees: the plugin
check, the secret scan, the checksums check, the release verify, the keep-alive, the release-notes lint, the setup
copies join (`setup_docs_test.go`: the same commands in the skill and the member pages) and the documentation gate
(`docs_check_test.go`). They run in `make test`, so they bite before a push.
