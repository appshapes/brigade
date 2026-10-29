# Manager digest: decisions and contributions, from the team's synced folder

**Status.** **v2, in build**, 2026-10-29, Trello card **162**, owner Rjae. The nine rulings of 2026-10-29 are
in §7 and settle the carrier, the sender, the state entry and the digest; the rows are the **P65** series of the
execution log (§6); P65-1 to P65-7 landed 2026-10-29 with the deviations §5.1, §5.3 and §5.4 note in place; the live
proof is `docs/experiments/E14-manager-digest.md`. **No
wire-shape change, no Supabase migration, no new dependency.** New: one hook event in the Claude Code package
(`Stop`), three commands (`brigade note`, `brigade decision`, `brigade report`), a settings file in the synced
folder, and a digest producer in the watcher. Folder sync ([folder-sync.md](folder-sync.md), released 0.11.0,
folder ids by repository since 0.16.0) is the carrier. Anchors are file:line at **929b7c0** (master, 0.40.0).
Execution-log rows are planned as the next free `P` series (§6).

## 1. What was asked

Rjae, 2026-10-29, in the session "support-duet": advise how "a development manager, responsible for multiple
Brigade teams, may be assisted in staying on top of both the decisions he's needed for AND the most meaningful
contributions since the last report", and "it would be great if the development manager could also use Brigade
directly or indirectly to take actions based on the report." Refined the same day: "Manager is pushed a report.
Manager may request a report ad hoc. Inputs are obtained either by sending an update-request message to each
session (not offline) in each team, or, via state synced (via syncthing) in each team." And: "We have the luxury
of waiting till we determine the **best** solution."

## 2. The answer in one page

- **State is a by-product, pulled; never a request that spends a teammate's model turn.** Each session's machine
  writes one entry file for the session into the repository's synced folder: facts its hooks know (member,
  session name, repository, branch, times, commits made, doing-line history) and what the model publishes on
  purpose (`brigade doing`, a new `brigade note` with the labels Done, Next, Blocked, and `brigade decision` when
  it is waiting on the manager). Nothing is read from the transcript beyond the two facts the roster already
  carries; no prompt, answer or tool call reaches the folder.
- **The folder is the carrier**, not the backend: it asks nothing of the message adapter and rides whichever
  sync adapter the team runs. Entries say `member:` and the session name; no signature (ruling 1).
- **The Stop hook asks for the end-of-turn state** only when a condition the hook can see itself holds (a commit
  with no note after it, a stale doing line after a long turn, a configured manager), at most once per ten
  minutes, never twice in a row, never in a quiet session, never where the permission rules would make a dialog.
- **Whichever session is active produces the digest.** The watcher of the first online session in roster order
  writes a facts-only digest when the period has elapsed and something changed, after a delay set by its rank;
  a marker file in the folder stops the others. No manager node: when the team is quiet there is nothing to say.
- **Delivery is the gateways the team already has**, Slack first, email second, as a DM to the manager; a
  decision goes out the moment it is created, the digest on the period. The manager's assistant (Claude Duet,
  Grok Bot, or none) reads Slack or email as it already does; Brigade integrates with no assistant directly.
- **The manager acts however they wish.** Sessions act on the answers, close their decisions, and that informs the
  next digest. Nothing is built for the manager's responses (ruling 4).

## 3. Ground truth, verified 2026-10-29 at 929b7c0

- **Folder sync** ([docs/team-member/use/sync-folders.md](../../docs/team-member/use/sync-folders.md), §12 of
  [docs/security.md](../../docs/security.md)): folders listed in `.brigade.json` `sync.folders`, inside the checkout,
  ignored by git; Brigade's own Syncthing instance per machine, introduced from the roster to every other online
  session **of the same repository**; sync runs only while a session is active on the machine and stops with the
  last; one clone per machine per folder; every teammate's machine can write, overwrite or delete anything in
  them; deletions propagate; `.stversions` keeps replaced files 14 days; the backend sees one string per session,
  `sync_peer`. The 0.40.0 removal (card 148) was the syncthing **message** adapter; folder sync and
  `internal/syncthing` stay.
- **The watcher** (`internal/harness/watch`) is the one long-lived Brigade process on a member's machine: the
  roster every 15 s (`polling.roster_seconds`), a heartbeat every 30 s, the sync adapter
  (`internal/harness/foldersync`), inbound delivery, and the session's credential.
- **Hooks** (`internal/harness/hook/hook.go:138-170`): the input type reads `session_id`, `cwd`,
  `hook_event_name`, `source`, `reason`, the stop flags and `transcript_path`, which the hook keeps in the by-pid
  map and never opens; `prompt` is absent from the type. `doingNudgeInterval = 10 * time.Minute` (hook.go:109).
  The Claude Code package registers SessionStart, UserPromptSubmit and SessionEnd (`plugin/hooks/hooks.json`);
  Grok's registers Stop as well (`grok/brigade/hooks/hooks.json:37`); `brigade hook stop` exists as a verb.
- **The transcript** is read locally by the watcher for the model id and the context occupancy only
  (`internal/harness/transcript/transcript.go:1-39`); security.md lines 150-156 promise that nothing else is read
  out, kept or transmitted. This plan keeps the promise.
- **Messages**: body cap `limits.max_body_bytes`, 16 KiB of UTF-8 (`internal/protocol/limits.go`); a closed
  recipient is accepted and the message waits (`20260830120000_brigade_schema.sql:508`, C-31); housekeeping deletes
  injected messages 24 h after injection, unacknowledged ones after 7 days, sessions 7 days after close or lease
  expiry (`20260830120200_brigade_housekeeping.sql:7-28`). `--member` picks the best session active before idle
  before offline, gateways left out (`internal/harness/commands/send.go:96`).
- **Gateways**: a body whose first line is `to: @name`, `to: #channel` or `to: <address>` leaves through the
  Slack or mail gateway; inbound, a person names a session or a member by label
  (`supabase/functions/_shared/gateway.ts:130-182`); outbound mail needs an address, there is no member-to-address
  mapping.
- **Protocol v1** is frozen against breaking change and takes additions announced through `capabilities`
  (`docs/protocol-v1.md:39`); this plan adds none.
- **The doing line** (card 25, [session-doing.md](session-doing.md)): one sentence of at most 160 characters in
  `session_description`, a prompt-hook reminder at most once per ten minutes where permissions allow; a Stop hook
  was declined 2026-09-19 on cost and reopened by Rjae 2026-10-29 ("I am not opposed to adding hooks (e.g., Stop)
  if it provides the best solution").
- **Telder** (the TL;DR plugin) was considered as a source of judged narrative and discarded by Rjae 2026-10-29
  ("merely an idea and can be discarded").

## 4. Routes considered and declined

| Route | Why not |
| --- | --- |
| A `session.state` capability and a `decisions` table in the backend (attributed, hosted push) | Ties the feature to every message adapter implementing it; ruling 1 chose the folder for adapter neutrality |
| An update-request message to every live session | A model turn per session per report, interrupts, live sessions only; the same ground as the 2026-09-19 Stop ruling |
| Telder's TL;DR forwarded into Brigade | Discarded by the owner; narrative now comes from `brigade note`, published on purpose |
| A direct assistant integration (a Duet connector, a Grok Bot route) | Ruling 6: assistants already read Slack and email |
| The manager's always-on node holding every team's folder | Ruling 2: the team's own active sessions produce; a quiet team has nothing to report |
| Entries signed with the Syncthing device key, verified against the roster's `sync_peer` | Ruling 1: not solving authenticated identity now; `member:` and the session name suffice |
| A lock file or leader for the producer | Rank from roster order plus a marker needs no lock and survives a sleeping producer |

## 5. The design, in travel order

### 5.1 The folder and its files (`internal/harness/foldersync`, `internal/harness/teamfile`, new `internal/harness/state`)

`.brigade.json`'s `sync` member gains an optional `state` string naming one of its listed folders, as listed, say
`"sync": {"folders": [".brigade/state"], "state": ".brigade/state"}`. Without it, nothing here runs; a `state`
that is not a string or names no listed folder makes the member unusable (`state_invalid`, `state_not_listed`),
as every other broken rule does. The team file is the project's committed configuration, not wire protocol. The
hook freezes the folder's absolute path into the by-pid map (`state_folder`) under the same conditions as the
sync members, so `sync: off` shares no state. Inside the folder:

| Path | Writer | Content |
| --- | --- | --- |
| `sessions/<session-id>.json` | the session's own machine, whole-file rewrite | the entry (below) |
| `decisions/<id>.json` | the asking session; the same session or member on close | question, options, default, by when, card, member, session, created, closed, outcome |
| `digests/last.json` | the producing watcher | time, producing session name, counts |
| `reports.json` | the manager, through `brigade report configure` | §5.5 |

The entry: `member`, `session_name`, `session_id`, `repository`, `branch`, `harness`, `started_at`,
`last_prompt_at`, `ended_at`, `end_reason`, `inbound` (accept, hold, refuse, with the "write instead" text),
`start_head`, `head`, `commits[]` (hash, subject, time; the card number parsed from the subject; last 100, by
the checkout's git author — P65-1 deviation: a `git pull` brings teammates' commits into `start_head..HEAD`,
and the author filter keeps them out of this session's list), `doing[]` (text, time; last 50), `notes[]` (text
with the three labels, time; last 20), `updated_at`. No model id, no token count, nothing from the transcript.
The canonical copy lives under `<state dir>/state/entries/<session id>.json` (P65-1: keyed by the session id, which
is unique, rather than by team and repository), and every write projects it into the folder, so a deletion in
the folder is healed on the next hook. One writer per file, so Syncthing produces no conflict copy. Retention (ruling 2): a live entry while it lives, an ended entry
14 days, a decision 30 days after close and indefinitely while open; the writer prunes its own, any watcher
prunes an entry past its term whose machine never returned.

### 5.2 The commands (`internal/harness/commands`, `internal/cli`)

- `brigade note` — stdin, at most 1,000 bytes, inside a session only; appended to `notes[]` and never sent to the
  backend. The skill teaches three labels, `Done:`, `Next:`, `Blocked:`, and the doing line's rules: no secrets,
  no paths, no customer names.
- `brigade decision` — stdin is the question; `--options`, `--default`, `--by`, `--card`; writes
  `decisions/<id>.json` and sends the ask at once through the first gateway of `reports.json`'s precedence to
  the configured manager, in the same labelled shape the digest uses. `brigade decision --close <id>` with the
  outcome on stdin. Refuses when the team has no `reports.json` manager, with one line saying so.
- `brigade report` — `configure` (writes `reports.json`; refuses unless `git check-ignore` says the folder is
  ignored), `show` (prints the digest the producer would send now, the ad hoc report inside a session), `send`
  (produces and sends one now, marker written).
- `brigade doing` additionally appends to `doing[]` in the entry.

Each is a row in the command table, so `make docs-generate` and a persona page naming it (`documentation.md`).

### 5.3 The hooks: the facts layer and the Stop ask (`internal/harness/hook`)

- **session-start**: records `start_head` and the branch — read from git's own files (`.git/HEAD`, the ref,
  `packed-refs`; worktrees followed), no process (P65-1 deviation from `git rev-parse`) — and creates the entry,
  or refreshes it on the continue path. **prompt**: `last_prompt_at`, the registry's name, and only when HEAD
  moved `git log <start_head>..HEAD` by the checkout's `user.email` into `commits[]` (the one process, an
  argument array, the allow-listed environment, git from the hook's own PATH), rewrite. **session-end**:
  `ended_at`, `end_reason`, final commits. A hook's budget is seconds; every git call has a short timeout (1.5 s
  at start, 1 s at a prompt, 0.3 s at the end) and a failure leaves the fields as they were.
- **stop** (new for the Claude Code host; Grok's existing `hook stop` gains the same ask): the gates of ruling 2.

| Gate | Checked how | Asks for |
| --- | --- | --- |
| The turn made a commit and no note followed | HEAD moved since the entry's last commit | a note |
| The doing line is older than ten minutes and the turn ran longer than a minute | stamps in the by-pid map | a refreshed doing line |
| A manager is configured for the team | `reports.json` present with `manager` | a decision, if the model is waiting on one |

  The ask is the hook's `{"decision":"block","reason":"<instruction>"}`; the next Stop event carries the active
  flag and is let through. Further gates: at most one ask per ten minutes (a stamp of the Stop hook's own,
  `<pid>.stop-ask`, which also records whether the last judged Stop asked, so "never twice in a row" is literal;
  P65-3), the same commit never asked twice, none in a quiet session, none where the permission rules would turn
  `brigade doing` into a dialog (the reminder's gate, session-doing.md §5.4), none when the plugin option `status`
  is `off`. The doing-line and decision gates open only after a turn longer than a minute. `plugin/hooks/hooks.json`
  gains the `Stop` entry in exec form, timeout 5; `make plugin-check` sees one more exec-form hook.
- **Harness parity**: where a harness has no Stop hook (Codex, Muse), the prompt hook carries the same ask at
  the next prompt. Codex's three pinned hook commands are unchanged.

### 5.4 The producer (`internal/harness/watch`)

Every 20 s (P65-4: the watcher's own tick over local files, not the sync goroutine's roster poll — the roster is
read once per due digest, when the rank is needed, so the producer adds no egress to a quiet team), a watcher
whose session is attached to a state folder with a manager in `reports.json` asks: has `every` elapsed since
`digests/last.json` (or, for a daily digest, has `at` passed in `tz` with no digest today), and has any entry or
decision changed since it. If both, it waits `rank × 20 s`, rank being the session's position among the
repository's online sessions ordered by registration time, the oldest first (P65-4 deviation from "roster
order": a listing that puts the freshest heartbeat first moves between two ticks, and two sessions could each
see rank 0), re-reads the marker, and produces only if nobody did. It writes the marker after sending. A producer whose machine sleeps leaves no marker; the next rank takes over on its
next poll. Duplicates need two decisions inside Syncthing's propagation gap and cost one extra DM.

The digest reads the folder and the roster only, never git (entries carry their commits). Sections in order,
each omitted when empty, caps keeping the body under 16 KiB (ruling 3):

| Section | Line | Cap |
| --- | --- | --- |
| Header | team, repository, period, producing session | one line |
| Decisions open | age, who asked, question, options, default, by when, card | all, oldest first |
| Decisions closed in the period | who asked, outcome, who acted | all |
| Shipped | per card: number, commit subjects, members and sessions; no-card commits last | 40 commit lines |
| Blocked | member, session, latest `Blocked:` text, age | all |
| Active now | member, session name, doing line, latest note, last seen | 30 |
| Ended in the period | member, session name, final note, commits made | 30 |
| Held or quiet | member, session name, where to write instead | all |
| Roster only | sessions on the roster with no entry | one line |

Notes are cut at 200 characters with a mark; a capped section ends with how many more. Plain text, a stable
label at the start of each line. The summary carries the counts: decisions open, commits, sessions active.

### 5.5 Delivery and settings

`reports.json` (one per repository, written by `brigade report configure`):

```json
{"version": 1, "manager": "Rjae", "recipients": ["@rjae", "<address>"], "every": "24h", "at": "08:00", "tz": "America/Los_Angeles"}
```

`manager` is a roster label. Recipients are typed by shape: a Slack handle or channel goes through the Slack
gateway; an address goes through the Slack gateway too when the team has one (it finds the person by their
email, as `/brigade:slack <address>` does) and through the mail gateway otherwise (P65-9 deviation, owner
2026-10-29: a manager's roster label is an address by default, and 0.43.0's "an address means mail" sent the
proof's asks by mail with the Slack gateway on the roster); the producer walks the list and sends to the first a
gateway on the roster can carry (found by its harness id), with the body's first line the gateway's existing
`to:` convention, so no gateway changes. Without any gateway, a plain Brigade message to the manager's best session and a roster
notice. The address rule (ruling 3): a Slack handle is fine in the folder; an address is written only after
`git check-ignore` confirms the folder is ignored, and nowhere else.

### 5.6 Skills and documentation

- `team-messaging` (all four packages): the Decision shape, the note labels, when to run each, and the privacy
  rules in the doing line's words. The `sessions` skill: the digest exists and `brigade report show` is the ad
  hoc report.
- Pages: `docs/team-member/use/<notes-and-decisions>.md` (named from its topic root's `Details:` line),
  `docs/team-admin/configure/<manager-digest>.md` (the folder, `reports.json`, the address rule),
  `options.md` for `status`, `commands.md` and `make-targets.md` regenerated, security.md: a section on what
  leaves the machine (entries to every teammate's machine; the digest and decisions to Slack or email and from
  there to whatever assistant reads them), `CHANGELOG.md`.

## 6. Rows (the P65 series of the execution log)

| Row | Work | State |
| --- | --- | --- |
| P65-1 | The state folder, the entry, the facts layer in the three hooks, retention | done 2026-10-29 |
| P65-2 | `brigade note`, `brigade decision`, `brigade doing` history | done 2026-10-29 |
| P65-3 | The Stop hook for the Claude Code package and the ask in Grok's; the `status` option; prompt-hook parity | done 2026-10-29 |
| P65-4 | The producer in the watcher: period, rank, marker, digest assembly | done 2026-10-29 |
| P65-5 | `brigade report configure|show|send`, `reports.json`, delivery precedence, the address rule | done 2026-10-29 |
| P65-6 | Skills, pages, security.md, changelog | done 2026-10-29 |
| P65-7 | Proof (§8) and release | E14 done 2026-10-29; release 0.43.0 |
| P65-9 | An address recipient through the Slack gateway when the team has one | done 2026-10-29; release 0.43.1 |

## 7. Owner rulings, 2026-10-29

1. **Carrier: folder sync.** "I am leaning towards Folder Sync carrier, because Supabase is an adapter and nothing
   (theoretically) stops an organization from creating their own adapter." No authenticated identity now:
   "`member: alice` is fine, even better would include the session name."
2. **No manager node.** The team's own active sessions produce; "if there are not active nodes then it's unlikely
   there is anything to update the manager about."
3. **The remaining cases** (Syncthing on every contributing machine, per-repository folders, deletions
   propagate) understood and accepted.
4. **Manager responses: nothing to build.** "A manager will respond to report items however they wish, and as
   sessions take action on the manager's responses they will effect and inform the next report."
5. **Existing mechanisms**, Slack before email.
6. **No direct assistant integration**; assistants read Slack and email.
7. **The sender**: the elected watcher on the period for the digest, the asking session at once for a decision,
   through Slack then email, settings in the folder. Confirmed.
8. **The state entry and the Stop hook** as §5.1 and §5.3. Confirmed.
9. **The digest and `reports.json`** as §5.4 and §5.5, with the address rule. Confirmed.

Earlier rulings this plan rests on: 2026-09-19 (doing line, reminder cadence, permission gate), 2026-09-20 (every
skill reachable by the model), 2026-09-08 (secrets never under the project directory), 2026-10-27 (card 148).

## 8. Proof before release

Two machines, or two engines on one as folder-sync.md §5 did, one repository with a state folder:

1. Entries appear on both machines within seconds of a prompt; a doing-line change shows in `doing[]`.
2. A commit followed by a Stop makes the model publish a note once; a one-line answer makes no ask; a quiet
   session gets none; the active flag stops a second ask.
3. `brigade decision` writes the file and the DM arrives in Slack with the labelled shape; `--close` records
   the outcome and the next digest lists it under closed.
4. With `every` short, exactly one digest is produced by the lower-ranked session, the marker propagates, and
   stopping that session hands production to the other.
5. `sync: off` on one machine makes its session roster-only in the digest.
6. `brigade report configure` with an address refuses in a checkout whose folder git does not ignore.
7. The digest body stays under 16 KiB with the caps hit.

## 9. Risks, and how each degrades

- **A duplicate digest** when two watchers decide inside the propagation gap: one extra DM, accepted.
- **The folder deleted by a teammate's machine**: every writer's next hook rewrites its entry from the canonical
  copy; the marker's loss produces one early digest.
- **A machine without Syncthing**: roster only, said so in the digest.
- **Stop-ask cost**: the gates in §5.3; `status: off` for a member who wants none.
- **An entry forged by a teammate**: accepted under ruling 1, the same trust a synced plans folder has today.
- **A quiet team with an open decision**: no reminder from Brigade; the assistant's memory of the last digest
  carries it.
- **Slack's limits on what an assistant reads**: the digest is a DM to the manager with the counts in its
  summary; email is the fallback by precedence.

## 10. Out of scope, on purpose

An update request to live sessions (`--ask`); signed entries; a backend state capability; neutral rendering of
the digest's sender in the gateway; mail addressed to a member by label; a cross-team aggregator (the assistant
aggregates); Telder.
