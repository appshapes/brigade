# Update

A release can change what the hosted project runs: a migration under `supabase/migrations/`, or a gateway
function under `supabase/functions/`. The release's section in `CHANGELOG.md` says so when it does; a release that
says nothing about either needs nothing from you.

## If you want to

| If you want to | Go to |
| --- | --- |
| apply what a release changed in the backend | [Apply a release](#apply-a-release) |
| know which migration adds what, and why members need not wait for you | [Know which migration adds what](#know-which-migration-adds-what) |
| keep a free-plan project's keep-alive from being disabled | [Keep the keep-alive armed](#keep-the-keep-alive-armed) |

## Apply a release

1. `git pull` the Brigade clone; `read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN`.
2. `make backend-install project=<ref>`: applies the pending migrations and leaves everything else alone.
3. Redeploy each gateway the team has with the same install command you used; check with `migration list` and `functions list`.

Details: [Apply a release](apply-a-release.md).

## Know which migration adds what

1. Plugin versions and migrations are independent by design: an older adapter works on a migrated project, a newer
   one on a project you have not migrated yet, losing only what the migration adds.
2. `migration list` says where each project stands.

Details: [Know which migration adds what](migrations.md).

## Keep the keep-alive armed

1. GitHub disables a public repository's scheduled workflow after 60 days without activity, silently.
2. A commit at least every 60 days, or Actions → keepalive → Enable workflow.

Details: [Keep the keep-alive armed](keep-alive-armed.md).
