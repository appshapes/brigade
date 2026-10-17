---
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
