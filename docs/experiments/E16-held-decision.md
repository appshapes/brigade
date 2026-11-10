# E16: a decision for the session's own user is asked in the conversation first (card 236)

**Date** 2026-11-09. **Drivers** `scripts/experiments/E16-held-decision.sh` (no model) and
`scripts/experiments/E16-held-decision-live.sh` (one real interactive session), run from this checkout on the dev
build `0.47.2-dev` over `981a69d` plus the card's working tree, as it was committed. **Machine** this one: macOS,
Claude Code 2.1.322.
**Backend** the fs adapter in a throwaway root, both times: no hosted project, no gateway, no mailbox and no Slack
workspace was involved, and nothing was sent to anyone. What "Slack received" is the inbox of a session registered
on the same store with the Slack gateway's harness (`gateway-slack`).

## What the card changed

Before it, `brigade decision --to <label>` sent the ask at once whoever the label named, and the end-of-turn ask
told a session to record a decision whenever it waited on a person. The person a session waits on at the end of a
turn is usually its own user, so they got a Slack message about a question that was on their screen. Now an ask
whose label names the asking session's own member is recorded and held: the session asks in the conversation, and
the session's watcher sends the ask only when the member's wait has passed with no prompt in that conversation and
the decision still open ([Record notes and ask for decisions](../team-member/use/notes-and-decisions.md#a-decision-for-the-sessions-own-user)).

## Part 1: the mechanism, with no model

One session of one member (`alice@held.invalid`) in a throwaway checkout whose `.brigade.json` names a state
folder, driven as Claude Code drives a session: a sleeper as `CLAUDE_PID`, the real `brigade hook session-start`,
`prompt`, `stop` and `session-end`, the real `brigade decision` and `brigade report` through the Bash tool's
environment, and a real `brigade watch` in sink mode. The member subscribes with `--wait 20s`; a second subscriber,
`bob@held.invalid`, is written into `reports.json` by the script, as E15 did.

| Leg | Assertion | Outcome |
| --- | --- | --- |
| setup | Team created on the fs adapter; the session's entry is in the state folder with the member's label; the watcher is ready; a `gateway-slack` session is on the roster | ok |
| D | `brigade report subscribe --recipients "@alice" --wait 20s` prints `an ask from your own session held for 20s` and writes `"wait": "20s"` into the member's entry | ok |
| A | `brigade decision --to alice@held.invalid --options "ship now \| wait"` prints `recorded:` and `held: alice@held.invalid is this session's own user, so ask them here in the conversation; it is sent to them at 02:23 UTC (in 20s) only if no prompt arrives here and it is still open`, and no `sent:` line | ok |
| A | The decision file carries `held_until` and no `sent`; the gateway's inbox holds no ask; `brigade report show` does not list the decision while its hold runs | ok |
| A | The watcher sent the ask after the wait and not before: `decision: held ask sent`, `how: slack` | ok, 40 s after the ask |
| A | The gateway received one ask: `to: @alice`, a blank line, the question, a blank line, `- ship now` and `- wait`; its summary is the question | ok |
| A | The two digests the watcher produced while the hold ran (both subscribers were due) do not carry the held decision | ok |
| A | The decision file records `sent: slack`, `sent_to: @alice`; the next prompt hook prints `Brigade decision <id>: your user did not answer here within their wait, so it was sent to them through the slack gateway; close it with the outcome when they answer.` | ok |
| B | A second ask for the member is held; a prompt hook two seconds later (the user answering in the conversation) ends the hold: `decision: hold ended with nothing sent`, `how: prompt`, and the file records `hold_ended: prompt` | ok, 16 s after the prompt |
| B | 65 s after the ask, past the wait and two checks more, the watcher had sent nothing further and the gateway held the one ask of leg A | ok |
| B | `brigade report show` lists the decision as open with `asked in the conversation`; `brigade decision --close` closes it | ok |
| C | `brigade decision --to bob@held.invalid` prints `sent: to bob@held.invalid at @bob through the slack gateway` at once | ok |
| D | A subscription that names no `--wait` prints `held for 10m0s`, and an ask then prints `(in 10m0s)` | ok |
| D | `--wait 0` prints `an ask from your own session sent at once`, and an ask for the member then goes out at once | ok |
| E | A Stop after a turn longer than a minute carries the decision sentence with `your own user is asked here in the conversation first, and the recorded one is held and reaches them only if they have not answered here within their wait` | ok |

Exit status 0: 29 assertions, none failed.

**Measured.** The held ask went out 40 s after it was recorded with a wait of 20 s: the watcher looks at its held
asks on its state check, every 20 s, and reads the decision and the last prompt once more before it sends, so an
ask goes out between the wait and the wait plus a check or two. The hold ended 16 s after the prompt for the same
reason. The table is the run on the build that was committed. Two earlier runs, before the agent review's fixes,
read 39 s and 17 s; the first of them was cut short by an edit to the script while it ran.

## Part 2: a real model

One interactive Claude Code session (2.1.322, Opus 5.5 at medium effort, `bypassPermissions`), started with
`--plugin-dir` on this repository's plugin tree and the dev build, typed into by E10's expect driver, in the same
kind of rig: the fs adapter, a throwaway checkout with a state folder, the member `alice@held.invalid` subscribed
with the default wait, a `gateway-slack` session on the store. The marketplace-installed plugin was switched off
for the run. No wording was tuned: the model had the shipped end-of-turn ask and the skill listing, and loaded no
skill. The table is the run on the build that was committed; a run before the agent review's fixes, on Claude Code
2.1.321, went the same way (the decision recorded 84 s after the prompt and closed 27 s after it was asked).

**The prompt**: run one command that takes 75 seconds, then "ask me, as your reply, whether it should be called
notes-a.md or notes-b.md, and create nothing until I have answered". The very first run asked for `sleep 75`,
which Claude Code refuses in the foreground; that turn ended in 4 s, no end-of-turn gate opened, and the run was
stopped. The runs that count used `python3 -c "import time; time.sleep(75); print(1)"`.

| Leg | What happened | Outcome |
| --- | --- | --- |
| 1 | The model ran the command and ended its turn on the question: "Should the new notes file be called **notes-a.md** or **notes-b.md**? I won't create anything until you answer." | the host's own flow |
| 1 | The Stop hook answered with the end-of-turn ask (a stale roster line, and the decision sentence). The model ran `brigade doing` and `brigade sessions --here`, then `brigade decision --to alice@held.invalid --options "notes-a.md \| notes-b.md"` with the question on stdin | ok, 88 s after the prompt |
| 1 | The decision file: `to: alice@held.invalid`, `held_until` ten minutes after the ask, no `sent`. The gateway session's inbox held no ask | ok |
| 1 | The model's closing reply: the question again, then "I also logged this question with Brigade. If you haven't replied here by 02:52 UTC, it will be sent to you through the team's gateway." | ok |
| 2 | The driver typed `notes-b.md` about 24 s after the ask. The model created the file and ran `brigade decision --close` with the outcome `notes-b.md`, 28 s after the ask by the file's two times and before the watcher's next check, so the file records the close and no `hold_ended` word | ok |
| 2 | Nothing was sent: the file has no `sent`, and the gateway session's inbox held no ask at the end | ok |

So the model asked in the conversation first, the recorded ask stayed on the machine, and the user's answer in the
conversation was the end of it. The one digest the session's watcher produced at its start (the member was
subscribed and had no marker) went to the gateway session and listed no decision.

**What two runs do not say.** How often a model records the decision at all: that is the model's choice, as it
was before this card, and a question it asks without recording is simply asked in the conversation, with no ask to
send later. Whether it uses the host's question dialog when the prompt does not say "as your reply": a dialog keeps
the turn open, so no end-of-turn ask arrives while it waits.

## Not proven here

- **A hosted gateway.** The ask a held decision's release sends is built by the function `brigade decision` uses
  for an ask sent at once (`digest.AskMessage`), and it goes through the same adapter call, so the gateways see
  nothing new. No real Slack message or mail was sent for this card.
- **Muse Code, Codex CLI and Grok Build.** The hold is in the command and the watcher, which those hosts run
  unchanged; their sessions were not driven here. On a host with no Stop hook the end-of-turn ask arrives at the
  next prompt, as before.
- **A prompt nobody typed.** A team message or a finished background task that starts a turn in the conversation
  fires the prompt hook as a typed prompt does (E10 for a task notification on Claude Code; the Codex queue report
  for a queued message), so it ends a hold too. The driver's prompt hook stands for any of them.
- **Two members.** One binding is one member; the second subscriber is an entry in the file.
