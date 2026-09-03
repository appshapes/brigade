# E3 — Interactive checks (P3-8)

Date prepared: 2026-09-03 · Ticket 15 · Claude Code 2.1.259 · Status: **checks 1 and 2 SETTLED, run
without a keyboard (§1b, §1c); checks 3–15 still open.** Checks 3, 4 and 5 are one settings-file
change away from running under the same driver; 6–13 need two sessions and the terminal-side steps;
14 needs the sandbox; 15 is observational.

This is the checklist of plan row P3-8: the things only an interactive terminal can show. Every headless half of the
plugin is measured in `E3-wiring.md` and `E3-smoke.md`; this file records the keyboard-dependent half. Fill in the
"Observed" column with what you saw and paste a transcript excerpt per row (the transcript is
`$CLAUDE_CONFIG_DIR/projects/<cwd-key>/<session>.jsonl`; `jq -c 'select(.type=="attachment")'` shows the hook
records, `jq -c 'select(.type=="queue-operation")'` the injected messages). Treat a green here as evidence only
where you name what you saw, not what you expected.

## 0. Setup (once, in your own terminal — never from inside a Claude Code session)

`team create` and `team join` refuse inside a session on purpose. Three terminals in all: ONE plain terminal in the
repository for everything in this section (both command blocks below run there, one after the other; keep it open
afterwards for the terminal-side steps of checks 10–13), then two more for the Claude Code sessions A and B. Run
this in the setup terminal:

```sh
make build
bin/brigade profile init --adapter '["'"$PWD"'/bin/brigade-adapter-fs"]'
bin/brigade team create --name ops --label dev --secret-file ~/brigade-ops.secret
bin/brigade profile status                              # expect: default adapter [...] (from sidecar)
```

Then, in the same terminal, a second profile for the peer session (it joins the same fs team; the secret goes from
the 0600 file into the stdin document, never onto argv). The join is ONE command — a brace group piped into
`brigade team join` — written on one line here so it is one paste and one Enter:

```sh
bin/brigade profile init --profile bob --adapter '["'"$PWD"'/bin/brigade-adapter-fs"]'
{ printf '{"human_label":"bob","join_secret":"'; tr -d '\n' < ~/brigade-ops.secret; printf '"}'; } | bin/brigade team join --profile bob
bin/brigade team members --profile bob                  # expect: two principals
```

Where things live (notes, not commands): `make plugin-dev` writes the dev-binary pointer
`${XDG_CONFIG_HOME:-~/.config}/brigade/dev-binary` and starts Claude Code with `--plugin-dir ./plugin`; `make
plugin-dev-off` removes the pointer afterwards. The fs store is `${XDG_STATE_HOME:-~/.local/state}/brigade/fs-adapter`;
the maps and pidfiles are under `${XDG_STATE_HOME:-~/.local/state}/brigade/`. Nothing here touches the Supabase
stack.

Now open the two session terminals (the setup terminal stays open). **Launch with an explicit permission mode**: the
`rjae@appshapes.com` account opted into Claude Code's auto-mode default offer, so a plain `claude` starts in `auto`,
where no permission prompt or Skill dialog ever appears and checks 1–5 measure nothing (that is what happened on the
first run, see §1a). `mode=` passes `--permission-mode`:

- **A (alice)**: `make plugin-dev mode=default` (the sidecar written by `profile init --adapter` above selects the
  fs adapter; `adapter=fs` is the same thing spelled out).
- **B (bob)**: `make plugin-dev mode=default profile=bob`.

The status line at the bottom of each session shows the mode; it must NOT say auto. Record it with the context line.

Both should print, as the first context line of the session, `Brigade: this session is "<name>" (<id>) in team
"ops"; inbound: accept; …`. Record both lines.

## 1. The checklist

| # | Check (plan row P3-8 / E0-8) | How | Expect | Observed |
| --- | --- | --- | --- | --- |
| 1 | **The skill grant in Manual mode** (D20, E0-8 (b)) | Nothing to configure first: the check needs NO `permissions.allow` rule matching `brigade` anywhere (your `~/.claude/settings.json` — the `rjae@appshapes.com` account's user settings — has no `permissions` block and this repository has no stored per-project approvals, so the precondition holds as is). In A, in the default (Manual) permission mode, ask: "Use the brigade team-messaging skill to list the team's sessions and tell me who is online." **If no Skill dialog appears**, repeat ONCE from a fresh temporary project directory — a brand-new empty directory Claude Code has never been started in: Claude Code keeps per-project state keyed by the launch directory (the trust decision, "don't ask again" approvals, and per E0-8 the Skill dialog's dismissal), so this repository's directory may already carry a dismissal from an earlier session, while an empty directory has no history. In the setup terminal: `make plugin-dev-pointer` (writes the dev pointer, starts nothing), then `cd "$(mktemp -d)"`, then `claude --plugin-dir /Users/rjae/Development/appshapes/brigade/plugin` (absolute path, since you are no longer in the repository; the plugin works from any directory because the profile and the fs store live under your home, not the project; accept the trust dialog for the new directory when it appears) and ask the same question. This rules out a dismissal stored for this repository; if there is still none, that is a behaviour change since E0-8 (2.1.252) — record the version and it becomes a D20 note (one prompt fewer). | ONE Skill dialog ("Use skill brigade:team-messaging?") whose dismissal is scoped to the project directory; then `brigade sessions` runs with **no** Bash prompt in that turn. | **PASS, 6/6 in Manual mode on 2.1.259** (§1c; 3 runs from a fresh temp directory, 3 with this repository as cwd). Exactly ONE Skill dialog — `Use skill "brigade:team-messaging"? / Claude may use instructions, code, or files from this Skill / … / Do you want to proceed? / ❯1. Yes / 2. Yes, and don't ask again for brigade:team-messaging in <dir> / 3. No / Esc to cancel · Tab to amend` — and after a single Yes the bare `brigade sessions` executed with **no Bash prompt**. Option 2's dismissal is scoped to the directory it names: selecting it silenced the dialog in that directory and a fresh directory still raised it. Mode was `default` in all four witnesses in every run; the null control stalled 3/3. **The earlier `auto`-mode reading (§1a) stays retracted; E0-8 (b)'s 2.1.252 result holds unchanged on 2.1.259.** |
| 2 | **The grant does not outlive the turn** | In A, the next prompt, without invoking the skill: "Without using any skill, run the Bash command `brigade sessions` directly and paste its output." Then read that turn in the transcript: `jq -c 'select(.type=="assistant") \| .message.content[]? \| select(.type=="tool_use") \| {name,input}' <transcript>.jsonl`. **If a `Skill` tool_use for `brigade:team-messaging` appears in that turn the model re-invoked the skill on its own, the grant applied legitimately, and the check is inconclusive** — re-ask in the explicit form above. | A Bash permission prompt appears (the grant cleared when you sent the next message). | **PASS, 6/6** (§1c). The turn stalled on a permission prompt in every run: a bare `brigade sessions` was attempted and did not execute, and the transcript's own `tool_result` for it reads "The user doesn't want to proceed with this tool use. The tool use was rejected" (the driver's shutdown Escape answering the open dialog), which is independent of the stall detector. **No run re-invoked the skill in that turn**, so none is inconclusive. The driver adds one clause to the prompt — "Use the bare command name `brigade`, never a path to the binary" — because only the bare form can match `Bash(brigade:*)`; see §1b. |
| 3 | **`permissions.allow` removes the prompt for the session** | Add the rule to the USER settings file of the config directory you launch from: `${CLAUDE_CONFIG_DIR:-$HOME/.claude}/settings.json` — for your `rjae@appshapes.com` sessions that is `~/.claude/settings.json` (check with `echo ${CLAUDE_CONFIG_DIR:-$HOME/.claude}` in the launch terminal); never the repository's `.claude/settings.json` or `.claude/settings.local.json`. Add a top-level member `"permissions": {"allow": ["Bash(brigade:*)"]}` beside the existing keys (valid JSON: a comma after the previous member). Alternatively, inside a session, `/permissions` opens the rules UI, where the same rule can be added at user scope. Restart A, repeat check 2's explicit "without using any skill" form. Remove the rule again before check 4. | No prompt. | |
| 4 | **The ask rule in bypass mode** (E0-8 (b)) | In that same settings file replace the allow rule with `"permissions": {"ask": ["Bash(brigade send*)"]}`. Start A in bypass mode: `make plugin-dev mode=bypassPermissions`. Ask A to send B a message. | The dialog appears for the heredoc `brigade send` (it is not silently denied); record how the multi-line heredoc renders and whether the dialog offers only Yes/No (an explicit ask rule cannot be one-click disabled — sitting correction 3). `brigade sessions` runs unprompted. | |
| 5 | **The deny rule is the off switch** | Same, with `"permissions": {"deny": ["Bash(brigade send*)"]}`. Ask A to send. | The send is blocked in bypass mode; the model reports the denial and (per the skill) does NOT propose another invocation form. Record its words. | |
| 6 | **Send and mid-turn receive between two people's sessions** | In A: "Send bob's session a message saying hello and then sleep 30 seconds with the Bash tool." While A sleeps, in B: "Reply to alice's message with brigade send --reply-to." | A receives B's reply mid-turn as a message from `@<bob's session name>` with the `<brigade-message …>` frame; the fs store shows the message under `acked/<alice>/`; A does not call the native `SendMessage`. | |
| 7 | **`/rename` propagates** | In A: `/rename alice-renamed`, wait ~30 s (one heartbeat), then in B: "Run `brigade sessions`." | B's listing shows A under the new name (the watcher re-reads the registry each heartbeat). | |
| 8 | **`/clear` keeps the Brigade session** | In A: note the session id from `brigade whoami`; `/clear`; `brigade whoami` again. | The same Brigade session id; the SessionStart hook re-fired (a new context line) and did NOT rotate the identity; the watcher pidfile pid is unchanged (`cat ~/.local/state/brigade/watchers/<claude pid>.json`). | |
| 9 | **`/compact` is a no-op for Brigade** | In A: `/compact`; then `brigade whoami`. | Same id, no new registration, no new context line beyond Claude's own. | |
| 10 | **The shadowing warning** (E0-8 (e)) | Put another `brigade` earlier on PATH in a terminal (`mkdir -p /tmp/shadow && printf '#!/bin/sh\necho DECOY\n' > /tmp/shadow/brigade && chmod +x /tmp/shadow/brigade`), then `PATH=/tmp/shadow:$PATH make plugin-dev`. | A second context line: `Brigade: another \`brigade\` at /tmp/shadow/brigade shadows the plugin's; …`; asking the model to run `brigade sessions` prints `DECOY`. Remove the decoy afterwards. | |
| 11 | **`~/.local/bin` symlink is NOT a shadow** | `ln -s "$PWD/plugin/bin/brigade" ~/.local/bin/brigade` (the setup skill's suggestion), start A. | No shadowing line (a symlink resolving to the plugin's bootstrap is exempt). Remove the symlink if you do not want it. | |
| 12 | **SessionEnd closes the session** | Quit A with `/exit`; in a terminal: `bin/brigade-adapter-fs --profile bob session list --include-offline` (needs `BRIGADE_FS_ROOT=$HOME/.local/state/brigade/fs-adapter` in that terminal) or in B: "Run `brigade sessions --all`." | A's session shows `offline` at once (closed by SessionEnd), not 90 s later; A's watcher pidfile is gone within ~2 s. | |
| 13 | **The watcher survives a SIGKILLed Claude and closes the session itself** (E0-5) | Start A, note its pid (`echo $CLAUDE_PID` via the Bash tool) and the watcher pid (the pidfile); `kill -9 <claude pid>` from a terminal. | Within ~2 s of the process being reaped the watcher exits, the pidfile disappears and A's session is `offline` (the zombie reads as dead). | |
| 14 | **Sandbox** (6.12; only if `sandbox.enabled` is on for you) | With the Bash sandbox on and no `allowedDomains` entry, ask A to run `brigade sessions`. | The fs adapter needs no network, so it works under the sandbox; `brigade send` succeeds with a read-only home (the map is only read). A hosted Supabase profile would need `<ref>.supabase.co` in `sandbox.network.allowedDomains` — record as not applicable if no hosted project exists (D32). | |
| 15 | **A third first-run interruption** (sitting correction 5) | Note any onboarding prompt (`Claude in Chrome extension detected`, the trust dialog, the fullscreen-renderer write) that appeared before the session prompt in any of the runs above. | Recorded, for P4-3/P4-5's `expect` drivers. | |


## 1a. Reading the first results (driver, 2026-09-03) — SUPERSEDED by §1b/§1c

> Kept as the record of a wrong reading and its retraction. Checks 1 and 2 were re-run properly in
> Manual mode; the results are in §1c and the method in §1b. The redo did **not** need a keyboard.


Rjae's first two observations — check 1 "No dialog, team-messaging use successful", check 2 "No prompt" — contradict
E0-8 (b) on 2.1.252 (one Skill dialog; a Bash prompt without the skill). Established from the machine's state: the
sessions run as `rjae@appshapes.com`, whose config directory is `~/.claude`; its `settings.json` has no `permissions`
block, this repository has no stored per-project approvals (`allowedTools` is empty in every config directory that
knows the repository) and no `.claude/settings*.json`, so no rule explains the silence. Two follow-ups decide it:
(1) repeat check 1 from a fresh temporary project directory — a dismissal stored for THIS repository is the
remaining benign explanation; none there means 2.1.259 no longer raises the dialog for a plugin skill, a D20 note
worth recording (one prompt fewer for every Manual-mode user); (2) for check 2, read the turn's transcript: a `Skill`
tool_use means the model re-invoked the skill and the grant applied legitimately, so the check needs the explicit
"without using any skill" prompt. Record the Claude Code version (`claude --version`) beside both.

**Result of the follow-ups (driver, 2026-09-03 11:25 EDT):** the transcripts of both sessions (the repository one at
11:09, the fresh-directory one at 11:19) carry `permissionMode: auto`, so neither check ran in Manual mode and both
observations are explained by the mode alone — RETRACTING the reading above that 2.1.259 raises no Skill dialog.
What the transcripts do establish: the skill was invoked (`Skill: brigade:team-messaging`, then `Bash: brigade
sessions`, `brigade whoami`, `brigade sessions --all`), and in check 2's turn the model ran `Bash: brigade sessions`
WITHOUT re-invoking the skill, which is exactly the shape check 2 needs — in Manual mode that turn prompts or it
does not. **Redo checks 1 and 2 in Manual mode**: `make plugin-dev mode=default` (A) and `make plugin-dev mode=default
profile=bob` (B); the status line must not say auto; record the mode with the context line. The mode came from the
account's opt-in to Claude Code's auto-mode default offer (`~/.claude.json`), not from any settings file.

## 1b. How checks 1 and 2 were run — without a keyboard (driver, 2026-09-03)

Checks 1 and 2 do not need a person at a keyboard. What they need is an interactive **pty** in
**Manual** permission mode, and `expect` supplies one. They were run by
`scripts/experiments/E3-interactive/run_manual.py`, with `run_headless.py` for the half `claude -p`
can settle and `analyze.py` for the tables, as `rjae@appshapes.com` in `~/.claude` — the account and
config directory this checklist prescribes. The driver is the 2.1.259 descendant of
`scripts/experiments/E0-8/run_b.py`, pointed at the **shipped** plugin rather than a probe plugin,
reusing that experiment's `common.py` for the expect prelude and the by-prefix environment strip.

**The detector is mechanical, not prose.** A `PreToolUse` hook fires once the model has produced tool
parameters and *before* the permission decision; `PostToolUse` fires only if the call actually ran. So
*attempt recorded + no exec recorded + the session stops making progress* means the permission system
intervened, and with `allow`/`deny`/`ask` all empty that intervention is a prompt. Both hooks are
supplied through `--settings`; **the shipped `plugin/` tree is never touched**, because
`make plugin-check` asserts its exact file list.

**The permission mode is recorded four ways**, since the first attempt at P3-8 was voided by an
unnoticed `auto`: the hook payload's `permission_mode` on every single tool call, the transcript's
`permission-mode` records, the Brigade by-pid map (written by the plugin's own `UserPromptSubmit`
hook, and read *mid-run* because `SessionEnd` deletes it), and the TUI's own status line in the
captured session log. Every run reported below reads `default` in all four.

**The null control is the point.** The `baseline` arm asks for `brigade sessions` with no skill in
play and must stall on an *unexecuted bare attempt*. A baseline run that does not stall means the
detector cannot go red and every other arm that day is void.

### What `claude -p` could and could not settle

`-p` has no approval surface, so with `--permission-prompts none` anything that would prompt is
denied instead. That is decisive about the permission **decision** and silent about **dialogs**
(`run_headless.py`, three arms, each artefact kept):

| Arm | Setup | Result |
| --- | --- | --- |
| `direct` | nothing pre-approved, no skill | bare `brigade sessions` **DENIED**, 0 executions |
| `grant` | **only** the `Skill` tool pre-approved | the skill ran, and `brigade sessions`, `brigade sessions --all` and `brigade team members` all **executed** |
| `expiry` | the `grant` session **resumed**, nothing pre-approved | bare `brigade sessions` **DENIED** again |

So the grant works on 2.1.259, covers several commands in its turn, and dies with the turn. It says
nothing about how many dialogs a person sees, which is what the pty run below measures.

### Five defects found in the harness before its results were believed

The driver was reviewed adversarially before its output was trusted. All are fixed in the committed
driver; they are recorded because each would have produced a confident wrong answer.

1. **Both dialogs say "Do you want to proceed?" on 2.1.259.** The Bash box reads *"This command
   requires approval / Do you want to proceed? / 1. Yes / 2. Yes, and don't ask again for: `<cmd>` * /
   3. Yes, and switch to auto mode / 4. No"*; the Skill box reads *"Use skill …? … / 1. Yes / 2. Yes,
   and don't ask again for `<skill>` in `<dir>` / 3. No"*. Matching `proceed` alone and pressing Enter
   would have **approved the very Bash prompt check 1 exists to detect**, reporting a pass in exactly
   the case that must go red. The driver now matches `approval` first and escapes it, presses nothing
   unless a `Skill` `PreToolUse` row exists, writes the matched dialog's text to `dialog-buffer.txt`,
   and `check1_pass` requires the Skill tool to have actually executed.
2. **A stall was scored as "a prompt appeared" without comparing attempts to executions**, so a turn
   in which the model called nothing at all would have counted as prompted. E0-8's conjunct is restored.
3. **Check 2's turn boundary was assumed, not observed** — a fixed nap, then a line count. A stage-1
   command still in flight would have been credited to stage 2 as "the grant outlived the turn". The
   driver now waits for quiescence and attributes rows by timestamp.
4. **The guard blind-restored `CLAUDE.md` and `settings.json`** from a start-of-run snapshot. No nested
   session here can write those paths, so every restore it could perform would have been reverting a
   *concurrent* edit, into a working tree whose next commit would sweep it up. The guard now reports
   drift and restores nothing, and it reads the real `~/.claude.json` (E0-8's looks for it inside the
   config directory, where it does not exist, so its drift report was a vacuous clean bill).
5. **A 24x80 pty wrapped the canary token across a line**, so the first canary attempt timed out on
   every run. The pty is now 200 columns.

Two smaller ones: `check1_pass` was forced false by check 2's *expected* stall, and the dialog-word
corroboration matched words the driver's own prompt puts on screen.

## 1c. Results — checks 1 and 2 are SETTLED (driver, 2026-09-03)

**Twelve interactive sessions, Claude Code 2.1.259, model `claude-opus-5`, permission mode `default`
(Manual) in all four witnesses in all twelve, canary green in all twelve.** Artefacts:
`.ignored/e3-interactive/final/` (one directory per run: `verdict.json`, `marks.ndjson`,
`session.log`, `dialog-buffer.txt`, `drive.exp`, `state/`).

| Arm | Runs | Skill dialog | `brigade` ran unprompted | Stalled | Reading |
| --- | --- | --- | --- | --- | --- |
| `baseline` (null control) | 3 | — | **no** (1 attempt, 0 executions) | at the first command | the detector goes red |
| `skill` (fresh temp dir) | 3 | **yes, one** | **yes** | at check 2 only | check 1 and check 2 pass |
| `skill-repo` (this repository) | 3 | **yes, one** | **yes** | at check 2 only | check 1 and check 2 pass |
| `dismiss` → option 2 | 1 | yes | yes | — | the dismissal is offered per directory |
| `dismiss2` same directory | 1 | **no** | yes | — | the dismissal persists there |
| `dismiss2` fresh directory | 1 | **yes** | yes | — | …and does **not** follow the machine |

**Check 1 passes, 6/6** (`skill` and `skill-repo`). One Skill dialog is raised, and after a single
"Yes" the skill's bare `brigade sessions` executes with **no Bash prompt** — 0 ms of wait in the
fastest run. E0-8 (b)'s 2.1.252 finding therefore still holds on 2.1.259, against the shipped plugin.
In `skill-repo` run 3 the skill ran **two** brigade commands in the granted turn, both unprompted,
which matches the headless `grant` arm's three.

**Check 2 passes, 6/6.** The next turn, asked for the same bare command with no skill in play,
stalls on a permission prompt in every run. The transcript's own `tool_result` for that call reads
*"The user doesn't want to proceed with this tool use. The tool use was rejected"* — the driver's
shutdown Escape answering it — which is independent of the stall detector. No run re-invoked the
skill in that turn, so none is inconclusive on the ground §1a warned about.

**The dismissal is scoped to the project directory**, and the dialog says so itself: option 2 reads
*"Yes, and don't ask again for brigade:team-messaging in `/private/var/…/brigade-e3-dismiss-…`"*,
naming the directory. Selecting it silences the dialog in that directory (`dismiss2` run 2) while a
brand-new directory still raises it (`dismiss2` run 3). It is one approval per repository.

**The null control fires, 3/3.** Every baseline run produced a bare `brigade sessions` attempt that
did **not** execute, and stalled. Without that the other rows would be unfalsifiable.

**Nothing the skill forbids happened in any of the twelve runs**: no full-path invocation of the
binary, no non-`brigade` Bash call, no other tool at all — in particular no native `ListAgents`.
That is worth stating precisely, because in the *headless* control where the Skill tool itself was
denied, the model did reach for the binary's full path and then for `ListAgents`. It had never
loaded the skill that forbids both. The rule works when the model can read it.

## 2. Teardown

`make plugin-dev-off`; optionally `rm -rf "$HOME/.local/state/brigade" "$HOME/.config/brigade"` (absolute paths, only if
you created them for this checklist and nothing else uses them) and `rm -f ~/brigade-ops.secret`.

## 3. Limits

**For checks 1 and 2 (settled).**

- Everything here is **one machine, one account, one version**: `rjae@appshapes.com` in `~/.claude`,
  macOS 25.6.0 arm64, Claude Code 2.1.259, model `claude-opus-5`. Nothing is claimed about other
  platforms or versions, and 2.1.252 vs 2.1.259 is the only version comparison made.
- **The prompts are not verbatim from this checklist.** Check 2's adds "Use the bare command name
  `brigade`, never a path to the binary", because only the bare form can match `Bash(brigade:*)` and
  an early run drifted to the full path (§1b). A person typing the shorter wording may see the model
  choose the full path, and would then be measuring a different command shape.
- **"One dialog" is bounded, not counted.** A second dialog raised before the skill's command reaches
  `PostToolUse` necessarily stalls the run, so a passing run had exactly one dialog in that window.
  A dialog raised *after* the first execution is not counted here; the headless `grant` arm covers
  that direction, running three brigade commands in one granted turn.
- **`option 2` was exercised once per direction**, not three times: one dismissal, one same-directory
  re-open, one fresh-directory re-open.
- The driver answers the Skill dialog with **option 1** in every arm but `dismiss`, so no run except
  that one persists anything; where the dismissal is *stored* was not found (it is not in
  `~/.claude.json`'s `projects` entry, whose `allowedTools` stayed empty).
- Nothing here measures a **second person's** session; checks 6–13 still need two principals.

**Anything the model did that the skill forbids.** In the twelve interactive runs: **nothing** — no
full-path invocation, no non-`brigade` Bash call, no other tool, no native `ListAgents`. But in the
*headless* control where the `Skill` tool itself was denied, the model ran the binary **by its full
path** and then fell back to **`ListAgents`** — both forbidden by the skill it had never been allowed
to load. The prohibition lives in the skill body, so a session denied the skill has not read it.

**Not run.** Checks 3–15. Checks 3, 4 and 5 need only a different `permissions` block in the same
`--settings` file the driver already writes, so the same harness reaches them. Checks 6–13 need a
second principal and the terminal-side steps of §0. Check 14 needs `sandbox.enabled`. Check 15 is
observational.
