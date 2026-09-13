# E6 — one team, many repositories

Date: 2026-09-11 · Ticket 15 · Row P11-1 · Status: **closed; the answer is "works as-is, docs only"**
Brief: `.context/plans/multi-repo-team.md` (chartered 2026-09-10, corrected at `d94bfe4`)
Driver: `.ignored/e6-multi-repo/run-phase-a.sh` (scratch, not committed). **The fs-rig half is not reproducible
from the tree as it stands:** the driver also needs a hand-written `adapters.json` registering the fs adapter by
name (see the finding at the end of 3.1), so re-running it means re-doing that step by hand. Committing a driver
under `scripts/experiments/` was not done because this study ships nothing; if the rig is wanted as a standing
fixture, that is its own row.

**One Brigade team already spans several repositories. Nothing in the code prevents it, nothing needed changing to
make it work, and both halves were run rather than reasoned about — the fs-adapter rig first, then the live
`brigade` team.** What is missing is a few sentences of documentation. On identification the answer is partial: an
unrenamed session's name does carry its checkout, but as `<basename>-<suffix>` and because **Claude Code** derives
it that way — not through Brigade's own fallback, which never fires in a real session — so a `/rename` drops it,
and the member that survives a rename, `workspace_label`, is `--json`-only and never in the frame.

Every path below is written `<repo>/…`; team, session and principal references are truncated to eight characters.

## Environment

| Component | Version |
| --- | --- |
| brigade (fs rig) | 0.4.1-dev (`bin/brigade`, `bin/brigade-adapter-fs`) |
| brigade (live team) | 0.5.2 (the installed plugin binary) |
| Backend (live) | Supabase, the `brigade` team |
| macOS | 25.6.0 (darwin/arm64) |

## Answers, one line each

| § | Question | Answer |
| --- | --- | --- |
| 3.1 | Two repositories, one team, fs rig | **Works.** Secret-free join in the second checkout; both sessions on one listing; delivery both ways; two pins, one team. |
| 3.2 | The same on the live team | **Works.** Confirmed against the real `brigade` team and torn down to a byte-identical config. |
| 3.3 | Telling repositories apart | **Solved for the unrenamed case only, and not by Brigade.** Claude Code derives the session name as `<basename>-<suffix>`; Brigade's own `basename(cwd)` fallback is last of three and never fires in a real session. A `/rename` drops the checkout. `workspace_label` survives a rename but is opt-in, `--json`-only, and never in the frame. |
| 3.4 | The join affordance | **Hand-copying is enough.** Measured friction: one `cp`, one `brigade team join`, no secret. A verb would save one `cp`. |
| 3.5 | What breaks | **Nothing breaks.** Four findings below, none blocking; the sharpest is that there is no checkout-scoped undo verb. |
| 3.6 | The workaround, compared | Native cross-session messaging is same-machine, not durable, and not addressed by team — a bridge, not a substitute. |

## Recommendation: works as-is, docs only

No code change, no new verb. The mechanism is complete; the documentation describes a narrower world than the code
implements. Four places say so; the third of them already concedes half of it, and the fourth is the second
placement of the same consequence:

1. **`docs/setup.md:99-103`, "The project owns the team".** It reads "A Brigade team belongs to a **project**: one
   file, `.brigade.json`, committed at the repository's top level, names the team every session in that checkout
   talks to." True, but it reads as one-team-one-repository. Add: *the same `.brigade.json`, committed in several
   repositories, makes them one team — the team key is derived from the adapter, URL and team reference, and
   carries no repository (`internal/harness/teamstore/teamstore.go:39-42`). Each checkout is joined once, and on a
   machine that already holds the credential the join needs no secret.*
2. **`plugin/README.md:132-133.`** It currently reads "A second checkout of the same project needs `team join` once
   too, but no secret. Several projects means several `.brigade.json` files: join each once, then `cd` between
   them — nothing is shared or switched." This is the closest existing sentence and it points the wrong way: it
   covers two clones of *one* repository, and it tells a reader that several projects mean several *teams*. Extend
   it to say several *different* repositories may carry the *same* file and be one team.
3. **`docs/security.md:157.`** The concession — "a session can now pin a second checkout to a team this machine
   already holds a credential for" — is exactly the mechanism this study exercised, so it needs no correction, only
   the consequence stated: **`team reset`, `team revoke-credentials` and `team leave` act on the credential or
   membership every checkout on the machine shares**, so there is no way to detach one checkout with a `team` verb.
4. **`docs/setup.md:336-343`, "Two commands end the credential".** The same consequence belongs here too, and this
   is the more important of the two placements: this is where someone who wants to detach *one* checkout actually
   looks, and today the section tells them only that both commands revoke at the backend, without saying that
   "the backend" means every checkout on the machine.

`brigade team list` needs nothing: it already prints `checkouts:` as a plural, one line per team.

## 3.1 Two repositories, one team, on the fs-adapter rig

Two throwaway `git init` repositories under one config store (one machine, one credential). `repo-a` created the
team; `repo-a/.brigade.json` was copied into `repo-b` byte-identically (`diff` clean); `repo-b` joined.

**The join, in `repo-b`, with no `--secret-file` and no secret anywhere:**

```
re-consent to team "e6-multi-repo" (f7e011fd…) at 127.0.0.1:1 via adapter "fs" — consented by this invocation
re-consented team "e6-multi-repo" (f7e011fd…); this session attaches at your next prompt
```

This is the re-consent path, not a join: `joinWithFile` finds a binding whose `team_ref` matches the file's and
calls `reconsent` instead of `firstJoin` (`internal/harness/commands/teamsetup.go:318-335`). No `team join` call
reaches the adapter. The shipped test asserts the same thing —
`internal/harness/commands/insession_test.go:301`, `TestTeamJoinInSessionSecondCheckoutNeedsNoSecret`, which
asserts both the missing secret and the absent adapter verb. **The live run and the test agree.**

**`projects.json` after: two pins, one team.**

```json
{"version":1,"projects":{
  "<scratch>/repo-a":{"adapter":"fs","url":"http://127.0.0.1:1","publishable_key":"placeholder",
                      "team_ref":"f7e011fd…","consented_at":"2026-09-10T16:42:19Z"},
  "<scratch>/repo-b":{"adapter":"fs","url":"http://127.0.0.1:1","publishable_key":"placeholder",
                      "team_ref":"f7e011fd…","consented_at":"2026-09-10T16:42:20Z"}}}
```

**Both sessions, one listing, one principal** (read from `repo-a`; the listing from `repo-b` is identical except
for which row is marked `(this session)`):

```
5e435379…  repo-a  alice@e6.invalid (unverified)  idle  inbound=accept  principal=7851fa6a…  (this session)
ac522a9a…  repo-b  alice@e6.invalid (unverified)  idle  inbound=accept  principal=7851fa6a…
```

**Delivery both ways:** `repo-a → repo-b` and `repo-b → repo-a` both answered `accepted: message <id> … durably
stored by the adapter`. `brigade inbox` reported `no held messages` at both ends, which is correct rather than a
miss: both sessions are `inbound: accept`, and the held queue is the `hold` policy's.

**Two methodology notes, so the transcript is not read as more than it is.** Every command ran in *in-session*
mode (`CLAUDE_PID` set) rather than at a TTY, because the driver has no terminal and because a Claude Code session
is what runs these commands in the real case; `SessionStart` had to be re-fired after the join, since a hook that
runs before any pin exists finds nothing to attach to and says so.

**A finding that is not about multi-repo but blocked the rig for twenty minutes.** `team create --adapter <path>`
accepts an absolute path and resolves it (`config.ResolveAdapter` → `parseSpec` takes a path, an argv array or a
registered name), but it writes that raw path into the committed `.brigade.json`, and `team join` then refuses the
file: *"the team file's adapter member must be a lowercase adapter name, never a command or path."* The registry
`adapters.json` is the intended mechanism, and `config.RegisterAdapter`'s own doc comment names its CLI writer as
`profile init --adapter` — **a command this build does not have** (`brigade profile` answers `unknown command`;
P7-6 rebuilt these paths and the comment was not updated). The rig proceeded by writing `adapters.json` by hand.
Not a multi-repo matter; worth a row of its own if a non-bundled adapter is ever meant to be used from the CLI.

## 3.2 The same, on the live `brigade` team

A throwaway `git init` checkout outside the repository tree, the **real** `<repo>/.brigade.json` copied into it
unchanged (`diff` clean), joined with the real credential store.

```
re-consent to team "brigade" (0a465da2…) at <project>.supabase.co — consented by this invocation
re-consented team "brigade" (0a465da2…); this session is already attached to it
```

No secret, no prompt, exit 0. The closing clause differs from the fs rig's ("already attached" rather than
"attaches at your next prompt") because the session running the command was already attached from the primary
checkout.

- **`projects.json`:** two pins, same `team_ref`; `diff` against the pre-run copy showed **only** the added entry —
  the existing pin was untouched, to the byte.
- **Credential store:** still exactly one directory, `9fb76ed0…`. No new credential, no new principal — the second
  checkout shares the first's.
- **Sessions:** the checkout's session registered as `live-team-checkout` (`459e5627…`) in team `brigade`, on the
  same `brigade sessions` listing as the primary checkout's session, same `principal=b49eac08…`, and the listing
  was identical read from either checkout.
- **Delivery both ways:** `accepted` in both directions between the two checkouts' sessions. No teammate's session
  was used as a receiver (brief §5).

**Teardown, local only.** The watcher and the stand-in process were killed, the directory deleted, and the pin
removed from `projects.json` with a read-modify-write that preserved mode 0600 and every other entry. The result
`diff`s clean against the pre-run copy. **No `team` verb was run** — see 3.5 (a).

## 3.3 Telling repositories apart

The brief expected `workspace_label` to be the answer. It is available, but it is not the one a reader reaches
first — and the default carries the checkout only until someone renames the session.

- **The session name carries the checkout by default, and a rename drops it.** The name is resolved as
  `entry.Name` (the Claude Code registry name) → `session_title` → `basename(cwd)`, **first non-empty wins**
  (`internal/harness/hook/hook.go:666`), so the checkout's directory name is the **last** resort, not a property
  of the checkout. When the first two are empty the basename does show — `repo-a`/`repo-b` on the rig,
  `live-team-checkout` on the live team — and it appears both in the second column of `brigade sessions` and in
  the frame's **`from-name`**, so a receiver can then tell "the back-end session" from "the front-end session"
  **in the message itself**.
- **In a real Claude Code session Brigade's fallback never fires, and that is the important part.** `entry.Name` is
  always populated, so the third candidate is effectively dead code outside a synthetic rig. Measured across this
  machine's session registry: **8 entries with `nameSource: derived`** (never renamed) are every one of them
  `<basename>-<suffix>` — `brigade-b3`, `brigade-c2`, `brigade-80`, `ifthen-pipeline-data-16` — and **15 with
  `nameSource: user`** are typically unrelated to their checkout, such as `code-graph-main` in a checkout whose
  basename is `ifthen-code-graph`. So a repository is legible in the name because **Claude Code derives its own
  session name from the directory and appends a disambiguating suffix**, not because of anything Brigade does. Two
  consequences worth stating plainly: the name carries `<basename>-<suffix>`, never the bare basename, so it is
  matched by prefix and not by equality; and anything built on "the session name identifies the checkout" rests on
  a Claude Code naming convention Brigade neither sets nor controls, plus nobody having run `/rename`.
- **A rename drops it.** `18-support-multi-repo-teams`, on this team's roster while this was written, is a session
  in a `brigade` checkout whose name contains no basename at all. For ThinkTech that is the awkward case rather
  than the rare one: the sessions a sender most needs to tell apart are exactly the ones a person is most likely
  to have renamed after what they are working on.
- **`workspace_label` is opt-in, `--json`-only.** With `share_workspace_label` and `workspace_label` set, the
  value reaches the wire and `brigade sessions --json` shows it. On the live team only the checkout that set it
  carried one; every other session reported `<absent>`. It appears in **no** human-readable output — not
  `sessions`, not `whoami`. Scoped precisely: within `internal/harness/commands/` the **only** use is the `--json`
  sanitiser (`format.go:133-135`), and the human renderer builds its line from id, name, label, state, inbound,
  principal and optionally model/context — no workspace label (`sessions.go:96-103`) — while `whoami` references
  it nowhere. The label is of course plumbed elsewhere, which is how it reaches the wire at all: the option pair
  and its gate (`config/options.go:19-20,127-132,185-191`), the registration that carries it
  (`hook/start.go:154-156`), and both adapters (`adapters/supabase/session.go:114`, `adapters/fs/session.go:111`,
  `adapters/fs/store.go:105`).
- **`from-label` is the human label, always.** `frame.go:352` writes `label(m.Sender.HumanLabel)`; there is no
  branch for a workspace label. Confirmed on a real delivered frame: `from-name="live-team-checkout"`,
  `from-label="(unverified)"` with no value, and no workspace label anywhere in the tag line. The frame's
  attribute list is fixed (U-04, `frame.go:70-79`), so this is a reading, not a change.

**So identification is a docs matter, not a wire matter** — the data is already on the wire and nothing needs
building to make one team span two repositories. Whether the human-readable `sessions` line should also show
`workspace_label` is **left open** rather than recommended against: the session name covers the default case, and
`workspace_label` is what survives a rename, so for a team whose sessions are routinely renamed the `--json`-only
placement is the weak point. That is a small CLI change and a separate row if Rjae wants it.

*Caveat on the frame evidence:* the synthetic session's watcher inherited the driving session's messaging socket
(the driver used `env`, not `env -i`), so both test messages were injected into the driving session. That is an
artifact of the harness used here, not product behaviour; the frames themselves are real and unmodified.

## 3.4 The join affordance

**Hand-copying is enough; a verb is not earned.** Measured friction for a second repository, end to end:

```sh
cp ../other-repo/.brigade.json .      # or `git add` it, once, in that repository
brigade team join                     # no secret, no flags, exit 0
```

One copy and one command, and the copy is a one-off per repository because the file is committed — the second
repository's collaborators get it from `git`, exactly as the first's do. A `team link <path>` verb would remove
one `cp` and add a command, a refusal surface and a documentation entry. `team create --into <other checkout>`
would be worse: it would write a second file from a machine that happens to have both checkouts, which is the
one thing the committed-file model is designed to avoid.

The friction that *is* real is not the copy — it is that nothing tells a reader this is allowed. That is the
documentation change above.

## 3.5 What breaks

**(a) There is no checkout-scoped undo verb — the sharpest finding.** `team reset`, `team revoke-credentials` and
`team leave` all act at the backend on the credential or membership that every checkout on the machine shares.
`docs/setup.md:336-343` says so for the first two, in its own words: *"Both revoke the credential family at the
backend, not just locally."* `team leave` ends membership (reversibly, `docs/setup.md:367`). None of the three
detaches one checkout. The only checkout-scoped undo is removing the pin from `projects.json` by hand, or
deleting the checkout and leaving the pin. **This was found before running the live measurement, and the brief's
own §6 cleanup instruction — `brigade team reset` "in that checkout only" — was wrong and would have ended the
real member's access; it was corrected at `d94bfe4` before anything ran.**

**(b) Orphan pins are never pruned.** `PruneOrphans` (`internal/harness/teamstore/write/write.go:173`) prunes
stale `tmp-*` credential directories by age; nothing prunes a pin whose checkout no longer exists. Harmless:
`LookupPin` is keyed by the checkout's canonical path, so an orphan is never consulted. It accumulates silently,
which is worth one sentence somewhere rather than a feature.

**(c) The drift check is per-checkout and correct for this case.** `hook/start.go:298-312` looks the pin up by
the canonical directory and compares it against *that* checkout's file. Two checkouts of one team each hold their
own matching pin, so neither can drift the other. Nothing to change.

**(d) The proof scripts assume one checkout per store, but do not break.** `scripts/proof.sh:600-621` writes a
per-checkout pin into each persona's own config dir — one checkout per *persona*, which is a different axis from
one checkout per *team*. No multi-repo assumption is violated; multi-repo is simply not covered. A coverage gap,
not a defect.

## 3.6 The workaround, compared

Rjae's fallback — Claude Code's native cross-session messaging, bridging a Brigade session to a session in the
adjacent repository — works, and it is strictly weaker in three ways that matter for the ThinkTech case. It is
**same-machine only**: it cannot reach a teammate's checkout of `thinktech-api` on their laptop, which is the
whole point of a team. It is **not durable**: there is no backend, so a message to a session that is not running
is not stored and delivered later. And it is **not addressed by team**: the sender addresses a local session, not
a member, so there is no roster, no principal, and no `(unverified)` labelling of who sent what. It is a bridge
between two sessions on one desk; the measured behaviour above is a team. Since the team case works as-is, the
bridge is not needed.

## What was run, and what was not

| Item | Status |
| --- | --- |
| 3.1 fs-adapter rig, both directions | Run, green |
| 3.2 live `brigade` team, both directions, clean teardown | Run, green |
| 3.3 `workspace_label`, `sessions`, `whoami`, a real frame | Run, green |
| 3.4 join friction | Measured from 3.1 and 3.2 |
| 3.5 code paths | Read, four findings |
| 3.6 workaround | Compared, not run (it needs two live Claude Code sessions on one machine and proves nothing this study turns on) |

Nothing was run against ThinkTech's repositories, no teammate's session received a test message, and no second
`.brigade.json` was added to this repository.
