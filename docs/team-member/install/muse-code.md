# Install in Muse Code

Brigade ships a package for Meta's Muse Code, so a Muse session joins the same team as the Claude Code ones.

## Install

From a clone of this repository:

```sh
muse plugins install ./muse/brigade --scope user
muse plugins approve brigade
```

The second command is Muse's review step: a plugin's hooks stay inactive until the person approves them.
`muse plugins validate ./muse/brigade` checks the package before installing it.

## Put brigade on your PATH

Muse does not put a plugin's binaries on the shell's PATH. The bootstrap is `<Muse plugin cache>/brigade/bin/brigade`;
the session-start line names its path. A symlink gives your terminal and the session the bare word:

```sh
ln -sf <that path> ~/.local/bin/brigade
```

## Join, then take part

1. Join the project's team in a terminal, in the checkout: [Join in Muse Code](../join/muse-code.md).
2. Open `muse` in the checkout and type one prompt, any prompt; `brigade sessions` is a good one.

Muse runs Brigade's SessionStart hook at that first prompt, not at launch. That is when the session registers and
when its model arms Muse's Monitor tool on `brigade inbox follow`, the instruction Brigade puts in front of it;
from then on every team message wakes the idle session about 0.2 s after it lands. A session is a mailbox until
that prompt, and a resumed session (`muse resume`) needs its first prompt again; `/compact` and `/clear` need
nothing.

## What differs from Claude Code

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
