# Time and usage per work item, as a separate plugin beside Brigade (work items 537 and 538)

Written on 2026-11-27 by the owner's session `reviewer-session-usage-rjae`, which reviewed work items 346 to 349
for the owner. This is the hand-over to the session that does the work. It has six parts: the owner's ruling,
what the review found, taking the four work items out of Brigade (work item 537), the plugin (work item 538),
what is still the owner's to decide, and the board. ADR 0040 records the decision.

**Added later on 2026-11-27:** the plugin has its name and its repository, both the owner's: Tally, in
`appshapes/tally`. That repository's `.context/plans/tally-plan.md` is the plan the build follows and replaces
section 4 below; its `docs/agent/repository/invariants.md` holds the owner's rules. Point 1 of section 5 is
decided. Work item 534 was reworded to the roster's context alone, and work item 350 carries the tag `tally`.

## 1. The ruling

On 2026-11-27 the owner read what work items 346 to 349 had built. The quoted words are the owner's.

- "A session must never be limited to a single work item; a session must be able to be actively working on
  multiple work items."
- "Teams that do not enable these new values must never incur anything related to this feature. Nothing surfaced
  in the UI, nor in files, nor in processing (load)."
- "I think adding work items to the brigade sessions list is a coupling we don't need".
- "Most of a session's time is spent idle, so I question whether we are looking at the time-worked problem
  correctly." And: "a better model is to only keep a time-spent property and when a session spends time on a
  work item it simply adds to that property."
- "Shipping a `prices.json` feels immediately brittle to me, and probably not something Brigade should be doing."
- "My concern is we continue to add more and more to Brigade, which was originally meant to be its core
  features: session collaboration."
- The direction is a separate plugin: "I really do not like the impact of these changes" on "the general
  Brigade user base."
- "We must not allow these changes that are in master to be released".

The owner also asked two questions. The review's answers are part of the direction:

- "Would a better design be updating values/stats on the stop event?" Yes. Section 4.5 says how.
- "Wouldn't it be simpler to ask the model itself to estimate new costs it spent per work item?" Not for the
  amounts: a model cannot see its own usage. The session says which work items a turn was for, and the plugin
  measures.

## 2. What the review found

Measured on 2026-11-27 on the owner's machine with Claude Code 2.1.332, on this repository's six most recent
sessions (four days). The scripts and the two scratch tests that produced the numbers are in
`.brigade/review/346-349/repro/`. File and line names are of commit 57b659c8.

| Finding | Evidence |
| --- | --- |
| No switch gated the usage reading. Only the sharing had one. | `internal/harness/watch/heartbeat.go:65` called `recordUsage` for every session; `watch/usage.go:109` gated `shareUsage` alone. |
| A team that enabled nothing got processing. | A usage record built at every heartbeat (30 seconds) and written again every five minutes; a Muse Code session ran `muse usage --json` every five minutes, idle or not. |
| A team that enabled nothing got files. | One usage record for each session in the state directory; one directory for each session in the board folder, written by the `work-item-start` skill for every team. |
| A team that enabled nothing got new output. | `sessions:` in `work-item show`, the `ITEM` column of `brigade sessions`, agents and a dollar figure on the page's cards, a "Plan limits" panel. |
| A session was limited to one work item. | ADR 0035, decision 1; `internal/harness/workitem/work.go:112`. |
| A span from start to stop measures open time. | 546 of 4,826 minutes of wall clock were in turns: 11 percent. One session was open 54 hours and in turns under 3. |
| Claude Code writes a subagent's calls to files of their own. The reader took the main transcript only. | 15 subagent files with 228 model calls beside six transcripts with 1,714; no sidechain record inside a main transcript. |
| Every cache write was a 1-hour write, priced at the 5-minute price. | 5.9 million cache-write tokens in the main transcripts, all `ephemeral_1h_input_tokens`; $16 for each million on Fable 5.2, priced at $10. |
| The cost method gave 81 percent of list price. | $269.07 against $331.84 on the same sessions. |
| Every token was priced at the session's current model. | `watch/usage.go:489`; 96 of the 228 subagent calls ran on another model. |
| Usage was credited by what the board said at the heartbeat. | Tokens spent before `start` went to the started work item, tokens spent before `stop` went to none, and after a switch the first work item's tokens went to the second (scratch test `zz_watch_review_test.go`). |
| A work item's sums were not kept. | They lived in each session's local record, removed 30 days after its last write (`usage/usage.go:312`); the page added up what was left. |
| A `prices.json` with one bad price gave a different table from load to load. | Four outcomes in 400 loads (scratch test `zz_usage_review_test.go`; `usage/prices.go:87`). |
| The nine shipped prices were right. The table could not say the rest. | Checked against Anthropic's pricing page on 2026-11-27. Not in the table: 1-hour cache writes (2 times input), fast mode (2 times), Haiku 5.5 over 100,000 tokens (5 times), US-only inference (1.1 times), web search. |
| A stop could go unwritten where a host cuts the SessionEnd hook short. | The hook wrote it last (`hook/end.go:119`); the watcher wrote it only when the process had died (`watch/attempt.go:482`). From the code; not reproduced. |
| An install got skills its binary refuses. | A Claude Code install made, or moved to 0.61.0, after 2026-11-26 took `master`'s plugin files: a `work-item-start` skill naming `brigade work-item start` and `stop`, and the `share_usage` option. The 0.61.0 binary answers `work-item takes one verb: list ...`. Measured on an install at commit 57b659c8. |

What held: the usage on every record of one message was the same (1,714 messages, 1,201 with several records),
so counting a message once by its id is right; the files were private and no symlink was followed; the tests
passed and CI was green.

## 3. Take work items 346 to 349 out of Brigade (work item 537)

This is first. Nothing is released before it is in Done.

### 3.1 What the commit does

- It removes what commits e0f737a2 to 57b659c8 added: the session links and `brigade work-item start` and
  `stop` (346); the usage reading, `brigade usage` and `brigade statusline` (347); the price table, the sums by
  work item, and the page's agents, plan limits and dialog (349); the option `share_usage` and the shared usage
  files (348). Their pages, tests, fixtures and skill steps go with them, in all five packages.
- `docs/security.md` has its words from before 346 and 348 again. Section 22, of work item 532, stays.
- ADRs 0035 to 0038 keep their text; each Status reads "Superseded by 0040". ADR 0040 is new.
- The changelog's section `[Unreleased]` loses the four entries and gains one under Fixed.
- What work item 532 added stays. Its Antigravity row in `internal/harness/config/host.go` loses its usage
  block, the two skills of its package are generated again, and its install page says "The roster shows no
  context yet (work item 534)".
- The execution log keeps rows P106-82 to P106-85 and gains a row for this work item.

### 3.2 How the patch was made

`.brigade/review/346-349/removal.patch` is the result, against commit 0ebd597f. To make it again on a later
head:

```sh
git diff --binary 57b659c8 451b4582 -- . \
  ':(exclude).context/plans/brigade-execution-log.md' ':(exclude)docs/decisions' \
  ':(exclude)CHANGELOG.md' ':(exclude)marketplace.json' \
  ':(exclude)docs/team-member/use/usage.md' > reverse.patch
git apply --3way --index reverse.patch      # on 0ebd597f: no conflict
git rm docs/team-member/use/usage.md        # 532 changed one line of it; it goes whole
```

The eight commits are one unbroken run on `master` (451b4582 is the commit before the first), so the reverse of
that range is exactly their removal. Then, by hand:

1. `internal/harness/config/host.go`: delete the `usage: UsageGaps{...}` block of the Antigravity row.
2. `docs/team-member/install/antigravity.md`: the bullet "No usage is read yet" becomes "The roster shows no
   context yet (work item 534). It shows the model, which the hook gives."
3. `CHANGELOG.md`: delete the four entries that name work items 346 to 349; add the Fixed entry.
4. `docs/decisions/`: the four Status lines, ADR 0040, its line in the index.
5. `.context/plans/`: this file, and the row in the execution log.
6. `make skills-generate` (it writes the two Antigravity skills), `make docs-generate` (no change),
   `make muse-digest`.

### 3.3 What it passed

On a scratch tree at 0ebd597f with the patch, on 2026-11-27: `go build ./...` and `go vet ./...`;
`make typecheck lint docs-check skills-check deps-check tidy-check schema-check`; the five package checks
(`plugin-check`, `muse-plugin-check`, `codex-plugin-check`, `grok-plugin-check`, `antigravity-plugin-check`);
`make test`. Not run there: `make vuln`. The session that lands it runs the whole acceptance gate again on its
own tree.

### 3.4 Landing it, and what follows

1. On an up-to-date `master`: `git apply --3way --index .brigade/review/346-349/removal.patch`. If origin has
   moved and it does not apply, make it again by 3.2.
2. The acceptance gate, the five package checks and `skills-check`; an agent review of the tree; then
   `make push message="537: Take work items 346 to 349 out of Brigade: usage, cost and time per work item
   become a separate plugin"`; watch `ci.yml` to green.
3. End every session that runs a build with 346 to 349, or start it again on a build without them: such a
   session still writes `sessions/` files to the team's board.
4. Remove the `sessions/` and `usage/` folders from the team's board folder (the directory that holds `items/`
   and `lists/`), on one machine; file sync carries the removal to the others. On 2026-11-27 they held one
   session directory with 14 changes and one usage file.
5. A release soon after. An install that took the unreleased skills under version 0.61.0 keeps them until its
   version changes; only a release or a reinstall repairs it.

### 3.5 Two things that left with it

- **The dialog.** Commit 4c94fde4 also made a click on a card open the work item in a dialog. It is removed with
  the rest. It can come back as a change of its own, at the owner's word.
- **Work item 534.** Its usage part is not built in Brigade. What is left for Brigade is the context in use on
  the roster for an Antigravity CLI session.

## 4. The plugin (work item 538)

`tally` below is a working name. The plugin's name is the owner's to give.

### 4.1 What it is

A plugin of its own, in a repository of its own, which a team installs beside Brigade if it wants time and
tokens per work item. A team that does not install it has no code of it, no file and no output. It needs
nothing new from Brigade. It works without Brigade too: a work item is then a number the session names.

### 4.2 The owner's rules, as things to test

| Rule | Holds when |
| --- | --- |
| Several work items at once | A session with three work items on gets each turn's time and tokens on all three. |
| Idle time is not work | Two turns of one minute, an hour apart, add two minutes. |
| A time-spent number that is added to | A work item's total is a sum; no start, no stop and no span is kept. |
| It reads at the end of a turn | No timer and no background process; an idle session runs none of the plugin's code. |
| Nothing in `brigade sessions` | The plugin changes no Brigade output. |
| No price table | It reports tokens. It shows a cost only where the harness states one. |
| Sharing is off unless a member turns it on | With the option off, nothing is written outside the plugin's own data directory. |
| Nothing added to Brigade | It calls the `brigade` command line as released; a Brigade without the plugin behaves as before. |

### 4.3 What Brigade gives it

Seen on 2026-11-27 with the released 0.61.0:

- `brigade whoami --json` and `brigade work-item show <number> --json` print one JSON document
  (`ok`, `protocol_version`, `result`). `result.item` of the second carries `id`, `number`, `title`, `list`,
  `assignee`, `tags`, `closed` and the comments.
- `brigade work-item comment <number>` takes a comment on stdin.
- In a Claude Code session Brigade's `bin/` is on the Bash tool's PATH, so a command the model runs can call
  `brigade` by name. Whether another plugin's hook can is not documented: prove it (4.6).

The plugin depends on these outputs. Treat a change to them as a change that can break it.

### 4.4 What Claude Code gives it

From Claude Code's documentation, read on 2026-11-27 (`code.claude.com/docs/en/hooks.md`,
`plugins-reference.md`, `sub-agents.md`, `statusline.md`). Confirm the exact words before relying on one.

- Every hook gets `session_id`, `transcript_path`, `cwd` and `hook_event_name` on stdin. The transcript "is
  written asynchronously and may lag the in-memory conversation".
- A subagent's transcript is `agent-<id>.jsonl` in a `subagents` folder under a folder named for the session,
  beside the transcript. Seen on 2.1.332.
- A plugin's hooks get `CLAUDE_PLUGIN_ROOT`, `CLAUDE_PLUGIN_DATA` (a directory kept across updates and removed
  with the plugin) and each option as `CLAUDE_PLUGIN_OPTION_<KEY>`.
- A command hook's `timeout` is in seconds. SessionEnd hooks have 1.5 seconds unless a `timeout` raises it.
- `"async": true` runs a command hook in the background.
- `StopFailure` exists for a turn that ends on an API error. Whether `Stop` runs on an interrupt is not in the
  parts read.
- No hook input and no variable carries tokens or cost. The status line's input does
  (`cost.total_cost_usd`, `rate_limits`), and only a member's own settings set a status line.
- A plugin can name another in `dependencies`; across two marketplaces that needs an allowlist in settings.

What the transcript holds, seen on 2.1.332: one record for each content block of a message, each with the same
`message.id` and the same `message.usage`; in `usage`, `input_tokens`, `output_tokens`,
`cache_read_input_tokens`, `cache_creation_input_tokens` and its split
`cache_creation.ephemeral_5m_input_tokens` and `ephemeral_1h_input_tokens`, with `speed`, `service_tier` and
`server_tool_use`; `message.model` as a bare id; and a `system` record of subtype `turn_duration` with
`durationMs` for a finished turn.

### 4.5 Design

**Hooks only.** No watcher, no timer.

| Hook | What it does |
| --- | --- |
| `SessionStart` | Notes the session. A new session's file offsets are 0. A session first seen in its middle starts at the files' ends: what was spent before the plugin's first reading is no work item's. It also finishes a turn that an earlier session left open. |
| `UserPromptSubmit` | Closes the turn before it if no `Stop` came, then stamps the new turn's start. |
| `Stop` | Closes the turn. |
| `SessionEnd` | Closes an open turn if its 1.5 seconds allow; otherwise the next `SessionStart` does. |

**Closing a turn** is three steps under one lock for the session:

1. Time: from the turn's start stamp to now. For a turn with no `Stop`, to the time of the last record the
   transcript got before the next prompt.
2. Tokens: read what the transcript and each `subagents/agent-*.jsonl` file gained since the saved offsets,
   whole lines only. An `assistant` record with a usage counts once for each `message.id`; the model
   `<synthetic>` does not count. Keep the sums by model: input, output, cache read, cache write of 5 minutes,
   cache write of 1 hour, and the calls. Keep a count of calls whose `speed` is not `standard`.
3. Add the time and the tokens to each work item the session is on, in equal shares. With none on, add them to
   the session's own "no work item" sum.

If the transcript lags at `Stop`, the rest is read at the next event and goes to the work items on then. Measure
the lag first (4.6); if it is real, the hook waits until the file has not grown for a moment, within its
timeout.

**Which work items.** `tally on 346 349` puts the session on them, `tally off 346` takes one away and
`tally off` all. The set is the session's and is kept in the plugin's data. A `/clear` and a resume keep it.
With Brigade there, `on` asks `brigade work-item show` for the work item's id and title and refuses a number
the board does not have.

**State.** One file for each session under `CLAUDE_PLUGIN_DATA`, mode 0600 in a directory of mode 0700, written
whole and atomically: the offsets, the set, the open turn's start, and the sums for each work item. A work
item's total on this machine is the sum over the session files. A session's file is never removed for its age.

**Commands.** `tally on <number>...`, `tally off [<number>...]`, `tally` (this session: its work items and what
it spent on each), `tally report [<number>] [--json]` (every work item on this machine). The hooks call the
same binary.

**A skill** tells the model when to run `on` and `off`: when it takes a work item, when it stops work on one,
and that several can be on. Brigade's own skills say nothing of it.

**Cost.** None is computed. Where a harness states a cost itself, the plugin may show that figure and say whose
it is. A team's own prices file is a later step, at the owner's word.

**What it never does.** It reads no auth file and no `$CLAUDE_CONFIG_DIR/sessions/*.key`. It sends nothing
anywhere. With sharing off it writes only in its own data directory.

**Built in Go**, as one binary, so that the readers and tests of the removed build carry over (4.8). The
launcher and the release steps can follow Brigade's. macOS and Linux first; Windows is the owner's to ask for.

### 4.6 Prove first

Each is a short run in a live session, written up as an experiment report in the plugin's repository.

1. A second plugin's `Stop`, `UserPromptSubmit` and `SessionEnd` hooks get `session_id` and `transcript_path`.
2. Whether `Stop` runs when the user interrupts a turn, and what `StopFailure` delivers.
3. How far the transcript lags at `Stop`: is the turn's last assistant record on disk when the hook runs? Twenty
   turns.
4. When a subagent's file is complete, and what `SubagentStop` gives.
5. Whether the plugin's hook can run `brigade` by name. If not, only commands the model runs call it.
6. How long closing a turn takes on a transcript of 100 MB with saved offsets. Aim under 200 milliseconds.
7. Whether `"async": true` is allowed on `Stop`, so that a turn never waits for the plugin.
8. What `SessionStart` says after `/clear` and after a resume, so that the set can follow the session.

### 4.7 Steps, and when each is done

1. **Prove first** (4.6). Done when each point has an answer and the design is changed where one is "no".
2. **The first version: Claude Code, on the member's own machine.** Done when every rule of 4.2 holds in a
   test and in a live session; a fixture with a `subagents` folder gives the calls of both; a turn across two
   models gives two sums; a session with no work item on still shows what it spent; uninstalling the plugin
   leaves Brigade as it was.
3. **Sharing with the team**, off by default, after the owner's decision of section 5. Done when a test proves
   that nothing leaves the data directory with the option off.
4. **The other harnesses**, each its own package: Codex CLI, Grok Build, Muse Code, Antigravity CLI.
5. **A budget on a work item** (work item 350).

Steps 3 to 5 each wait for the owner's word.

### 4.8 What to carry over, and what to fix on the way

Read each with `git show 57b659c8:<path>`.

| From | For |
| --- | --- |
| `internal/harness/transcript/transcript.go` (`count`, `Tokens`) and `tokens_test.go` | The Claude Code reader. Add the subagent files, the sums by model and the split of cache writes. |
| `internal/harness/usage/usage.go` (`Value[T]`, the private atomic writes) and its test | The state's types and files. |
| `internal/harness/usage/statusline.go` and `testdata/statusline.json` | A later step: a cost the harness states. Do not write the file at every refresh of the status line. |
| `internal/harness/watch/hostfacts_codex.go`, `hostfacts_muse.go`, `grokname.go` (their usage parts) and `hostfacts_usage_test.go` | Step 4. None of the three was run live for sums by work item. |
| `internal/harness/usage/prices.go`, `internal/harness/workitem/work.go` | Not carried: no price table, no spans. |

### 4.9 Not part of it

- Usage on the Brigade server's team page (work item 508). It would put usage back in Brigade and change the
  frozen REST contract.
- Plan limits and paid extra usage. On Claude Code they come from the status line alone.
- Anything in Brigade's board page, roster or skills.

## 5. The owner's to decide

1. **The plugin's name and its repository.** Work item 538 can start its step 1 without it; the first version
   cannot be published without it.
2. **How a team sees totals** (step 3). Three ways: a comment on the work item through
   `brigade work-item comment`, at most one for each session and work item, which changes nothing in Brigade;
   one number on a work item that anything can add to, which is a small change to Brigade and is the owner's
   "time-spent property"; or no shared total. The review recommends deciding after the first version is in
   use.
3. **Work items 350, 508 and 534.** The review recommends: 350 becomes the plugin's step 5; 508 is not built;
   534 keeps only the context on the roster.
4. **The dialog** of work item 349: back as a change of its own, or not.
5. **Closing work items 346 to 349.** Closing is the owner's.

## 6. The board

- 537: this removal. 538: the plugin.
- 346 to 349: in Done, with a comment that names the ruling and work item 537. Not closed.
- 350, 508, 534: in Wanting, each with a comment that says where this leaves it. Not changed otherwise.
