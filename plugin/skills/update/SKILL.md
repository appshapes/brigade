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

1. Run `claude plugin marketplace update brigade`, then `claude plugin update brigade@brigade` — one per Bash call,
   from this session, so they act on this session's configuration directory. Relay each command's output.
2. The new version loads only when the user runs `/reload-plugins` or starts a new session; nothing you can run
   does that. End with one line telling them to run `/reload-plugins`. If the update reports the plugin is already
   current, say that instead.
