# In-session `brigade team create|join|rotate-secret` — P7-11

Brief, 2026-09-08. File:line citations are to the tree BEFORE this change (commit f53b7c7) unless marked
otherwise. Ticket 15 (all commits on `master`, messages `15: <Imperative summary>`, `make push message="15: …"`,
merges only, never rebase). Owner: Rjae. Tier: Fable author + one adversarial verifier (the log's rule for security
logic and negative security tests). No wire-protocol change: protocol v1 stays frozen; every change is harness-side.

**The change in one paragraph.** The three `brigade team` verbs that handle the join secret — `create`, `join`,
`rotate-secret` — stop refusing inside a Claude Code session. The refusal (`RefusalInSession`, plan 6.4, shipped in
P3-3 on 2026-09-03) existed so the secret would never pass through the chat. Since P5-2 (`rotate-secret`) and P7-5
(`create`, owner ruling 1 of 2026-09-06) both producers write the secret to a 0600 `--secret-file` and print nothing
secret; this brief gives `join` the symmetric consumer — `--secret-file <path>` — so the secret never passes through
the chat on any of the three paths, and the refusal has nothing left to protect. The point of the change is the
member's first command: inside a session `brigade` is already on `PATH`, so `!brigade team join --secret-file <path>`
needs no `<plugin>/bin/…` path that today no new member can find (the documented discovery, `brigade whoami`'s
`terminal:` line, works only after joining — `whoami.go:52-56` needs the by-pid map, which `start.go:258` never writes
for an unjoined session). `revoke-member` and `transfer` keep their own refusal (rule 15; owner ruling 2 below), and
`inbox release` is untouched (D18).

---

## 1. Owner rulings (2026-09-08, in session)

1. **Lift the in-session refusal for `create`, `join` and `rotate-secret`.** "We should have done this when we switched
   to reading the team key from a file."
2. **`revoke-member` and `transfer` stay terminal-only.** Their refusal answers rule 15 (a session reading untrusted
   teammate text must not be talked into a destructive administrative act), which `--secret-file` does not touch.
   Recorded against `RefusalAdminInSession` (`commands.go:130`) and the archive's P5-2 offer (archive :2833) —
   still open, not exercised.
3. **The target shape is `!brigade team join …` from inside the session.** Measured for the Bash tool: `CLAUDE_PID`
   set, stdin `/dev/null`, stdout and stderr pipes into the chat, `brigade` on `PATH`. Claude Code documents the `!`
   prefix as running the command in the session's shell; it was NOT separately measured here (in particular whether
   a `!` command raises a permission dialog — the docs therefore call the typed command itself the consent and
   mention the dialog only for the model-run form). The docs show the `!` form because it is what a human reaches
   for.

4. **`--secret-file` is read plainly** (ruled after the landing commit, 2026-09-08): Brigade checks that the file is
   outside the repository — the repository's own rule, already on `create` — and nothing about its mode or owner.
   The administrator's `team create` wrote the file, members save the copy they were sent wherever they like, and
   `!brigade team join --secret-file <path>` reads it. The 0600/owner check the landing commit carried came from
   reusing the store reader, not from any requirement on the harness, and is gone with the `umask` line the docs
   had grown around it.

This reverses, for three of six verbs, the 2026-09-06 ruling in `team-ux-v2-proposal.md` ("Survives untouched",
:197; :54, :78) and the P7-5/P7-7 acceptance lines that carried the refusals through 0.2.0 (`team-ux-v2-implementation-plan.md:130`, `:195`). The other three stay under that ruling.

## 2. Ground truth (measured 2026-09-08 in this session, and the map)

- **Stdin in a session is `/dev/null`, stdout and stderr are pipes into the chat** (`[ -t 0 ]` false; `/dev/fd/0` is
  `cr--r--r-- root wheel`; `read -t 2` returns 1 at once). So no no-echo prompt is possible in-session, nothing can
  hang waiting for one, and every byte a command prints is transcript.
- **Plugin options never reach the Bash tool** (`docs/research/plugin-bootstrap-cli.md:19-24`, verified on 2.1.251;
  E0-7 corroborates). An in-session command cannot read `config_dir` from its environment; today the only carrier is
  the by-pid map's `ConfigDir` (`bypid.go:102`), written only for a registered session.
- **The prompt hook already re-registers a session whose map is missing** (`prompt.go:46-49` → `retryConnect` :107-122
  → `connect` → `register` → `WriteByPID` at `start.go:164`), throttled to once per minute by a stamp
  (`hook.go:94`, `:710`). So after an in-session join the *current* session attaches at its next prompt.
- **`--secret-file` is harness-visible only on `create`** (`teamsetup.go:82-107`: mandatory, absolute, outside the
  toplevel via `checkSecretFileOutside` :112-127). `rotate-secret` forwards it to the adapter, which checks
  absolute + existing directory (`supabase/team.go:586-595`) and **nothing about the repository**. `join` accepts
  only `--label` (`parseJoinFlags` :272-281); both adapters' join grammar is `--label`/`--prompt`
  (`supabase/team.go:312-313`, `fs/team.go:196-197`; protocol row `docs/protocol-v1.md:153`).
- **`join` reaches the repo-file path only at a TTY** (`team.go:79`; `teamsetup.go:250-252`), else the scripted stdin
  pass-through (`team.go:89`). Lifting the gate alone would send an in-session join to the pass-through with
  `/dev/null` as its document.
- **Production `Deps` are zero** (`cli/command.go:193-201`): `confirm` reads `inv.In` — in a session, `/dev/null`;
  `readSecretTerminal` calls `term.ReadPassword` on the real stdin — fails on a non-tty.
- **`writePinAndReport` prints the raw `f.TeamName`** (`teamsetup.go:448`), unsanitized — a repo-sourced string that
  in-session lands on the model's stdout (the hook's `notJoinedLine` sanitizes with `attr()`).
- **`docs/security.md:348-349` is already wrong today**: it says `team join` reads the secret "from a file you pass with
  `--secret-file`". This brief makes it true.
- The gate keyed solely on `CLAUDE_PID` and the P7 adversarial review already rated it advisory
  (`team-ux-v2-proposal.md:231-233`): the join secret, not the refusal, is the backstop for any pin rewrite that
  changes `adapter`, `url` or `team_ref` (owner ruling 3, 2026-09-06). That backstop is untouched here.

## 3. Design

### 3.1 The gate

`team.go:63-66` (`case "create", "join", "rotate-secret": if inv.inSession() { return refuseInSession() }`) is deleted.
`RefusalInSession` (`commands.go:121-124`) and `refuseInSession` (`raw.go:168-174`) go with it; `inSessionRefusal`
stays for the admin pair; `refuseReleaseInSession` is separate and untouched. Comments at `team.go:30-41`, `:72-74`,
`raw.go:176-178`, `commands.go:119`, `:125-129` are rewritten to the new split: *administration* is what a session
may not drive; *secrets* travel in files on every path.

### 3.2 `team join --secret-file <path>` (harness-only; no wire change)

- New optional flag beside `--label` in `parseJoinFlags`. The path must be **absolute** (`usage` otherwise, mirroring
  create's :106-107) and must **not resolve inside the discovered toplevel** (`checkSecretFileOutside`, both
  spellings, a `..` component refused outright, and — since a join's file exists — its symlink-resolved location
  checked too). The file is read plainly, following symlinks, up to 64 KiB; the first line, trimmed, is the secret.
  Its mode and owner are never checked (ruling 4).
- The flag replaces the no-echo read, nothing else: `firstJoin` continues with `ParseJoinSecret` → the team-ref check
  before any network (:355-361) → the **captured** `Call` with the existing `{join_secret,…}` stdin document (:366).
  The adapters never see the flag; protocol v1 is untouched.
- Accepted at a TTY too (a human who saved the secret may use it). In a session it is the only secret source.
- The file is never deleted by Brigade; the docs tell the human to delete it once every machine has joined.

### 3.3 Routing and the consent gate without a terminal

- `team.go:79` routes `join` into `teamJoin` when stdin is a terminal **or** `inv.inSession()`. Outside a session a
  non-TTY join stays the scripted stdin pass-through (P7-5 correction 7; `TestTeamJoinNonTTYNeverOpensTheFile` keeps
  its contract). `teamsetup.go:250-252`'s refusal applies only outside a session, and its text drops the dead
  `--profile` remedy.
- In a session there is no y/N prompt: **the invocation is the consent.** In `default` permission mode the Bash
  permission dialog shows the exact argv; in `auto`/`bypassPermissions`/`-p` the user has already accepted that
  commands run unprompted (D18's own reasoning). `confirmGate` in-session prints the same line as a statement —
  `joining team "<name>" (<ref>) at <host>` — sanitized (`SanitizeName`, `sanitizeID`, `hostOf`), so the transcript
  records what was joined. `printDiff` lines are sanitized the same way. The declined path (`declined`) is
  terminal-only by construction.
- Which paths need the secret in a session is unchanged from ruling 3: first join, dead credential, and any pin
  rewrite that changes `adapter`/`url`/`team_ref` require `--secret-file` (a session that omits it gets `usage`
  naming the flag and the file rule, with zero spawns); the secret-free paths — a second checkout of a team this
  machine already holds a credential for, and a publishable-key-only drift — proceed without a prompt. Residual
  risk 4.2 records what that concedes.

### 3.4 Where the store is: the start-facts file

An in-session `create`/`join` must write the credential and the pin to the **same** store the hooks read, or a
persona's `config_dir` option (the two-personas-one-machine dev shape) silently lands in the other persona's store.
The by-pid map carries `ConfigDir` but is written only for a registered session, and `Validate` (`bypid.go:129-146`)
rightly refuses a map without a session id (E0-7: the map decides identity).

- **`sessionmap.Store` gains a start-facts file** `sessions/by-pid/<pid>.start.json` — `{claude_pid, claude_session_id,
  config_dir, plugin_bin, written_at}` — with `WriteStart`/`ReadStart`/`DeleteStart` under the same owner-only,
  no-symlink read rule as the map (`readStrict`). The SessionStart hook writes it in `resolve()` right after
  `ParseOptions` succeeds and **before** `resolveTeam` — so it exists for a joined and an unjoined session alike —
  and every `retryConnect` rewrites it (idempotent). SessionEnd deletes it beside `DeleteByPID`; `/compact` and
  `/clear` leave it (they leave the map too). The hook writes it through `sessionmap` (state dir), not
  `teamstore/write`, so the depguard rule "the hook only reads the team store" holds unchanged: the hook still cannot
  join.
- `dialectTarget` (`teamsetup.go:456`), in a session, takes `config_dir` from the start-facts file. A session with no
  start-facts file refuses with `config` (`not_registered`-shaped: "run /reload-plugins, then join") rather than
  guessing the XDG default — the one outcome this file exists to prevent.
- `rotate-secret` is a pass-through and already runs under the map's `ConfigDir`/`TeamKey` in an attached session
  (`commands.go:317-324`); no change.
- The map itself is untouched: no reader relaxes `Validate`.

### 3.5 Attaching the current session

`writePinAndReport` in a session prints `joined team "<name>" (<ref>); this session attaches at your next prompt` and
**removes the prompt hook's retry stamp** so the very next prompt registers instead of waiting out the minute. The
stamp path helper moves from `hook` (`retryStampPath`, `hook.go:710`) to `config.RegisterRetryStamp(stateDir, pid)`,
which `hook` and `commands` both call — neither package imports the other today and neither starts to. The terminal
line keeps its wording ("sessions in this checkout attach on their next start").

### 3.6 `create` and `rotate-secret` in a session

- `create` already has its non-TTY form (`--name` mandatory when stdin is not a terminal, `teamsetup.go:138-139`;
  `--secret-file` mandatory, absolute, outside the toplevel). Its four output lines (`:234-239`) stay, except line 2
  becomes `wrote .brigade.json at the repository toplevel`. The rule: no path under the checkout on the model's
  stdout; the secret file's own path — the user's argv — is printed so the person knows what to delete. The
  adapter's "secret written to file" warning goes to the adapter log, not the caller (`client.go:162-173`).
- `rotate-secret` gains the **outside-the-repository check at the harness** before it is forwarded: `--secret-file` is
  read from `raw.Rest` without being consumed and checked with `checkSecretFileOutside` against the discovered
  toplevel (outside any checkout the check is skipped; the adapter's absolute-path rule still applies). Its
  pass-through envelope carries `{team_ref, team_name, secret_version, rotated_at, secret_file}` — no secret — and
  the adapter's one stderr line names the file, not the secret.

### 3.7 Model-facing lines

Every fixed line that sent the model to a terminal for `join` is rewritten; none carries a path (F1):

- `notJoinedLine` (`hook.go:479-481`): `Brigade: not joined: this project uses team "<name>" — save the join secret to
  a file outside the repository, then run `brigade team join --secret-file <path>` (here or in a terminal).`
- `driftLine` (`:486`): `Brigade: not connected: .brigade.json does not match the team you joined here — run
  `brigade team join` to review the change (a cross-team change needs --secret-file).`
- `notConnected` (`:442`), `notConnectedFor` (`:453`), `classify.go:94`'s stop notice: `run `brigade team join`` —
  the "in a terminal" qualifier goes.
- `parse.go:23-24` (the `--join-secret` poison remedy): `put the secret in a file outside the repository and run
  brigade team join --secret-file <path>, or supply it on stdin` — `--prompt` is no longer a harness remedy.
- `internal/harness/config/session.go:42` (not registered) is unchanged.

### 3.8 Skills and permissions

- `plugin/skills/team-messaging/SKILL.md`: the four-command surface stays the *unprompted* surface. A fifth sentence
  allows `brigade team join --secret-file <path>` **only when the user asks for it and names the file** — the model
  never asks for the secret, never writes the file, never invents a path. The `unauthenticated` remedy row and the
  "Setup" section are rewritten: the secret goes in a file, never in the chat; the command runs in a session or a
  terminal. `allowed-tools: Bash(brigade:*)` stays (D20); residual 4.3 records the widened grant.
- `plugin/skills/setup/SKILL.md` stays human-facing and tool-less (`manifests_test.go:525-529` unchanged): the primary
  form is `!brigade team …` inside a session, the terminal form and the symlink are the alternative, and
  `${CLAUDE_PLUGIN_ROOT}/bin/brigade` disappears from the member path.
- `docs/security.md` recommends `"ask": ["Bash(brigade team*)"]` for users who want a dialog on the three verbs in
  every mode, with section 5.1's whole-text matching caveat.

### 3.9 Records and rules

- `CLAUDE.md:16-17`: "The join secret is read from stdin, a no-echo prompt, or a `--secret-file` outside the
  repository (whose mode and owner Brigade never checks — owner ruling 4) — never argv, never the chat."
- `CHANGELOG.md`: the next version's entry records the reversal explicitly against the 0.1.0 lines (`:129-133`) and
  the 0.2.0 join entry (`:26`) — written at release prep, per the P7-9 convention; the execution-log row carries the
  obligation until then.
- `v0.1.0-followups.md:8-13` "Not listed here": the open question is now this brief; the walked-back per-session
  join stays walked back.
- `team-ux-v2-proposal.md`: a dated line under "Owner rulings" pointing here.

## 4. Threat argument and residual risks (state in `docs/security.md`, do not fix)

**What the refusal protected and what replaces it.** Threat T5's chat-paste path (`security-threat-model.md:204`,
ADV-5 at `:89`): a secret in the transcript is in front of a model that ADV-4 text can talk into echoing it. On all
three verbs the secret now lives only in a 0600 file named on argv; no stream the transcript captures carries it
(create: captured stdio, result without the secret; rotate-secret: envelope without the secret, one stderr line naming
the file; join: the harness reads the file and hands the adapter the same captured stdin document as before). C-05,
the poison flag and the `brg1.` no-secrets scan are unchanged. The refusal never protected the file: a terminal-side
create leaves the same 0600 file a session's `cat` can read as the user (`docs/security.md:179` already says this of
the hold file).

- **4.1 The model as reader of the file.** A prompt-injected session can read the secret file it was told the path of
  and paste it into a reply. Unchanged in kind from every other file under `$HOME`; the skill forbids pasting
  credential-file contents (`security-threat-model.md:136`), the docs tell the human to delete the file after use.
- **4.2 The model as writer of the pin.** `consented_at` can now be written by a model-driven command. The secret
  backstop still gates every membership-grade rewrite; what a model can do without a secret is pin a second checkout
  to a team this machine already holds, or accept a publishable-key-only drift — presence exposure to a team the user
  is already in, and a key the backend either accepts or does not. Rationale G's sentence "only terminal commands
  write this store" (`team-ux-v2-proposal.md:109,137,150`) is no longer true; leg 1 of "the hook may never join"
  (`:134-138`) is gone and the invariant rests on legs 2 and 3 (depguard + the zero-write not-joined test).
- **4.3 The no-prompt grant widens.** The team-messaging skill's `allowed-tools: Bash(brigade:*)` and any user
  `permissions.allow: Bash(brigade:*)` now cover three more verbs (D20). Measured exposure is small (the skill did not
  load in 98 hostile sessions, `security.md:235`); the `ask` rule in 3.8 is the user-side gate.
- **4.4 A talked-into rotation.** `rotate-secret` is also creator-only administration: a prompt-injected creator
  session that rotates invalidates the outstanding secret (onboarding denial; members untouched). Accepted under
  ruling 1; the `ask` rule covers it.
- **4.5 The sandbox.** Under Claude Code's Bash sandbox the credential directory is not writable (`security.md:374`;
  U-28), so an in-session `create`/`join` fails at the store write with a plain `config` error; the docs say to use
  the terminal form there. `--secret-file` under the sandbox tends to land in `$TMPDIR`.
- **4.6 `PATH`.** A session-run verb executes whichever `brigade` is first on `PATH`; the shadowing check at session
  start (`security.md:423`) now matters for these three too.

## 5. Corrections to the design records this brief adopts (do not paper over)

- `implementation/09-testing.md:170` (row 2's evidence lists the refusal): replaced by the 0600 `--secret-file`
  outside the toplevel on every path + C-05 + the no-secrets scan. `brigade-proof-results.md:44` never rested on it.
- `implementation/10-security.md:21` (T5 mitigation column): the refusal cell is superseded by `--secret-file` on all
  three verbs.
- `implementation/06-plugin.md:456,460` (U-28): create/join are now session-runnable and are the exception to "the
  session-bound commands write nothing outside `$TMPDIR` and the project directory" — they write the store, and
  fail under the sandbox (4.5).
- `implementation/08-phases.md:113` / archive `:1224-1227` (P3-3 DONE, the origin of the refusal): reversed for three
  verbs by ruling 1 above.

## 6. Tests

Delete/move: `team_test.go:100-118` rows `create`/`join`/`rotate-secret` (the admin rows stay as the regression pin
that ruling 2 held); `raw_test.go:99` row and the `:109` inequality; `team.txtar:87-105` (the `:106-113` admin block
stays byte for byte). Add, in-session (`sessionEnv` + a start-facts file in the fixture's state dir):

- join with `--secret-file`: no confirm prompt asked (the `Confirm` seam must not be called), the sanitized statement
  on stdout, binding + pin written **in the start-facts config dir** (not the XDG default), the retry stamp removed,
  the success line names the next prompt; a raw team name with control characters is sanitized on stdout.
- join without `--secret-file` and no binding: `usage` naming the flag, zero spawns, store unchanged.
- `--secret-file` relative / inside the toplevel (literal, via `lnk/..`, via a symlinked file) / missing / a
  directory / larger than 64 KiB: the refusal, zero spawns. A 0644 file joins — every positive test uses one.
- wrong-team secret from a file: `invalid_input`, zero spawns.
- second checkout, no flag: pinned; cross-team drift without the flag: `usage`; with the flag: joined.
- no start-facts file in a session: `config`, zero spawns, nothing written.
- create in a session: writes `.brigade.json`, binding and pin in the start-facts config dir; the four lines, line 2
  without an absolute path; secret file 0600, no secret on stdout/stderr.
- rotate-secret in a session: forwarded with the map's team key; `--secret-file` inside the toplevel refused before any
  spawn.
- sessionmap: start-facts round trip, owner/symlink refusals, delete; hook: written on the joined and the not-joined
  path, before any network, rewritten by `retryConnect`, deleted at session end, left alone on `/compact`.
- hook/watch/cli wording pins updated where a line changed (`resolve_test.go:51`, `hook_test.go:170/210`,
  `prompt_test.go:165`, `start_test.go:469`, `classify_test.go:79-80`, `supervise_test.go:203/287`,
  `hook-session-start.txtar:131/148/165/171`, `hook-prompt.txtar:65/199`).
- `team.txtar`: the in-session block becomes positive — `env CLAUDE_PID=4242` + a seeded start-facts file, join with
  `--secret-file $WORK/join.secret` succeeds through the fs adapter, nothing secret on either stream (the C-05
  pattern), `rotate-secret` reaches the adapter (fs answers its own `usage`), the admin pair still refuse.
- Untouched by construction: conformance C-03/C-04/C-05 (adapter-level), e2e rig, proof and smoke scripts,
  `TestTeamJoinNonTTYNeverOpensTheFile`, `manifests_test.go:525`, `setup_docs_test.go:44-50` (its witnesses stay
  substrings of the new forms).

## 7. Docs

`docs/setup.md` (:4, :93, :96-101, :107, :120-130, :137-138, :150-158 "Terminal use", :304-308), `docs/security.md`
(:135-139 "Six" → three, :179, :210-214 the `ask` rule, :346-351 section 8, :372-377 sandbox, :405-407, :421-425),
`plugin/README.md` (:24-25, :77, :92-98, :110, :180-182, :191-193), both skills (3.8), `docs/adapter-authors.md`
(:2078-2082), `internal/adapters/fs/README.md` (:88-89), `Makefile` (:365-379 plugin-dev comment), `CLAUDE.md`
(:16-17). Experiment reports (`E3-wiring.md`, `E3-interactive.md`, `E4-interactive.md`) are history and keep their
text. The member path everywhere reads:

```
!brigade team join --secret-file ~/brigade-<team>.secret      # the file team create wrote, saved anywhere outside the repo
```

with the terminal form (`brigade team join`, no-echo prompt, after the symlink) as the alternative.

## 8. Acceptance

1. `!brigade team join --secret-file <abs path>` in an unjoined checkout inside a session joins, prints one sanitized
   statement and one success line, no secret on any stream, and the session is attached at the next prompt without
   `/reload-plugins`.
2. The same with a persona `config_dir` option lands in that persona's store (the start-facts path), never the
   default.
3. `!brigade team create --url … --key … --name … --secret-file <abs path outside>` and
   `!brigade team rotate-secret --secret-file <abs path outside>` succeed in a session; a path inside the checkout is
   refused before any spawn on all three verbs.
4. `revoke-member`/`transfer`/`inbox release` refuse in a session exactly as before (byte-identical lines).
5. `make test`, `make lint`, `make plugin-check`, `make schema-check` green; `docs/protocol-v1.md` and the schema
   unchanged; `docs/allowed-deps.txt` zero-diff.
6. No doc, skill, README, Makefile comment or fixed line still says create/join/rotate-secret refuse in a session or
   shows `<plugin>/bin/brigade` / `${CLAUDE_PLUGIN_ROOT}/bin/brigade` on the member path.
7. Adversarial verification (one Fable verifier) finds no path by which a secret reaches stdout, stderr, argv, a log
   or a file under the repository on any of the three verbs, in or out of a session.

## 9. Adversarial verification (2026-09-08, three lenses, all fixed in the landing commit)

- **Secret-leak paths:** no secret reached stdout, stderr, argv, the adapter log, the by-pid map, the start facts
  or any error message on any verb, in or out of a session, across CRLF/second-line/empty/64 KiB/2 MiB files, symlinked
  files and directories, and `--secret-file=VALUE`. One bypass found and fixed: `checkSecretFileOutside` was lexical —
  `<outside>/lnk/../x.secret` with `lnk` pointing into the checkout passed both spellings while the kernel resolved
  inside; a `..` component is now refused outright on all three verbs. Also fixed: a `brg1.`-shaped value given as
  `rotate-secret`'s `--secret-file` would have ridden the child's argv (now the poison refusal); the in-session
  consent line was printed before three refusals (malformed or wrong-team secret, cross-team re-point without the
  flag) — the secret is now checked whole before the line, and an in-session cross-team re-point goes straight to
  the membership-grade path; `team list`, `--team` resolution and the pass-throughs in a not-yet-attached session
  read the XDG default store instead of the start facts (`storeDir`, and `terminalTarget`'s start-facts fallback —
  which also makes `rotate-secret` work before the first prompt after an in-session create). The start facts are
  judged no new trust exposure relative to the by-pid map (same strict reader, pid match; a planted file redirects
  only where a join writes). The secret-free pin of an unpinned checkout by a model (residual 4.2) is confirmed as
  the mechanics say — including a `.brigade.json` the session wrote itself — and stays as designed.
- **Test strength (source mutations):** every requested mutation failed a test except two, both closed: the hook
  recording the XDG default instead of the `config_dir` option in the start facts (acceptance 2 had no test —
  `TestHookStartFactsCarryTheConfigDirOption`), and `ReadStart` without `Validate` (a planted relative `config_dir`
  is now refused on read). Refusal subtests now assert an empty stdout; the failed-registration subtests assert the
  start facts are down before the network; cross-team re-point, dot-dot-through-symlink, symlinked-file, malformed
  secret, create-without-start-facts and rotate-before-first-prompt cases added; the txtar shows `rotate-secret`
  reaching the adapter in a session.
- **Docs vs code:** `xclip -o` read the PRIMARY selection (now `-selection clipboard`); the recommended ask rule
  `Bash(brigade team*)` would also have gated (and under `-p` denied) `brigade team members` (now per-verb rules);
  "attaches at your next prompt" was unconditional but the prompt hook registers only an UNmapped session (the join
  now reads the map and says "already attached" or "stays on team X until /reload-plugins"; docs qualified); the
  `!`-form consent sentence leaned on a permission dialog nobody measured for `!` (dropped; ruling 3 records the
  gap); the three setup copies disagreed on where `leave`/`reset` run (unified); a redirection keeps an existing
  file's mode (noted); the setup skill's "two commands" vs the code's three (fixed); the brief's citations were to
  the pre-change tree (now said so).

**Ruling 4 (same day, after the landing commit).** The verifier's symlinked-file and group-readable rows were closed
by the strict reader; ruling 4 replaces that with the location rule alone — the file's real path (symlinks
resolved) must be outside the repository — and the group-readable case becomes the positive control. Docs, skills,
`CLAUDE.md` and the txtar (dave joins through a plain `cp` of the file) follow.
