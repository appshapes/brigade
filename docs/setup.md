# Setting up Brigade

This document is completed by P5-7, which adds the member-facing setup, the "Leaving and uninstalling" sequence
and the sandbox and shadowing notes. Today it holds the parts that already exist: how to reach the plugin's binary
from your own terminal, the team administrator's commands (rotating the join secret, revoking a member,
transferring the team), and the hosted project's administrator's responsibilities: deploying the backend,
the one workflow to arm and the one GitHub rule to remember.

## Terminal use

Inside a session the Bash tool finds `brigade` on its PATH because the plugin puts it there. Your own terminal
does not, and the two commands that must run there — `brigade team create` and `brigade team join` — refuse to
run from inside a session, because the join secret must never pass through the chat (`brigade profile …` runs in
either place; `brigade inbox release` is terminal-only too, and `brigade inbox --recent` arrives with P5-5).
The three administrative
commands — `brigade team rotate-secret`, `brigade team revoke-member` and `brigade team transfer` — refuse inside
a session too, in the same shape: `rotate-secret` with that same line, because it produces a secret;
`revoke-member` and `transfer` with their own, because administration is not driven by chat.
`brigade whoami`, run in a session, prints the plugin binary's absolute path on its own line:

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

## Holding messages for review

Brigade delivers team messages into your session as they arrive. Set the plugin option `team_inbound` to `hold`
and it stops doing that: an arriving message is recorded — sender, summary, when it came — and **not** delivered
and **not** acknowledged, so it stays on the server and its sender is never told it arrived. At your next prompt
the session shows one line: how many are held, who they are from, and what to run.

Reading and releasing are things you do **in your own terminal**, never from the chat. `brigade inbox` lists what
is waiting, with each message's body fetched fresh from the server and shown once — nothing is stored on your
machine and nothing is acknowledged by looking. `brigade inbox release --all`, or
`brigade inbox release <message_id>…`, hands the ones you chose to the session's watcher, which delivers them the
way an ordinary message is delivered, within a few seconds. `brigade inbox release` refuses to run inside a Claude
Code session, so no model can release its own reading; inside a session `brigade inbox` shows only the count and
the sender names.

Held messages are not stored forever. Nothing is acknowledged, so the backend keeps them under its unacknowledged
retention — at least seven days on the bundled adapter — and deletes them after that. A session that holds
messages also fills its inbox: the backend refuses new messages to it once sixty are unacknowledged, and senders
are told so.

**Claude Code's own `crossSessionInbound` setting is a different layer.** It sits between Brigade and your session
and Brigade cannot release from it. If Brigade's session-start scan finds `crossSessionInbound` set to `hold` or
`refuse` in one of your settings files, it sets its own policy to `refuse` and says so: releasing into a session
Claude Code will not deliver to would throw the message away *and* tell its sender it arrived. Remove that
setting, or set it to `accept`, and then use `team_inbound: hold` if you want the review step.
One blind spot to know about: a `crossSessionInbound` passed with `--settings` on the command line is invisible to
that scan, so a session started that way can hold or drop Brigade frames without Brigade knowing.
## Team administration

Every command in this section runs in **your own terminal**; inside a session each one refuses the way `team create`
and `team join` do (`usage`, exit 2): `rotate-secret` with their line, `revoke-member` and `transfer` with
`run this in your own terminal: team administration is not driven from a session`. They are pass-throughs to the
adapter, so what you see is the adapter's
own JSON envelope (`--json` is parsed and has no effect on them, exactly as for `team create`), and they exist only
on an adapter that advertises the capability `team.admin` — the bundled Supabase adapter does.

### Where the authority lives

`teams.created_by` is the only administrative authority of a team, and the creator's profile directory
(`~/.config/brigade/profiles/<name>`, or under the `config_dir` option) is the only credential that exercises it.
Anonymous credentials are unrecoverable by design: `brigade profile reset`, a lost or deleted `session.json`, or
the backend's refresh-token reuse detection each ends secret rotation, revocation and transfer for that team,
permanently. **Keep a 0700 backup of the profile directory.** If you will hand the team over, run
`brigade team transfer` *first* — after the loss there is nothing to transfer, and the only other recovery is to
create a new team and re-invite everyone. `brigade team leave` does **not** end it: `created_by` survives a leave,
so a rejoin with the current secret restores administration
(`supabase/migrations/20260830120000_brigade_schema.sql:279-280`) — but a `profile reset` does end it, which is why
the order in [plugin/README.md](../plugin/README.md), "Leaving and uninstalling", matters.

### Revoking a member

```sh
brigade team members                                    # copy the principal_ref of the member to remove
printf '{"principal_ref":"<ref>"}' | brigade team revoke-member
# or, without stdin:
brigade team revoke-member --principal <ref>
brigade team revoke-member --principal <ref> --ban      # also blocks a rejoin with the current secret
```

A **revoke** closes that member's open sessions at once (a running watcher of theirs ends within about a second,
with the same `unauthorized` a `team leave` produces), removes them from the roster, and ends their access to
their own inboxes immediately — and they may rejoin with the current secret, as the same principal. A **ban**
does all of that and makes the member's rejoin answer *exactly* what a wrong secret answers, so a banned member
cannot tell it was banned. **Write the `principal_ref` down before you ban**: a banned member is no longer listed
by `brigade team members`, and un-banning — `brigade team revoke-member --principal <ref>` without `--ban` —
needs the ref. If the ref is lost, the recovery is a secret rotation (below) plus a fresh principal on the
member's side (`brigade profile reset`, then `brigade team join`). You cannot revoke or ban yourself; leaving is
`brigade team leave`, and it is reversible by a rejoin.

### The leaked-secret playbook

Order matters:

```sh
brigade team rotate-secret --secret-file ~/brigade-<team>.secret   # the file is 0600; the old secret dies now
# its result's secret_version is the NEW version; the version before the rotation is that number minus one
# hand the new secret to every member you want to keep, over a password-grade channel
brigade team members                                                # the roster before the eviction
brigade team revoke-member --max-version <secret_version minus one>
```

Rotation alone revokes nobody — everyone who is already in stays in, which is exactly what makes it safe to run
first. The second command evicts every member who joined at or below that version, **except you**, closes their
sessions, and lets them come back with the new secret (it never bans; a specific principal is banned one at a time
with `--principal … --ban`). Take the version from `rotate-secret`'s own result: `brigade team members` does not
show a member's version (its row is fixed by the protocol), but running it before and after shows who was evicted.
And: **the new secret is written to the file and nowhere else** — it is never printed, `--secret-file` is
mandatory, and if the file cannot be written after the rotation the secret is gone and the only fix is to rotate
again. Members that were never revoked keep working through a rotation without noticing it.

### Transferring the team

```sh
brigade team members                       # the new creator must be an active member
brigade team transfer --principal <ref>
```

Afterwards every administrative command of the old creator answers `unauthorized`; the old creator stays an
ordinary active member with its sessions untouched, and can `brigade team leave` if it also wants out. A transfer to
a principal that is not an active member of the team is refused, and a transfer to yourself is an accepted no-op.

The security discussion of these commands — what an operator can see, why a banned member cannot tell it was
banned, and what the creator's profile directory is worth to an attacker — belongs in `docs/security.md`, which
P5-7 writes; the operational steps stay here.

## Hosted project: the administrator's responsibilities

### 1. Deploying the backend

Everything a Brigade team needs lives in one single-purpose Supabase project: a handful of tables and the RPCs
that are the only write path into them, in a schema called `brigade`, exposed on the Data API, with Realtime
restricted to private channels. Deploying it is five commands and takes a few seconds.

You need a **personal access token** (Account → Access Tokens in the dashboard, `sbp_…`) in
`SUPABASE_ACCESS_TOKEN`. You do **not** need the database password. The CLI has a `--password` flag on both
commands below, which makes everyone assume otherwise, but on CLI 2.116.0 `SUPABASE_DB_PASSWORD` is accepted and
ignored by `link`, and `db push --linked` mints a temporary login role for itself through the Management API
using the access token — the push prints `Initialising login role...` when it does. A personal access token
alone deploys the whole schema.

Paste the token at a prompt rather than typing it on a command line, so it never reaches your shell history
(`read -s` is bash and zsh; `npx supabase login` is the interactive alternative):

```sh
read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN   # paste sbp_…; nothing is echoed
npx --yes supabase@2.116.0 link --project-ref <ref>           # writes only supabase/.temp/, which is gitignored
npx --yes supabase@2.116.0 db push --dry-run                  # lists the migrations that are not applied yet
npx --yes supabase@2.116.0 db push --yes
scripts/backend-settings.sh <ref>                             # the settings below, one field at a time
npx --yes supabase@2.116.0 migration list --linked            # every version on BOTH sides
```

or, as one command:

```sh
make backend-install project=<ref>          # add dry=1 to stop after the dry run and only print the diffs
```

`migration list --linked` is the check: every migration in `supabase/migrations/` must appear in both the
`Local` and the `Remote` column, in version order, with no one-sided row. `db push --dry-run` before it is the
gate — it names the migrations that are **not applied yet**, which on a first deployment is every file under
`supabase/migrations/` and on a later run is only what is still pending (an empty list means the project is
already up to date). If it names a file you do not recognise, stop and find out why before pushing.

**Order matters, and it is not a preference.** Apply the migrations *before* exposing the `brigade` schema on the
Data API. A project that exposes a schema which does not exist yet leaves PostgREST looping on
`3F000 schema "brigade" does not exist`; it never becomes healthy, and the error it reports names PostgREST
rather than the cause. `make backend-install` runs the two steps in that order for you.

**If you would rather click.** The four settings `scripts/backend-settings.sh` writes have dashboard
equivalents, and either route is fine:

- **Settings → API → Exposed schemas** — add `brigade` to the list (keep `public` and `graphql_public`).
- **Authentication → Sign In / Providers → Allow anonymous sign-ins** — on. Every Brigade principal is an
  anonymous user; membership, not anonymity, is the authorization gate. Leave CAPTCHA off in the same place: a
  command-line client cannot solve a browser challenge, and `/signup` sits behind the CAPTCHA middleware.
- **Authentication → Sessions** — leave the time-box and the inactivity limit **unset**. They silently kill idle
  principals, which is exactly what a long-running session looks like.
- **Realtime → Settings → Allow public access** — **off**. Brigade only ever joins private channels; with this
  off, a public join is refused outright rather than quietly receiving nothing.

What is **not** an option is `supabase config push`. That command sends this repository's `supabase/config.toml`
whole, and that file describes the **local** development stack: pushing it would set the hosted project's
anonymous sign-in limit to 1000 per hour per IP (the hosted default is 30) and its site URL to
`http://127.0.0.1:3000`. `make supabase-config-push` refuses to run without `i_know=1` for that reason.

Finally, hand the team what it needs:

```sh
npx --yes supabase@2.116.0 projects api-keys --project-ref <ref>
```

The **project URL** and the **publishable key** are the only two values a member ever receives, and both are
public by design. The secret key, the service-role key, the database password and your personal access token
stay on your machine. Creating the first team is `brigade team create` — see
[plugin/README.md](../plugin/README.md), "Administrator: create a team".

### 2. The two repository variables

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

### 3. The keep-alive workflow

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

### 4. GitHub's 60-day rule for scheduled workflows

In a **public** repository, GitHub automatically disables a scheduled workflow when no repository activity has
occurred for 60 days. This repository is private today, so the rule does not apply and there is nothing to do.
On the day it becomes public — P5-10 at the earliest, and only if the release is distributed from a public
repository rather than from public releases alone — keeping the keep-alive armed becomes an administrator's
task: either a commit at least every 60 days, or a manual re-enable under Actions → keepalive → Enable
workflow.

### 5. The hosted project's settings

The project this workflow keeps alive must be created with the settings listed in
[plugin/README.md](../plugin/README.md), "Administrator: create a team" — anonymous sign-ins on, CAPTCHA off,
no Pro session time-box or inactivity limit, Realtime "Allow public access" off, and the migrations from
`supabase/` applied. That list lives there and is not repeated here; section 1 above is how to put a project
into that state, and `scripts/backend-settings.sh <ref>` re-checks every one of those settings and reports what
it found, without changing anything that is already right.

### 6. The Bash sandbox

With Claude Code's Bash sandbox on, add the hosted project's host — `<ref>.supabase.co`, which for this
repository's own project is `wmgtaraqmoufmrnyojzf.supabase.co` — to `sandbox.network.allowedDomains`
(plugin/README.md, "Headless and sandboxed sessions"). The allowlist matches the literal host as written in the
profile's url, not a resolved address. The hooks and the watcher run outside the sandbox; only the commands the
model runs are inside it. A **local** Supabase stack is out of reach from a sandboxed session: the adapter honours
`NO_PROXY` and never proxies loopback (`internal/adapters/supabase/client.go`), and E0-8 measured that a loopback
`allowedDomains` entry does not lift the refusal. Decided 2026-09-04 not to change this for v1 — the hosted domain entry is
the sandbox story; revisit if a developer needs the local stack from inside a sandboxed session.

### 7. Retention and cleanup

The backend keeps a message until it is acknowledged or **7 days** old, whichever is later, and an acknowledged message for
24 hours; a session that has been offline for 3 days still receives everything sent to it when it resumes, and one offline
for 8 days does not (measured live, `TestIntegrationRetentionResumeAfterThreeAndEightDays`). Anonymous principals that
never joined a team — the keep-alive's daily sign-up is one such — are deleted once they are 7 days old, in batches of up
to 1,000, by `brigade.gc_anonymous_users()`, which runs last inside `gc_expired()`: from the hourly pg_cron job where the
extension exists, and opportunistically from about one member heartbeat in fifty everywhere. A principal that created a
team, or that holds a membership row of any status, is never deleted. A deleted principal's still-unexpired access token
reads nothing and writes nothing (its RPCs answer an empty set or a foreign-key error) for at most an hour until it expires.
Nothing here needs an administrator's hand; the numbers are `describe`'s `retention` members, which a test pins to the
migrations.
