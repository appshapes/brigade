# Muse Code package: implementation plan (card 88, outcome 2 on route B)

Date: 2026-10-17 (overnight, after the live test of `docs/experiments/muse-participation.md`)
Owner ruling (Rjae, 2026-10-16 late): the first-prompt limitation is accepted; implement the section-6 TUI shape of
`.context/plans/muse-team-participation.md`, autonomously, through to a release.
Status: implemented 2026-10-17 (row P37-2 in `.context/plans/brigade-execution-log.md`); released as 0.31.0 (row P37-3).

## 1. The shape, in one paragraph

A second package, `muse/brigade/`, native Muse manifest (`.muse-plugin/plugin.json`, argv-array hooks, no
shell), the same bootstrap `bin/brigade`, VERSION and checksums as `plugin/` (release-prep copies them,
checksums-check compares them). Muse runs the three hooks with `MUSE_SESSION_ID` (a UUID) in the environment and a
cleared environment otherwise; the hook's parent process is Muse itself, so the hook keys the session's state by
that pid exactly as Claude's is keyed by `CLAUDE_PID`, and writes a pointer `sessions/by-host/muse/<uuid>.json`
so a command run in the model's shell (which has the UUID, not the pid) finds the map. The watcher is spawned by
the SessionStart hook as today — a settings/plugin hook's detached child survives on Muse (measured) — in **sink
mode**: one NDJSON record per frame appended to `${stateDir}/inbox/<brigade_session_id>.ndjson`, acknowledged on
append as a socket post is. The model arms Muse's `monitor` tool once (persistent, `wake_delay_ms: 0`) on
`brigade inbox follow`, which tails that file and prints each frame as it lands; the monitor wakes the idle
session 0.2 s later. UserPromptSubmit does not fire on woken turns, so the watcher's heartbeat is the heartbeat;
SessionEnd gets 0.1–0.4 s, so the hook only signals the watcher and removes its files, and the watcher's exit path
closes the session. Hook stdout is byte-capped under Muse's 16 KiB kill.

## 2. Decisions

| # | Decision | Why |
| --- | --- | --- |
| D1 | Host detection in `config`: `CLAUDE_PID` → claude-code, else `MUSE_SESSION_ID` → muse. `InSession` is either. Every BRIGADE_* is stripped in both | the human-only verbs and the state-dir pinning must hold on Muse too |
| D2 | The map stays `ByPID`, keyed by the host pid (Muse: the hook's parent pid, injectable `Deps.Getppid`); new members `harness` (absent = claude-code) and `model` (Muse's stdin names it) | no state migration; one watcher, one pidfile, one log path per pid |
| D3 | `sessions/by-host/muse/<uuid>.json` → `{host_pid, brigade_session_id}`; `config.Session` follows it and refuses a map whose native id or harness differs | the model's shell knows the UUID only; fail closed on a mismatch |
| D4 | Options for Muse come from `${configDir}/muse-options.json` (same keys as `userConfig`), turned into `CLAUDE_PLUGIN_OPTION_*` entries before `ParseOptions`; `config_dir` is not an option there (the file is found under the XDG default/HOME) | `userConfig` is inert on Muse; hooks get no XDG_* |
| D5 | Policy on Muse: the card-50 parity rule (bypassPermissions needs Claude's `crossSessionInbound: accept`) and the user-settings write do not apply; the native scan is skipped; doing rules are `none` | they read Claude Code's settings files, which Muse has none of; Muse's own approval policy is the gate |
| D6 | Delivery: watcher `--sink` (existing mode), `Wrap: false`, a Muse-receiver frame preamble with host-neutral prose and its own goldens; `brigade inbox follow` prints `Brigade: team message <id> from "<name>" follows (N lines)`, the frame, and `Brigade: end of message <id>` in one write | one event batch per message; the model reads the frame as Claude's model does; the Claude bytes and anchor are untouched |
| D7 | Arming text: printed by SessionStart (every source but compact) and by the prompt hook when the follow process has not been seen for this session (a liveness stamp the follow command refreshes), throttled | the session is a mailbox until armed; a resume kills the monitor |
| D8 | SessionEnd on Muse: SIGTERM the watcher, delete the map, the pointer, the start facts, the stamps; no adapter call | 0.1–0.4 s budget; the watcher's own exit closes the session |
| D9 | `brigade` on the model's PATH is not guaranteed on Muse: the session-start line names the bootstrap's absolute path once, and the skill text says to use it when `brigade` is not found | nothing puts a plugin's bin on PATH; Muse has no `Bash(brigade:*)` rule for an absolute path to break |
| D10 | CI: `make muse-plugin-check` (allowlist, native manifest shape, argv hooks naming existing executables and known subcommands, bin assets byte-equal to plugin/, no symlinks, skills reachable); release-prep bumps the Muse manifest and copies the assets; checksums-check rule (a) covers both manifests; release.yml's guard too | one canonical pin, two packages |

## 3. Order of work (each a commit)

1. `config`: Host, InSession, Session via the by-host pointer; `sessionmap`: ByHost store, ByPID.Harness/Model.
2. `hook`: host-aware facts (Getppid), identity, options file, policy exemptions, sink spawn, context lines, prompt
   re-arm line, byte cap, Muse SessionEnd.
3. `watch`: sink mode under a host (no registry refresh, Wrap off, frame host); `frame`: Muse variant + goldens;
   `inbound.Config.Host`.
4. `commands`: `inbox follow`; `whoami` host line.
5. `muse/brigade/` package: manifest, bin assets, skills, README.
6. `scripts/ci/muse-plugin-check.sh`, release-prep/checksums-check/release.yml, Makefile, checks_test.go.
7. Docs: setup (Muse install), security (Muse section), CHANGELOG, plugin README pointer, docs/development.md.
8. Integration on this machine: a second clone joined, the package installed into the Muse test root with a
   bootstrap that execs the dev build, a real message from a Claude session to the Muse session and a reply.
9. Release 0.31.0 (`make release version=0.31.0 card=88`), execution-log row, card comment.
