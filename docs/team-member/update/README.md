# Update

A plugin version and the backend's migrations are independent by design: you update when you like, the project
owner migrates when they like, and nothing has to be sequenced.

## If you want to

| If you want to | Go to |
| --- | --- |
| update Brigade in Claude Code | [Update in Claude Code](#update-in-claude-code) |
| update Brigade in Muse Code | [Update in Muse Code](#update-in-muse-code) |
| update Brigade in Codex CLI | [Update in Codex CLI](#update-in-codex-cli) |
| know what an update can change about what your sessions share, and who is behind | [Know what an update changes](#know-what-an-update-changes) |

## Update in Claude Code

1. Nothing to schedule: Claude Code updates Brigade in the background after a session starts and asks you to run
   `/reload-plugins`; do that.
2. To update right now: `/brigade:update`, then `/reload-plugins`.

Details: [Update in Claude Code](claude-code.md).

## Update in Muse Code

1. `muse plugins marketplace update brigade`, then `muse plugins remove brigade`, `muse plugins install brigade@brigade`, `muse plugins approve brigade`.
2. Start a new session and type one prompt; re-point the `~/.local/bin/brigade` symlink.

Details: [Update in Muse Code](muse-code.md).

## Update in Codex CLI

1. `codex plugin marketplace upgrade brigade`, then `codex plugin add brigade@brigade`.
2. Start a new session and type one prompt.

Details: [Update in Codex CLI](codex.md).

## Know what an update changes

1. Read the version's section in `CHANGELOG.md`: it says when a release changes what your sessions share.
2. The VERSION column of `brigade sessions` shows who on the team is behind.

Details: [Know what an update changes](what-an-update-changes.md).
