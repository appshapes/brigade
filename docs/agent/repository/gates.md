# Gates: what runs before a push, and what a release needs

- **The acceptance gate** is `make typecheck lint build test vuln deps-check schema-check tidy-check docs-check`.
  `make test` is Docker-free and stack-free: the live Supabase tests are opt-in behind `BRIGADE_TEST_LIVE=1`, which
  only `make test-integration` (and CI's `supabase` job through it) sets. Run `make supabase-start supabase-env`
  once, then `make test-all`, before pushing anything that touches `supabase/` or `internal/adapters/supabase`.
- **Release pins.** Never commit a `plugin/bin/checksums.txt` or `plugin/bin/VERSION` you did not produce with
  `make release`; `muse/brigade/bin/` carries copies the release sequence writes. `make checksums-check` requires
  the committed file to be reproduced by a fresh cross-compile or backed by the published release (in the
  pre-release `0.0.0` state it requires the file to be empty and skips the rest).
- **Package checks.** `make plugin-check` carries the Claude Code plugin tree's checks: the `plugin/` file
  allowlist, mode 100755 in git for `plugin/bin/brigade`, `VERSION == plugin.json`, no `.mcp.json`, `mcpServers`
  or `channels`, exec-form hooks whose command paths exist and are executable, `sh -n` and `shellcheck -s sh`, and
  the only secret scan (`scripts/ci/no-secrets.sh`). `make muse-plugin-check` is the same for `muse/brigade/`:
  allowlist, native manifest, one wrapper per hook, assets equal to `plugin/bin`'s. A new harness package gets a
  check of its own in `scripts/ci/` and a job step in `ci.yml`.
- **shellcheck is pinned.** CI's Ubuntu runner (Blacksmith's `blacksmith-4vcpu-ubuntu-2404` image since P5-17)
  has shellcheck **0.9.0**, printed by `ci.yml`'s `fast` job in its "Runner image inventory" step (run
  33998830833); this machine has 0.11, and versions disagree (0.10 reported SC2317 for a trap-invoked function's
  body and SC2015 for `A && B || C`; 0.11 reports SC2329 for the same function). Before pushing a shell file run
  `docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:v0.9.0 -s sh <file>` as well as the local one, and
  re-pin this sentence and `scripts/ci/README.md`'s copy whenever the inventory step prints a different version.
- **The documentation gate** (`make docs-check`, card 110) runs inside `make test`, in CI's `fast` job, as a
  precondition of `scripts/release-prep.sh` and as a guard step of `release.yml`. The rules are in
  [documentation.md](documentation.md). A release with no `## [<version>]` changelog section does not leave
  the developer's machine.
- **Local development.** `make plugin-dev` writes the dev-binary pointer and starts Claude Code with the local
  plugin; `make plugin-dev-off` removes it. Two personas on one machine: pass
  `--settings '{"pluginConfigs":{"brigade@inline":{"options":{"config_dir":"<dir>"}}}}'` (P7-7: each persona is
  its own credential store).
- **Scripts that run outside the current session** (proof and experiment scripts; the e2e, proof,
  harness-smoke and plugin-dev targets) strip the inherited session environment **by prefix**, never by a name
  list: unset every variable whose name begins with `CLAUDE` (no underscore, so `CLAUDECODE` as well as
  `CLAUDE_*`) plus `AI_AGENT`, keeping only `CLAUDE_CONFIG_DIR`; a Muse session strips `MUSE_*` the same way. The
  enumerated eight-name list in plan 9.6/7.4 is short by at least three (`CLAUDE_CODE_BRIDGE_SESSION_ID`,
  `CLAUDE_EFFORT`, `AI_AGENT`, measured in E0-4 and E0-7), and the next release of a harness can add more.
- **Evidence.** Experiment reports live in `docs/experiments/` (indexed in its README) and their driver scripts in
  `scripts/experiments/`; research digests and their evidence under `docs/research/` (the threat model defines
  U-01..U-25, I-01..I-33, E2E-*, CI-*; U-26..U-28 and I-34 are the folder-sync plan's additions, its sections
  9.8/9.9). Scratch in `.ignored/`; plans in `.context/plans/`.
