# Antigravity CLI: the skills a session loads, and how a message arrives

The Antigravity CLI package (`antigravity/brigade/`) ships the same fifteen skills, worded for Antigravity CLI. A
user invokes one as `/brigade:<name>`. The skills are the model's documentation in a team. This page names them
and says what differs from Claude Code.

| Skill | What it carries |
| --- | --- |
| [`antigravity/brigade/skills/team-messaging/SKILL.md`](../../../antigravity/brigade/skills/team-messaging/SKILL.md) | the command surface, the rules to send, the inbound frame, the gateways, the roster line, the notes, decisions and digest of a project with a state folder |
| [`antigravity/brigade/skills/sessions/SKILL.md`](../../../antigravity/brigade/skills/sessions/SKILL.md) | print the roster as it is |
| [`antigravity/brigade/skills/whoami/SKILL.md`](../../../antigravity/brigade/skills/whoami/SKILL.md) | print `brigade whoami`, the identity of this session, as it is |
| [`antigravity/brigade/skills/join/SKILL.md`](../../../antigravity/brigade/skills/join/SKILL.md) | run `brigade team join --secret-file <path>` on the file that the user named |
| [`antigravity/brigade/skills/move/SKILL.md`](../../../antigravity/brigade/skills/move/SKILL.md) | ask the administrator for the server's URL and the paths it needs, never for a secret; run `brigade team move --to <url> --secret-file <path>` and print its output; the members run nothing |
| [`antigravity/brigade/skills/update/SKILL.md`](../../../antigravity/brigade/skills/update/SKILL.md) | install a newer release by its tag, then tell the user to start a new `agy` session |
| [`antigravity/brigade/skills/setup/SKILL.md`](../../../antigravity/brigade/skills/setup/SKILL.md) | how a person creates a team, installs the package, joins, leaves and uninstalls, with every command and never a secret in the chat |
| [`antigravity/brigade/skills/email/SKILL.md`](../../../antigravity/brigade/skills/email/SKILL.md) | one `brigade send` to the mail gateway |
| [`antigravity/brigade/skills/slack/SKILL.md`](../../../antigravity/brigade/skills/slack/SKILL.md) | one `brigade send` to the Slack gateway |
| [`antigravity/brigade/skills/quiet/SKILL.md`](../../../antigravity/brigade/skills/quiet/SKILL.md) | make this one session quiet, and return it |
| [`antigravity/brigade/skills/subscribe/SKILL.md`](../../../antigravity/brigade/skills/subscribe/SKILL.md) | the user's own subscription to the digest, and `off` |
| [`antigravity/brigade/skills/work-items/SKILL.md`](../../../antigravity/brigade/skills/work-items/SKILL.md) | print the Brigade Board, the team's board of work items, as it is |
| [`antigravity/brigade/skills/work-item/SKILL.md`](../../../antigravity/brigade/skills/work-item/SKILL.md) | read, add or change one work item, or name, rename, hide or unhide the board's lists |
| [`antigravity/brigade/skills/work-item-start/SKILL.md`](../../../antigravity/brigade/skills/work-item-start/SKILL.md) | take a work item and complete it; with other sessions of the user's named after the number, lead them as followers |
| [`antigravity/brigade/skills/work-item-next/SKILL.md`](../../../antigravity/brigade/skills/work-item-next/SKILL.md) | say which work item to take next and why, from the board and the roster; it changes nothing |

What differs from Claude Code:

- **A message arrives at the next model call.** The watcher keeps each message in a private local file. Before
  the next model call, the PreInvocation hook adds it to the conversation as a user message: a
  `<brigade-message>` frame that your user did not type. One answer carries at most about 64 KiB of messages;
  the rest follows at the next model call. Brigade's own lines, such as the start line, are user messages too.
- **The Stop line.** If a message arrived during a turn that ended by itself, the Stop hook makes the turn
  continue one time. Its reason comes to you as a system message, one fixed line: "Brigade: a team message
  arrived during this turn. It follows now, as a message your user did not type." The message follows at the
  next model call.
- **No ask for the roster line or a note.** Brigade never reminds you to run `brigade doing`, and never asks
  for a `brigade note` at the end of a turn. Run them when the work changes, or after a commit.
- **Another harness's marker.** If `agy` started from the shell of another harness's session, Brigade is off:
  the start says so in one line, and no session registers. Tell your user to start `agy` from a plain terminal.
- **Nothing wakes an idle session yet** (work item 533). A message to an idle session waits for the next prompt
  of your user. There is no monitor to arm.
- **The command.** `brigade` is not on your PATH. Run the bootstrap by the path that the session-start line
  names, under `~/.gemini/config/plugins/brigade/bin/brigade`. Run one `brigade` command in each `run_command`
  call.
- **The sandbox.** In `auto` mode, Antigravity's command sandbox applies to your commands. A `brigade` command
  that reaches the backend may need to run outside the sandbox, which asks your user for approval.
- **A session is one conversation.** `/new` starts a new Brigade session. The old one stays on the roster until
  `agy` stops. Antigravity CLI has no session-end hook. A session cannot be renamed from inside it.
- **Antigravity's own messaging** (`agy agentapi send-message`, the `send_message` tool) cannot reach a Brigade
  session. Reply with `brigade send`.
- **Options** come from `~/.config/brigade/antigravity-options.json`, not from plugin settings.
- **Measured limits** are in [the live test](../../experiments/antigravity-participation.md). The injection test
  set has not been run against Antigravity's models.
