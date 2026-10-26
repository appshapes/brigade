# Move a team to Syncthing

`brigade team migrate` moves a team that lives in a Supabase project onto the syncthing adapter
([Create a team over Syncthing](create-over-syncthing.md)): it creates the team over Syncthing under the **same
reference and name**, so the folder ids the project already syncs stay what they are, mints a new join secret,
and rewrites `.brigade.json`. The creator runs it; nothing on the project is changed or deleted.

## The command

Inside the project checkout, from a session (`/brigade:migrate ~/brigade-<team>.secret` runs it) or from your own
terminal, with `syncthing` on PATH:

```sh
!brigade team migrate --secret-file ~/brigade-<team>.secret
```

then `git add .brigade.json && git commit && git push`.

- The new file names `adapter: syncthing`, your machine as the seed device, and keeps the project's `sync` and
  `polling` members. The `gateway` member is dropped: the hosted mail and Slack gateways live in the Supabase
  project, and a team over Syncthing has none.
- The new secret goes to `--secret-file` and nowhere else. The old secret opens the old team on the project; it
  does not open this one.
- `team migrate` refuses in a checkout whose file already names the syncthing adapter.

## What the members do

Each member pulls, then joins with the new secret file: `/brigade:join <path>` (Claude Code), the `join` skill
(Muse Code), `$brigade:join <path>` (Codex CLI) or `brigade team join --secret-file <path>` in a terminal, while a
session of yours (the seed) is running. Their sessions move at their next start. Until a member has joined, their
sessions stay on the old team.

## What is not carried over

- **Messages.** The old backend keeps its messages for its retention; none of them move.
- **The hosted gateways.** Mail and Slack through the project's gateways end for this team; the webhook gateway
  stays bundled on the Supabase adapter.
- **The project.** When every member has moved, the project owner may delete the project
  ([Delete the project](../../project-owner/delete/delete-the-project.md)) or keep it for other teams.

## Check it worked

`brigade team status` in the checkout names the team, `engine: running` and `team_folder: present`; a member who
has joined appears in `brigade sessions` within seconds of their next session start.
