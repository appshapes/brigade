# Invariants: protocol, output, secrets, configuration, dependencies

- **Protocol v1 is frozen** in `docs/protocol-v1.md`. Changing a wire shape means changing `internal/protocol`
  (types and `Validate`), `docs/protocol-v1.schema.json` (`make schema`), the conformance suite and both adapters
  in one commit.
- **Stdout is protocol output.** Never write to stdout from a command, the watcher or an adapter except protocol
  JSON/NDJSON or the documented human output of `harness/commands` (forbidigo enforces it); diagnostics go to
  stderr through the redacting logger; never `slog.Any` or `slog.AnyValue`.
- **Never spawn with a shell** in shipped code: argument arrays with an allow-listed environment. A test fixture
  that is a shell script is launched as `/bin/sh <script>` (an argv array, never `sh -c`) so the fresh file is
  read, not exec'd; exec'ing it is what macOS's first-exec assessment and Linux's ETXTBSY need.
- **Secrets.** Never on argv, in logs, in `describe` output, or in files under the project directory. The join
  secret is read from stdin, a no-echo prompt, or a `--secret-file` outside the repository (whose mode and owner
  Brigade never checks, owner ruling 4, 2026-09-08): never argv, never the chat. The Supabase secret/service-role
  key never appears in the adapter, the plugin, the repository or a CI variable that ships. Never set
  `META_API_KEY`, never copy a harness's auth file, never run a harness's logout against a real configuration.
- **Claude Code's configuration directory.** Never hardcode `~/.claude`; use `CLAUDE_CONFIG_DIR ?? ~/.claude`.
  Never read or copy `$CLAUDE_CONFIG_DIR/sessions/*.key`; the session registry JSON is read best-effort only and
  never written. Inside a session (`CLAUDE_PID`, `MUSE_SESSION_ID` or `CODEX_THREAD_ID` set) the harness ignores
  every inherited `BRIGADE_*` variable and reads its configuration from the hook-written by-pid map; a Muse or
  Codex session also ignores `XDG_*` and resolves Brigade's directories from `HOME` alone (cards 88, 107). Codex runs
  its hooks with no `CODEX_*` variable: the package's `hooks/hooks.json` passes `--host codex --plugin-root`, and
  its three command strings keep the exact quoted form `make codex-plugin-check` pins (Codex runs them through its
  shell; Brigade itself still spawns nothing with one).
- **Package boundaries.** `internal/harness` must not import `internal/adapters/supabase` (depguard): the plugin
  talks only the adapter protocol and spawns the bundled adapter as a child process (`brigade adapter supabase …`).
- **Dependencies.** The shipped binary links only the modules in `docs/allowed-deps.txt` (`make deps-check`);
  dev tools live in `tools.mod`, never in `go.mod`; `go.mod` has no `toolchain` line. From 0.1.0 on, `go.mod`'s
  `go` line is a release-reproducibility pin: the release job rebuilds from the tag and diffs against the
  committed checksums, so a toolchain bump changes the bytes. Bump the version with it, or accept a red `fast`
  job until the next release (`make checksums-check` rule (c)).
- **Every Brigade skill is reachable by the model.** Never add `disable-model-invocation` to a skill under a
  package's `skills/` (card 28, owner ruling 2026-09-20: the security model is open and team-based, and a skill
  the model cannot reach cannot help unasked). `make plugin-check` check 10 enforces it and is the one deliberate
  carve-out from the 2026-09-08 ruling that CI does not police what a skill declares; never remove it on the
  strength of that ruling.
