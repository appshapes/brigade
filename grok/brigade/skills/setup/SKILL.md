---
name: setup
description: Explain human Brigade team setup, joining and removal for direct Grok Build monitor sessions, without ever asking for secrets in chat.
---

# Set up Brigade for Grok Build

Use the repository's `docs/project-owner/install/README.md` and `docs/team-admin/create/create-the-team.md`
for backend setup and administration. A team is a committed `.brigade.json` with public coordinates; its join
secret travels separately through a password-grade channel, in a file outside the repository. Never print it,
put it on argv or ask for it in chat. A user who names their saved file can invoke the `join` skill.

Install through the native marketplace and normal trust flow, then start `grok --no-leader`. The session-start
notice is UI-only. A completed turn offers one monitor setup attempt using normal command permission, with
explicit user retry after a refusal/failure. The team-messaging skill defines exact monitor setup and receiving.
Leader-backed TUIs are unsupported. Do not change trust files or permission policies to work around this.

Grok options are in `~/.config/brigade/grok-options.json`. Left at its default, `label` sends the email address of
the Claude account on this machine when there is one, as on every harness; set `label` to a literal or `none`
before joining to send that instead. Membership joining and monitor readiness are separate. Refer to the member's
`install/join/configure/update/leave/grok.md` pages for ordered procedures, status and retained local backlog.
Transfer ownership before an administrator leaves; revoke membership before deleting credential copies.

A team in a Supabase project, or on a Brigade server, can move to a Brigade server with its join secret, and its
members run no command: each one's next session follows, and a session that is already running follows at its
next prompts, from plugin 0.56.0, except in Grok Build, where it follows when it next starts. A team that leaves
a Brigade server is followed from plugin 0.57.0. The `move` skill asks the team's administrator for the server's
URL and the path of the team's secret file and runs `brigade team move --to <url> --secret-file <path>`. `docs/team-admin/members/move-the-team.md` has the
procedure.
