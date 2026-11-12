# Gates: what runs before a push, and what a release needs

- **The acceptance gate** is `make typecheck lint build test vuln deps-check schema-check tidy-check docs-check`.
  `make test` is Docker-free and stack-free: the live Supabase tests are opt-in behind `BRIGADE_TEST_LIVE=1`, which
  only `make test-integration-adapter` sets (`make test-integration` runs it, and so does CI's `supabase` job).
  Run `make supabase-start supabase-env` once, then `make test-all`, before pushing anything that touches
  `supabase/` or `internal/adapters/supabase`.
- **CI's shape** (card 245, owner rulings 2026-11-10). `ci.yml`'s `fast`, `lint`, `windows`, `supabase`, `e2e` and
  `server` jobs run side by side, so a run is as long as its longest job: never make one of them wait on another, and keep
  each under five minutes. The `master` ruleset (`master-pr-checks`) requires `fast`, `lint`, `supabase` and `e2e`
  by job name: a job added or renamed in `ci.yml` changes the ruleset in the same piece of work, or it gates
  nothing (or blocks every pull request). There is no macOS job and no cross-host `reproducibility` job; never
  add one back without the owner. The `fast` job passes `go_test_p=4` to `make test`; everywhere else the pin is 2.
- **The server** (work item 89, `.context/plans/brigade-server.md`). `server/` is a Rust workspace that shares no
  code with the Go side and is outside the acceptance gate: never make `make test`, `make push` or a Go package
  depend on it. Its gate is `make server-check server-test server-deps-check`; run it before pushing anything
  under `server/`. Every crate it links is named in `server/deny.toml`, and a new one is added there in the same
  change. Cavalry is a git dependency pinned to one commit in `server/crates/brigade-log/Cargo.toml`: a new pin
  may need the feed started again from a snapshot, and `Shape` in `feed.rs` is the one place a pin's differences
  go. CI's `server` job runs the gate and is not required by the `master` ruleset until the first server release
  (the plan's ruling 5); the contract (`docs/backend-rest-v1.md`) still changes only in one commit with the
  adapter, the stand-in and a green conformance run.
- **Release pins.** Never commit a `plugin/bin/checksums.txt` or `plugin/bin/VERSION` you did not produce with
  `make release`; `muse/brigade/bin/` carries copies the release sequence writes. `make checksums-check` requires
  the committed file to be reproduced by a fresh cross-compile or backed by the published release (in the
  pre-release `0.0.0` state it requires the file to be empty and skips the rest).
- **The Muse marketplace catalog** (cards 115, 114). `marketplace.json` at the repository root is what Muse reads
  first in a checkout; its one entry carries the package digest Muse computes over `muse/brigade/`, and Muse refuses
  an install whose tree does not match it. `internal/musedigest` computes the same value without Muse (SHA-256
  over each file's path, a zero byte, its bytes and 0xFF, in byte order of the paths; its tests pin the digests
  Muse reported). `make muse-digest` (`go run ./cmd/brigade-muse-digest write`) rewrites the catalog after any
  change under `muse/brigade/`, `make release` rewrites it in step 4, and `make muse-plugin-check`, `make test`
  and `release.yml`'s guard verify it on every machine, so **no machine needs Muse Code**, the release machine
  included (the owner ruling of 2026-10-17 put Muse there "for now"; card 114's done criterion ended that). Where `muse` is on PATH, `make test` also has Muse
  install the package from the catalog in a throwaway root: the cross-check that the reproduction still is what
  Muse computes. Every file under `muse/brigade/` counts, an ignored `.DS_Store` included, so
  `muse-plugin-check.sh` and `release-prep.sh` refuse a file there that git does not track. Never hand-edit the
  digest.
- **Package checks.** `make plugin-check` carries the Claude Code plugin tree's checks: the `plugin/` file
  allowlist, mode 100755 in git for `plugin/bin/brigade`, `VERSION == plugin.json`, no `.mcp.json`, `mcpServers`
  or `channels`, exec-form hooks whose command paths exist and are executable, `sh -n` and `shellcheck -s sh`, and
  the only secret scan (`scripts/ci/no-secrets.sh`). `plugin/bin/brigade.exe` is the committed Windows launcher
  (card 133, `cmd/brigade-launcher`): never hand-built, never version-stamped; `make launcher-check` (CI's `fast`
  job) rebuilds it with the release flags and diffs, so a change to its source, a toolchain bump or a bump of
  `golang.org/x/sys` (the one module it links; the monthly dependency agent does that one itself) means
  `make launcher` and committing the result in the same commit. `make muse-plugin-check` is the same for `muse/brigade/`:
  allowlist, native manifest, one wrapper per hook, assets equal to `plugin/bin`'s; `make codex-plugin-check` for
  `codex/brigade/`: allowlist, native manifest, the three exact hook commands, `.agents/plugins/marketplace.json`,
  assets equal; `make grok-plugin-check` for `grok/brigade/`: allowlist, native manifest, the four exact hook
  commands, `.grok-plugin/marketplace.json`, assets equal. A new harness package gets a check of its own in
  `scripts/ci/` and a job step in `ci.yml`.
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
