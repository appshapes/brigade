# Feature ideas for the existing installations — research, 2026-09-24

Trello card 34 ("Research feature enhancements"). Status: **research only — nothing built, nothing carded beyond
34, nothing committed by the session that wrote this.** Owner: Rjae. Constraint from the ask: small or medium
features that aid the three existing installations; no new adapter.

**Since then:** items 1 to 5 and 9 were built on 2026-09-27 (§7; execution log P23-1..P23-6), to ship in one
release with the items that follow them. **The frame measurement of §4.2 is Trello card 40** (2026-09-27): Rjae
agreed it is worth doing, chose the spot-check size, and has no time for it now. **The release does not wait
for it** (her ruling, the same day). A plain re-run of the sweeps would measure nothing, because no model in
them reads a reply frame; the card says what to change.

**Method.** One workflow of seven agents, sized to the ask (memory rule: Opus for fan-out, Fable for judgement):
four Opus ideators, one lens each (in-session operator experience; coordination between sessions; folder-sync
follow-ups; reliability and administration) — 30 raw ideas, each with a novelty check against the tree; one
synthesizer (Fable) merged, scored and ranked them to 14 and dropped 10 with reasons; two critics (Fable) then
checked the 14 — one for "already exists / already queued / honest size", one for the invariants and the
security model — and each named what the shortlist was missing. The session writing this spot-checked the code
claims behind the top ideas (`send.go:44-56`, `supervise.go:71-77`, `frame.go:339-354`, `start.go:573-581`,
`prompt.go:195-213`, `watch.go:803-805`) and corrected one critic (§5, idea 8). Cost: 806k subagent tokens, 227
tool calls, 16 minutes.

Not proposed, because already there or queued: the doing line (25), MODEL/CONTEXT/VERSION columns, member
labels (24), the narrow table (27), migrations from a workflow (30), the staleness fixes (26), the VS Code
rename as NAME (PR #40), multi-repo teams, hold/release, frame levels, `poll_on_prompt`, folder sync (32/33).

## 1. The list, grouped by what it takes to ship

Every item below needs **no protocol-v1 change** (the two sync items add optional members to the sync/1 adapter
contract, which `pid` already set the precedent for). "Small" is about a day or less; "medium" a few days.

### A. Small, no ruling needed — ship in any order

| # | Feature | Lenses that raised it | Touches |
|---|---|---|---|
| 1 | `brigade send` accepts the table's five-character SESSION id and says who got it and whether they can read it now | operator, coordination, ops | `commands/send.go:51-56, :84, :109-114`; `format.go:99` (`shortSessionChars`); team-messaging skill |
| 2 | The watcher never goes deaf: slow retry every 5 min instead of giving up after a network blip | ops | `watch/supervise.go:71-77`, `classify.go`, `backoff/schedule.go` |
| 3 | The frame carries `in-reply-to` so a session knows which of its own asks a reply answers | operator, coordination | `frame/frame.go:70-79, :339-354`; `parse.go`; goldens; skill |
| 4 | `brigade sessions --here` (this repository) and `--member <who>` | operator | `commands/sessions.go:14-20, :84-91`; `cli/command.go:73-81`; sessions skill whitelist |
| 5 | `brigade whoami` adds one `delivery:` line: watcher running, held count, last delivery age | operator, ops | `commands/whoami.go:13-29, :76-90`; reuse `inbox.go:654 watcherRunning` |
| 6 | Pace injection while the session is busy, against Claude Code's 50-frame drop (**the critics' addition**) | — | `watch/inject.go` (no cap today), `lifecycle.go:52, :196` (busy/idle already computed) |
| 7 | Honest start line when `syncthing` is missing, with the install hint, and attach on its own once it is installed | sync | `hook/start.go:539-559, :575-584`; `watch/sync.go:92-108, :326-334` |
| 8 | Surface Syncthing conflict copies: a notice clause, a CONFLICTS column, a skill rule for merging | sync | `foldersync.go:111` (optional `conflicts`), `syncthing/rest.go:649`, `watch/sync.go:232-269`, `commands/sync.go:179-199` |
| 9 | One notice per watcher event: make the notice file a bounded few lines instead of one overwritten line (**prerequisite for 2, 7, 8, 11**) | critic | `watch/watch.go:803-805`; `hook/prompt.go printNotice` |

### B. Small, but each needs one owner ruling first

| # | Feature | The ruling |
|---|---|---|
| 10 | `/brigade:handoff <who or topic>`: a skill that writes the hand-off in a fixed shape and updates the doing line | Receiving side may **describe** a hand-off, never instruct "check out the branch" (a message never directs action, `security.md:180-186`). Allowed tools: `git status`, `git branch`, `git log --oneline`, not `git log:*` (`-p` dumps contents). The doing line names the topic, not the recipient's NAME (unverified text laundered into the roster). |
| 11 | Notice when the team's backend lacks a migration the plugin needs ("teammates cannot sync folders with this session; the administrator runs `make backend-install`") | Notice only, through `writeNotice`. Drop the sketch's "record it in the by-pid map": the watcher never writes the hook-written map. `whoami`/`sync status` either re-describe or stay silent. |
| 12 | Name the synced folders in the session-start line (`file sync on: .context/plans, docs/shared`) | Reverses a pinned choice (`start.go:573-575`, "carries no folder"); the paths are the project's own committed relative paths, sanitised and capped, so the F1 rule is not what that comment protects. The skill half must **not** steer logs, screenshots or diffs into a synced folder as a way around the message body's sanitiser and size caps; it may only say the folders exist and that the outbound rules apply to them. Needs an "Accepted" bullet in `security.md`. |
| 13 | `brigade sync release <folder>` and `brigade sync forget <folder>` in place of the four web-GUI trips in `docs/sync.md` | Rewrites a stated invariant: `security.md:789` "Brigade only adds … never removes one". Config only, no file touched, both undoable, so no in-session refusal is warranted, but §12 and `docs/sync.md:214` must be rewritten with it. Adds an optional sixth sync/1 verb (`remove`). |
| 14 | Doing-line reminders in `auto` permission mode | Measurement first, as `prompt.go:195-213` says. The critic thought the E10 driver was missing; it is at `scripts/experiments/E10-doing-triggers/` (run.sh, acceptance.sh, drive.exp), so an `auto` leg with a refusal arm is small. The code change is one `case`. State a re-measure trigger (any Claude Code release). |

### C. Medium

| # | Feature | Notes |
|---|---|---|
| 15 | My next session in a repository picks up the mail left in my own ended sessions there | Protocol-sanctioned (C-31; `protocol-v1.md:734-735`). **Gate on local liveness** (own by-pid map / watcher pidfile shows the old process dead), never on roster `state: offline`, which also covers a lease-expired session whose watcher is merely cut off — two drainers is what C-19b forbids. Ack through `Client.Ack(oldID)` after injection; the hold path needs a per-message session id in `inbox.go`. Needs an "Accepted" bullet (mail to an ended session is read under the new session's inbound policy) and a frame re-measurement (it adds a Brigade-authored sentence). |
| 16 | `brigade sync status` answers "has Frank's machine got my file, and is he syncing at all": a per-peer completion column and a NOT SYNCING list of same-repo teammates with no sync peer, with their VERSION | Local Syncthing REST only (`/rest/db/completion`); the NOT SYNCING half reuses the roster read `peerLabels` already makes (`commands/sync.go:228-252`). |

## 2. Each item, in the member's words

**1. Send by short id, and know whether they can read it.** `brigade send 3f9a2 <<'EOF' …` works with the five
characters the table shows; an ambiguous suffix is refused with the matching rows. The confirmation says
`accepted: message m-… to "frank-brigade-reviewer" (…3f9a2)` and adds a note when the recipient was offline,
`hold` or `refuse` at the moment of sending. Why: today every send is preceded by `brigade sessions --json` just to
copy one id, and a send to a session that ended an hour ago returns a bare `accepted`
(`message_integration_test.go:268-272`). Sketch: one `ListSessions(ctx, true)` in `Send`; exact match first, then a
suffix of at least five characters; the resolved full id goes into `SendRequest` and the idempotency key; a failed
roster call still sends a full id and adds no note. Only adapter-assigned id suffixes are matched, never names.

**2. The watcher never goes deaf.** Ten failures inside five minutes exit the watcher (`supervise.go:71-77`) and
the only respawn is the next `UserPromptSubmit` (`prompt.go:329-360`), so an idle session parked for a hand-off
stops receiving and shows offline until a person types. Change: when the give-up trips on a transport-shaped
retryable code (`unavailable`, `ready_timeout`, `start_failed`), switch to a fixed jittered five-minute schedule,
write one notice, and write `Brigade: reconnected` when an attempt passes `HealthyAfter`; every other code still
exits as today, and the Claude-liveness check keeps running. Lease revival on reconnect is already the documented
behaviour.

**3. `in-reply-to` on the frame.** The adapter already stamps `reply_to` on the envelope (protocol 4.4.5) and
`frame.Build` drops it. One `writeAttribute` when `m.ReplyTo` is set, `Parse` learns the optional attribute, one
golden each way, one skill sentence. The proof scripts pin the tag line only for non-reply frames, which stay
byte-identical. Re-run `proof-headless` and the E5 sweep afterwards (§4).

**4. `sessions --here` and `--member`.** One team spans several repositories and each person runs about five
sessions, so every routing read loads every row and doing line. `--here` keeps rows whose `workspace_label`
equals this session's (with `share_workspace_label` off it says it cannot filter, never an empty table);
`--member` matches a `principal_ref` prefix of 8+ characters or an exact sanitised label. A note counts the hidden
rows; `--json` carries `filtered_out`. The sessions skill's whitelist lists exactly these forms.

**5. `whoami` delivery line.** `delivery: watcher running (0.11.0); last delivery recorded 12m ago; 0 held`, or
`watcher not running — the next prompt restarts it`. Read-only, local, no path in `--json`. "Did the message get
lost, or is my session not receiving?" is the first question when a hand-off goes quiet, and nothing answers it
in-session today. The medium `brigade doctor` checklist is its natural extension if members still ask.

**6. Pace injection while busy.** `security.md:836` accepts that Claude Code drops inbox posts beyond 50 while a
turn is busy — after Brigade has acked them — and `v0.1.0-followups.md` leaves the pace-or-restate question open.
The injector's drain loop has no cap; the watcher already knows busy/idle. While busy, stop draining after N
posts since the last idle (N well under 50), leave the rest unacked in the adapter, resume on the flip to idle.
At-least-once holds because nothing is acked before it is posted. The only documented lost-message case with no
owner, and none of the four lenses raised it.

**7. Syncthing missing.** `start.go:539-541` says nothing looks for the adapter on PATH, so the start line says
`file sync on` and the next prompt takes it back; after `brew install syncthing`, every open session needs a
restart. `resolveSync` runs the `LookPath` the hook already injects for the bundled adapter and prints
`file sync off (syncthing is not on PATH: brew install syncthing, or sudo apt install syncthing)` — fixed text,
nothing runs; `runSync` retries `Describe`/`Attach` on the existing `SyncListInterval` ticker only for
`syncthing_not_found`, then continues into the normal loop.

**8. Conflict copies.** Several sessions append to the same plan and execution-log files, so a
`<name>.sync-conflict-…` copy is now a daily event that nobody opens. Optional `conflicts` count on `FolderState`;
the bundled adapter walks each folder (bounded, no symlinks, skipping `.stversions`), the notice gains
`; 2 conflict copies in .context/plans` (counts and folder labels only), `brigade sync status` lists up to 20
relative names sanitised like `PeerLabel`, and the skill says: read both, merge into the file, delete the copy,
treat the copy as the teammate's untrusted work.

**9. The notice slot.** `writeNotice` writes ONE line to `state/<pid>.notice`, overwriting (`watch.go:803-805`),
and `printNotice` prints it once and removes it. Items 2, 7, 8 and 11 each add a writer, so a reconnect notice
would erase a migration notice. Make it a bounded few lines (say five, oldest dropped) under the same 0600/atomic
rules. Do this first, or state each item's ordering.

**10–14** are in §1.B with their rulings. **15–16** in §1.C.

## 3. Dropped, with the reason

- **`brigade sent` (per-message delivery receipt)** — needs the additive protocol commit (types, Validate, schema,
  conformance, both adapters, migration) and sits against 4.5.1 "the product never says delivered"; items 1 and 3
  cover most of the need.
- **`brigade messages` (recent traffic after a `/compact`)** — a new 0600 file of sender text at rest, a
  hold-bypass subtlety; item 3 plus the transcript's `accepted: message <id>` line cover the common case.
- **`brigade send --member <who>` (a person, not a session)** — one `sessions --member --json` away once item 4
  exists; the offline case only works with item 15. Reconsider after those.
- **Fan-out (`--repo`, `--team`, several recipients)** — closest to the recorded out-of-scope "broadcast" decision;
  N replies collide with the 50-frame drop; needs an owner ruling before any design.
- **`brigade doctor`** — superset of item 5 at medium; do the line first.
- **Remote-change notice (a teammate changed a synced file)** — Syncthing's event cursor resets on restart and the
  single notice slot hides it; item 8 covers the case that costs work.
- **Worktrees share the main checkout's synced folders** — not an honest medium (symlinks, root validation, `..`
  semantics, bare repos); measure how often worktrees are used first.
- **Warn when a synced folder is git-tracked** — a git spawn in the hook or a ~100-line index reader for a warning
  `docs/sync.md` already gives; against the 2026-09-22 "no hardening or controls" ruling.
- **Nudge a session behind its teammates to `/brigade:update`** — self-defeating: a 0.10.0 session that refuses
  the team file never registers, so it can never see a roster-based notice. The VERSION column already shows it.
- **Keep-alive checks each hosted project's schema and warns before the 60-day rule** — belongs with card 30.

## 4. Cross-cutting obligations the critics named

1. **Notice slot** (item 9) before items 2, 7, 8, 11.
2. **Frame is measured text.** Items 3 and 15 change what a session receives; `security.md:196-204` and E5 pin
   what was measured, and corpus items 27–30 are already listed as unmeasured. One line item: re-run
   `proof-headless` and the E5 default-level sweep after the frame changes land, and fold in 27–30.
3. **"Accepted for this version"** in `docs/security.md` needs a bullet for each item that widens what happens
   without a prompt: 12 (the skill names folders whose files land on every teammate's machine), 13 (Brigade now
   removes folder configuration on an explicit verb), 15 (mail addressed to an ended session is read under the new
   session's inbound policy). No idea's touch list named that section.

## 5. Corrections to the agents' output

- Critic 1 said the E10 driver was absent from the checkout. It is present:
  `scripts/experiments/E10-doing-triggers/{run.sh,acceptance.sh,drive.exp,acceptance.exp,hook.sh}`. Item 14 is
  therefore small, not medium.
- The synthesizer's sketch for item 11 wrote to the by-pid map from the watcher; critic 2 caught it and this list
  drops that mechanism.
- The synthesizer's item 10 (the hand-off skill) told the receiver to check out the branch; critic 2 caught it.

## 6. A possible carding

Five cards would hold it: **routing ergonomics** (1, 3, 4, 5); **watcher resilience** (9, 2, 6); **folder-sync
follow-ups** (7, 8, 12, 13, 16); **hand-off skill and the auto-mode measurement** (10, 14); **mail for ended
sessions** (15, on its own, with 11 alongside as the other backend-facing notice). Or each item as its own card.

## 7. Built

**Item 1, 2026-09-27** (card 34; Rjae: "Go ahead with Item 1"). As sketched in §2, with four differences:

1. **The first line of the confirmation is unchanged.** The sketch put the name inside it
   (`accepted: message m-… to "frank-brigade-reviewer" (…3f9a2)`). `scripts/proof-headless.sh:862` reads the two
   ids off that line, and the recorded streams under `scripts/ci/testdata/` carry it. So the recipient is a
   second line, `recipient: 3f9a2, idle, inbound accept, name "frank-brigade-reviewer" (unverified)`, and each
   reason the message waits is a `waiting:` line after it. The adapter's facts come first on the recipient line
   and the name last, so a name written to look like a state has nothing after it.
2. **The ambiguous refusal lists ids and states, no names.** A name in a list of candidates could forge one.
3. **The roster read has its own 5-second budget** (`RosterTimeout`), not the 20 s of the send.
4. **A short id whose roster read failed says so.** The sketch sent the argument as given and added nothing. A
   short id then comes back `not_found` from the adapter, which reads as "no such session". The message now adds
   that the roster could not be read. "Short" means shorter than the sender's own session id.

An argument the roster does not resolve still goes to the adapter as given: a capped roster can lack a session
the adapter knows.

**Item 2, 2026-09-27** (card 34; Rjae: "Go ahead with Item 2"). As sketched in §2, with these differences:

1. **Which failures go slow is decided by code, not by the sketch's three names.** The sketch listed
   `unavailable`, `ready_timeout` and `start_failed`; the last two are reasons, and both carry the code
   `unavailable` (or `rate_limited`, for a start). The rule is `backoff.Retryable(code)`: `unavailable` and
   `rate_limited` go slow. The one restarted failure that still ends the watcher is a child that exits with a
   status no code owns, a crash, whose code is `internal`.
2. **A slow wait is cut short by the session's activity.** Not in the sketch. The watcher that exited was
   started again by the next prompt, at once. The one that stays is alive, so the prompt hook leaves it alone,
   and a person at the keyboard would wait out the rest of five minutes. The wait's tick reads the registry's
   busy/idle, as the event loop does, and a change of it ends the wait once 30 s of it have passed.
3. **A slow wait applies a release file.** Not in the sketch. The event loop does it on the same tick, and a
   wait of minutes with no loop running would leave `brigade inbox release` unanswered for as long.
4. **The notices are sentences.** `Brigade: this session is not receiving team messages (unavailable). Brigade
   tries again at least every 5 minutes. Messages wait on the server until it reconnects.` and `Brigade: this
   session is receiving team messages again, after about 17 minutes without.` The sketch's `Brigade:
   reconnected` said too little to a reader who never saw the first line.

**The notice slot (item 9) was not done first.** §4 asked for that, or for each item's ordering. The ordering
for item 2: the slot holds one line and the newest wins. The two lines above are about one fact, whether the
session receives, so the later is the true one. A sync summary written between them replaces either, as it
replaces a stop notice today. Item 9 is still the fix for that, and it matters more with every writer added.

**Item 3, 2026-09-27** (card 34; Rjae: "Go ahead with Item 3"). As sketched in §2, with two choices the sketch
left open:

1. **The attribute is last on the tag line**, after `sent-at`. Every frame still begins with the eight
   attributes in their order, so a reader that builds the tag line by position (`proof.sh:865`,
   `proof-headless.sh:1202`, `proof-idle-wake.sh:1586`, `proof-crash-resume.sh:2033`) sees what it saw for every
   frame that answers nothing.
2. **An id that could be no message id is left out, and the message is delivered.** The pipeline's step 1 does
   not bound `reply_to`, and the frame prints ids in full. So `in-reply-to` is capped at the 200 bytes the
   pipeline puts on a `message_id` (a test holds the two equal). Rejecting the message instead would lose an
   answer over a field the receiver can do without.

**The re-measurement §4.2 asks for was not run.** `proof-headless` and the E5 default-level sweep need a
logged-in `claude` outside this session, and the release is a bundle: one run before it covers item 3 and
whatever else changes the frame (item 15). `docs/security.md` and the changelog say the attribute is unmeasured.
What was checked without a model: a reply's frame is the plain frame plus the attribute and nothing else, at
every level; the real fs adapter's reply reaches a socket with it; the Supabase backend's envelope carries
`reply_to` (the schema's `message_envelope`, read, not run).

**Item 4, 2026-09-27** (card 34; Rjae: "Go ahead with Item 4"). As sketched in §2, with these choices the sketch
left open:

1. **In a terminal `--here` is the repository of the working directory**, named as the hook names a session's
   default (`teamfile.RepoName`). The sketch spoke of a session only.
2. **A `--here` that cannot filter shows the roster whole and says why**, in a note and in `--json`'s
   `filter_note`. It is not an error: the reader still gets the roster, with its `REPO` column.
3. **The note counts apart the sessions that share no repository name.** `--here` leaves them out, and they
   may be here.
4. **`--member` also takes the bracketed `MEMBER` cell** (`[9f3c1a20]`), which is what a reader copies for a
   session with no label. A label and a principal are both tried, and a row matching either is shown.
5. **The value of `--member` is never printed.** It came from argv.
6. **`--json` gains `here` and `filter_note` beside `filtered_out`.**
7. **The filters run before the offline sessions are hidden**, so `offline_hidden` counts sessions that
   matched.

**Item 5, 2026-09-27** (card 34; Rjae: "Go ahead with Item 5"). The sketch's three facts, and one it could not
have had:

1. **The line says whether the watcher is connected.** The sketch said `watcher running`. Since item 2 a
   watcher stays alive through an outage, so "running" alone would read as "receiving" in exactly the case the
   line exists for. The pidfile cannot tell the two apart. So the watcher keeps one new file,
   `state/<claude pid>.watch.json` (`internal/harness/watchstate`): a state word (`connecting`, `connected`,
   `retrying`), the time it began, and the watcher's own pid. `whoami` trusts it only beside a live pidfile
   naming the same pid, so a killed watcher's last word and a replaced watcher's are both ignored.
2. **"Last delivery" is the time the seen file was last written.** That file is saved after every injection
   and holds no times of its own.
3. **A pending file that cannot be read leaves the count out** (`held count not readable`). Zero would be a
   guess.
4. **A session with no inbox socket says so**, instead of `watcher not running`: nothing will start one.
5. **`--json` gets a `delivery` member** with times, not ages, and no path, id or file name.

**Item 9, 2026-09-27** (card 34; Rjae: "Go ahead with Item 9"). The sketch said "a bounded few lines (say five,
oldest dropped)". Built as that, with one rule the sketch did not have:

1. **One line per topic, not the last five lines.** The sync summary is rewritten whenever it changes, so five
   plain slots would fill with sync summaries and push out the one line that says the session is not
   receiving. Each writer names a topic (`watcher`, `sync`); a topic's newer line replaces its older one, and
   topics stand side by side. Five is the bound on topics. Items 7, 8 and 11 each pick a topic: 7 belongs to
   `sync`, 8 and 11 want their own.
2. **The hook takes the file by renaming it, then reads it.** The old read-then-remove could remove a line the
   watcher had just written. No lock is held across the two processes, so no lock file is left per session.
   What remains possible is a line of another topic printed twice, when the watcher read the file a moment
   before the hook took it.
3. **The file is JSON with a version**, like the pending and release files. A file that does not parse is read
   as the one plain line an older watcher wrote, so a watcher replaced at a prompt still gets its last word
   printed.

This settles the ordering note under item 2 above: the watcher's two lines and the sync line no longer share a
slot.
