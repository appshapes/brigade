---
name: update
description: >-
  Move the Brigade plugin to the repository's current release from inside this Muse Code session, then have the
  user start a new session. Use when the user asks to update Brigade, or when you find this session's plugin is
  behind the release and moving it is the next useful step.
user-invocable: true
---

# Update the plugin

1. The package is installed from a checkout of the Brigade repository (`muse plugins install ./muse/brigade
   --scope user`), so updating is pulling that checkout and refreshing the install: run `git pull --no-edit` in
   the Brigade checkout the user named (never one you chose), then `muse plugins update brigade`, one per shell
   call, and relay each command's output. If the checkout is not known, ask the user where it is and stop; do
   not clone one.
2. The new version loads only in a new session; nothing you can run does that. The session's background watcher
   — the process that heartbeats and receives — is replaced by the new version at the next prompt of a new
   session. End with one line telling the user to start a new session (and to type one prompt in it, which is
   when Brigade registers a Muse session). If the update reports the plugin is already current, say that instead.
