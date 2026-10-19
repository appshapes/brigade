# Grok Build: experimental skills and delivery gate

Grok Build participation is experimental. This source tree does not register Grok sessions or deliver team messages; native identity, session-specific liveness and admission remain gated.

The package's seven skills remain model-reachable. Their command forms use Grok's terminal tool and the
installed package bootstrap; no native cache path or shell-tool identity has been assumed.

| Skill | Purpose |
| --- | --- |
| [`grok/brigade/skills/team-messaging/SKILL.md`](../../../grok/brigade/skills/team-messaging/SKILL.md) | team messaging |
| [`grok/brigade/skills/sessions/SKILL.md`](../../../grok/brigade/skills/sessions/SKILL.md) | sessions |
| [`grok/brigade/skills/join/SKILL.md`](../../../grok/brigade/skills/join/SKILL.md) | join |
| [`grok/brigade/skills/update/SKILL.md`](../../../grok/brigade/skills/update/SKILL.md) | update |
| [`grok/brigade/skills/setup/SKILL.md`](../../../grok/brigade/skills/setup/SKILL.md) | setup |
| [`grok/brigade/skills/email/SKILL.md`](../../../grok/brigade/skills/email/SKILL.md) | email |
| [`grok/brigade/skills/slack/SKILL.md`](../../../grok/brigade/skills/slack/SKILL.md) | slack |

The Grok frame is host-specific, retains full message/reply IDs and untrusted-source semantics, and never
attributes the sender to Claude Code. This proves rendering only. Hooks emit no model context, register no
session and spawn no watcher. The watcher rejects a Grok map before constructing its pipeline; its injector
also refuses a Grok route before a sink or socket write. No seen state or ACK is justified by saved-history
resume, a socket write, a queue notification or hook output.

A transport contract must distinguish receiver liveness, native admission, wake, model-visible content and
processing. A shared leader PID is not a session liveness witness. New/load/fork/compact/end semantics,
subagent exclusion, exact hook/tool ID agreement and lost-response behavior remain unimplemented pending
[the live gates](../../experiments/grok-participation.md). No history reader supplies Grok metadata.
