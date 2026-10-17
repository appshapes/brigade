# Codex CLI package: implementation plan (card 107, route A of the reassessment)

Date: 2026-10-17 (after the live test of `docs/experiments/codex-queue-participation.md`, commit f5e2c3e)
Owner ruling (Rjae, 2026-10-17, on the reassessment's recap): "Go ahead." The first-prompt limitation is
accepted as for Muse; the customer configuration is `codex --dangerously-bypass-approvals-and-sandbox`.
Status: implemented 2026-10-17 (row P40-1 of `.context/plans/brigade-execution-log.md`); the release is row P40-2.

## 1. The shape, in one paragraph

A third package, `codex/brigade/`: a native Codex manifest (`.codex-plugin/plugin.json`), a `hooks/hooks.json`
whose three string commands run the same bootstrap `bin/brigade` as `plugin/` (`"${PLUGIN_ROOT}/bin/brigade" hook
<event> --host codex --plugin-root "${PLUGIN_ROOT}"`), the seven skills worded for Codex, and `bin/` copies of
the pins; the repository root's `.agents/plugins/marketplace.json` names it, so `codex plugin marketplace add
appshapes/brigade` and `codex plugin add brigade@brigade` install it. Codex runs the hooks client-side with the
thread id on stdin and no `CODEX_*` variable in their environment, so the hook learns its host from `--host` and
its native id from the document; the model's shell is the daemon's child and carries `CODEX_THREAD_ID`, which is
what `brigade` commands key on there. The state is keyed by the hook's parent pid (the TUI) as Muse's is, with a
pointer `sessions/by-host/codex/<thread>.json`. Delivery is a plain outside wake: the watcher, started by
SessionStart in `--host codex` mode, runs `codex queue --thread <thread> --message <frame>` for each frame — an
argv array through the quiet spawn seam, never a shell — after checking the TUI pid is alive, and re-reads the
thread id from the map on every tick so `/new` (a new thread in the same TUI) is followed. No arming, no monitor:
`UserPromptSubmit` fires on queued turns, the queued text is a user turn, and the frame's Codex variant says so.
The session is reachable once its first prompt has run SessionStart.

## 2. Decisions

| # | Decision | Why |
| --- | --- | --- |
| D1 | Host detection in `config`: `CLAUDE_PID` → claude-code, else `MUSE_SESSION_ID` → muse, else `CODEX_THREAD_ID` → codex. The hook takes `--host codex` and adopts the document's `session_id` as the thread id (measured: hooks carry no `CODEX_*`). Every `BRIGADE_*` and every `XDG_*` is stripped in a Codex session | the hooks inherit the TUI's environment, the model's shell the daemon's; HOME alone is what both agree on |
| D2 | The map stays `ByPID`, keyed by a number derived from the thread id (`codexKey`: FNV-1a, above 2^30, no real pid reaches it); the hook's parent goes into the new member `host_process_pid`, which the watcher polls for liveness; `harness: "codex"`, `model` from the document | **measured 2026-10-17 on this machine**: with the hooks trusted (daemon mode, the documented path) Codex runs them from the shared app-server daemon, so the parent is the daemon, shared by every Codex session, not the TUI; the live test's "parent = the TUI" held only under `--dangerously-bypass-hook-trust`, which forces embedded mode. One thread, one map; the parent is the TUI in embedded mode (quit → gone in 2 s) and the daemon in daemon mode |
| D3 | `sessions/by-host/codex/<thread>.json` → `{host_pid: <the key>, brigade_session_id}`; `/new` is a new thread, hence a new key, a new map and a new Brigade session; the old thread's state stands until Codex ends that thread | in daemon mode nothing names the old thread's end but Codex's own `SessionEnd` (archive, delete, or 30 idle minutes without a client, per Codex's documentation); the retired-pointer idea of the first draft assumed one pid per TUI |
| D4 | Options from `${configDir}/codex-options.json`, the keys of `userConfig` | Codex plugins have no option mechanism Brigade can read |
| D5 | Policy as Muse (D5 of the Muse plan): no Claude settings scan, no parity rule, no write under `~/.claude`; `team_inbound` is the knob | Codex's own approval policy is the gate |
| D6 | Delivery: `brigade watch --host codex`; per frame `codex queue --thread <id> --message <frame>` (`codex` found on the watcher's PATH; `CODEX_HOME` passed through when the hook had it); the TUI pid checked before each queue; a `Wrap: false` frame of the `codex` variant | measured: daemon mode starts the turn inside the command's 45 ms; embedded mode polls ~10 s; the text is on argv (visible to `ps` of the same user, accepted) |
| D7 | SessionStart prints the start line and one line naming the bootstrap's path, on every source but compact; no arming line | nothing puts a plugin's `bin/` on PATH (0.153.4, standing); the model's shell must type the path |
| D8 | SessionEnd as Muse: SIGTERM the watcher, delete the map, the pointer, the start facts, the stamps; `timeout: 3` in hooks.json | the hook has 1–3 s; the watcher's exit closes the session; the lease expires if it cannot |
| D9 | `--plugin-root "${PLUGIN_ROOT}"` on the hook's argv is how the hook knows the package root | no `CODEX_PLUGIN_ROOT` variable exists in a hook's environment |
| D10 | `scripts/ci/codex-plugin-check.sh` (`make codex-plugin-check`, in CI), release-prep bumps the Codex manifest and copies the assets, checksums-check rule (a) and release.yml's guard compare them, `internal/docscheck` knows `.codex-plugin` | one canonical pin, three packages |
| D11 | `whoami`: `host: codex`; the delivery word for a host without a socket is `not_running`, never `none` | `none` meant "no socket, no watcher" and is false for Muse and Codex |

## 2a. What the two live runs on this machine measured (2026-10-17)

- **Embedded mode** (`--dangerously-bypass-hook-trust`, which forces it): the hooks' parent is the TUI; the first
  prompt registered the session (`host: codex` in `whoami`, the model on the roster), a message from a Claude Code
  session was visible in the TUI about 3 s after the send (the ~10 s queue poll, caught early), the model loaded
  the `team-messaging` skill, published a doing line and replied with `brigade send --reply-to` (the reply reached
  the sender 21 s after the send, model time); `/quit` fired SessionEnd and the session was offline within 1 s.
- **Daemon mode** (hooks trusted in `/hooks`, the documented path): the hooks' parent is the app-server daemon
  (`codex app-server --listen`, ppid 1); the frame was visible in the TUI **0.62 s** after the send; the reply
  came 19 s later; `/quit` fired no SessionEnd, the thread stayed loaded, the daemon lived on. Hence D2 and D3 as
  revised. Whether Codex's documented SessionEnd after 30 idle minutes fires is measured on the quit thread (see
  the execution-log row).
- Installing the plugin does not trust its hooks: the first launch afterwards shows a dialog ("Trust all and
  continue"); `/hooks` lists the three with "needs review" and `t` trusts them; a prompt before that registers
  nothing.

## 3. Order of work

1. `config`: HostCodex, `CodexThreadID`, per-host options file, `Session` through the pointer.
2. `hook`: `--host` and `--plugin-root`, Codex facts, the path line, pointer retirement, SessionEnd.
3. `frame`: the Codex variant and its golden.
4. `watch`: `--host codex`, the queue delivery, the thread id from the map, the pid check.
5. `codex/brigade/` and `.agents/plugins/marketplace.json`.
6. `scripts/ci/codex-plugin-check.sh` and its test, release-prep, checksums-check, release.yml, Makefile, docscheck.
7. Docs: the registry entry, the six pages, the topic roots, the agent pages, security §16, the rule files, README, CHANGELOG.
8. Integration on this machine: the package installed from a local marketplace with a bootstrap that execs the dev build, a Codex TUI in tmux, a message from this session and a reply.
9. Release 0.32.0, the execution-log row, the card.
