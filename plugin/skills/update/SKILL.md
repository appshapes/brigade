---
name: update
description: >-
  Move the Brigade plugin to the marketplace's current release from inside this session, then have the user run
  /reload-plugins. Runs only when the user invokes it.
user-invocable: true
disable-model-invocation: true
allowed-tools: Bash(claude plugin:*)
---

# Update the plugin

1. Run `claude plugin marketplace update brigade`, then `claude plugin update brigade@brigade --scope user` —
   one per Bash call, from this session, so they act on this session's configuration directory. User scope first,
   always: when a user-scope install exists it decides the version every folder under this Claude Code account
   loads — every repository and every clone, even one whose project-scope record is newer — so updating it moves
   them all (measured on Claude Code 2.1.270, `docs/experiments/E8-plugin-scope.md`). If it answers
   `not installed at scope user`, the plugin is installed per project: run
   `claude plugin update brigade@brigade --scope project`, which moves this folder only. Relay each command's
   output, and say which of the two happened — every folder of this account, or this one.
2. The new version loads only when the user runs `/reload-plugins` or starts a new session; nothing you can run
   does that. The session's background watcher — the process that heartbeats and injects — is replaced by the new
   version at the next prompt after that (0.5.1; before it, a running session kept its old watcher until it
   ended). End with one line telling them to run `/reload-plugins`. If the update reports the plugin is already
   current, say that instead.
