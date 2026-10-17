# Keep the keep-alive armed

In a **public** repository, GitHub automatically disables a scheduled workflow when no repository activity has
occurred for 60 days. This repository is public, so the rule applies to its `keepalive.yml`. A disabled keep-alive
is silent: it does not fail, it stops running, and a free-plan project then pauses about seven days later.

- A commit at least every 60 days keeps it armed; so does **Actions → keepalive → Enable workflow** by hand.
- If a hosted project ever pauses, check the workflow's page first, then resume the project from the dashboard
  (**Resume project**; Supabase keeps a paused project restorable for up to a year).
- `gh workflow run keepalive.yml` runs it on demand; its four rungs and what a red run means are in
  [Arm the keep-alive](../install/keep-alive.md).
