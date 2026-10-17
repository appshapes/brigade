# Know the CI

| Workflow | When | Jobs |
| --- | --- | --- |
| `ci.yml` | every push and pull request | `fast` (Ubuntu: the gate, `checksums-check`, `plugin-check`, `muse-plugin-check`, `codex-plugin-check`, `docs-check`, `functions-check`), `macos` (the tests and a cross-build), `reproducibility` (the two cross-builds must be byte-identical), `supabase` (the live suites and `make e2e` against a local stack), `deploy-staging` (master, when `BRIGADE_STAGING` is set) |
| `release.yml` | a `v*` tag | guards that the plugin pins the tag, the three manifests agree, the documentation gate passes and the source reproduces the committed checksums; goreleaser builds a draft; the build is verified against the checksums; publish; dispatch the notes |
| `send-release-notes.yml` | weekly, and on dispatch | the release-notes email ([Send the release notes](../release/release-notes.md)) |
| `keepalive.yml` | daily | one job per hosted project ([Arm the keep-alive](../../project-owner/install/keep-alive.md)) |
| `claude.yml`, `review-pull-request.yml`, `update-documentation.yml`, `review-repository.yml`, `update-dependencies.yml` | an issue labelled `claude`; a PR; monthly | the agentic loop ([Use the agentic loop](../contribute/agentic-loop.md)) |

[scripts/ci/README.md](../../../scripts/ci/README.md) is the reference: every workflow, every script under
`scripts/ci/` and `scripts/`, what calls it, how to run it locally, the required GitHub configuration (secrets,
variables, rulesets) and the fixtures. The runner is Blacksmith's `blacksmith-4vcpu-ubuntu-2404` image, whose
tool versions the `fast` job prints in its "Runner image inventory" step; shellcheck is 0.9.0 there. Every CI job
sets `timeout-minutes` except one whose body is a reusable workflow, where GitHub rejects it.
