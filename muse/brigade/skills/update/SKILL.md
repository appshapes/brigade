---
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
   session, to type one prompt in it (which is when Brigade registers a Muse session) and to re-point their
   `~/.local/bin/brigade` symlink at the path the session-start line names. If the update reports the plugin is
   already current, say that instead.
