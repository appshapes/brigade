{{/* update is a block for each harness: what refreshes a package and loads it again is the harness's own
     commands from the first line to the last. Change the block of the harness the change is for. */ -}}
{{if in "claude"}}---
name: update
description: >-
  Move the Brigade plugin to the marketplace's current release from inside this session, then have the user run
  /reload-plugins. Use when the user asks to update Brigade, or when you find this session's plugin is behind
  the marketplace's release and moving it is the next useful step.
user-invocable: true
allowed-tools: Bash(claude plugin:*)
---

# Update the plugin

1. Run `claude plugin marketplace update brigade`, then `claude plugin update brigade@brigade --scope user`, then
   `claude plugin update brigade@brigade --scope project` — one per Bash call, from this session, so they act on
   this session's configuration directory. Update BOTH scopes: the user-scope install is the one Brigade's docs
   ask for and it serves every folder under this account; a project-scope install exists only where a repository
   committed `enabledPlugins` for Brigade, and in that folder it is the one a session loads (settings precedence:
   project over user). An answer of `not installed at scope …` is not a failure — it means that scope has no
   install — so relay it in one clause and move on. Relay each command's output. (Updates also arrive on their
   own when the marketplace entry carries `autoUpdate: true`; this skill is for updating right now.)
2. The new version loads only when the user runs `/reload-plugins` or starts a new session; nothing you can run
   does that. The session's background watcher — the process that heartbeats and injects — is replaced by the new
   version at the next prompt after that (0.5.1; before it, a running session kept its old watcher until it
   ended). End with one line telling them to run `/reload-plugins`. If the update reports the plugin is already
   current, say that instead.
{{end}}{{if in "muse"}}---
name: update
description: >-
  Move the Brigade plugin to the repository's current release from inside this Muse Code session, then have the
  user start a new session. Use when the user asks to update Brigade, or when you find this session's plugin is
  behind the release and moving it is the next useful step.
user-invocable: true
---

# Update the plugin

1. Find out how the package was installed: run `muse plugins inspect brigade --json` and read
   `record.source.provenance`.
   - `marketplace-user-added`, the normal install from this repository's marketplace: run, one per shell call,
     relaying each command's output, `muse plugins marketplace update brigade`, then `muse plugins remove brigade`,
     then `muse plugins install brigade@brigade`. The reinstall is needed because Muse's `plugins update` re-reads
     the first snapshot of a git marketplace, not the refreshed one. Then tell the user to approve the hooks
     themselves, `muse plugins approve brigade` in their terminal or `/plugins`, Installed, `brigade`, Runtime:
     approval is the person's review, and this skill never runs it. If Muse refuses a command because the plugin
     is in use, give the user the three commands to run in a terminal and stop.
   - `native-local`, a developer's install from a clone of the Brigade repository: run `git pull --no-edit` in the
     checkout the user named (never one you chose), then `muse plugins update brigade`. If the checkout is not
     known, ask the user where it is and stop; do not clone one.
2. The new version loads only in a new session; nothing you can run does that. This session keeps running on the
   version it loaded; its background watcher, the process that heartbeats and receives, is replaced by the new
   version at the first prompt of a new session, and its end-of-session hook went with the old package, so
   Brigade's watcher closes this session by liveness instead. End with one line telling the user to start a new
   session and to type one prompt in it (which is when Brigade registers a Muse session); the session-start line
   then names the new package's path, which is the command in the session and in their terminal. If the update
   reports the plugin is already current, say that instead.
{{end}}{{if in "codex"}}---
name: update
description: >-
  Move the Brigade plugin to the marketplace's current release from inside this Codex CLI session, then have the
  user start a new session. Use when the user asks to update Brigade, or when you find this session's plugin is
  behind the release and moving it is the next useful step.
---

# Update the plugin

1. The package is installed from the `brigade` marketplace (`codex plugin marketplace add appshapes/brigade`,
   then `codex plugin add brigade@brigade`), so updating is refreshing the marketplace's snapshot and installing
   again: run `codex plugin marketplace upgrade brigade`, then `codex plugin add brigade@brigade`, one per shell
   call, and relay each command's output. Never pass `-c`, `--enable` or `--disable` to either.
2. The new version loads only in a new session; nothing you can run does that. The session's background watcher
   — the process that heartbeats and receives — is replaced by the new version at the next prompt of a new
   session. End with one line telling the user to start a new session (and to type one prompt in it, which is
   when Brigade registers a Codex session). If the install reports the plugin is already current, say that
   instead.
{{end}}{{if in "grok"}}---
name: update
description: >-
  Move the installed Brigade Grok package to the marketplace's current content from inside this session,
  preserving native trust and monitor permissions: refresh the `brigade` marketplace, update the plugin, then
  have the user reload the Plugins tab or start a new `grok --no-leader` session. Use when the user asks to
  update Brigade, or when you find this session's plugin is behind the release and moving it is the next
  useful step.
---

# Update the Grok package

1. The package is installed from the `brigade` marketplace (`grok plugin marketplace add appshapes/brigade`,
   then `grok plugin install brigade --trust`), so updating is refreshing the marketplace's snapshot and
   updating the plugin from it. Where `brigade whoami` prints `strict: on` (the member's option `grok_strict`),
   do this only when the user requests the update. Run
   `grok plugin marketplace update brigade`, then `grok plugin update brigade`, one command per terminal call,
   and relay each output: the second prints the version it left and the one it reached (`updated (0.57.0 →
   0.58.0)`). Stop on a refusal. Do not edit host configuration or trust stores.
2. The new version loads only after a reload: nothing you can run does that. Ask the user to reload the Plugins
   tab or start a new `grok --no-leader` session, as the host instructs. After a reload, a new session or a
   load, the hooks bind the new receiver incarnation, and the session's background watcher — the process that
   heartbeats and receives — is replaced by the new version at a prompt.
3. The new incarnation's first eligible Stop offers monitor setup once; use the `team-messaging` skill's exact
   command and ordinary permission flow. Do not claim a native monitor is running from a startup banner, and do
   not carry an old incarnation's command into a new session.
{{end}}