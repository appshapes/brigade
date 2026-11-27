# Brigade for Antigravity CLI

Status: In trial. It may not work for you yet: [what is not proven](../../docs/team-member/install/antigravity.md#in-trial).

This folder is the Antigravity CLI package of Brigade. Brigade is team messaging between the coding sessions of
different people and machines. With this package, a session of Google's Antigravity CLI (`agy`) takes part
beside Claude Code, Muse Code, Codex CLI and Grok Build sessions. One Brigade team, one backend, five kinds of
session.

A member installs one release by its tag, in a terminal:
[Install in Antigravity CLI](../../docs/team-member/install/antigravity.md). The other procedures are
[join](../../docs/team-member/join/antigravity.md), [update](../../docs/team-member/update/antigravity.md),
[configure](../../docs/team-member/configure/antigravity.md) and
[leave](../../docs/team-member/leave/antigravity.md). What the model reads:
[AI Agent › Antigravity CLI](../../docs/agent/in-a-team/antigravity.md).

## What this package ships

- **`plugin.json`**, the native manifest: the name, the version and the description.
- **`hooks.json`**: three hooks, SessionStart, PreInvocation and Stop. Each runs
  `./bin/brigade hook <event> --host antigravity --plugin-root .`. Antigravity CLI runs a plugin's hook in the
  plugin's folder, so the paths are relative.
- **`bin/`**: copies of the bootstrap, `VERSION` and `checksums.txt` of `plugin/bin`. The release sequence keeps
  them equal, and `make antigravity-plugin-check` checks them.
- **The skills**, under `skills/`: the same fifteen as the other packages, worded for Antigravity CLI. A user
  invokes one as `/brigade:<name>`.

## What is different from the Claude Code package, and why

- **A message arrives at the next model call.** The watcher keeps each message in a private local file. The
  PreInvocation hook adds the new messages to the conversation as user messages before each model call. A hook
  does not get what `agy agentapi send-message` needs, so nothing wakes an idle session yet (work item 533).
- **No marketplace entry.** `agy plugin install` installs a folder at a tag, so a push to `master` reaches no
  member. A change here reaches a member when they install a newer release.
- **A session is one conversation**, and the `agy` process tells if it is alive. There is no session-end hook:
  the watcher ends the session when the process stops.
- **Options** come from `~/.config/brigade/antigravity-options.json`. Brigade's directories come from `HOME`
  alone.
- **`brigade` is not on the PATH of the model's shell.** The session-start line names the bootstrap's path.

The decision: [ADR 0039](../../docs/decisions/0039-take-antigravity-cli-in-as-a-harness-that-gets-a-message-at-its-next-model-call.md).
The measurements: [the live test](../../docs/experiments/antigravity-participation.md).
