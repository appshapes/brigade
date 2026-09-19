# E10 — Does the `UserPromptSubmit` hook fire on a wake-up turn?

Card 25, plan row P16-0 (`.context/plans/session-doing.md`). Run 2026-09-19 18:56Z on macOS, Claude Code
**2.1.275** (the banner of the session under test; the CLI auto-updated to 2.1.278 during the run). Driver:
`scripts/experiments/E10-doing-triggers/run.sh` — one real interactive session under `expect`, launched outside the
calling session with the inherited environment stripped by prefix, `--permission-mode bypassPermissions`, and a
throwaway logging hook installed through `--settings` on `SessionStart` and `UserPromptSubmit`. The hook records
the event name, a timestamp and two booleans about the prompt (does it look like a task notification; does it look
like a Brigade frame) and never the prompt text. Raw output under `.ignored/card-25/e10/20260919T185604Z/`
(gitignored; `hooks.ndjson`, `tty.log`, `summary.txt`).

## Why

Card 25's reminder to the model rides the `UserPromptSubmit` hook's stdout. The card's live example — a session that
took its pivot on a human prompt and then ran for 2 h 25 m on 53 task-notification turns with no human prompt — is
served by that reminder only if the hook fires on such wake-up turns. The hooks page says only "runs when the user
submits a prompt"; a silent hook run leaves no transcript record, so this could not be read off disk. The plan's
§6 gate: if the answer is no, the trigger row gains a compact-time line and a hint on `brigade send`/`sessions`, and
the owner rules on a Stop hook first.

## What happened

The prompt asked the model to start `sleep 25` as a background Bash task, end its turn, and answer the task
notification with one word.

| UTC | Event | Hook fired | Prompt looked like |
| --- | --- | --- | --- |
| 18:56:06 | `SessionStart`, source `startup` | yes | — |
| 18:59:02 | the typed prompt submitted | **`UserPromptSubmit`** | a human prompt (212 chars) |
| 18:59:29 | background task completed → wake-up turn | **`UserPromptSubmit`** | **a task notification** (449 chars) |

The TUI showed `running UserPromptSubmit hooks… 0/2` at the moment `Background command "Sleep for 25 seconds in
background" completed` was delivered, and the model answered `DONE` on that turn.

**Answer: yes. A task-notification wake-up turn fires `UserPromptSubmit`, exactly as a typed prompt does.** The
plan's gate resolves to "P16-5 as written": no compact-time line, no `send`/`sessions` hint, no Stop hook.

## Not measured, and why

- **A Brigade-message (inbox socket) wake-up.** The driver's peer arm ran `brigade send` from the stripped
  environment, which has no session to send from, so no probe was delivered. The live example took one such turn
  in 44 hours against 69 task notifications; and a session that receives a message and then does any work will
  reach a task notification or a prompt soon after. Not worth a second run for this card.
- **`auto` mode's classifier on a `brigade` verb with a quoted heredoc.** Not run: the plan keeps `auto` out of the
  eligible modes until it is measured (P16-7 can carry it, with the real verb).
- **The other P16-0 items** (background SessionStart ordering, `cwd` after `cd`, `/reload-plugins`,
  `CLAUDE_CODE_SESSION_ID` after `/clear`, where "don't ask again" persists): the design no longer depends on any of
  them — the conversation id travels inside the nudge stamp, the settings scan runs at SessionStart, the
  `/reload-plugins` case is safe under the id-inequality test either way, and the mid-session grant is a
  documented blind spot. Rjae's instruction for this row: measure only what is needed.

## Driver quirk, recorded

`expect` types fast enough that the TUI treated the prompt as a paste: the first `\r` became a newline inside the
input box and the prompt was only submitted by the later `\r` that followed `/exit` (both lines went in one
submission; the model ignored the `/exit` line and did the task). The measurement is unaffected — the two
`UserPromptSubmit` firings are 27 s apart, the second flagged as a task notification — but a future run that
needs the prompt submitted on time should send the text and a separate `send "\r"` after a short pause.
