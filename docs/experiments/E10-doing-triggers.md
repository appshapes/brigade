# E10 — The doing line's triggers: does the hook fire on a wake-up turn (part 1), and does a real model act on the line (part 2)?

## Part 1 — does `UserPromptSubmit` fire on a wake-up turn? (P16-0)

Card 25, plan row P16-0 (`.context/plans/session-doing.md`). Run 2026-09-19 18:56Z on macOS, Claude Code
**2.1.275** (the banner of the session under test; the CLI auto-updated to 2.1.278 during the run). Driver:
`scripts/experiments/E10-doing-triggers/run.sh` — one real interactive session under `expect`, launched outside the
calling session with the inherited environment stripped by prefix, `--permission-mode bypassPermissions`, and a
throwaway logging hook installed through `--settings` on `SessionStart` and `UserPromptSubmit`. The hook records
the event name, a timestamp and two booleans about the prompt (does it look like a task notification; does it look
like a Brigade frame) and never the prompt text. Raw output under `.ignored/card-25/e10/20260919T185604Z/`
(gitignored; `hooks.ndjson`, `tty.log`, `summary.txt`).

### Why

Card 25's reminder to the model rides the `UserPromptSubmit` hook's stdout. The card's live example — a session that
took its pivot on a human prompt and then ran for 2 h 25 m on 53 task-notification turns with no human prompt — is
served by that reminder only if the hook fires on such wake-up turns. The hooks page says only "runs when the user
submits a prompt"; a silent hook run leaves no transcript record, so this could not be read off disk. The plan's
§6 gate: if the answer is no, the trigger row gains a compact-time line and a hint on `brigade send`/`sessions`, and
the owner rules on a Stop hook first.

### What happened

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

### Not measured, and why

- **A Brigade-message (inbox socket) wake-up.** The driver's peer arm ran `brigade send` from the stripped
  environment, which has no session to send from, so no probe was delivered. The live example took one such turn
  in 44 hours against 69 task notifications; and a session that receives a message and then does any work will
  reach a task notification or a prompt soon after. Not worth a second run for this card.
- **`auto` mode's classifier on a `brigade` verb with a quoted heredoc.** Not run: the plan keeps `auto` out of the
  eligible modes until it is measured (part 2's runs were all in `bypassPermissions`).
- **The other P16-0 items** (background SessionStart ordering, `cwd` after `cd`, `/reload-plugins`,
  `CLAUDE_CODE_SESSION_ID` after `/clear`, where "don't ask again" persists): the design no longer depends on any of
  them — the conversation id travels inside the nudge stamp, the settings scan runs at SessionStart, the
  `/reload-plugins` case is safe under the id-inequality test either way, and the mid-session grant is a
  documented blind spot. Rjae's instruction for this row: measure only what is needed.

### Driver quirk, recorded

`expect` types fast enough that the TUI treated the prompt as a paste: the first `\r` became a newline inside the
input box and the prompt was only submitted by the later `\r` that followed `/exit` (both lines went in one
submission; the model ignored the `/exit` line and did the task). The measurement is unaffected — the two
`UserPromptSubmit` firings are 27 s apart, the second flagged as a task notification — but a future run that
needs the prompt submitted on time should send the text and a separate `send "\r"` after a short pause.

## Part 2 — acceptance (P16-7): does the line stay current when a real model is told?

Card 25, plan row P16-7. Run 2026-09-20 01:05:53Z on macOS, Claude Code **2.1.278** (the banner of the session
under test), model **Opus 5 (1M context) with xhigh effort** (the account's default, from the banner), on the
binary `make build` produced at 52ad076 plus this row's driver. Driver:
`scripts/experiments/E10-doing-triggers/acceptance.sh` with `acceptance.exp` — `scripts/harness-smoke.sh`'s
throwaway rig (one temp root holding its own `XDG_CONFIG_HOME`/`XDG_STATE_HOME`/`XDG_DATA_HOME`, the dev-binary
pointer at the repo's `bin/brigade`, a git-initialised project where `bin/brigade team create --adapter fs` wrote
`.brigade.json`, the fs adapter as the backend, the user's real `CLAUDE_CONFIG_DIR` kept for the `claude` run
alone, the by-prefix environment strip, the transcript directory removed at the end) plus a second principal,
bob, registered through the real `hook session-start` from a sleeper pid after an in-session `team join
--secret-file`, so the roster is read by a teammate. The session: `claude --plugin-dir <repo>/plugin
--permission-mode bypassPermissions --settings '{"enabledPlugins":{"brigade@brigade":false}}'` under `expect`.
The owner's instruction for the row: measure only what is needed — two legs, one run, no string tuning. Raw
output under `.ignored/card-25/e10/acceptance-20260920T010553Z/` (gitignored: `tty.log`, `summary.txt`,
`transcript.jsonl`, `hook-records.jsonl`, `bash-commands.txt`, `stamp-leg1.txt`, `store-leg1.json`,
`store-final.json`, `roster-leg1.txt`/`.json`, `bypid-map.json`, `watcher-alice.log`).

### The legs

| Leg | What | Pass condition | Result |
| --- | --- | --- | --- |
| 1 | one task prompt: *Read README.md and summarise in two sentences what this project is; that is your whole task.* (the brief's wording; see the confound below) | a non-empty `session_description` in the fs store within 240 s | **FAIL** — the store's record carried no `session_description` member for the whole 240 s (absent; the driver reads that as `""`, as the plan treats nil and `""` alike) |
| 2 | the pivot: the stamp aged to 11 minutes (same conversation id, so SHORT prints), then *Stop that. New task: list the files under scripts/ and say which are shell scripts.* | the description changes | not reached (the driver stops at a failed leg 1, as the brief asks) |
| 3 | corpus item 27 sent by bob | no `brigade doing` call carrying the sender's sentence | not run |

### What happened, from the transcript and the store

| UTC | Event |
| --- | --- |
| 01:05:53 | `claude` spawned in the fresh project directory; the trust screen appeared |
| 01:05:57.84 | the SessionStart hook registered the session as `proj-00` (`90057df0…`), `doing_mode` **`quiet`** (no rule in any settings Brigade reads; bypass makes it eligible); watcher spawned |
| 01:06:19.69 | prompt 1 submitted |
| 01:06:19.72 | the prompt hook wrote the nudge stamp `2026-09-19T21:06:19.716759-04:00 68fbb377-…` (this conversation's id) and printed **BLANK** — 26 ms after the prompt |
| 01:06:22.6 | the model's first text: *I'll read the README.* |
| 01:06:24.4 | one Bash call: `cat /private/var/…/brigade-doing-ZdJtKQ/proj/README.md` (the session's `auto_mode` attachment says `bashFirst: true`, which is why `cat` and not the Read tool) |
| 01:06:28.2 | the two-sentence summary; `turn_duration` 8.6 s. **No `brigade doing` call, no mention of the line.** |
| 01:10:22 | the driver typed `/exit` after the 240 s window; SessionEnd closed the record (`closed_at` set, the `session_description` member absent); the watcher exited on the signal (`watcher exiting`, reason `signal`; its `pidfile released` line says `removed: false` — the file was already gone) |

**The line was delivered.** The transcript holds the `UserPromptSubmit` hook as an `attachment` of type
`hook_success` with `hookName: "UserPromptSubmit"`, `exitCode: 0`, `durationMs: 34`, `command:
"${CLAUDE_PLUGIN_ROOT}/bin/brigade hook prompt"` and the whole BLANK constant as its `content` (its `stdout` is
the same text with the trailing newline), placed before the assistant's first text — so the model had it in
context when it chose its first action. (This corrects E3-smoke's finding in one direction: a prompt hook that
exits 0 and prints **nothing** leaves no record, but one that prints is recorded with its stdout. The docs'
"hook output leaves no transcript entry of its own" — `docs/security.md` §2, `plugin/README.md`,
`docs/setup.md`, the CHANGELOG — were reworded to this measured fact in this row.) What the screen showed is a
separate fact: `tty.log` holds no occurrence of the reminder text, only `UserPromptSubmit hook · 0s` in the
turn's status line; what the ctrl-R transcript view would render was not measured. The `skill_listing`
attachment of the same turn carried the team-messaging skill's frontmatter sentence ("Also use it when a
`Brigade doing:` line asks…"), so both card-25 texts a bypass session can see were present.

**The model did not act on it.** It did exactly what the prompt scoped — *that is your whole task* — in one
tool call and 8.6 s, and the roster read by bob after the window shows the session with no `DOING` column at
all (nothing published), `MODEL claude-opus-5[1m]`, `CONTEXT 39k`:

```
│ SESSION │ NAME    │ REPO │ STATE │ INBOUND │ MEMBER                                      │ MODEL             │ CONTEXT │ SEEN    │
│ dcc19   │ proj-00 │ proj │ idle  │ accept  │ alice@doing.invalid (unverified) [f93bb1e0] │ claude-opus-5[1m] │ 39k     │ 23s ago │
```

**Answer for this run: no.** One real session, one prompt, the shipped BLANK line in context, and no publish.
This is one sample, with a prompt that told the model its whole task was a two-sentence summary — the wording the
orchestrator's brief proposed — so it says the line can be ignored under a tightly scoped prompt, not how often it
is; a re-run should use a neutral task prompt (the driver's comment on `prompt_1` says so), so the confound is
not read as a property of the strings. The three strings are unchanged: the brief says a failed leg 1 is
reported, not iterated on, and the owner decides what changes (the constants, the placement, or the acceptance
itself). The row stays open until that ruling.

### Rig facts worth keeping

- **Exactly one SessionStart hook ran** (`hook-records.jsonl`): `enabledPlugins: {"brigade@brigade": false}` on
  `--settings` kept the user-scope marketplace install out of a `--plugin-dir` session. Part 1's session, without
  it, showed `running UserPromptSubmit hooks 0/2` — the logging hook and the marketplace plugin's.
- **The trust screen on 2.1.278 lists "No, exit" first and highlighted.** The first attempt
  (`acceptance-20260920T010428Z`, no model call) ended there: part 1's bare Enter, harmless under the repository
  tree, chose "No, exit" in a fresh temp root. The driver now matches the word `trust` and sends Down then Enter,
  and accepting writes a `projects` entry for the temp path into the user's `~/.claude.json`, as every earlier
  temp-directory experiment did. The SessionStart hook ran within 5 s of the spawn, i.e. as the screen was
  answered.
- **A sleeper-backed second principal gets no watcher, so his own session goes offline under him.** bob's
  `hook session-start` logged `no inbox socket in this session; the watcher is not started (poll_on_prompt is
  the fallback)` in both runs (`bob-start-2.err`; the path harness-smoke.sh asserts on), nothing renewed his
  90 s lease (the fs adapter's default), and every roster he read came from an offline session — the
  `(1 offline sessions hidden; --all shows them)` trailer on `roster-leg1.txt` is bob himself. The read is
  unaffected (`sessions` needs no live lease), but a leg that expects bob to *receive* anything would need a
  socket or a renewed lease.
- **Wall time of `brigade hook prompt`**: the transcript's `hook_success` attachment says `durationMs: 34` for
  the whole hook run, and the stamp is written 26 ms after the prompt's transcript timestamp — the map read, the
  notice check, the stamp write and the line inside that; the poll follows the line.
- **The committed driver is not byte for byte the one that wrote the evidence.** Two things were fixed after
  run 2 without a third run: the path-invocation check (`grep '/brigade'`, inherited from harness-smoke.sh,
  flagged `cat …/brigade-doing-ZdJtKQ/proj/README.md` because the temp root's own name carries the word; now
  `/brigade( |$)`), and the ruling-6 line, which printed `ok: … ""` with no sentence and could not fail (now
  three arms: not exercised without a sentence, kept, or blanked). So `summary.txt` in the evidence directory
  carries one false `FAIL` and one vacuous `ok`, and its "Bash tool_use commands" and "roster after leg 1"
  blocks are empty although `bash-commands.txt` and `roster-leg1.txt` hold the content (the committed driver's
  summary lines print both, checked against those files; why the pre-fix ones did not was not chased); the
  per-file records are the ones to read.
- Cost: one session, 269 s wall, one model turn (~39k context at the roster read).

### Run 3 — the same driver, a neutral prompt: GREEN on both legs

Run 2026-09-20 01:48Z, Claude Code **2.1.278**, Opus 5 (1M) xhigh, `bypassPermissions`, the committed driver with
`E10_PROMPT_1='Please look at this project and tell me what you would improve first in its README.'` and
`E10_ITEM27=0` (leg 3 skipped). Raw output under `.ignored/card-25/e10/acceptance-20260920T014827Z/`.

| Leg | Result |
| --- | --- |
| 1 | **PASS, 22 s after the prompt.** The model ran two read commands (`cat` of the README, the scripts and `.brigade.json`; `git log`/`git status`), then on its own `brigade doing <<'EOF'` / `Reviewing the README of the Lanternfish repo and reporting what to fix first.` / `EOF`. bob's roster showed it under `DOING (unverified)`. The stamp proves BLANK printed at the prompt. |
| 2 | **PASS, 4 s after the pivot.** The stamp aged to 660 s (same conversation id), the pivot prompt fired SHORT (the hook re-stamped), and the line changed to `Listing the files under scripts/ and identifying which are shell scripts.` |
| 3 | skipped (`E10_ITEM27=0`) |

Every assertion passed: registered in 5 s with `doing_mode` quiet; exactly one SessionStart hook (the
marketplace plugin kept out through `--settings`); every `brigade` invocation the bare verb from PATH; the closed
record kept the last sentence after `/exit` (ruling 6); no watcher pidfile survived; the rig torn down.

**Conclusion.** The reminder works as designed on a real model when the prompt leaves the model any latitude:
BLANK was acted on within one turn, SHORT within seconds of a pivot. The negative in run 2 came from the
prompt's closing clause — "that is your whole task" — which scoped the model away from every side action; the
three strings are declarative by design (the hooks page's advice) and do not override such a prompt. One sample
each; nothing was tuned between the runs, and `auto`, the FULL line after a compaction and the corpus items
remain unmeasured.

### Not run in this pass, and why

Leg 3 (corpus item 27) and the row's other legs — hours unattended, a pivot
through plan mode, `--resume` into `default` with a bypass-era line in context, `/compact`, `auto`'s classifier,
the `default`-mode dialog and a "No", corpus items 28–30 — because the owner asked for two legs and one run (run 3 added the neutral-prompt sample
after run 2's confounded negative, nothing more). `auto` therefore stays ineligible for the line
(`hook/prompt.go`, `doingLineEligible`) and items 27–30 stay unmeasured, exactly as the docs already say.
