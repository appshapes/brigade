# OpenCode and goose participation capability spike

Date: 2026-10-14
Companion to `docs/experiments/pi-participation.md` and `docs/experiments/codex-participation.md`.

OpenCode and goose are terminal coding agents. This spike checks whether a session of each can join a Brigade team as a full teammate.
A teammate needs a stable identity and must wake when a message arrives while the session is idle.

| Agent | Result |
| --- | --- |
| OpenCode 2.0.29 | **No blocker found.** Idle wake works in the interactive TUI. Tested live. |
| goose | **Not tested.** goose is not installed. Docs show hooks but no documented idle wake. Same risk as Codex. |

## OpenCode

The run used an isolated service. It has its own config, data and state directories, port 4097, and the local model `qwen/qwen3-coder-next` through LM Studio.
The real background service on port 49374 stayed untouched.
The fixture is in `scripts/experiments/opencode-participation/`. Raw output stays in `.ignored/`.

OpenCode 2.0 splits into a background service and clients. The TUI and the CLI are clients of the service.
A plugin runs inside the service, once for each project folder ("location").

| Capability | Verdict | Evidence |
| --- | --- | --- |
| Idle wake from a plugin | PASS | A trigger file made the plugin call `ctx.session.prompt({ sessionID, text })`. The service ran a full turn. No client was attached. |
| Idle wake in the interactive TUI, from a plugin | PASS | Same trigger with a TUI open on the session in tmux. The message and the model reply appeared live. |
| Idle wake in the interactive TUI, from outside | PASS | `opencode run --session <id> "<text>"` from another process. The message and reply appeared live in the open TUI. No plugin needed. |
| Session ID in the plugin | PASS | Events carry `sessionID`. The `ctx.session.hook("prompt")` event carries `sessionID`, `messageID`, `delivery`. |
| Session ID in bash tool children | PASS | A bash child had `OPENCODE_SESSION_ID`, `OPENCODE` and `AI_AGENT`. |
| Plugin env injection into shell children | PASS | `ctx.shell.hook("create.before")` set `BRIGADE_PROBE_SESSION`. A bash child saw it. The hook event has no `sessionID` field. |
| Human-only verb boundary | SAME AS CLAUDE | Bash variables can be unset or changed. This is the best-effort boundary already in `docs/security.md`. |
| Approval gate | NOT TESTED (probe runs with bash allowed) | The probe config set `bash` and `edit` to allow. Brigade's "allow by default" model does not depend on a gate. |
| Plugin lifetime | FINDING | The plugin sets up when a project folder loads and cleans up on `location.shutdown`. Listeners must close in the cleanup function. |
| v1 plugins on v2 | FINDING | A v1 plugin (named function export) fails to load: "Plugin must export a default definition with an id and an effect or setup function". |
| New session, fork, resume, compaction: ID behavior | NOT RUN | |
| Two sessions in one service | NOT RUN | The probe plugin picks the last active session. That is wrong with two sessions. Brigade must register each session when it sees its first `prompt` hook. |
| Steer versus queue delivery | NOT RUN | The `prompt` hook reports `delivery: steer` by default. |
| Hosted model | NOT RUN | |

Security note: the probe config sets `bash` and `edit` to `allow`, so the local model runs commands with the account's permissions. Only fixed prompts were sent. A narrow allowlist was not tested. Run the probe in a container or VM before you send any other prompt.

Findings outside Brigade's scope:
- The real file `~/.config/opencode/plugins/ifthen-hooks.js` is a v1 plugin. It fails to load on 2.0.29 (the real service log shows the error). Its hooks do not run. This needs a v2 port.
- `opencode run` without `</dev/null` waits on stdin and never ends.
- The v2 plugin API is beta. The docs say it may change.

What this means for Brigade:
- OpenCode has two wake paths. The plugin path pushes messages in. The CLI path needs no plugin: the harness runs `opencode run --session <id>`.
- The harness needs a session registry for OpenCode, keyed on `OPENCODE_SESSION_ID`. The Go harness keys on `CLAUDE_PID` today.
- The plugin is JavaScript. A Node or Bun runtime comes with OpenCode, so no new dependency.
- Assumption: the plugin calls the existing `brigade` CLI, so the protocol and adapters do not change.

## goose

goose is not installed, so this section comes from documentation only. Nothing here is tested.

| Capability | Verdict | Evidence |
| --- | --- | --- |
| Lifecycle hooks | DOCUMENTED | `SessionStart`, `SessionEnd`, `Stop`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse` and more. A plugin dir (`~/.agents/plugins/<name>/hooks/hooks.json`) lists commands. Each command gets JSON on stdin with `event` and `session_id`. |
| Context injection from a hook | NOT DOCUMENTED | Hook output only allows or blocks. The docs do not describe an `additionalContext` field. |
| Blocking `Stop` hook | DOCUMENTED | It keeps the turn going. The count of consecutive blocks is capped (`GOOSE_STOP_HOOK_BLOCK_CAP`). |
| Async or background hooks | NOT DOCUMENTED | Hooks run and are awaited. Default timeout is 30 seconds. |
| Idle wake | NOT DOCUMENTED | Hooks run only during a turn. This is the failure that closed Codex. |
| Server | UNVERIFIED | `goosed` serves a reply endpoint (third-party mirror docs): a POST with a user message and `session_id` returns an event stream. It is not clear that the CLI or desktop shows such a message in the user's open session. |
| ACP | PARTIAL | `goose acp` serves the Agent Client Protocol on stdio. The core session methods come from the ACP spec. Session listing and live cross-client visibility are not documented. |

What this means for Brigade:
- goose is the closest to Claude Code in hook design. It still has no documented way to wake an idle interactive session.
- An issue in a goose fork (kojiromike/goose #15) says goose has an internal `send_message` that runs a turn without telling any client. Sessions driven this way can stay hidden from the desktop window. This is a proposal, not shipped behavior.
- A teammate that cannot be woken is a mailbox. Treat goose as a Codex-class risk until a spike shows otherwise.

## Open questions

1. goose: does a message sent to `goosed` appear in the user's open CLI or desktop session? A spike needs goose installed.
2. OpenCode: does the session ID stay the same after compaction, fork and `/new`?
3. OpenCode: how does a Brigade plugin learn which session a TUI shows when two sessions share one service?
4. Does Rjae accept OpenCode as a supported harness? The Codex ruling was won't-do, so this needs a new decision.

## Reproduce (OpenCode)

```
scripts/experiments/opencode-participation/setup-isolated.sh <dir>
. <dir>/env.sh
cd <dir>/work && opencode run --title t "Reply OK." </dev/null
echo "Reply with exactly PONG." > <dir>/trigger.txt
```

Sources: opencode.ai v2 plugin docs (`/v2/docs/build/plugins`, `/migrate-v1`), the local service log, goose-docs.ai hooks and architecture pages.
