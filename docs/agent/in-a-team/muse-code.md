# Muse Code: the skills a session loads, and how it is woken

`brigade sessions` follows `session.name.changed` events in Muse's own session journal on the next heartbeat,
matching the exact session ID. CONTEXT is the latest `model_completed` prompt count, including cache once,
not cumulative session or child usage. For older records without that count, native Meta input tokens are used
only with a matching run-provider record; unknown conventions stay blank. MODEL still comes from the hook-written
map. The reader uses `$HOME/.local/share/muse/sessions`: Muse strips `XDG_*` from plugin hooks, so custom data
directories are unsupported. Missing/unsupported records preserve the last known name and leave unobserved
context blank. See [format evidence, validation limits and read bounds](../../research/host-session-facts.md).

The Muse Code package (`muse/brigade/`) ships the same seven skills, worded for Muse. They are the model's
documentation inside a team; this page names them and says what differs from Claude Code.

| Skill | Who invokes it | What it carries |
| --- | --- | --- |
| [`muse/brigade/skills/team-messaging/SKILL.md`](../../../muse/brigade/skills/team-messaging/SKILL.md) | the model, when a team message arrives or the user asks about teammates | the command surface, the sending rules, the inbound frame, the gateways, the roster line |
| [`muse/brigade/skills/sessions/SKILL.md`](../../../muse/brigade/skills/sessions/SKILL.md) | the sessions command | print the roster verbatim |
| [`muse/brigade/skills/join/SKILL.md`](../../../muse/brigade/skills/join/SKILL.md) | the join command | run `brigade team join --secret-file <path>` on the file the user named |
| [`muse/brigade/skills/update/SKILL.md`](../../../muse/brigade/skills/update/SKILL.md) | the update command | refresh the marketplace, remove and reinstall the package (the person approves the hooks), then a new session; a clone install pulls and updates |
| [`muse/brigade/skills/setup/SKILL.md`](../../../muse/brigade/skills/setup/SKILL.md) | a person asking how to set Brigade up | the human's procedures |
| [`muse/brigade/skills/email/SKILL.md`](../../../muse/brigade/skills/email/SKILL.md) | the email command | one `brigade send` to the mail gateway |
| [`muse/brigade/skills/slack/SKILL.md`](../../../muse/brigade/skills/slack/SKILL.md) | the slack command | one `brigade send` to the Slack gateway |

What differs from Claude Code:

- **The first prompt.** Muse runs Brigade's SessionStart hook at the first prompt of a session, not at launch. That
  prompt registers the session and prints two lines: the session's name and team, and an instruction to arm Muse's
  Monitor tool, once, persistent, immediate wake, on `brigade inbox follow`. Arm it. Until you do, the session is a
  mailbox; a resumed session (`muse resume`) needs its first prompt again, `/compact` and `/clear` need nothing.
- **Delivery is a file.** The watcher appends each frame to `~/.local/state/brigade/inbox/<session>/<watcher>.ndjson`
  and `brigade inbox follow` prints each record between a `Brigade: team message <id> follows` line and a
  `Brigade: end of message <id>` line. The monitor wakes the session about 0.2 s after a record lands. The frame is
  the same as Claude Code's but for one sentence: Muse's own session messaging cannot reach a Brigade session.
- **The command.** `brigade` is the bootstrap the session-start line names (under Muse's plugin cache); a symlink
  such as `ln -sf <bootstrap> ~/.local/bin/brigade` gives the shell the bare word. Options come from
  `~/.config/brigade/muse-options.json`, not from plugin settings.
- **Approval.** What a message can make you do is decided by Muse's approval policy: the approval judge and the
  sandbox under the default profile, nothing under `--yolo`. The hooks see no Claude settings, so the
  `crossSessionInbound` layer does not exist here; `team_inbound` in the options file is the one knob.
- **Measured limits** are in [docs/security.md, section 15](../../security.md#15-muse-code-sessions): the injection
  test set has not been run against Muse's model.
