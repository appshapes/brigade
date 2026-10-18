# Install in Muse Code

Brigade ships a package for Meta's Muse Code, so a Muse session joins the same team as the Claude Code and Codex
CLI ones. Muse installs it from this repository's marketplace catalog, from inside a session or from a terminal.

## In a session

1. `/plugins marketplace add brigade appshapes/brigade`: Muse clones the repository, reads its `marketplace.json`
   and stores a snapshot; the plugin panel opens on its Marketplaces tab with `brigade` listed.
2. `/plugins install brigade@brigade`: Muse installs the package at user scope and checks it against the digest the
   catalog carries, so a package that differs from the one the release recorded is refused.
3. Approve the hooks, which stay inactive until a person reviews them: `/plugins`, the Installed tab, `brigade`,
   the Runtime tab; or `muse plugins approve brigade` in a terminal.

## In a terminal

```sh
muse plugins marketplace add brigade appshapes/brigade
muse plugins install brigade@brigade
muse plugins approve brigade
```

`appshapes/brigade` is GitHub shorthand for `https://github.com/appshapes/brigade.git`; a git URL or a local clone
works the same, and `#<ref>` after the source pins a branch or a tag. Muse reads the snapshot offline afterwards:
[Update in Muse Code](../update/muse-code.md) refreshes it.

## From a clone (developers)

```sh
muse plugins install ./muse/brigade --scope user
muse plugins approve brigade
```

A package installed this way follows the clone: `git pull`, then `muse plugins update brigade`.
`muse plugins validate ./muse/brigade` checks the package before installing it.

## Put brigade on your PATH

Muse does not put a plugin's binaries on the shell's PATH. The bootstrap is `bin/brigade` under the installed
package in Muse's plugin cache; the session-start line names its path. A symlink gives your terminal and the session
the bare word:

```sh
ln -sf <that path> ~/.local/bin/brigade
```

The path carries the package's digest, so re-point the symlink after an update.

## Join, then take part

1. Join the project's team in a terminal, in the checkout: [Join in Muse Code](../join/muse-code.md).
2. Open `muse` in the checkout and type one prompt, any prompt; `brigade sessions` is a good one.

Muse runs Brigade's SessionStart hook at that first prompt, not at launch. That is when the session registers and
when its model arms Muse's Monitor tool on `brigade inbox follow`, the instruction Brigade puts in front of it;
from then on every team message wakes the idle session about 0.2 s after it lands. A session is a mailbox until
that prompt, and a resumed session (`muse resume`) needs its first prompt again; `/compact` and `/clear` need
nothing.

## What differs from Claude Code

- **The catalog.** Muse reads `marketplace.json` at the repository root; Claude Code reads
  `.claude-plugin/marketplace.json` and Codex CLI `.agents/plugins/marketplace.json`. Muse takes the first of the
  three it finds, in that order, and the native one carries the package digest Muse requires.
- **Options** come from `~/.config/brigade/muse-options.json`, not plugin settings: [Configure in Muse Code](../configure/muse-code.md).
- **Directories.** A Muse session resolves Brigade's directories from `HOME` alone, so `~/.config/brigade` and
  `~/.local/state/brigade` whatever `XDG_*` says; a member who sets `XDG_CONFIG_HOME` puts `config_dir` in the
  options file.
- **Approval.** Muse's default profile reviews each tool call and confines the shell to the workspace, with one
  review on the first connection to the backend's host; `--yolo` reviews nothing. Brigade never writes under the
  workspace.
- **Privacy.** Muse reads a person's `~/.claude` skills and rules into its context by default and sends them to
  Meta with every prompt; Brigade's hooks do not. The live test ran with Muse's kill switch
  (`MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=on`).

What Muse's own layers add and what has not been measured: [docs/security.md, section 15](../../security.md#15-muse-code-sessions).
The package itself: [muse/brigade/README.md](../../../muse/brigade/README.md); the measurements behind it:
[the live test](../../experiments/muse-participation.md).
