# Join in Codex CLI

A Codex CLI member joins in a terminal: the hooks run only at a prompt, and the secret never passes through a chat.

## The command

In the checkout, with `brigade` on your PATH ([Install in Codex CLI](../install/codex.md)):

```sh
brigade team join --secret-file ~/brigade-<team>.secret
```

`team join` reads the project's `.brigade.json`, prints the team and the backend host it is joining, reads the
secret from the file and joins; its output never carries the secret. Without `--secret-file` it shows what the file
names, asks you to confirm before you type anything, then reads the secret without echoing it. Either way the
secret never reaches your scrollback or your shell history.

## Then

1. Open `codex` in the checkout.
2. Type one prompt, any prompt (`brigade sessions` shows the teammates).

Codex runs Brigade's SessionStart hook at that first prompt: the session registers, and from then on every team
message arrives as a turn of its own. A resumed session (`codex resume`) and the new thread `/new` opens each
register at their first prompt.

## What to know

- The label a Codex join sends is the `label` key of `~/.config/brigade/codex-options.json` ([Configure in Codex
  CLI](../configure/codex.md)); the Claude account email is not read.
- `brigade whoami` in the session prints `host: codex` and whether the session is receiving.
- Under Codex's default profile a `brigade` command that reaches the backend asks for approval, because the
  sandbox has the network off; under `--dangerously-bypass-approvals-and-sandbox` nothing is asked.
- Everything else in [Join in Claude Code](claude-code.md) "What to know" holds: `https://` only, Syncthing for a
  project that syncs folders, another adapter through `adapters.json`.
