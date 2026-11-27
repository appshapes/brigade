# Join in Antigravity CLI

An Antigravity CLI member joins from the session, with `/brigade:join`, or from a terminal. The secret stays in
the file that your administrator sent, and never goes through a chat.

## From the session

1. Save the secret file outside the project folder, for example `~/brigade-<team>.secret`.
2. Start `agy` in the checkout. A session in a checkout that has not joined says so in one line.
3. Type `/brigade:join ~/brigade-<team>.secret`. The skill runs `brigade team join --secret-file <path>` on the
   path that you gave, and shows the output. It never reads or shows the secret.
4. Type your next prompt. The session attaches, and the start line names your team.

In `auto` mode, the join may need to run outside Antigravity's command sandbox, which asks for your approval.

A second checkout of a team that this machine already holds joins with `/brigade:join` and no path.

## From a terminal

In the checkout, run the bootstrap by its path ([Where brigade is](../install/antigravity.md#where-brigade-is)):

```sh
~/.gemini/config/plugins/brigade/bin/brigade team join --secret-file ~/brigade-<team>.secret
```

Then start a new `agy` session in the checkout and type one prompt. The session registers at that prompt.

## What to know

- The join sends the `label` key of `~/.config/brigade/antigravity-options.json`
  ([Configure in Antigravity CLI](../configure/antigravity.md)). If you do not set it, it is `account`: where a
  Claude account is on this machine, the join sends its email address ([Choose your label](your-label.md)). Set
  `none`, or a text, before you join.
- `brigade whoami` in the session prints `host: antigravity`.
- Each `agy` conversation is a session of its own: `/new` starts a new session.
- The other items of [Join in Claude Code](claude-code.md) "What to know" apply too: `https://` only, Syncthing
  for a project that syncs folders, another adapter through `adapters.json`.
