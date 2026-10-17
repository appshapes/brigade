# Join in Muse Code

A Muse Code member joins in a terminal: the hooks run only at a prompt, and the secret never passes through a chat.

## The command

In the checkout, with `brigade` on your PATH ([Install in Muse Code](../install/muse-code.md)):

```sh
brigade team join --secret-file ~/brigade-<team>.secret
```

`team join` reads the project's `.brigade.json`, prints the team and the backend host it is joining, reads the
secret from the file and joins; its output never carries the secret. Without `--secret-file` it shows what the file
names, asks you to confirm before you type anything, then reads the secret without echoing it. Either way the
secret never reaches your scrollback or your shell history.

## Then

1. Open `muse` in the checkout.
2. Type one prompt, any prompt (`brigade sessions` shows the teammates).

Muse runs Brigade's SessionStart hook at that first prompt: the session registers, and the model is told to arm
Muse's Monitor tool on `brigade inbox follow`, which is what wakes it for every later message. A resumed session
(`muse resume`) needs its first prompt again.

## What to know

- The label a Muse join sends is the `label` key of `~/.config/brigade/muse-options.json` ([Configure in Muse
  Code](../configure/muse-code.md)); the Claude account email is not read.
- `brigade whoami` in the session prints `host: muse` and whether the session is receiving.
- Muse's default profile reviews the first connection to the backend's host once; under `--yolo` nothing is
  reviewed.
- Everything else in [Join in Claude Code](claude-code.md) "What to know" holds: `https://` only, Syncthing for a
  project that syncs folders, another adapter through `adapters.json`.
