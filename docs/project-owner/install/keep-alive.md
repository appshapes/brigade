# Arm the keep-alive

Supabase "pauses Free Plan projects that show low activity over a 7-day period"; "typically a few user requests to
the database each day over the previous week is enough" ([Project Pausing](https://supabase.com/docs/guides/platform/free-project-pausing),
read 2026-09-04). `.github/workflows/keepalive.yml` in this repository runs `scripts/ci/keepalive.sh` once a day at
10:37 UTC against each project it knows, and on demand (`gh workflow run keepalive.yml`). One keep-alive serves one
project; a paid plan needs none.

## The variables

The workflow reads each project's URL and publishable key from repository **variables**, one pair per job:

```sh
gh variable set BRIGADE_SUPABASE_URL --body https://<ref>.supabase.co
gh variable set BRIGADE_SUPABASE_PUBLISHABLE_KEY --body sb_publishable_...
```

A second project is a second job in `keepalive.yml` and a second pair, named for it. The jobs this repository
carries today, each with its pair; `scripts/ci/keepalive_test.go` pins every job name and `vars.` expression
against these lines:

```sh
# thinktech-brigade, whose team lives in another repository
gh variable set BRIGADE_THINKTECH_SUPABASE_URL --body https://<ref>.supabase.co
gh variable set BRIGADE_THINKTECH_SUPABASE_PUBLISHABLE_KEY --body sb_publishable_...

# the AAFP Board Review team's project, whose team spans two other repositories
gh variable set BRIGADE_AAFP_BOARD_REVIEW_TEAM_SUPABASE_URL --body https://<ref>.supabase.co
gh variable set BRIGADE_AAFP_BOARD_REVIEW_TEAM_SUPABASE_PUBLISHABLE_KEY --body sb_publishable_...
```

A fourth project is a fourth job, a fourth pair and a fourth block here. The script itself stays single-project: each
job maps its own pair onto the two names `keepalive.sh` reads, so a red run names the job, and therefore the
project, on the run page.

- **Set both names of a pair, spelled exactly as the workflow does.** Both unset is a green no-op that says so;
  exactly one set fails the run on purpose; a pair under a name the workflow does not read leaves the job green
  forever while the project pauses on day seven.
- **Variables, not secrets.** Both values are public by design, the only two things a team's members receive. A
  secret would be masked in the run log for no gain and hide the very hostname a failing run has to name. The
  project's secret key, service-role key, database password and your access token are never repository variables.

## What a run does

Four rungs, one line each: `GET /auth/v1/health`, `POST /auth/v1/signup` (an anonymous sign-up, a row in
`auth.users`), `POST /rest/v1/rpc/my_team_ids` (one query through the Data API) and `POST /auth/v1/logout?scope=global`.
The middle two reach the database, which is the point. Each run leaves one anonymous user, deleted by the backend's
own garbage collection once it is 7 days old.

## What a failing run means

The Actions failure e-mail is the alert, and the `::error::` annotation names the rung: *health*, the project is
paused, deleted or unreachable (restore it from the dashboard: open the paused project, **Resume project**;
restorable for up to a year); *anonymous sign-up*, most often anonymous sign-ins were turned off
([Review the project's settings](../configure/project-settings.md)); *Data API*, an error other than PostgREST's
"not there yet" (`PGRST106`, `PGRST202`), which are only warnings.

## GitHub's 60-day rule

In a public repository GitHub disables a scheduled workflow after 60 days without repository activity. A disabled
keep-alive is silent. Keeping it armed is [Keep the keep-alive armed](../update/keep-alive-armed.md).
