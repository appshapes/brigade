# Install in Antigravity CLI

Brigade ships a package for Google's Antigravity CLI, the `agy` command. An `agy` session joins the same team as
the Claude Code, Muse Code, Codex CLI and Grok Build sessions. It gets a team message before its next model call.

Status: In trial. It may not work for you yet: [In trial](#in-trial) below names what is not proven.

## Install

In a terminal, put the version of a Brigade release in the tag:

```sh
agy plugin install https://github.com/appshapes/brigade/tree/v<version>/antigravity/brigade
```

`<version>` is a release number, without the `v`. The [Releases page](https://github.com/appshapes/brigade/releases)
of the repository lists each release tag. The first release with this package is the release after 0.61.0.

`agy` clones the repository at that tag and installs the `antigravity/brigade` folder into
`~/.gemini/config/plugins/brigade/`. `agy plugin list` shows the plugin. After the install, start a new `agy`
session: `agy` loads the hooks when a session starts.

## What you get, and when

You get the package of the release that you name, and nothing more. The package has no marketplace entry, so a
push to this repository changes nothing for you. To move to a newer release, install again with the newer tag:
[Update in Antigravity CLI](../update/antigravity.md).

## Where brigade is

Antigravity CLI does not put `brigade` on the PATH of the model's shell. The command is the package's bootstrap,
`~/.gemini/config/plugins/brigade/bin/brigade`. The session-start line names this path. Use the same path in
the session and in your terminal ([Use brigade in a terminal](terminal.md)).

## Join, then take part

1. Join the project's team: [Join in Antigravity CLI](../join/antigravity.md).
2. Start `agy` in the checkout and type one prompt.

The package has three hooks. SessionStart registers the session, and `agy` gives it 60 seconds. PreInvocation
runs before each model call and gives the model the team messages that arrived. One answer carries at most about
64 KiB of messages; the rest comes at the next model call. Stop runs at the end of a turn. If a message arrived
during a turn that ended by itself, Stop makes the turn continue one time, and the message comes at the next
model call.

A session is one `agy` conversation. `/new` starts a new conversation, and so a new Brigade session. The old
session stays on the roster until `agy` stops. `agy --conversation <id>` (or `agy -r <id>`) opens a conversation
again, and the session registers again. Antigravity CLI has no session-end hook: the watcher ends the session
when the `agy` process stops.

**Start `agy` from a plain terminal.** If you start `agy` from the shell of another harness's session (Claude
Code, Muse Code, Codex CLI or Grok Build), its environment also carries that harness's session marker. Then
Brigade cannot prove the session's identity: the hooks register no session and start no watcher, and the start
says so in one line. A Grok Build session that an `agy` agent's command starts has no Brigade either, by Grok
Build's own rule. A Codex CLI session that an `agy` agent's command starts joins as a Codex CLI session.

## In trial

The measurements are on agy 1.5.18 on macOS, on 2026-11-27
([the live test](../../experiments/antigravity-participation.md)). These parts are not proven or not done:

- **Nothing wakes an idle session yet** (work item 533). A message to an idle session waits for your next
  prompt.
- **The roster shows no context yet** (work item 534). It shows the model, which the hook gives.
- **Only the CLI was tested.** The Antigravity App and the Antigravity IDE were not tested.
- **One platform.** Only macOS was measured. The package does not run on Windows.
- **The injection test set was not run** against Antigravity's models.
- **The sandbox can ask for approval.** In `auto` mode, Antigravity's command sandbox applies to the commands
  of the agent. A `brigade send` that the agent runs may need to run outside the sandbox, which asks for your
  approval.
- **The roster shows the conversation's title.** That is the title that `agy` makes after the first turn, or the
  name that you give with `/rename` in `agy`. Until a title exists, the roster shows the name of the folder.
  `brigade rename` does not work in an `agy` session: use `/rename`.
- **Brigade does not ask for the roster line.** Brigade never reminds the model to set the session's
  sentence on the roster, and never asks for a note at the end of a turn. Both need a permission mode, and the
  hooks of `agy` give none. In `auto` mode, a `brigade doing` can also need your approval to run outside the
  sandbox. You or the model can still run `brigade doing` and `brigade note` by hand.
- **A message can come two times.** The hook moves its read position after it writes its answer. If `agy`
  stops the hook between the two steps, the next model call gets the same messages again. This is rare. The
  skill tells the model to compare the `message-id` before it acts on a message again.

## What differs from Claude Code

- **Options** come from `~/.config/brigade/antigravity-options.json`, not from plugin settings:
  [Configure in Antigravity CLI](../configure/antigravity.md).
- **A message is a user message** that the hook adds to the conversation. Its first sentence says that you did
  not type it. Brigade's own lines, such as the start line, are user messages too. The one system message is
  the line of the Stop hook that says a team message arrived during the turn.
- **`poll_on_prompt` does nothing.** A message always comes from the watcher's local file.
- **Antigravity's own messaging** (`agy agentapi send-message`, the `send_message` tool) cannot reach a Brigade
  session.

The package itself: [antigravity/brigade/README.md](../../../antigravity/brigade/README.md).
