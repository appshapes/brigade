# Feature ideas for the existing installations — research, 2026-09-24

Trello card 34 ("Research feature enhancements"). Status: **research only — nothing built, nothing carded beyond
34, nothing committed by the session that wrote this.** Owner: Rjae. Constraint from the ask: small or medium
features that aid the three existing installations; no new adapter.

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
