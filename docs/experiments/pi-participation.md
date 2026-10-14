# Pi participation capability spike

Date: 2026-10-14
Client: pi 1.2.0 (`@earendil-works/pi-coding-agent`), local model `qwen/qwen3-coder-next` through LM Studio
Host: macOS (Darwin 25.6.0)
Result: **No blocker found. The idle-wake test that stopped Codex passes on pi.**

Pi is a terminal coding agent. This spike checks whether a pi session can join a Brigade team as a full teammate.
A teammate must have a stable identity and must wake when a message arrives while the session is idle.

The run used an isolated pi home (`PI_CODING_AGENT_DIR`). It holds only the local-model settings, with placeholder keys.
No cloud credential was used. The session environment was stripped of `CLAUDE*` and `AI_AGENT`.
The fixture is in `scripts/experiments/pi-participation/`. Raw output stays in `.ignored/`.

## Result

| Capability | Verdict | Evidence |
| --- | --- | --- |
| Idle wake, headless (RPC mode) | PASS | A trigger file made the extension call `pi.sendMessage(..., { triggerTurn: true })`. The client sent no prompt. Pi ran `before_agent_start` (`messageType: custom`), a full turn, and `agent_end`. |
| Idle wake, interactive TUI | PASS | Same trigger in a tmux-hosted TUI. The message and the model reply appeared in the open conversation. |
| Session identity in the extension | PASS | `ctx.sessionManager.getSessionId()` and `getSessionFile()` return the same ID that RPC `get_state` returns. |
| Session identity in bash tool children | PASS | `PI_SESSION_ID`, `PI_SESSION_FILE`, `PI_CODING_AGENT`, `AI_AGENT` are present. Documented in pi's `environment-variables.md`. |
| Delivery while a turn runs | PASS | A trigger sent with `deliverAs: "followUp"` during a turn gave a second `agent_end`. |
| Long-lived watcher in an extension | PASS | `fs.watch` started from `session_start`. It ran until `session_shutdown`. |
| Human-only verb boundary | SAME AS CLAUDE | The bash variables can be unset or changed. This is the best-effort boundary already documented in `docs/security.md`. |
| Approval gate | NONE | Pi does not ask before tool calls (`security.md`). Brigade's "allow by default" model does not depend on one. |
| `/new`: ID and wake | PASS | New session ID. A trigger after `/new` woke the new session. Exactly one run, so no stale watcher. |
| `fork` and `clone`: ID | PASS | Each gives a new session ID and a `session_start` with `reason: fork`. |
| Resume: ID | PASS | `switch_session` to a saved file, from a second process, restored the same ID. A trigger after resume woke the session. |
| Extension instance per session | FINDING | Pi unloads the extension (`session_shutdown`) and loads a new instance on every `new`, `fork` and `resume`. State in the old instance is gone. |
| Duplicate `session_start` | FINDING | Each new instance gets `session_start` twice with the same ID. The handler must be idempotent. |
| Two sessions, one folder | PASS | Two pi processes had different IDs. A trigger file for one did not wake the other (each has its own trigger path in this test). |
| Nested pi in a bash child | FINDING | A child pi process inherits the parent's `PI_SESSION_ID` in its environment, but its own `ctx.sessionManager` returns its own ID. Read the ID from `ctx`, not from the environment, inside the extension. |
| `steer` delivery | PARTIAL | Trigger accepted while a turn ran, and the run ended. I did not check the reply text. |
| `nextTurn` delivery | PARTIAL | No extra run started, as the name says. I did not check that the next turn saw the message. |
| Compaction: ID and wake | NOT RUN | Pi refused: "Nothing to compact (session too small)". |
| Subagents | NOT RUN | |
| Extension packaging and install from a Brigade plugin | NOT RUN | Docs describe pi packages (`docs/packages.md`). Not checked live. |
| Hosted model (not LM Studio) | NOT RUN | Nothing in the design depends on the model. Not proven. |

## What this means for Brigade

- A pi extension can do what the Claude hooks do: register the session, show the roster, and push inbound messages into the open conversation.
- The Go harness assumes Claude. It keys on `CLAUDE_PID` and the hook-written by-pid map. Pi needs a second identity path based on `PI_SESSION_ID`.
- The extension is TypeScript. Brigade ships Go and shell today. A pi package adds a Node runtime dependency for pi users only.
- The extension reloads on every session replacement. It must close its listener on `session_shutdown` and open a new one in each `session_start`.
- Assumption: the extension calls the existing `brigade` CLI, so the protocol and adapters do not change.

## Open questions

1. Does the session ID stay the same after compaction? The spike could not compact.
2. Brigade must register a new teammate on `new` and `fork` (new ID), and re-attach on `resume` (same ID). Which `reason` values map to which action?
3. Who owns pi now? Sources name both `badlogic/pi-mono` and `earendil-works`. Pick the canonical repository before any docs link to it.
4. Does Rjae accept pi as a supported harness? The Codex ruling was won't-do, so this needs a new decision.

## Reproduce

```
scripts/experiments/pi-participation/rpc_probe.py <workdir>
scripts/experiments/pi-participation/tui_probe.sh <workdir>
scripts/experiments/pi-participation/session_probe.py <workdir>
```

Pi docs used: the installed package's `docs/extensions.md`, `environment-variables.md`, `rpc.md`, `security.md`.
