# Know what teammates see about your session

Every active member of the team, and whoever runs its backend, sees each session's name, state, your label, its
model identity and context occupancy, its Brigade version, the repository name, and one sentence about its work.
Never sent: your host's native session id, your working directory, your hostname, your username, your transcript or
the path to it.

## The sentence under your row

Your session's **own model** writes it with `brigade doing`: one present-tense sentence of at most 160 characters
on stdin, refused when empty, not UTF-8, longer than that, shaped like a credential or naming a path on your
machine; `brigade doing --clear` removes it. Brigade reads nothing about your work for it: not your prompts, not
your transcript. What keeps the model at it is a fixed line the prompt hook prints, at most once per ten minutes,
saying the line is blank or due again only if the work has changed. You never see that line as it happens; the
first thing you see is a `brigade doing` call in your transcript, like any other command the model runs.

The line lives inside one conversation: blank after a new session, `claude --resume` and `/clear`; kept across
`/compact` and a watcher restart; kept on the closed record for the backend's retention (7 days) once the
session ends, where `brigade sessions --all` shows it as `offline`.

**Where Brigade asks the model.** Only where the settings it can read say the call will neither raise a dialog
nor be denied: always in `bypassPermissions`; in `default`, `acceptEdits` and `dontAsk` only under an `allow`
rule that covers the verb exactly (`Bash(brigade:*)` or `Bash(brigade doing:*)`); never in `plan`, `auto` or
`claude -p`, and never where an ask or deny it can see matches `brigade doing`. So a Manual-mode member with no
rule is never asked and has a blank line, and one line in your user settings changes that:

```json
{ "permissions": { "allow": ["Bash(brigade doing:*)"] } }
```

**To stop it:** set `share_doing` to `false` (from the next session start the line is blanked, the reminders stop
and `brigade doing` refuses), run `brigade doing --clear`, put an ask or deny on `Bash(brigade doing*)` in your
settings, or answer No to the harness's dialog where one is raised. What the refusals cannot recognise (a
hostname, a person's or a customer's name, your account email when `label` is `none`) and the settings Brigade
cannot see: [docs/security.md, section 2](../../security.md#2-the-person-who-runs-the-backend-can-read-everything)
and [section 5](../../security.md#5-sending-what-the-ask-and-deny-rules-stop-and-what-they-miss).

## Names, models and context by host

The watcher reads local host records at each heartbeat. Claude Code supplies model and context from its
transcript and the name from its session registry. Muse Code and Codex CLI supply names and context from their
own history, matched by exact native session ID; their model still comes from the hook-written session map.
Only the roster name and the two derived facts, model and context, are shared. File contents and paths stay local.

| Host | Name changes | CONTEXT |
| --- | --- | --- |
| Claude Code | Registry updates, including `/rename` | Latest assistant input plus cache creation and cache reads |
| Muse Code | Journal `session.name.changed`, next heartbeat | Latest model completion's counted-once prompt tokens; native Meta input count as a fallback |
| Codex CLI | Thread title index, next heartbeat | Latest rollout `token_count` input tokens, including cache once |

CONTEXT is the last reported input context, not lifetime usage, output tokens or a percentage of the model's
window. Compaction can reduce it. Until a supported usage record exists it stays blank; temporarily missing
records keep the last known values. Reads are bounded, so unusually large or unsupported records may be skipped.
Codex 0.163.0's paginated mode still writes the supported rollout events; page-only/database-only histories are
unsupported. Muse custom data directories are unsupported because its plugin hooks do not receive the original
XDG data setting. Details: [Muse](../../agent/in-a-team/muse-code.md), [Codex](../../agent/in-a-team/codex.md).

## The repository name

`share_workspace_label: true` sends the checkout's `origin` remote's name (else its directory's), never the
working directory path; `workspace_label` replaces it; `share_workspace_label: false` withholds it, at the cost of
`--here` and of folder sync, which both match on it.
