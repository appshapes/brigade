---
name: setup
description: Explain Brigade setup for Grok Build, including the current experimental participation gate; never request a secret in chat.
---

# Setup status

Grok Build participation is experimental and currently gated. Explain that the native package source is
present, while installed/trusted hooks, session identity, receiver liveness and ingress admission still need
measured evidence. Do not promise that installing, joining, prompting, or resuming enables receipt.

The human's setup procedures are in `docs/project-owner/install/README.md` and
`docs/team-admin/create/create-the-team.md` in the Brigade repository. A team uses a committed `.brigade.json`
with public backend coordinates. The join secret is delivered separately through a password-grade channel,
saved outside the repository and passed only by the user's chosen `--secret-file` path. Never read it into
chat, argv text, logs or a transcript. An administrator transfers ownership before leaving a team they own.

The experimental install/configure/join/update/leave procedures are under
`docs/team-member/` with filename `grok.md`; do not substitute another harness's trust or configuration flow.
