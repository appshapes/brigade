---
name: update
description: >-
  Move the Brigade plugin to a newer release from inside this Antigravity CLI session, then have the user start
  a new `agy` session. Use when the user asks to update Brigade, or when you find that this session's plugin is
  behind the release and the move is the next useful step.
---

# Update the plugin

1. The package has no marketplace entry. `agy plugin install` installs one release from its tag, and an install
   of a newer tag replaces the installed copy. If the user did not name a version, ask them for it. The releases
   are at `https://github.com/appshapes/brigade/releases`.
2. Run this command in one `run_command` call, with the version in the tag, and relay its output:
   `agy plugin install https://github.com/appshapes/brigade/tree/v<version>/antigravity/brigade`.
   If the command fails, give the user the same command to run in their terminal, and stop.
3. The new version loads only in a new `agy` session, because `agy` loads the hooks when a session starts. You
   cannot start a new session. In the new session, the new version replaces the background watcher, the process
   that sends heartbeats and receives messages. End with one line that tells the user to start a new `agy`
   session.
