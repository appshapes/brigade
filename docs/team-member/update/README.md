# Update

A plugin version and the backend's migrations are independent by design: you update when you like, the project
owner migrates when they like, and nothing has to be sequenced.

## If you want to

| If you want to | Go to |
| --- | --- |
| update Brigade in Claude Code | [Update in Claude Code](#update-in-claude-code) |
| update Brigade in Muse Code | [Update in Muse Code](#update-in-muse-code) |
| update Brigade in Codex CLI | [Update in Codex CLI](#update-in-codex-cli) |
| update Brigade in Grok Build | [Update in Grok Build](#update-in-grok-build) |
| know what an update can change about what your sessions share, and who is behind | [Know what an update changes](#know-what-an-update-changes) |

## Update in Claude Code

1. Nothing to schedule: Claude Code updates Brigade in the background after a session starts and asks you to run
   `/reload-plugins`; do that.
2. To update right now: `/brigade:update`, then `/reload-plugins`.

Details: [Update in Claude Code](claude-code.md).

## Update in Muse Code

1. `muse plugins marketplace update brigade`, then `muse plugins remove brigade`, `muse plugins install brigade@brigade`, `muse plugins approve brigade`.
2. Start a new session and type one prompt; your terminal runs the path `brigade whoami` now prints.

Details: [Update in Muse Code](muse-code.md).

## Update in Codex CLI

1. `codex plugin marketplace upgrade brigade`, then `codex plugin add brigade@brigade`.
2. Start a new session. Codex asks to review Brigade's hooks again: choose **Trust all and continue**. If a
   session shows `Hook failed: plugin contents changed; hook approval required` instead, quit and start again.
3. Type one prompt.

Details: [Update in Codex CLI](codex.md).

## Update in Grok Build

1. `grok plugin marketplace update brigade`, then `grok plugin update brigade`.
2. Reload through Grok's Plugins tab or start a new `grok --no-leader` session; its first turn asks once to arm
   the inbox monitor again.

Details: [Update in Grok Build](grok.md).

## Know what an update changes

1. Read the first lines of the version's section in `CHANGELOG.md`, "Who must act": the Team Member line says
   what you have to do before or after the update, or `nothing`. A section up to 0.57.0 has no such lines: read
   its `Changed` entries, which say when a release changes what your sessions share.
2. The VERSION column of `brigade sessions` shows who on the team is behind.

Details: [Know what an update changes](what-an-update-changes.md).
