# Install

From nothing to a project a Team Admin can create a team in. You need a Supabase account, [Node.js](https://nodejs.org)
(for `npx`), `jq`, `make`, a working IPv6 route, and a clone of this repository (`git clone
https://github.com/appshapes/brigade`), which holds the migrations.

## If you want to

| If you want to | Go to |
| --- | --- |
| create the Supabase project with the right settings | [Create the project](#create-the-project) |
| deploy Brigade's schema, functions and settings into it | [Deploy the backend](#deploy-the-backend) |
| keep a free-plan project from pausing | [Arm the keep-alive](#arm-the-keep-alive) |
| give a Team Admin what they need, and nothing more | [Hand over the values](#hand-over-the-values) |

## Create the project

1. [Supabase dashboard](https://supabase.com/dashboard) → New project: a region near the team (it cannot change later),
   a generated database password you never need again, Data API on, exposed schemas left at `public`.
2. Note the reference: the `<ref>` in `https://<ref>.supabase.co`.
3. Make a personal access token (`sbp_…`) at supabase.com/dashboard/account/tokens.

Details: [Create the project](create-the-project.md).

## Deploy the backend

1. In the Brigade clone: `read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN` (paste; nothing is echoed).
2. `make backend-install project=<ref>` (`dry=1` first lists what it would do). Its last line is the publishable key.
3. Check: every migration appears in both columns of the list it prints.

Details: [Deploy the backend](deploy-the-backend.md).

## Arm the keep-alive

1. Free plan only: `gh variable set BRIGADE_SUPABASE_URL --body https://<ref>.supabase.co` and
   `gh variable set BRIGADE_SUPABASE_PUBLISHABLE_KEY --body sb_publishable_…` in a repository that runs `keepalive.yml`.
2. A second project is a second job and a second variable pair.

Details: [Arm the keep-alive](keep-alive.md).

## Hand over the values

1. The project URL and the publishable key, both public, to the Team Admin.
2. The secret key, the service-role key, the database password and your access token stay on your machine.

Details: [Hand over the values](hand-over-the-values.md).
