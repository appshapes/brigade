---
name: update
description: Update an installed Brigade Grok package when the user requests it, preserving native trust and monitor permissions.
---

# Update the Grok package

When the user requests an update of an existing installation, run `grok plugin marketplace update brigade`,
then `grok plugin update brigade`, one command per terminal call, and relay each output. Stop on a refusal.
Do not edit host configuration or trust stores. Ask the user to reload the Plugins tab or start a new session
as the host instructs. After reload/new/load, the hooks bind the new receiver incarnation. Its first eligible Stop offers monitor
setup once; use the team-messaging skill's exact command and ordinary permission flow. Do not claim a native
monitor is running from a startup banner, and do not carry an old incarnation's command into a new session.
