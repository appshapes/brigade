# Setting up Brigade

This document is completed by P5-7, which adds the member-facing setup, the revoke procedure, the "Leaving and
uninstalling" sequence and the sandbox and shadowing notes. Today it holds only the parts that already exist:
how to reach the plugin's binary from your own terminal, and the hosted project's administrator's one workflow
to arm and one GitHub rule to remember.

## Terminal use

Inside a session the Bash tool finds `brigade` on its PATH because the plugin puts it there. Your own terminal
does not, and the commands that must run there — `brigade team join`, `brigade profile …`, `brigade inbox` —
refuse to run from inside a session, because the join secret must never pass through the chat. `brigade whoami`,
run in a session, prints the plugin binary's absolute path on its own line:

```
session 09365acd… "payments-api" in team "ops" (profile default, adapter supabase 0.1.0); inbound: accept
terminal: /Users/you/.claude/plugins/brigade/bin/brigade
```

Symlink that path onto your own PATH, and your terminal follows the version the plugin is pinned to, upgrade
for upgrade, with nothing to reinstall — the bootstrap resolves its own symlinks and execs the binary
`plugin/bin/VERSION` names:

```sh
ln -s /Users/you/.claude/plugins/brigade/bin/brigade ~/.local/bin/brigade
```

A symlink, not a copy: a `brigade` on your PATH that is not the plugin's own is what the session-start
shadowing warning is about, and a copy goes stale at the next plugin upgrade.

## Hosted project: the administrator's responsibilities

### 1. The two repository variables

The keep-alive workflow reads the hosted project's url and publishable key from repository **variables**:

```sh
gh variable set BRIGADE_SUPABASE_URL --body https://<ref>.supabase.co
gh variable set BRIGADE_SUPABASE_PUBLISHABLE_KEY --body sb_publishable_...
```

or Settings → Secrets and variables → Actions → Variables in the web interface. Use the names above exactly:
they are the adapter's own environment names, and `SUPABASE_URL` / `SUPABASE_PUBLISHABLE_KEY` already name the
*local* development stack in `.env.test`.

Both values are public by design — they are the only two things a team's members ever receive
([plugin/README.md](../plugin/README.md), "Administrator: create a team"). Variables, not secrets: a secret
would be masked in the run log for no gain, and the mask would hide the very hostname a failing run has to
name. The project's secret key, its service-role key, the database password and your personal access token are
never repository variables and never secrets of this workflow.

Until both variables exist, every keep-alive run is a green no-op that says so on the run page. Setting only
one of them fails the run on purpose: a half-configured repository is a misconfiguration, not a fork.

### 2. The keep-alive workflow

`.github/workflows/keepalive.yml` runs `scripts/ci/keepalive.sh` once a day at 10:37 UTC (off the hour,
because GitHub can delay `schedule` events under load and names the start of every hour as one of those
times), and on demand:

```sh
gh workflow run keepalive.yml
```

The script climbs four rungs against the hosted project and prints one line for each: `GET /auth/v1/health`
(the project answers), `POST /auth/v1/signup` (an anonymous sign-up, which inserts a row into `auth.users`),
`POST /rest/v1/rpc/my_team_ids` (one query through the Data API) and `POST /auth/v1/logout?scope=global`.
The middle two reach the database, which is the point.

**Why.** Supabase "pauses Free Plan projects that show low activity over a 7-day period"; a project "is
considered inactive if it does not receive sufficient user database activity over the past week", and
"typically a few user requests to the database each day over the previous week is enough to keep the project
from being paused" ([Project Pausing](https://supabase.com/docs/guides/platform/free-project-pausing), read
2026-09-04). The cadence is daily because the quoted rule asks for activity *each day* of the previous week, so
a weekly run would not do; a daily one is also why a single missed run — an Actions incident, a schedule
delayed under load — is harmless.

**What a failing run means.** The Actions failure e-mail is the alert, and the `::error::` annotation on the
run page names the rung:

- *health* — the project is paused, deleted or unreachable. Restore it from the dashboard: open the paused
  project and choose **Resume project**. Supabase says a paused project can be restored "for up to 1 year
  after it was paused".
- *anonymous sign-up* — most often anonymous sign-ins have been turned off. Turn them back on under
  Authentication → Sign In / Providers → Allow anonymous sign-ins; the rest of the project's settings are in
  [plugin/README.md](../plugin/README.md), "Administrator: create a team", which this document does not
  duplicate.
- *Data API* — an error other than PostgREST's own "not there yet" answers. A 406 `PGRST106` (the `brigade`
  schema is not exposed) or a 404 `PGRST202` (the function is missing) is only a warning: it means the schema or
  `brigade.my_team_ids()` is not on the project yet, which is P5-1's job, and the sign-up on the rung above
  has already generated the day's activity.

**One anonymous user per day.** Each run mints an anonymous principal and signs it out globally; the row in
`auth.users` stays until P5-3 adds the cleanup to `gc_expired()`. That is deliberate and expected.

### 3. GitHub's 60-day rule for scheduled workflows

In a **public** repository, GitHub automatically disables a scheduled workflow when no repository activity has
occurred for 60 days. This repository is private today, so the rule does not apply and there is nothing to do.
On the day it becomes public — P5-10 at the earliest, and only if the release is distributed from a public
repository rather than from public releases alone — keeping the keep-alive armed becomes an administrator's
task: either a commit at least every 60 days, or a manual re-enable under Actions → keepalive → Enable
workflow.

### 4. The hosted project's settings

The project this workflow keeps alive must be created with the settings listed in
[plugin/README.md](../plugin/README.md), "Administrator: create a team" — anonymous sign-ins on, CAPTCHA off,
no Pro session time-box or inactivity limit, Realtime "Allow public access" off, and the migrations from
`supabase/` applied. That list lives there and is not repeated here.

### 5. The Bash sandbox

With Claude Code's Bash sandbox on, add the hosted project's host (`<ref>.supabase.co`) to `sandbox.network.allowedDomains`
(plugin/README.md, "Headless and sandboxed sessions"); the hooks and the watcher run outside the sandbox, only the commands
the model runs are inside it. A **local** Supabase stack is out of reach from a sandboxed session: the adapter honours
`NO_PROXY` and never proxies loopback (`internal/adapters/supabase/client.go`), and E0-8 measured that a loopback
`allowedDomains` entry does not lift the refusal. Decided 2026-09-04 not to change this for v1 — the hosted domain entry is
the sandbox story; revisit if a developer needs the local stack from inside a sandboxed session.
