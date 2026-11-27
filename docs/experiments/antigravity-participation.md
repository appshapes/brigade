# Antigravity CLI participation: an idle session wakes, from a process the agent starts (work item 531)

Date: 2026-11-27 (UTC). Work item 531, "Antigravity as a harness: test idle wake-up first".
Client: Google Antigravity CLI **1.5.18** (`agy`), macOS (Darwin 27.2.0, arm64 host, x86_64 binary).
Mode: `auto`, the default, which turns on Antigravity's command sandbox. Model: Gemini 4 Hydrogen (High).
Account: the operator's own sign-in. Nothing of it was copied or read.
Driver: by hand. One interactive `agy` session in a throwaway, gitignored workspace, three prompts typed by the
operator, a probe hook, and one script the agent ran. No driver is kept in the repository.

**Result: yes, with one condition.** A message sent with `agy agentapi send-message <conversation id> <text>`
started a turn in an idle session that nobody typed into, and the model answered with the code word the message
asked for. The condition: only a process that the agent itself starts, outside the command sandbox, has what
`agentapi` needs. A hook has not.

## 1. What a sender needs

`agy agentapi send-message` talks to the session's own local language server. It needs two values, which it reads
from its environment: `ANTIGRAVITY_LS_ADDRESS` and `ANTIGRAVITY_CSRF_TOKEN` (a per-process secret). Without them it
answers `ANTIGRAVITY_LS_ADDRESS is not set`. Given the address of the right port but no token, it answers
`unauthenticated: missing CSRF token`. The token is not on the `agy` process's command line.

## 2. Who gets those values

| Process | Variables it got (names only) |
| --- | --- |
| A hook (`SessionStart`, `PreInvocation`, `Stop`), parent the `agy` process | `ANTIGRAVITY_CONVERSATION_ID` only |
| A command the agent ran (`run_command`) | `ANTIGRAVITY_AGENT`, `ANTIGRAVITY_AGENTAPI_EXE`, `ANTIGRAVITY_APP_DATA_DIR`, `ANTIGRAVITY_CONVERSATION_ID`, `ANTIGRAVITY_CSRF_TOKEN`, `ANTIGRAVITY_LS_ADDRESS`, `ANTIGRAVITY_LS_VERSION`, `ANTIGRAVITY_PROJECT_ID`, `ANTIGRAVITY_SOURCE_METADATA`, `ANTIGRAVITY_TRAJECTORY_ID` |

So a watcher that a hook starts cannot wake the session. A process the agent starts can.

## 3. The arms

| Arm | What ran | Result |
| --- | --- | --- |
| 1 | The agent ran a script that forked a child and returned; the child was to send after 45 s. | Nothing was sent: the child left no log. The command's end stopped it. No turn. |
| 2 | The agent started the script as a background task inside the sandbox. It waited 45 s, sent, then stayed alive 10 minutes. | The send failed: `dial tcp [::1]:…: connect: operation not permitted`. No turn. |
| 3 | The same, as a background task **outside** the sandbox. Antigravity's own safety monitor flagged the request, and the operator approved it. | The send succeeded (16:08:30Z). The idle session started a turn and answered `WAKE-K9S`. The task was still alive, so its end did not cause the turn. |
| 4 | A message file in the same shape, written straight into the conversation's `messages/undelivered/` folder. | No turn in 45 s; the file stayed undelivered. It was removed. Antigravity takes a message through its API only. |

Before arm 1 the session had answered one prompt and then sat idle for minutes with no turn.

## 4. The hooks, as measured

- Hooks load from `.agents/hooks.json` in the workspace, `~/.gemini/config/hooks.json` and a plugin's `hooks.json`.
  The workspace's file loaded only after the operator trusted the folder, and its `SessionStart` ran on the
  session's second start.
- Events that ran: `SessionStart` once, `PreInvocation` before every model call (three in one short turn), and
  `Stop` at the end of each turn. The woken turn of arm 3 ran `PreInvocation` and `Stop` as a typed turn does.
  There is no session-end hook.
- The document on stdin (field names): `conversationId`, `cwd`, `workspacePaths`, `modelName`, `transcriptPath`,
  `artifactDirectoryPath`, `lastUserInput`, `hookEventName`; `PreInvocation` adds `invocationNum` and
  `initialNumSteps`; `Stop` adds `fullyIdle`, `terminationReason`, `executionNum`, `continuationActive`, `error`.
- Each hook's parent process was the `agy` process.
- What a hook may print (Antigravity's hooks page): `SessionStart` and `PreInvocation` may add steps to the
  conversation (`injectSteps`, a system or user message); `Stop` may answer `"decision": "continue"`.

## 5. The session's own record

The conversation's `transcript.jsonl` (under `~/.gemini/antigravity-cli/brain/<conversation id>/`) carries the model
and, per model call, `input_tokens`, `output_tokens` and `cache_read_tokens` (field names read; no value was
read). A sent message is kept as a JSON file under the conversation's `messages/` folder, with a `read.json` beside
it.

## 6. What this settles, and what it leaves

- An Antigravity CLI package can wake an idle session the way Grok Build's does: the agent starts Brigade's
  listener as a background task outside the sandbox, and the listener calls `agentapi` when a message arrives
  (work item 533). Until then, a message reaches the model at its next model call, through the `PreInvocation`
  hook (work item 532).
- Not measured: whether one `unsandboxed(...)` allow rule approves the listener for good; what stops it at `/new`
  or at the session's end; the Antigravity App and IDE (their sidecars get `agentapi` by design); Linux and
  Windows.
