# Know what teammates see about your session

Every active member of the team, and whoever runs its backend, sees each session's name, state, your label, its
model identity and context occupancy, its Brigade version, the repository name, and one sentence about its work.
Never sent: your Claude Code session id, your working directory, your hostname, your username, your transcript or
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

## The two values read from your transcript

So that the roster can show which model a session runs and how full its context is, the watcher reads your
session's transcript file, locally, at each heartbeat, and sends **only those two values**: the model identity and
the context occupancy in tokens. Nothing else is read out, kept or transmitted, and the path is never sent.

## The repository name

`share_workspace_label: true` sends the checkout's `origin` remote's name (else its directory's), never the
working directory path; `workspace_label` replaces it; `share_workspace_label: false` withholds it, at the cost of
`--here` and of folder sync, which both match on it.
