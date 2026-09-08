# Brigade team UX v2 — the project owns the team

Design proposal, 2026-09-06. Ground truth: the drift, mechanics and threat briefs. Zero users; 0.1.0 UX may break. No wire-protocol change anywhere in this design — protocol v1 stays frozen; everything here is client-side configuration and command surface.

**The model in one paragraph.** A committed file in the repo says which team this project talks to. The file is discovery, never authority: cloning it grants nothing, and no session ever acts on it beyond printing one line until a human has run one terminal command in that checkout. The admin runs one Brigade command and one git commit. The member runs one Brigade command with no parameters, ever, per checkout. Sessions then attach automatically, forever, from a user-side pin the repo cannot touch.

---

## 1. The repo file: `.brigade.json` at the project root

**Path.** `.brigade.json`, a single regular file at the repository root, committed.

**Why this path and not the alternatives:**

- **Not `.brigade/team.json`.** A directory is an attractive nuisance: it invites a second file, then state, then caches — exactly what T14 forbids in the project directory. One file with a closed schema is an invariant you can hold; a directory is a surface that grows. It also makes the read discipline one `Lstat` instead of a traversal.
- **Not `.claude/settings.json`.** Claude Code deliberately stopped reading `pluginConfigs` from project settings ("a cloned repository must not be able to supply them", v2.1.207). Brigade must not re-open that class through a side door, and sharing another tool's file makes "unknown field = refuse" unenforceable.
- **A root dotfile** is where humans look in a PR diff — and the team-swap defense (§5) leans on the change being visible in review. It is also trivially inside the path CI's `scripts/ci/no-secrets.sh` scan covers.

**Shape.** Closed schema, six fields, all public by design:

```json
{
  "version": 1,
  "adapter": "supabase",
  "url": "https://<ref>.supabase.co",
  "publishable_key": "sb_publishable_…",
  "team_ref": "t_…",
  "team_name": "ops"
}
```

**Parser rules (every reader — hook, join, admin commands — shares one parser):**

- Opened via `Lstat`; symlinks refused (mirrors the by-pid map reader and U-19). Regular file only; world-writable refused; hard cap 4096 bytes.
- Unknown field ⇒ refuse loudly, naming the field, never echoing its value. This is how `adapter_command`, `profile`, `config_dir`, `team_inbound`, `frame`, `frame_file`, and every path or command stays out — the file can never carry behavior or executables (threat §3).
- `adapter` is a **name, never a command or path**: `^[a-z0-9][a-z0-9_-]{0,31}$`. Resolution of name→executable happens strictly user-side (the user's `adapters.json`, else the bundled adapter when the name is `supabase`). An unknown name means "not connected", never execution. The repo names a dialect; it never names code.
- `url` must be https (loopback http allowed — U-26). `publishable_key` with the `sb_secret_` prefix refused, as today.
- The whole file is scanned for secret-shaped content (`brg1.` pattern). A hit refuses the file **and** tells the human to rotate now (threat §7): a committed secret is leaked, full stop. There is no field the secret could legally occupy.
- `team_name` is untrusted display text: T1 sanitizer plus a 32-character cap before it reaches any model-visible line or log (threat §10; the existing hook-context sanitizer test gains a repo-file variant). Everything else that reaches output goes through the same sanitizer.
- File contents never appear raw in error messages and only pass the redacting logger (T12, U-23..U-25).

**Who writes it.** `brigade team create` writes it, once, in the admin's terminal (§2). After that the file is **read-only to Brigade**: no member command, no hook, no watcher ever writes it — or anything else — into the project directory. security.md's "Nothing is written into your project directory" is re-scoped explicitly, not silently: *"nothing except the one team file `.brigade.json`, written once by the administrator's `team create`, containing only public values."* (threat §14)

**Discovery.** Readers walk up from the session's working directory and take the nearest `.brigade.json`, stopping at the git toplevel when inside a worktree (see open question 2 for whether the walk may continue to `$HOME`). Nearest-wins is safe against a PR planting a subdirectory file, because a new file location has no pin (§5) and therefore attaches nothing.

---

## 2. The admin path: one Brigade command, one git commit

```sh
brigade team create --url https://<ref>.supabase.co --key sb_publishable_… --secret-file ~/brigade-<team>.secret
```

Run in the admin's own terminal, cwd anywhere inside the project checkout. The in-session refusal (`usage`, exit 2, `in_session`, "run this in your own terminal: the join secret must never pass through the chat") survives unchanged.

**Inputs.** `--url` and `--key` are absorbed from the late `profile init` — both are declared configuration-not-secrets, so argv is fine, exactly as `profile init` accepted them. Team name via `--name` or the existing TTY prompt; display label via `--label` or prompt. The Supabase project itself must already exist (the "Hosted project" section of setup.md, unchanged — Brigade cannot create Supabase projects with a publishable key, and pretending otherwise would be dishonest mechanics).

**What one run does, in order:**

1. Refuses if `.brigade.json` already exists at the target root (`conflict`), unless `--force`.
2. Mints the admin principal and creates the team (today's `ensureIdentity` + one `create_team` RPC — unchanged).
3. Writes the join secret 0600 to `--secret-file` (written before anything is bound, as today) — or prints it exactly once in the stdout envelope if the flag is absent (open question 1 proposes making the flag mandatory, as `rotate-secret` already does).
4. Writes the local credential store and binding for this team (§3's layout) and the project pin for this checkout (§5), so the admin's own sessions attach immediately.
5. Writes `.brigade.json` at the git toplevel (cwd if not in a worktree), 0644, prints its path, and prints the honest remaining step: *commit and push this file.*

**Where the join secret goes:** into `--secret-file` (or once to stdout), then to members over a password-grade channel. Never into `.brigade.json` — no field exists, the parser hunts it, CI scans for it, and a committed one is treated as leaked with rotation as the only answer.

So the admin path is: **one `brigade` command, one `git commit`/`push`, one password-manager share.** Brigade deliberately does not commit for you: the admin should see the file before it enters history, and a tool that commits is a tool a prompt can drive.

---

## 3. The member path: one command, no parameters

```sh
brigade team join
```

Run in the member's own terminal, cwd anywhere inside the checkout. No `--profile`, no `--url`, no `--key`, no flags at all (optional `--label`). The in-session refusal survives unchanged.

**Flow (TTY):**

1. Discover and parse `.brigade.json` (§1 rules).
2. Show the human what the file says, **before any secret is typed** — the anti-phishing gate (threat §8):
   ```
   join team "ops" (t_4f9c…) at <ref>.supabase.co? [y/N]
   ```
   Host and team ref come from the file; the https rule has already been enforced on the file-sourced URL.
3. Read the join secret with `term.ReadPassword` — the existing `join secret (not echoed): ` prompt on stderr — then the optional echoed label. `--join-secret` on argv stays poison-scanned and refused everywhere.
4. Mint the anonymous principal if none exists for this team (signup only here and in `create`, as today), send the secret once to `join_team`, never persist it. The unknown-team/wrong-secret answer stays byte-identical (I-18) — a file naming a bogus `team_ref` is not an existence oracle.
5. On success, store locally (below) and print one success line. Done forever for this checkout.

**Non-TTY rule.** A non-TTY `team join` does **not** read the repo file at all: it reads the one stdin JSON document (the existing `TeamJoinRequest`, including its `backend` member) exactly as today. The repo-file path is TTY-only, so the human confirmation gate is never bypassed by piping, and scripted joins stay fully explicit.

**Re-run when already a member** (second checkout of the same repo, or after file drift): `team join` detects the existing credential for the file's team, verifies membership live, shows what changed (if anything), asks `[y/N]`, and writes only the pin — **no secret is asked for again**. One verb covers first join and re-consent (open question 3).

**What gets stored locally, and keyed how.** The profile directory becomes an internal credential store the user never names. Under `${config_dir:-~/.config/brigade}`:

```
teams/<key>/
  team.json          # binding: adapter, url, publishable_key, team_ref, team_name,
                     # principal_ref, human_label, created_at — NEVER a secret
  session.json       # the GoTrue credential, 0600, atomic, flock sidecar (unchanged)
  session.json.lock
projects.json        # the per-checkout pins, §5
```

`<key>` = lowercase hex `sha256(adapter + "\n" + url + "\n" + team_ref)`, truncated to 32 characters — deterministic, so the hook goes from file to credential dir with one path construction and no scanning. The hash is never trusted alone: `team.json` inside carries the real values and is verified field-by-field against the file on every attach. One credential dir per team, shared by every checkout that pins to that team. `brigade team list` renders the store human-readably (team, host, principal, pinned checkouts).

All of today's guarantees carry over verbatim: 0600 in 0700, refusal of group/world-readable `session.json`, atomic writes, refresh-token family revocation (I-34). Deleting, re-cloning or branch-switching the repo neither creates, destroys nor switches anything here — only terminal commands write this store (threat §13).

---

## 4. The SessionStart hook: attach-only, ever

**New resolution order:**

1. **Repo file first.** Discover `.brigade.json` (§1 discipline). No file ⇒ Brigade stays off, **silently** (DEBUG log only — a repo without Brigade must not nag). Nothing falls back: no profile fallback, no environment fallback (in-session `BRIGADE_*` stays stripped, U-27).
2. **Pin check.** Look up `projects.json` for the realpath of the discovered file's directory (§5). No pin, or pin mismatch ⇒ one line, nothing else (below).
3. **Credential check.** Compute `<key>`, verify `teams/<key>/team.json` matches the file on `adapter`/`url`/`publishable_key`/`team_ref`. Missing or mismatched ⇒ the same not-joined line.
4. **Attach.** From here the chain is byte-for-byte today's: resolve the adapter user-side (option override → user's `adapters.json` by the file's *name* → bundled), `describe`, `session register`, write the 0600 by-pid map, spawn the watcher with a from-scratch environment. The map's `profile` member becomes the team `<key>` plus `config_dir`; everything else it carries is unchanged.

**Exact one-line messages** (house style; `<name>` is the file's `team_name`, sanitized, ≤32 chars — the only repo string that ever reaches the model, and only in this line):

- File present, valid, but never joined here:
  ```
  Brigade: not joined: this project uses team "<name>" — run `brigade team join` in your own terminal.
  ```
- File drifted from the pin (§5) — note it deliberately echoes **nothing** from the hostile file:
  ```
  Brigade: not connected: .brigade.json does not match the team you joined here — run `brigade team join` in your own terminal to review the change.
  ```
- File invalid: `Brigade: not connected (config: team_file_<reason>): fix .brigade.json.` — reasons `symlink`, `unknown_field`, `unsupported_version`, `url_not_https`, `adapter_unknown`, `too_large`. The secret-shaped case gets its own urgent line: `Brigade: not connected: .brigade.json contains what looks like a join secret — remove it and rotate now (brigade team rotate-secret).`

**The hook may never join — enforced structurally, three ways:**

1. **No code path.** The hook binary contains no call that transmits a join secret and no signup call; identity minting lives only in `team create`/`team join`, which keep their in-session refusal — and the hook itself runs with `CLAUDE_PID` set, so even the commands refuse under it.
2. **Read-only store.** The hook only reads `teams/` and `projects.json`; the sole writers are the terminal-only commands. Attach-only means: the hook may *use* credentials that exist, never mint, copy or repair them.
3. **Pinned by test.** A new unit test runs the hook in a repo with a valid file and no pin and asserts **zero network I/O** and no store writes — the not-joined and drift paths must be provably inert.

Once attached, **nothing re-reads the repo file**: session-bound commands and the watcher resolve only through the 0600, owner-checked, non-symlinked by-pid map (threat §6). A prompt-injected model rewriting `.brigade.json` mid-session changes nothing until the next SessionStart — where it hits the drift refusal. The model-visible team name in the start line comes from the server's `describe` via the binding, never from the file, so the file cannot speak in the start line at all once attached. The file cannot set frame, frame level, or inbound policy — those fields don't parse (threat §11) — and the file's presence shares no cwd, hostname or path with the server: pins are local-only, the registration payload is unchanged, workspace label stays opt-in (U-22, I-33).

---

## 5. The team-swap defense: pin at join, refuse on drift

The attack: a member of teams A and B has checkouts of a project pinned to A; a PR edits `.brigade.json` to name B (which the member also has credentials for); without a defense, sessions silently register into B and replies leak context there.

**Keying the credential store by url+team_ref is not enough** — the swapped file simply resolves to the *other* valid credential. So membership and attachment are separated:

- **Join writes a pin.** `team create` and `team join` record, user-side in `~/.config/brigade/projects.json` (0600, flock'd, written only by those terminal commands):

  ```json
  {
    "version": 1,
    "projects": {
      "/Users/anna/dev/payments": {
        "adapter": "supabase",
        "url": "https://abc.supabase.co",
        "publishable_key": "sb_publishable_…",
        "team_ref": "t_4f9c…",
        "consented_at": "2026-09-06T18:02:11Z"
      }
    }
  }
  ```

  Keyed by the **realpath of the checkout**, not by git remote — remotes are attacker-claimable strings in a hostile clone; the path is what the human physically stood in when they consented.

- **The hook attaches only when file == pin**, byte-wise, on `adapter`, `url`, `publishable_key`, `team_ref`. Any drift ⇒ the loud one-line refusal above, and the session attaches to **neither** team — no follow, no fallback to the old pin (the file in front of you is the project's declared truth; if they disagree, a human decides).

- **Re-consent is a fresh explicit command with the diff shown** (threat §5): `brigade team join` in that checkout prints old → new for every drifted field ("team: ops (t_4f9c…) → ops-eu (t_88a1…)", "backend host: abc.supabase.co → evil.example.com"), asks `[y/N]`, and needs no secret when the user is already a member of the new team, a normal secret-join when not. Only then is the pin rewritten.

This also gives the legitimate flows for free: a publishable-key rotation is the admin editing the file and committing; every member sees one refusal line, runs `team join`, reads a one-line diff, confirms, and is done — no secret retyped. A hostile clone at a *new* path naming the victim's real team has no pin at that path, so it gets the not-joined line and registers nothing (threat §4, ADV-4).

---

## 6. What dies, what survives

**Dies (deleted, not hidden — zero users, no migration code):**

| Gone | Replaced by |
|---|---|
| `brigade profile init` | `team create --url/--key` (admin); the repo file (member) |
| The `profile` userConfig option | The repo file + pin. No user-axis team selector exists at all |
| `--profile` on every command | Nothing in-session (the map is the authority, unchanged); an optional `--team <ref-or-name>` on **terminal** commands only, for disambiguation outside a checkout |
| `profiles/<name>/` layout | `teams/<key>/` store (§3) |
| `profile status/reset/revoke-credentials` | Same semantics as `team status` / `team reset` / `team revoke-credentials`, resolving via cwd's pin, else the sole local team, else `--team` |
| docs/setup.md's two-command member flow, the "second team = second profile + profile option" paragraph, the `--settings pluginConfigs profile` dev recipe | §7's flows; two dev personas on one machine use `config_dir` |

**Survives as advanced/hidden:**

- `config_dir` — redefined: "where Brigade's credential store lives"; also the two-personas-per-machine dev tool.
- `adapter_command` — redefined: "overrides the executable for the adapter the team's binding names"; an adapter-author tool. Never sourced from the repo.
- The stdin `TeamJoinRequest` (with its `backend` member) — the scripting/CI join path, unchanged.
- `BRIGADE_SUPABASE_URL`/`_PUBLISHABLE_KEY` bootstrap — human shells and CI only; stripped in-session as ever.

**Survives untouched:** `team_inbound`, `share_workspace_label`/`workspace_label`, `poll_on_prompt`, `frame`/`frame_file`; the terminal-only six (create, join, rotate-secret, revoke-member, transfer, inbox release — the owner's ruling); the leaked-secret playbook; hold's human gate; every admin command; the by-pid map and watcher architecture; the redaction, uniform-error and stdout disciplines.

Ships as **0.2.0**, breaking. New tests: file-parser fuzz + unknown-field/secret-shape refusals, symlink refusal, swap-drift refusal, attach-only zero-network, and the repo-file variant of the hook-context sanitizer test.

---

## 7. The two lives, end to end

**Contractor: three projects, three teams, one machine.**

Once: install the plugin (two commands, unchanged). Per project, once ever: `cd ~/dev/acme && brigade team join` → confirm `join team "acme-eng" (t_…) at acme.supabase.co? [y/N]` → paste that team's secret from the password manager. Repeat for `~/dev/globex` and `~/dev/initech`. Three joins, zero settings, zero profile names.

Daily: `claude` in any checkout attaches to that project's team. Switching teams is `cd`. Locally: three `teams/<key>/` credential dirs, three pins. When a PR in acme edits `.brigade.json` to name globex's team (which she belongs to), her next acme session prints the drift refusal and attaches to nothing; `brigade team join` shows her exactly what moved, and she says no.

**Single team, small shop.**

Admin: put the Supabase project in shape (docs, unchanged), then `brigade team create --url … --key … --secret-file ~/brigade-ops.secret`, type the team name, `git add .brigade.json && git commit && git push`, share the secret file's contents via the password manager. Her own sessions already attach.

Each member: clone, install the plugin, `brigade team join`, confirm host + team, paste the secret. That is the entire lifetime configuration. A new machine is the same one command; a second checkout on the same machine is the same command minus the secret (re-pin only). No one on the team ever hears the word "profile".

---

## 8. Open questions for the owner (3)

1. **Create-time secret delivery.** Make `--secret-file` mandatory on `team create` (as `rotate-secret` already is), or keep the print-exactly-once stdout fallback? Mandatory is symmetric and scrollback-safe; the fallback is friendlier for a first solo try.
2. **Discovery scope.** Should the walk-up stop at the git toplevel (a repo without the file is simply "no Brigade"), or continue to `$HOME` so a personal `~/.brigade.json` can serve as a default team for non-repo directories? The latter neatly replaces the old "default profile" for solo use, at the cost of one more place a team can be named.
3. **One verb or two.** `brigade team join` here covers both first join (secret) and re-consent after drift (no secret, diff + confirm). Keep one verb with two behaviors, or split re-consent into its own command (e.g. `brigade team pin`) for explicitness at the cost of a second thing to learn?


---

# Adversarial review (same workflow, 2026-09-06)
Verdict: NOT sound as drafted — sound after the fixes below are applied. The high fix amends sections 3 and 5.

**[high]** The secret-free re-consent (§3/§5) defeats the pin defense it exists to serve. The pin rewrite — the design's core defense against cross-team exfiltration — is gated only by isatty + [y/N] + the in-session refusal, and that refusal is advisory: it keys solely on the CLAUDE_PID environment variable (internal/harness/config/environ.go), which any Bash-tool invocation strips with `env -u CLAUDE_PID`, and a TTY is fabricable with `script -q /dev/null`. Concrete failure: a prompt-injected session in a checkout pinned to team A (victim also a member of B) rewrites .brigade.json to name B (the model has project-dir write), then runs `printf 'y\n' | env -u CLAUDE_PID script -q /dev/null brigade team join`; because the victim holds a credential for B, no secret is asked, the pin is rewritten, and the victim's next session in that checkout silently attaches to B — exactly threat claim 5's exfiltration channel, with no human involved. Today every membership-grade mutation outside a session is backstopped by the join secret (protocol/team.go requires join_secret; rotate-secret is --secret-file-mandatory); this is the first security-relevant binding change with no secret backstop, and security.md's promises are stated for 'every permission mode'.

*Fix:* Require the join secret for any pin write that changes adapter, url, or team_ref (a cross-team re-point IS a membership-grade event); allow secret-free re-consent only for a publishable_key-only drift, which keeps the legit rotation flow one command with no secret retyped. Additionally document (see residual risks) that the CLAUDE_PID gate is advisory and that pin/credential-store integrity ultimately rests on OS permissions and the Bash sandbox, as it already does for profile.json today.

**[medium]** "CI scans for it" is false on both counts (§1, §2). scripts/ci/no-secrets.sh scans only JWT shapes and sb_secret_ (lines 27-28) — there is no brg1 pattern anywhere in the script. And that scan runs in Brigade's own repo CI; the .brigade.json files that matter live in users' project repos, which never run Brigade's CI. Of the three claimed layers against a committed join secret (parser hunts it, CI scans it, committed = leaked/rotate), the middle layer does not exist. The parser refusal plus the hook's urgent rotate line are the only real defenses.

*Fix:* Add a brg1\. pattern to scripts/ci/no-secrets.sh for Brigade's own tree (cheap, catches dogfooding accidents); delete or reword the CI claim in the design; ship a documented one-line pre-commit/CI grep that adopting teams can paste into their own repos, and state plainly that the parser + hook line are the operative defense.

**[medium]** `team create` cannot compute `<key>` before the team exists — the deterministic store keying is circular at create (§2 step 2-4 vs §3). <key> = sha256(adapter+url+team_ref), but create's team_ref is minted by the create_team RPC, while ensureIdentity (signup) writes session.json into the profile/store dir BEFORE that RPC (internal/adapters/supabase/team.go:207), and the store dir is chosen by the --profile argv the harness passes at adapter spawn time — before any team_ref exists. The flow as written cannot name the credential dir it promises. `team join` does not have this problem (the file and the secret both carry team_ref up front).

*Fix:* Create under a temporary key and atomically rename + rewrite the binding after the RPC returns (with crash-safety: an orphaned temp dir must be prunable), or drop the hash-is-the-path property for create and keep a verified index — the design already never trusts the hash alone (team.json is verified field-by-field on every attach), so an index lookup costs nothing.

**[medium]** Join never checks that the typed secret's team matches the file's team_ref (§3 step 4). The secret embeds its team_ref (brg1.<team_ref>.<secret>; internal/protocol/joinsecret.go exposes TeamRef() for exactly this class of local check), and the file independently names team_ref, but the flow sends the secret to join_team without comparing them. Concrete failure: a contractor with three teams in one password manager pastes the wrong team's secret; the join succeeds into the secret's team while the pin and <key> record the file's team — the credential lands in a dir keyed by the wrong team, and every subsequent attach hits the field-by-field mismatch and prints the not-joined line forever, with no recovery short of manual store surgery. Fail-closed, but a support landmine sitting inside the design's one member command.

*Fix:* Before any network call, compare JoinSecret.TeamRef() against the file's team_ref and refuse with a clear local 'this secret is for a different team than this project's .brigade.json names' line. Purely local check — creates no existence oracle (I-18 untouched: nothing is sent).

**[medium]** Discovery scope past the repo boundary is left half-open and the §1 wording already leaks the hazard: 'stopping at the git toplevel when inside a worktree' implies that in a normal clone the walk may continue above the toplevel, and open question 2 proposes walking to $HOME. If a .brigade.json above a repo's toplevel is ever discoverable from inside a repo, then once the user pins that outer directory (e.g. a personal default at $HOME), every checkout under it that lacks its own file — including a freshly cloned hostile repo — silently attaches sessions to the personal team: auto-attach in an untrusted working tree with no per-repo consent, resurrecting ADV-4 with the pin gate structurally bypassed (the pin is keyed to the outer directory, not the checkout the session is actually in).

*Fix:* The walk always stops at the repository toplevel, worktree or not; a file above a toplevel never applies inside any git checkout. If the owner wants a personal default for non-repo directories (open question 2), scope that pin to explicitly exclude paths inside git checkouts, and say so in security.md.

**[low]** The adapter choice is invisible at the consent gate (§3 step 2). The confirm line shows team name, ref and host but not the adapter name the file selected. adapters.json is a real user-side registry (.context/plans/implementation/03-architecture.md:82; ResolveAdapter's name path), so a hostile file can silently pick which registered adapter executable the attach will spawn and which dialect interprets the url — the one repo-controlled degree of freedom the gate does not display.

*Fix:* Print the adapter name in the confirm gate whenever it is not the bundled default (e.g. 'via adapter "fs" from your adapters.json'), and refuse at join time — not only at attach time — when the file's adapter name has no user-side resolution.

**[low]** Pin keying by realpath has two unspecified soft spots (§5). (a) macOS APFS is case-insensitive by default: /Users/a/dev/Payments and .../payments are one directory but two distinct pin keys, yielding spurious not-joined lines (fail-closed, not a hole). (b) The design must pin the realpath of the discovered file's directory computed by the same canonicalization function the hook uses, or symlinked-home/bind-mount users get permanent pin-vs-hook drift.

*Fix:* One shared canonicalization function (EvalSymlinks on the file's directory) used by create, join and the hook; document the case-insensitivity caveat; on 'no pin found', log at DEBUG the canonical key looked up so the mismatch is diagnosable.

**[low]** The kill-list in §6 conflates the harness command surface with the frozen adapter protocol. Protocol v1 keeps `--profile <name>` on every adapter command, `describe.profile.*`, `profile init/status/reset/revoke-credentials` and the profile_bound conflict semantics frozen (docs/protocol-v1.md:87, 152-157, 244-261, 630). The design's plan is actually consistent — the map's profile member carrying the 32-hex <key> passes CheckProfileName (internal/adapterkit/profile.go:93), and the harness keeps speaking --profile to the adapter — but the table says 'profiles/<name>/ layout: dies' and 'profile init: dies' without stating that the adapter-protocol profile vocabulary survives underneath. A later cleanup taking the table literally would be a frozen-protocol violation requiring the one-commit protocol change per CLAUDE.md.

*Fix:* Add one sentence to §6: the deletions are the harness/user surface only; the adapter protocol's profile vocabulary (flags, describe members, verbs, conflict reasons) stays frozen and is driven internally with the team <key> as the profile name. Also update the messages that name profiles (e.g. internal/harness/config/environ.go:131 'the terminal commands take --profile instead') and security.md's credential path (§8: profiles/<name>/session.json → the new store path) in the same change.

## Residual risks (document, don't fix)
- Local trust-root integrity: projects.json and teams/<key>/ are OS-permission-protected only; any process running as the user — including a permissive-mode or bypass-mode model editing files directly — can rewrite pins or bindings without any Brigade command, exactly as it can rewrite profile.json today. The CLAUDE_PID in-session refusal is advisory (env-strippable). Must be stated in security.md; the high-severity fix restores the secret backstop on the command path but cannot protect the files themselves.
- Phishing at first join in a hostile clone: the confirm gate (host + team ref + name) is the only defense; a plausible lookalike hostname plus a join secret helpfully supplied in the repo's README can still walk a careless human into an attacker's team. Per threat claim 8 this is the accepted posture; document it.
- ADV-4 at a fresh path: a hostile clone naming the victim's real team attaches nothing automatically, but a victim who runs `team join` there and confirms pins it — and the re-consent needs no secret since the credential exists. The consent line is the whole defense at a new path; document that joining in an untrusted checkout registers your sessions from that working tree.
- team_name is deliberately not pinned: a PR can change the display name shown in the not-joined line (sanitized, 32-char capped). Cosmetic-only by design; worth one sentence in security.md.
- The admin path is one Brigade command only after the Supabase project exists and is prepared (docs/setup.md 'Hosted project'); Brigade cannot create Supabase projects with a publishable key. This is the honest, permanent gap against the owner's ideal of a literal single admin command — surface it to the owner rather than papering over it.
- Availability lever: any merged PR touching .brigade.json (including making it invalid) disconnects every member's sessions in that checkout until each human re-consents; this is the same mechanism that makes key rotation work and is fail-closed, but repo writers gain a team-wide disconnect switch.
- Requirements fit is otherwise met: the member path is one parameterless terminal command per checkout; the project file plus the user-side pin — not the file alone — determines the team, which is the security-mandated reading of 'each project has its own team'; no profile-flavored concept survives in the mainline user path (config_dir, adapter_command and --team are advanced/terminal-only escape hatches, and the adapter-protocol profile vocabulary is invisible to users).

---

# Owner rulings (2026-09-06, in session)

The three open questions of §8, ruled by Rjae:

1. **`--secret-file` is mandatory on `team create`** — symmetric with `rotate-secret`; the secret never reaches
   scrollback. The print-once fallback is dropped.
2. **Discovery stops at the repository toplevel, always** — no `~/.brigade.json` fallback; a repo without the
   file is simply "no Brigade". (This also adopts the reviewer's medium fix on discovery scope.)
3. **One verb** — `brigade team join` covers first join and re-consent. Per the review's high fix, any pin
   rewrite that changes `adapter`, `url` or `team_ref` requires the join secret; only a publishable-key-only
   drift re-consents without one.

Also adopted: all eight review fixes above. Target: 0.2.0, breaking, no protocol change.

**2026-09-08.** Reversed for three of the six "survives untouched" verbs: `team create`, `team join` and
`team rotate-secret` run inside a session (P7-11, `.context/plans/in-session-team-verbs.md`); `revoke-member`, `transfer`
and `inbox release` stay under this ruling.
