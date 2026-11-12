# Know the CI

| Workflow | When | Jobs |
| --- | --- | --- |
| `ci.yml` | every push and pull request | `fast` (Ubuntu: the gate without lint, `checksums-check`, `plugin-check`, `launcher-check`, `muse-plugin-check`, `codex-plugin-check`, `grok-plugin-check`, `docs-check`, `functions-check`), `lint` (`make lint`), `windows` (the suite minus the files tagged `!windows`), `supabase` (pgTAP, the advisor lints and the adapter's live tests against a local stack), `e2e` (conformance(supabase) and `make e2e` against a local stack of its own), `server` (the Rust workspace under `server/`: `make server-check`, `make server-deps-check` and `make server-test`), `deploy-staging` (master, when `BRIGADE_STAGING` is set) |
| `release.yml` | a `v*` tag | guards that the plugin pins the tag, the four manifests agree, the documentation gate passes and the source reproduces the committed checksums; goreleaser builds a draft; the build is verified against the checksums; publish; dispatch the notes |
| `send-release-notes.yml` | weekly, and on dispatch | the release-notes email ([Send the release notes](../release/release-notes.md)) |
| `keepalive.yml` | daily | one job per hosted project ([Arm the keep-alive](../../project-owner/install/keep-alive.md)) |
| `claude.yml`, `review-pull-request.yml`, `update-documentation.yml`, `review-repository.yml`, `update-dependencies.yml` | an issue labelled `claude`; a PR; monthly | the agentic loop ([Use the agentic loop](../contribute/agentic-loop.md)) |

[scripts/ci/README.md](../../../scripts/ci/README.md) is the reference: every workflow, every script under
`scripts/ci/` and `scripts/`, what calls it, how to run it locally, the required GitHub configuration (secrets,
variables, rulesets) and the fixtures. The runner is Blacksmith's `blacksmith-4vcpu-ubuntu-2404` image, whose
tool versions the `fast` job prints in its "Runner image inventory" step; shellcheck is 0.9.0 there. Every CI job
sets `timeout-minutes` except one whose body is a reusable workflow, where GitHub rejects it.

The first six jobs of `ci.yml` wait on nothing, so a run is as long as its longest job (card 245: under five
minutes, from eleven). The `master` ruleset requires `fast`, `lint`, `supabase` and `e2e` by job name: add or
rename a job and the ruleset together. `windows` is not required, and neither is `server` until the first server
release (work item 89). No job runs on macOS: a person's commit has passed `make push` on the
developer's machine, a Mac today, before it reaches origin, and a release is where the Linux rebuild must
reproduce the checksums `make release` pinned.
