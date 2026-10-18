# Join in Muse Code

A Muse Code member joins from the session, with the `join` skill, or from a terminal. Either way the secret
stays in the file your administrator sent and never passes through a chat.

## From the session

1. Save the secret file outside the project folder, say `~/brigade-<team>.secret`.
2. Open `muse` in the checkout and type one prompt, any prompt: Muse runs Brigade's SessionStart hook at the
   first prompt, and a session in a checkout it has not joined says so in one line.
3. Ask the session to use the `join` skill with the file's path. The skill runs exactly
   `brigade team join --secret-file <path>` on the path you gave, never one it chose, and relays the output,
   which ends `this session attaches at your next prompt`. The secret is read from the file, never printed.
4. Type your next prompt: the session registers, the start line names your team, and the model is told to arm
   Muse's Monitor tool on `brigade inbox follow`, which is what wakes it for every later message.

A second checkout of a team this machine already holds joins the same way with no path: the skill runs
`brigade team join`, which re-consents that checkout.

## From a terminal

In the checkout, with `brigade` on your PATH ([Install in Muse Code](../install/muse-code.md)):

```sh
brigade team join --secret-file ~/brigade-<team>.secret
```

`team join` reads the project's `.brigade.json`, prints the team and the backend host it is joining, reads the
secret from the file and joins; its output never carries the secret. Without `--secret-file` it shows what the file
names, asks you to confirm before you type anything, then reads the secret without echoing it. Then open `muse`
in the checkout and type one prompt; the session registers at that prompt. A resumed session (`muse resume`)
needs its first prompt again.

## What to know

- The label a Muse join sends is the `label` key of `~/.config/brigade/muse-options.json` ([Configure in Muse
  Code](../configure/muse-code.md)); the Claude account email is not read.
- `brigade whoami` in the session prints `host: muse` and whether the session is receiving.
- Muse's default profile reviews the first connection to the backend's host once; under `--yolo` nothing is
  reviewed.
- Everything else in [Join in Claude Code](claude-code.md) "What to know" holds: `https://` only, Syncthing for a
  project that syncs folders, another adapter through `adapters.json`.
