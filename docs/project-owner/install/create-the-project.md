# Create the project

Only these are fixed when the project is created; everything else Brigade needs is applied afterwards by
`make backend-install`, which is idempotent and reads every field back.

- **Region**: the one choice that **cannot be changed later**. Put it near the people who will use it.
- **Postgres**: the current default (17, `ga` release channel) is right; Brigade pins nothing.
- **Database password**: generate one and do not record it. Nothing in Brigade uses it, and it can be reset from
  the dashboard at any time.
- **Enable Data API**: **on**, with its exposed-schema list left at the default `public`. **Do not add
  `brigade` here.** The schema does not exist until the migrations run, and a project exposing a schema that is
  not there leaves PostgREST looping on `3F000 schema "brigade" does not exist`, never healthy, with an error
  that names PostgREST rather than the cause. `make backend-install` appends `brigade` *after* the push.
- **Automatically expose new tables**: off, though it does not matter; the first migration revokes every grant
  on the schema's tables and grants exactly what it wants.
- **Enable automatic RLS**: on, equally redundant; the same migration enables row-level security explicitly.

One project for Brigade alone: anyone who can read its database can read every message of every team in it. The
free plan allows two active projects per organisation.

## Then

1. **The reference**: the `<ref>` in `https://<ref>.supabase.co`, also Project Settings → General → Reference ID.
2. **A personal access token**: avatar → Account Preferences → Access Tokens (supabase.com/dashboard/account/tokens)
   → Generate new token. It starts with `sbp_` and stays on your machine. A classic token (badge "Legacy")
   reaches every project you own. A scoped token needs, on this project: Database read-write, Data API Config
   read-write, Auth Config read-write, Realtime Config read-write, API Keys read; Projects read makes `projects
   list` work. One scoped token per project, deleted after the deploy, exposes one project if it leaks. (The
   permission list is read from Supabase's table, not measured with a token holding exactly these.)

Dashboard menu names change. If one differs, look for "API Keys" under the project's settings.

Next: [Deploy the backend](deploy-the-backend.md).
