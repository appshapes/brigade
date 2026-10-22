# Install

Brigade installs into the harness your sessions run in. Nothing is compiled and no server is started: the package
downloads one pinned, checksum-verified binary on first use.

## If you want to

| If you want to | Go to |
| --- | --- |
| install Brigade in Claude Code | [Install in Claude Code](#install-in-claude-code) |
| install Brigade in Muse Code | [Install in Muse Code](#install-in-muse-code) |
| install Brigade in Codex CLI | [Install in Codex CLI](#install-in-codex-cli) |
| run `brigade` in your own terminal, or build it from source | [Use brigade in a terminal](#use-brigade-in-a-terminal) |
| use Grok Build install status | [Grok Build](#grok-build) |

## Install in Claude Code

1. Open Claude Code in a project that uses Brigade and trust the folder when it asks.
2. Type `/plugin install brigade@brigade`. When it asks where to install, choose **user**.
3. Type `/reload-plugins`.

If step 2 shows Brigade's options instead of asking where to install, Brigade is already installed. The first
machine of a project runs `/plugin marketplace add appshapes/brigade` first.

Details: [Install in Claude Code](claude-code.md).

## Install in Muse Code

1. In a Muse session: `/plugins marketplace add brigade appshapes/brigade`, then `/plugins install brigade@brigade`.
2. Approve the hooks: `/plugins`, Installed, `brigade`, Runtime; or `muse plugins approve brigade` in a terminal.
3. Join in a terminal, then type one prompt in the session.

The same in a terminal: `muse plugins marketplace add brigade appshapes/brigade`, then `muse plugins install brigade@brigade`.

Details: [Install in Muse Code](muse-code.md).

## Install in Codex CLI

1. In a terminal: `codex plugin marketplace add appshapes/brigade`, then `codex plugin add brigade@brigade`.
2. The next `codex` asks to trust the plugin's hooks: choose Trust all and continue (or `/hooks`, then `t`, later).
3. Join in a terminal, then open `codex` in the checkout and type one prompt.

Details: [Install in Codex CLI](codex.md).

## Use brigade in a terminal

1. In a session, `brigade whoami`; copy its `terminal:` line.
2. Run that path in your terminal with the command after it, such as `<that path> inbox release --all`. No
   symlink and no copy: both go stale at the next upgrade.
3. `go install github.com/appshapes/brigade/cmd/brigade@latest` builds the command-line tool alone: no hooks, no
   watcher, no session integration.

Details: [Use brigade in a terminal](terminal.md).

## Grok Build

Use a normally installed/trusted package in `grok --no-leader`; the inbox monitor starts through normal command permission.

Details: [Grok Build](grok.md).
