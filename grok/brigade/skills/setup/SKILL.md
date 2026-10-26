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

Grok options are in `~/.config/brigade/grok-options.json`. Set `label` to a literal or `none` before joining;
no Claude account is borrowed. Membership joining and monitor readiness are separate. Refer to the member's
`install/join/configure/update/leave/grok.md` pages for ordered procedures, status and retained local backlog.
Transfer ownership before an administrator leaves; revoke membership before deleting credential copies.

A team can also run with no Supabase project: `brigade team create --adapter syncthing --name <team> --secret-file
<path>` keeps the team's messages in a folder Syncthing replicates between the members' machines (`syncthing` on
every member's PATH). `docs/team-admin/create/create-over-syncthing.md` has the details; the `migrate` skill moves
an existing team.
