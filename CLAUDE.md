# Development Guidelines

**Plans and temporary docs:**
- All plans **must** go in `.context/plans/`
- Ephemeral scratch **must** go in `.ignored/` (gitignored).

# Overrides
- @./CLAUDE.user.md (optional, local-only, gitignored — absent in fresh clones)

# Brigade
- Protocol v1 is frozen in docs/protocol-v1.md. Changing a wire shape means changing internal/protocol (types and
  Validate), docs/protocol-v1.schema.json (`make schema`), the conformance suite and both adapters in one commit.
- Never write to stdout from a command, the watcher or an adapter except protocol JSON/NDJSON or the documented human
  output of harness/commands (forbidigo enforces it); diagnostics go to stderr through the redacting logger; never
  `slog.Any`. Never spawn with a shell; always argument arrays with an allow-listed environment.
- Never put secrets on argv, in logs, in `describe` output, or in files under the project directory. The join secret is
  read from stdin or a no-echo prompt only. The Supabase secret/service-role key must never appear in the adapter, the
  plugin, the repo or CI variables that ship.
- Never hardcode `~/.claude`; use `CLAUDE_CONFIG_DIR ?? ~/.claude`. Never read or copy `$CLAUDE_CONFIG_DIR/sessions/*.key`;
  the session registry JSON is read best-effort only and never written. Inside a Claude Code session (CLAUDE_PID set)
  the harness ignores every inherited BRIGADE_* variable and reads its configuration from the hook-written by-pid map.
- internal/harness must not import internal/adapters/supabase (depguard); the plugin talks only the adapter protocol
  and spawns the bundled adapter as a child process (`brigade adapter supabase …`).
- The shipped binary links only the modules in docs/allowed-deps.txt (`make deps-check`); dev tools live in tools.mod,
  never in go.mod; go.mod has no `toolchain` line.
- From 0.1.0 on, go.mod's `go` line is a release-reproducibility pin: the release job rebuilds from the tag and diffs
  against the committed checksums, so a toolchain bump changes the bytes — bump the version with it, or accept a red
  `fast` job until the next release (`make checksums-check` rule (c)).
- `make test` is Docker-free and stack-free: the live Supabase tests are opt-in behind `BRIGADE_TEST_LIVE=1`, which only `make test-integration` (and CI's `supabase` job through it) sets. Run `make supabase-start supabase-env` once, then `make test-all` before pushing anything
  that touches supabase/ or internal/adapters/supabase.
- Never commit a plugin/bin/checksums.txt or plugin/bin/VERSION you did not produce with `make release`. CI now
  verifies them on every push: `make checksums-check` requires the committed file to be reproduced by a fresh
  cross-compile or backed by the published release (in the pre-release `0.0.0` state it requires the file to be
  empty and skips the rest), and `make plugin-check` carries the plugin-tree checks — the `plugin/` file
  allowlist, mode 100755 in git for `plugin/bin/brigade` and 100644 for everything else, VERSION == plugin.json,
  no `.mcp.json`/`mcpServers`/`channels`, exec-form hooks whose command paths exist and are executable, `sh -n`
  and `shellcheck -s sh` — and the only secret scan (`scripts/ci/no-secrets.sh`). CI's Ubuntu runner —
  Blacksmith's `blacksmith-4vcpu-ubuntu-2404` image since P5-17 — has shellcheck **0.9.0** (printed by `ci.yml`'s
  `fast` job in its "Runner image inventory" step, run 33998830833; the earlier "0.10" was inferred once from
  diagnostic codes on the GitHub-hosted runner, never printed) and this machine has 0.11, and versions disagree
  (0.10 reported SC2317 for a trap-invoked function's body and SC2015 for `A && B || C`; 0.11 reports SC2329 for
  the same function): before pushing a shell file run
  `docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:v0.9.0 -s sh <file>` as well as the local one, and
  re-pin this sentence and `scripts/ci/README.md`'s copy whenever the inventory step prints a different version.
- Local dev: `make plugin-dev` writes the dev-binary pointer and starts Claude Code with the local plugin; `make
  plugin-dev-off` removes it. Two profiles on one machine: pass
  `--settings '{"pluginConfigs":{"brigade@inline":{"options":{"profile":"<name>"}}}}'`.
- Experiment reports live in docs/experiments/ and their driver scripts in scripts/experiments/; the research digests and
  their evidence are committed under docs/research/ (the threat model defines U-01..U-25, I-01..I-33, E2E-*, CI-*;
  U-26..U-28 and I-34 are this plan's additions, defined in its sections 9.8/9.9); scratch in .ignored/; plans in
  .context/plans/.
- Proof and experiment scripts, and the e2e/proof/harness-smoke/plugin-dev targets, run outside the current Claude
  session. Strip the inherited session environment BY PREFIX, never by a name list: unset every variable whose
  name begins with `CLAUDE` — note there is no underscore, so this covers `CLAUDECODE` as well as `CLAUDE_*` —
  plus `AI_AGENT`, keeping only `CLAUDE_CONFIG_DIR`. The enumerated eight-name list in plan 9.6/7.4 is short by at
  least three — `CLAUDE_CODE_BRIDGE_SESSION_ID`, `CLAUDE_EFFORT` and `AI_AGENT` (that last one is not even
  `CLAUDE_`-prefixed), measured in E0-4 and E0-7 — and the next Claude Code release can add more.
- Commit messages: `15: <Imperative summary>`; `make push message="15: ..."`; merges only, never rebase.
