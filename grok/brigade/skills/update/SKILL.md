---
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
