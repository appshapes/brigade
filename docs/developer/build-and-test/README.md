# Build and test

## If you want to

| If you want to | Go to |
| --- | --- |
| run what a push and a release must pass | [Run the gate](#run-the-gate) |
| run the tests, the live suites, the Syncthing tests, the conformance suite | [Run the tests](#run-the-tests) |
| run the proof scripts against real sessions | [Run the proofs](#run-the-proofs) |
| know what each CI job and script does | [Know the CI](#know-the-ci) |
| see every make target with its help line | [See every make target](#see-every-make-target) |

## Run the gate

1. `make typecheck lint build test vuln deps-check schema-check tidy-check docs-check`.
2. `make push` runs typecheck, pull, build and test before it commits; CI's `fast` job runs the rest.

Details: [Run the gate](gates.md).

## Run the tests

1. `make test`: unit, testscript, harness, conformance(fs), with `-race`; no Docker, no stack.
2. `make supabase-start supabase-env`, then `make test-all`, before touching `supabase/`.
3. `BRIGADE_TEST_SYNCTHING=1 go test -run TestRealSyncthingIntegration ./internal/syncadapters/syncthing` for the real daemon.

Details: [Run the tests](tests.md).

## Run the proofs

1. `make e2e`: the no-LLM proof in watcher sink mode against the local stack (CI runs it).
2. `make proof`: adds the headless LLM, idle-wake and crash-and-resume runs (needs a logged-in `claude`).

Details: [Run the proofs](proofs.md).

## Know the CI

1. `ci.yml`: `fast` (Ubuntu), `macos`, `reproducibility`, `supabase`, `deploy-staging`; `release.yml` on a `v*` tag.
2. [scripts/ci/README.md](../../../scripts/ci/README.md) says what every script does and what calls it.

Details: [Know the CI](ci.md).

## See every make target

1. `make help`, or the generated page.

Details: [make targets](../reference/make-targets.md).
