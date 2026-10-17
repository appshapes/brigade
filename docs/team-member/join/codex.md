# Join in Codex CLI

A Codex CLI member joins from the session, with `$brigade:join`, or from a terminal. Either way the secret stays in the
file your administrator sent and never passes through a chat.

## From the session

1. Save the secret file outside the project folder, say `~/brigade-<team>.secret`.
2. Open `codex` in the checkout and type one prompt, any prompt: Codex runs Brigade's SessionStart hook at the
   first prompt, and a session in a checkout it has not joined says so in one line.
3. Type `$brigade:join ~/brigade-<team>.secret`. The skill runs exactly `brigade team join --secret-file <path>` on the
   path you gave, never one it chose, and relays the output, which ends `this session attaches at your next
   prompt`. The secret is read from the file, never printed. Under Codex's default profile the join reaches the
   backend from the model's shell, so Codex asks for your approval once; under
   `--dangerously-bypass-approvals-and-sandbox` nothing is asked.
4. Type your next prompt: the session registers, the start line names your team, and from then on every team
   message arrives as a turn of its own.

A second checkout of a team this machine already holds joins the same way with `$brigade:join` and no path: the skill
runs `brigade team join`, which re-consents that checkout.

## From a terminal

In the checkout, with `brigade` on your PATH ([Install in Codex CLI](../install/codex.md)):

```sh
brigade team join --secret-file ~/brigade-<team>.secret
```

`team join` reads the project's `.brigade.json`, prints the team and the backend host it is joining, reads the
secret from the file and joins; its output never carries the secret. Without `--secret-file` it shows what the file
names, asks you to confirm before you type anything, then reads the secret without echoing it. Then open `codex`
in the checkout and type one prompt; the session registers at that prompt. A resumed session (`codex resume`) and
the new thread `/new` opens each register at their first prompt.

## What to know

- The label a Codex join sends is the `label` key of `~/.config/brigade/codex-options.json` ([Configure in Codex
  CLI](../configure/codex.md)); the Claude account email is not read.
- `brigade whoami` in the session prints `host: codex` and whether the session is receiving.
- Under Codex's default profile a `brigade` command that reaches the backend asks for approval, because the
  sandbox has the network off; under `--dangerously-bypass-approvals-and-sandbox` nothing is asked.
- Everything else in [Join in Claude Code](claude-code.md) "What to know" holds: `https://` only, Syncthing for a
  project that syncs folders, another adapter through `adapters.json`.
