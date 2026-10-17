# Deploy the backend

Everything a Brigade team needs lives in the project: a handful of tables and the RPCs that are the only write
path into them, in a schema called `brigade`, exposed on the Data API, with Realtime restricted to private
channels. Deploying it is one command and takes a few seconds.

## The command

In the Brigade clone, with the token pasted at a prompt so it never reaches your shell history (`read -s` is bash
and zsh):

```sh
read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN   # paste sbp_…; nothing is echoed
make backend-install project=<ref>                               # dry=1 stops after the dry run and prints the diffs
```

It applies the migrations not applied yet, exposes `brigade` on the Data API, sets the project settings one field
at a time (`scripts/backend-settings.sh`), lists every migration, and prints the publishable key on its last
line. Copy that line. It is idempotent: run it again after a release and it applies only what is pending.

The five commands it runs, if you would rather run them one at a time:

```sh
npx --yes supabase@2.116.0 db push --dry-run --project-ref <ref>   # the migrations not applied yet
npx --yes supabase@2.116.0 db push --yes --project-ref <ref>
scripts/backend-settings.sh <ref>                                  # the settings, one field at a time
npx --yes supabase@2.116.0 migration list --project-ref <ref>      # every version on BOTH sides
npx --yes supabase@2.116.0 projects api-keys --project-ref <ref>   # prints EVERY key; see below
```

## What to know

- **No `supabase link`, no `supabase login`, no database password.** `db push --project-ref` mints a temporary login
  role through the Management API with the access token. `supabase link` fails for every project and every
  personal access token since Supabase stopped revealing the legacy `service_role` key to one (measured 2026-09-10
  on CLI 2.116.0 and 2.117.0); Brigade never needed it.
- **`projects api-keys` prints every key, the legacy `service_role` JWT included.** `make backend-install` filters
  it down to the publishable key. If you run it by hand, paste its output nowhere.
- **The check** is `migration list`: every file under `supabase/migrations/` in both the `Local` and `Remote`
  column, in version order, no one-sided row. If `db push --dry-run` names a file you do not recognise, stop and
  find out why before pushing.
- **Order matters, and it is not a preference.** Migrations before exposing the schema; the command does it in
  that order for you.
- **Not `supabase config push`.** It sends this repository's `supabase/config.toml` whole, which describes the
  local development stack: it would set the hosted anonymous sign-in limit to 1000 per hour per IP and the site
  URL to `http://127.0.0.1:3000`. `make supabase-config-push` refuses without `i_know=1`.

## If a step fails

Two errors stop `make backend-install` before anything changes; both are safe to retry.

- `IPv6 is not supported on your current network`: the database has only an IPv6 address. `curl -6 -s -m 5
  https://api64.ipify.org` should print an address. On a Mac, `networksetup -getinfo Wi-Fi` showing `IPv6: Off`
  (some VPN clients leave it so) is fixed by `sudo networksetup -setv6automatic Wi-Fi`; otherwise use a network
  with IPv6, such as a phone hotspot.
- `unexpected login role status 403 … does not have the necessary privileges`: the token lacks Database
  read-write, does not cover this project or was made in another account (`npx --yes supabase@2.116.0 projects
  list` should show the project), or your organisation role is below Administrator.

## The settings, if you would rather click

`scripts/backend-settings.sh <ref>` writes four settings and re-checks every one without changing what is already
right; the dashboard equivalents are [Review the project's settings](../configure/project-settings.md).

Next: [Arm the keep-alive](keep-alive.md) on a free plan, then [Hand over the values](hand-over-the-values.md).
