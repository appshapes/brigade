# Install

Brigade installs into the harness your sessions run in. Nothing is compiled and no server is started: the package
downloads one pinned, checksum-verified binary on first use.

## If you want to

| If you want to | Go to |
| --- | --- |
| install Brigade in Claude Code | [Install in Claude Code](#install-in-claude-code) |
| install Brigade in Muse Code | [Install in Muse Code](#install-in-muse-code) |
| run `brigade` in your own terminal, or build it from source | [Use brigade in a terminal](#use-brigade-in-a-terminal) |

## Install in Claude Code

1. Open Claude Code in a project that uses Brigade and trust the folder when it asks.
2. Type `/plugin install brigade@brigade`. When it asks where to install, choose **user**.
3. Type `/reload-plugins`.

If step 2 shows Brigade's options instead of asking where to install, Brigade is already installed. The first
machine of a project runs `/plugin marketplace add appshapes/brigade` first.

Details: [Install in Claude Code](claude-code.md).

## Install in Muse Code

1. From a clone of this repository: `muse plugins install ./muse/brigade --scope user`.
2. `muse plugins approve brigade` (Muse's review step: hooks stay inactive until you approve them).
3. Join in a terminal, then open `muse` in the checkout and type one prompt.

Details: [Install in Muse Code](muse-code.md).

## Use brigade in a terminal

1. In a session, `brigade whoami`; copy its `terminal:` line.
2. `ln -sf <that path> ~/.local/bin/brigade`; repeat after a plugin upgrade, because the path carries the version.
3. `go install github.com/appshapes/brigade/cmd/brigade@latest` builds the command-line tool alone: no hooks, no
   watcher, no session integration.

Details: [Use brigade in a terminal](terminal.md).
