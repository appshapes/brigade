# Codex CLI: the skills a session loads, and how it is woken

`brigade sessions` follows host renames from `$CODEX_HOME/session_index.jsonl` (default `$HOME/.codex`) on the
next heartbeat, matching the exact thread ID. CONTEXT is the latest rollout `token_count` event's input tokens,
including cached input once; it is neither cumulative usage nor window capacity. MODEL still comes from the
hook-written session map. The observed 0.163.0 paginated history retains these events, as classic JSONL does.
Missing/unsupported records leave context blank and the name at its last known value. Reads are bounded;
page-only or database-only storage is unsupported. See [format evidence and limits](../../research/host-session-facts.md).

The Codex CLI package (`codex/brigade/`) ships the same eight skills, worded for Codex. They are the model's
documentation inside a team; this page names them and says what differs from Claude Code.

| Skill | Who invokes it | What it carries |
| --- | --- | --- |
| [`codex/brigade/skills/team-messaging/SKILL.md`](../../../codex/brigade/skills/team-messaging/SKILL.md) | the model, when a team message arrives or the user asks about teammates | the command surface, the sending rules, the inbound frame, the gateways, the roster line |
| [`codex/brigade/skills/sessions/SKILL.md`](../../../codex/brigade/skills/sessions/SKILL.md) | `$brigade:sessions` | print the roster verbatim |
| [`codex/brigade/skills/join/SKILL.md`](../../../codex/brigade/skills/join/SKILL.md) | `$brigade:join <path>` | run `brigade team join --secret-file <path>` on the file the user named |
| [`codex/brigade/skills/update/SKILL.md`](../../../codex/brigade/skills/update/SKILL.md) | `$brigade:update` | refresh the marketplace, install again, then a new session |
| [`codex/brigade/skills/setup/SKILL.md`](../../../codex/brigade/skills/setup/SKILL.md) | a person asking how to set Brigade up | the human's procedures |
| [`codex/brigade/skills/email/SKILL.md`](../../../codex/brigade/skills/email/SKILL.md) | `$brigade:email <address> <text>` | one `brigade send` to the mail gateway |
| [`codex/brigade/skills/slack/SKILL.md`](../../../codex/brigade/skills/slack/SKILL.md) | `$brigade:slack <to> <text>` | one `brigade send` to the Slack gateway |
| [`codex/brigade/skills/quiet/SKILL.md`](../../../codex/brigade/skills/quiet/SKILL.md) | `$brigade:quiet [on [--to <where>] [--refuse]]`, `$brigade:quiet off`, `$brigade:quiet status` | make this one session quiet with `brigade inbound hold`, the roster line and a standing instruction; `off` returns it |

What differs from Claude Code:

- **The first prompt.** Codex runs Brigade's SessionStart hook at the first prompt of a session, not at launch. That
  prompt registers the session and prints two lines: the session's name and team, and where the `brigade` command
  is. Nothing to arm. A resumed session (`codex resume`) registers again at its first prompt and the new thread
  `/new` opens is a new session; `/compact` needs nothing. A quit session stays on the roster until Codex ends its
  thread (archive, delete, or 30 minutes after the quit, measured), because Codex fires no SessionEnd at `/quit`
  in its normal, daemon mode.
- **Delivery is a queued turn.** Brigade's watcher runs `codex queue --thread <id> --message <frame>` for each
  message, so it arrives as a turn of its own: a `<brigade-message>` frame that looks like a prompt and says in its
  first sentence that your user did not type it. Read it as the frame says. The frame is the same as Claude Code's
  but for one sentence: `codex queue` cannot reach a Brigade session.
- **The command.** `brigade` is the bootstrap the session-start line names (under Codex's plugin cache); a symlink
  such as `ln -sf <bootstrap> ~/.local/bin/brigade` gives the shell the bare word. Options come from
  `~/.config/brigade/codex-options.json`, not from plugin settings.
- **Approval.** What a message can make you do is decided by Codex's approval policy: the sandbox and the approval
  prompts under the default profile, nothing under `--dangerously-bypass-approvals-and-sandbox`. The hooks see no
  Claude settings, so the `crossSessionInbound` layer does not exist here; `team_inbound` in the options file is
  the one knob.
- **Hooks your user has not trusted do not run.** Codex asks once, at the first launch after the install, or in
  `/hooks`; until then no prompt registers the session and `brigade whoami` says so. Tell your user; nothing you
  run can trust them.
- **Measured limits** are in [docs/security.md, section 16](../../security.md#16-codex-cli-sessions): the injection
  test set has not been run against Codex's model, and any process of your user can queue into your session.
