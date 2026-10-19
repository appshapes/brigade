---
name: update
description: Update an installed Brigade Grok package when the user requests it, preserving native trust and reporting the experimental delivery gate.
---

# Update the Grok package

When the user requests an update of an existing installation, run `grok plugin marketplace update brigade`,
then `grok plugin update brigade`, one command per terminal call, and relay each output. Stop on a refusal.
Do not edit host configuration or trust stores. Ask the user to reload the Plugins tab or start a new session
as the host instructs. This does not establish Brigade registration or delivery: the package remains gated
until the measured integration contract is implemented and released. Do not claim a watcher was replaced.
