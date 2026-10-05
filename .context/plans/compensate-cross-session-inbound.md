# Card 50 — Team messages held by Claude Code in bypass-permissions sessions

Owner's report (Rjae, 2026-10-05): the thinktech session `2171-data-warehouse-api` (Claude Code 2.1.289,
started with `--dangerously-skip-permissions`) saw Claude Code hold each Brigade message behind an approval
dialog, although `crossSessionInbound` was "set to accept". The dialog said the sender had not attested its
permission mode, the session bypasses prompts, and the remedy was to set `crossSessionInbound` to `accept`.
Requirement: no such pauses.

## 1. Root cause

Two facts about Claude Code, one assumption of Brigade's that no longer holds.

1. **Where the accept lived.** The `accept` was in the repositories' `.claude/settings.json` (both thinktech
   checkouts, and this repository's own). Claude Code's resolver reads managed settings, `--settings` and the
   user file for the first value, then lets the project and local files only **raise** it on the ladder
   `accept < hold < refuse`. Its settings reference says so: "a project or local value that isn't stricter is
   ignored". A repository `accept` is a no-op; the value stays unset; the per-message default runs. None of the
   three user files (`~/.claude`, `~/.claude-thinktech`, `~/.claude-ifthen`) carried the key.
2. **Why the default holds Brigade's post.** With no value set, a receiving session in `bypassPermissions`
   holds every message whose sender does not identify itself as bypassing too (cause `no-mode-asserted`), for
   a dialog that expires after `dialogExpiry` (five minutes; a headless session has no dialog and drops it).
   One exception: a post Claude Code verifies as the session's **own child**. Brigade's watcher is detached
   (`Setsid`, parent pid 1). In 2.1.289 the own-child verdict on macOS reads the poster's pid and walks its
   ancestors; `self` only when the session's pid is among them; the session token in the auth line counts only
   when there is no process evidence at all. On 2.1.261 (docs/security.md's measurement) the token alone had
   passed every Brigade post. So the watcher is `not-self`, asserts no `from_mode`, and is held. The prompt
   hook's own posts (a direct child) would pass; the watcher's do not.

Evidence: the strings and inbound-policy code of the 2.1.287–2.1.289 bundles under
`~/.local/share/claude/versions/`, Claude Code's cross-session-messaging page and settings reference (quoted in
`docs/research/claude-code-docs-gaps.md` §9), and the settings files on this machine.

## 2. What was ruled out

- **Asserting `from_mode` from the watcher.** To pass, Brigade would have to echo the receiving session's own
  class, which attests nothing; Claude Code documents `fromMode` as a relay host's statement of the *sending*
  session's class. Brigade does not know the teammate's mode, and a prompting teammate into a bypass receiver
  would be held as `mode-mismatch` anyway. Not done.
- **A watcher that is the session's child by ancestry** (a long-lived `async` SessionStart hook). Feasible and
  aligned with Claude Code's documented own-child case, but it changes the process model every E-series
  measurement rests on and needs a measured experiment on both platforms. Owner ruling 2026-10-05: "If we can
  avoid the need for the setting then we must" — then, told what the watcher change entails: "Can our
  brigade-install add that line to the user's config if it is missing? If so I would prefer that over this
  watcher change." So the setting is written, and the watcher stays as it is (follow-up §4).
- **Keeping `accept` and letting the dialog appear.** The post "succeeds", Brigade acknowledges, and a dialog
  nobody answers in five minutes — or any headless bypass worker — loses the message while the sender was told
  it arrived. That is the blind acknowledgement the native-hold rule already refuses (3.6, E0-9).

## 3. Decision and as built (this commit)

- **Remedy on the owner's machine (done by hand, before the write below existed):** `"crossSessionInbound":
  "accept"` added as the first member of `settings.json` in `~/.claude`, `~/.claude-thinktech` and
  `~/.claude-ifthen`. Claude Code applies a settings change live and releases held messages
  ("crossSessionInbound now accepts"). The teammate's machine gets the line from Brigade at their next bypass
  session start once this ships.
- **The write (owner's preference, 2026-10-05):** `policy.EnsureUserAccept` puts the member into the user
  `settings.json` when the session bypasses prompts and the file has no `crossSessionInbound` at all. Bounds:
  regular file only (a symlink is refused unread), at most `MaxSettingsBytes`, one JSON object; the member is
  inserted first by text so every other byte stays; the result is parsed back and compared member by member
  with the original before `WriteAtomicMode` under the file's own mode; a present member of any value is left
  alone; a missing file is created with the one member at 0600, a missing directory is not created. It runs in
  the SessionStart hook and in the prompt hook before the native scan, so the decision that follows sees the
  file as it now is, and prints one fixed line (`EnsureResult.Line`: added, created, or skipped with a fixed
  reason). The plugin option `claude_inbound_setting` (`true` by default) turns it off, and the refuse-and-warn
  rule below is then the behaviour. Claude Code's settings watcher covers directories that held a settings file
  at session start (a string in the 2.1.289 bundle says so), so an edit to an existing file applies live and a
  created file is read at the next start; the lines say which. docs/security.md §2's "never writes" promise is
  now "writes exactly one thing", with the bounds.
- **`internal/harness/policy`:** `Scan` gains `UserFile`, `UserAccept` (an `accept` at the top level of the
  user file) and `RepoAccept` (the most specific repository file carrying one, named in the warning only).
  `Effective` takes the permission mode: when it is `bypassPermissions`, no native hold/refuse was found, the
  policy is not already refuse and `UserAccept` is false, the policy is `refuse` with `Scan.ParityWarning()` —
  fixed text naming the user file, the one line to add, the repository accept that does not count, and the
  managed/`--settings` blind spot. Plan mode is read as prompting (fails open to accept: the dialog, never a
  silent loss); `""` decides nothing.
- **`hook/prompt.go`:** `redecide` runs the SessionStart decision again at every prompt with the prompt's
  `permission_mode` and a fresh scan; a changed policy is written to the map, the refuse side prints the
  decision's warnings, the accept/hold side prints one fixed line, and `ensureWatcher(…, replace=true)`
  replaces the live watcher so it runs under the new `BRIGADE_TEAM_INBOUND`. Cost: `ParseOptions` and three
  small file reads per prompt.
- **`commands/inbox.go`:** the release refusal names the third cause.
- **Tests:** policy table now 5 options × 5 scans × 7 modes × 4 entrypoints (the mode decides exactly one row
  set); `TestEffectiveBypassNeedsTheUserAccept`, `TestParityWarningText`, `TestScanAcceptPlaces`;
  `hook/parity_test.go` (SessionStart and prompt, accept and hold options, watcher replacement with the new
  env); `hook-prompt.txtar` gains the parity section. The hook fixture's default `ReadFile` now models a user
  file with the accept (the documented setup); `settingsReader` adds it to the doing-rule documents.
- **Docs:** `docs/security.md` §2 (reads at every prompt) and §6 (rewritten: the default's dialog does apply
  to Brigade, where an accept counts, what Brigade does); `docs/setup.md` "Holding messages for review" (the
  line to add and where); `plugin/README.md` option row; `docs/research/claude-code-docs-gaps.md` §9;
  CHANGELOG under Unreleased. The repository's `.claude/settings.json` drops its no-op `accept`.

## 3b. Measured (2026-10-05, Claude Code 2.1.289, this machine)

By hand, since the smoke script is stale (§4): the member removed from `~/.claude/settings.json`; a headless
`claude -p -n card50-live --dangerously-skip-permissions --plugin-dir ./plugin` in this checkout, the dev binary
through a throwaway `XDG_CONFIG_HOME` pointer, Brigade's real store through the `config_dir` option, the
installed plugin disabled through `--settings`, the session environment stripped. Observed: the SessionStart
context line said `inbound: accept` (its document carries no `permission_mode` in `-p`, so the write waited for
the prompt hook); within 20 s the user file had the member first (`accept`); the session's transcript carries
the prompt hook's line `Brigade: added "crossSessionInbound": "accept" to /Users/rjae/.claude/settings.json, …`;
a message sent from this session (`5100eafe…`) arrived mid-turn — the frame is in the transcript, no
`peer_message_hold` anywhere — and the model replied with `brigade send … --reply-to`; the reply (`d9cb7d3e…`)
reached this session, whose explicit accept the same write had restored. A first attempt ended early because the
build blocks a standalone `sleep` in the Bash tool; the second used a bounded `until`-style loop released by a
marker file.

## 4. Follow-ups (not built; owner's call)

- **A watcher that is the session's child by ancestry.** An `async: true` SessionStart hook that never exits
  would be a direct child of Claude Code and pass the own-child verdict with no setting at all. Claude Code does
  not enforce `timeout` on async hooks and kills them at `-p` teardown (documented); the process would have to
  redirect its stdout and stderr to the log file, ignore SIGINT and SIGHUP, and be replaced through the pidfile
  protocol on `/clear`, `--resume` and `/fork`; the prompt hook could keep it alive only through a second async
  entry. It changes the process model every E-series measurement rests on and needs a measured experiment on
  both platforms. Set aside for the write above (owner, 2026-10-05); worth revisiting if Claude Code stops
  honouring the user-file accept for a non-child poster.
- **`scripts/harness-smoke.sh` is stale**: it still drives `brigade profile` (gone since P7-6) and stops before
  Claude Code starts. The card 50 arm (`mode=bypassPermissions`) is in it for the day it is ported.
- ~~The thinktech repositories still carry the no-op `accept`~~ Done 2026-10-05 on the owner's request ("clean up
  the useless repo-level accept wherever it exists"): removed from eleven checkouts under `~/Development` — the
  three aafp repositories, telder, thinktech-api, thinktech-app, thinktech (php) and thinktech-web, one commit per
  repository pushed from one clone each, the duplicate clones pulled; a file that held only the member was
  deleted. The user files keep theirs.
- **Measure the fix live** from a teammate's session into a bypass session with the user-file accept: this
  change was verified by code, documentation and the owner's observation of the hold, not by a delivered
  message.
- **`make plugin-dev mode=bypassPermissions`** now relies on the developer's user file carrying the accept
  (this machine's three do); a `--settings` accept would not be seen by Brigade's scan.
