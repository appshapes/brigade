# Supabase local development and CI workflow for the Brigade adapter

Date: 2026-08-30. Environment: macOS arm64, Docker 29.6.1 running, Node 24.16, npm 11.13, pnpm; Supabase CLI not installed.
Everything version-sensitive below was checked today against official docs, the `supabase/cli` source tree (branch `develop`), npm, Homebrew and GitHub. Confidence is marked where it is less than verified.

## 0. Decisions this digest recommends

1. Install the CLI as an exact-pinned devDependency (`supabase@2.116.0`) and run it through `npx supabase` / `pnpm exec supabase`. Do not rely on Homebrew for the repo workflow; CI (`supabase/setup-cli@v3`) reads the same version from the lockfile, so local and CI match without extra configuration.
2. Keep the schema in `supabase/migrations/*.sql` (plain migrations, not declarative schemas) and test it two ways: pgTAP RLS tests (`supabase test db`, fast, transactional) and supabase-js integration tests against the running local stack with several *real* anonymous principals (this is the only way to exercise `signInAnonymously`, the JWT path, and Realtime).
3. Set `[auth] enable_anonymous_sign_ins = true` and raise `[auth.rate_limit] anonymous_users` locally; disable analytics and exclude unneeded containers to keep `supabase start` fast in CI.
4. Do not make correctness depend on `pg_cron`. Use it only for housekeeping (lease sweeps, retention). Expiry must be enforced in queries (`last_seen_at > now() - lease`).
5. Treat the Supabase *publishable* key as distributable configuration and the *secret*/service_role key as never-leaves-the-admin-machine. The local stack's keys are hard-coded, well-known values.
6. An install script for a self-hosted team backend is feasible with the CLI, given a personal access token; it should default to "existing project ref" and make project creation optional.

## 1. Installing and pinning the CLI

Verified today:

- npm package `supabase`: latest `2.116.0`, published 2026-08-26; dist-tags `latest`, `beta` (`2.117.0-beta.6`), `hotfix` (`1.142.2`). The package is a JS shim (`bin: dist/supabase.js`) that resolves a per-platform binary from `optionalDependencies` (`@supabase/cli-darwin-arm64`, `@supabase/cli-linux-x64`, ...). There is no install script, so `npm ci --ignore-scripts` still works. Source: https://registry.npmjs.org/supabase
- GitHub release `v2.116.0`, 2026-08-26: https://github.com/supabase/cli/releases
- The repo has been restructured into a TypeScript/Bun workspace: `apps/cli` (TS CLI), `apps/cli-go` (legacy Go CLI, still the implementation behind most commands), `packages/stack` (local runtime), `packages/config` (config.toml schema). Source: https://github.com/supabase/cli (README, repository layout)
- Official install docs: `npm install supabase --save-dev` then `npx supabase --help`; "Pin the version in package.json so your whole team uses the same CLI version"; Node.js 20 or later required for npx use. https://supabase.com/docs/guides/local-development/cli/getting-started
- Homebrew: `brew install supabase/tap/supabase` (docs) and `brew install supabase` (homebrew-core formula, also at 2.116.0 today). Source: README and https://formulae.brew.sh/api/formula/supabase.json
- Docker: `supabase start` "Requires Docker or compatible container runtime"; the start docs recommend at least 7 GB RAM for the full stack. https://supabase.com/docs/reference/cli/supabase-start and `apps/cli/docs/supabase/start.md`

Recommendation for this repo:

```json
{ "devDependencies": { "supabase": "2.116.0" } }
```

and in the Makefile `supabase := npx supabase` (resolves `node_modules/.bin/supabase`; with pnpm use `pnpm exec supabase`). Never use bare `npx supabase@latest`; the version must come from the lockfile so that `supabase/setup-cli@v3` installs the identical version in CI (see section 9).

Homebrew is fine as a personal convenience but the repo must not depend on it (verified-facts: not installed on this machine, and CI would diverge).

## 2. `supabase init` and project layout

- `supabase init` writes `supabase/config.toml` from the default template and, when inside a git repository, `supabase/.gitignore`. Optional flags `--force`, `--with-vscode-settings`, `--with-intellij-settings`. It does not create `migrations/`, `seed.sql` or `functions/` (the local-development overview page still says it does; the current CLI's own side-effect doc lists only `config.toml` and `.gitignore`). Sources: https://github.com/supabase/cli/blob/develop/apps/cli/src/legacy/commands/init/SIDE_EFFECTS.md and https://supabase.com/docs/guides/local-development/overview
- `supabase migration new <name>` creates `supabase/migrations/` if absent and writes `<timestamp>_<name>.sql`; output of `db diff` can be piped to it via stdin. https://supabase.com/docs/reference/cli/supabase-migration-new
- Seeds: `[db.seed] enabled = true`, `sql_paths = ["./seed.sql"]` (globs allowed, relative to `supabase/`, processed in declared order). Seeds run on first `supabase start` and every `supabase db reset`, after migrations. https://supabase.com/docs/guides/local-development/seeding-your-database
- The directory override is `--workdir` or `SUPABASE_WORKDIR` (init.md).

Proposed layout for Brigade:

```
supabase/
  config.toml
  migrations/
    20260830120000_brigade_schema.sql      # tables, RLS, RPCs
    20260830120100_brigade_realtime.sql    # publication / broadcast triggers
    20260830120200_brigade_housekeeping.sql# pg_cron jobs (optional)
  seed.sql                                 # empty or test fixtures only
  tests/
    helpers/auth.sql                       # \ir-included helper functions (see 6.2)
    rls_team_isolation.sql
    rls_sessions.sql
    rls_messages.sql
  .gitignore                               # written by init (.temp, .branches, .env ...)
```

Create `supabase/seed.sql` explicitly (may be empty). Confidence "likely": a missing literal `sql_paths` entry is skipped rather than fatal, but this was not executed today.

## 3. `supabase start`, `status`, `stop`

Ports (config defaults, verified from the current template and config reference):

| Service | Port | Key |
| --- | --- | --- |
| API gateway (Kong): REST `/rest/v1`, Auth `/auth/v1`, Realtime `/realtime/v1`, GraphQL `/graphql/v1` | 54321 | `[api] port` |
| Postgres | 54322 | `[db] port` |
| Shadow DB (used by `db diff`) | 54320 | `[db] shadow_port` |
| Studio | 54323 | `[studio] port` |
| Mailpit (email testing UI) | 54324 | `[local_smtp] port` |
| Analytics (Logflare) / Vector | 54327 / 54328 | `[analytics]` |
| Pooler (Supavisor) | 54329 | `[db.pooler]` (disabled by default) |

Sources: https://supabase.com/docs/guides/local-development/cli/config and https://github.com/supabase/cli/blob/develop/apps/cli-go/pkg/config/templates/config.toml

Connection values:

- Project URL `http://127.0.0.1:54321`; DB URL `postgresql://postgres:postgres@127.0.0.1:54322/postgres`; Studio `http://127.0.0.1:54323`. https://supabase.com/docs/guides/local-development/cli/getting-started
- Keys. The current CLI prints "Publishable" and "Secret" keys (pretty output groups: Development Tools, APIs, Database, Authentication Keys, Storage S3). The local defaults are hard-coded constants: publishable `sb_publishable_ACJWlzQHlZjBrEguHvfOxg_3BJgxAaH`, secret `sb_secret_N7UND0UgjKTVK-Uodkm0Hg_xSvEMPvz`, JWT secret `super-secret-jwt-token-with-at-least-32-characters-long`. They can be overridden via `[auth] publishable_key`, `secret_key`, `jwt_secret`, `anon_key`, `service_role_key` (Secret-typed, so `env(...)` works). Sources: https://github.com/supabase/cli/blob/develop/packages/stack/src/JwtGenerator.ts ("Hardcoded opaque key defaults matching Go CLI (pkg/config/apikeys.go)") and https://github.com/supabase/cli/blob/develop/apps/cli-go/pkg/config/auth.go
- Legacy `anon`/`service_role` JWTs are still generated locally from the JWT secret and still exported by `supabase status -o env`, tagged deprecated in source. Exported names: `API_URL`, `GRAPHQL_URL`, `DB_URL`, `STUDIO_URL`, `MAILPIT_URL`, `INBUCKET_URL` (deprecated), `PUBLISHABLE_KEY`, `SECRET_KEY`, `JWT_SECRET` (deprecated), `ANON_KEY` (deprecated), `SERVICE_ROLE_KEY` (deprecated), `S3_PROTOCOL_ACCESS_KEY_ID`, `S3_PROTOCOL_ACCESS_KEY_SECRET`, `S3_PROTOCOL_REGION`. `--override-name api.url=SUPABASE_URL` renames on export. Source: https://github.com/supabase/cli/blob/develop/apps/cli-go/internal/status/status.go and https://supabase.com/docs/reference/cli/supabase-status . Confidence on "deprecated names are still emitted in `-o env`": likely (the values map is populated unconditionally; whether the encoder filters the `deprecated` tag was not executed).
- Hosted keys: publishable (`sb_publishable_...`) maps to `anon` when unauthenticated and `authenticated` when a user JWT is present; secret (`sb_secret_...`) maps to `service_role` and bypasses RLS; legacy JWT keys "will be deprecated by the end of 2026". https://supabase.com/docs/guides/getting-started/api-keys
- Known gotcha: issue #4524 (CLI 2.51.0) reports the local `sb_secret_...` key failing with "invalid JWT ... token contains an invalid number of segments" when used for an admin client, while the legacy service_role JWT worked; closed as not planned. https://github.com/supabase/cli/issues/4524 . For test fixtures that need admin access locally, be prepared to fall back to `SERVICE_ROLE_KEY`.

Commands:

- `supabase start [-x svc,...] [--ignore-health-check]`. Excludable: `gotrue, realtime, storage-api, imgproxy, kong, mailpit, postgrest, postgres-meta, studio, edge-runtime, logflare, vector, supavisor`. First run pulls images (Postgres `17.6.1.165`, PostgREST `v16.2`, Auth `v2.196.0`, Realtime `v2.129.9`, Storage `v1.71.0`, Studio, pg-meta `0.98.0`, Mailpit, imgproxy, edge-runtime, Logflare, Vector, Supavisor as of the current catalog). Sources: https://supabase.com/docs/reference/cli/supabase-start and https://github.com/supabase/cli/blob/develop/packages/stack/src/ServiceCatalog.ts
- `supabase db start` starts only Postgres and applies migrations; official CI docs use it before `supabase test db`. https://supabase.com/docs/guides/deployment/ci/testing
- `supabase status [-o env|json|pretty|toml|yaml] [--override-name k=v]`.
- `supabase stop [--no-backup] [--all]`: data is kept across restarts unless `--no-backup`. `apps/cli/docs/supabase/stop.md`
- `supabase db reset [--no-seed] [--sql-paths ...] [--version] [--last n] [--linked|--db-url]`: recreates the local Postgres container, applies all migrations, then seeds; discards other local changes. https://supabase.com/docs/reference/cli/supabase-db-reset and `apps/cli/docs/supabase/db/reset.md`
- `supabase db diff [--local] [-s public] [-f name]`: produces SQL for schema changes; pipe or `-f` into a migration. https://supabase.com/docs/guides/local-development/overview

For Brigade's adapter (a CLI, no browser, no email, no storage, no edge functions) the minimal integration stack is Postgres + PostgREST + GoTrue + Realtime + Kong:

```
supabase start -x studio,postgres-meta,imgproxy,storage-api,edge-runtime,mailpit,logflare,vector,supavisor
```

Note `gen types --local` does not need the `postgres-meta` service: it runs a one-shot pg-meta container itself (needs only `supabase_db_<project_id>` running). Source: https://github.com/supabase/cli/blob/develop/apps/cli/src/legacy/commands/gen/types/SIDE_EFFECTS.md

## 4. Type generation

- `supabase gen types --lang typescript --local > src/adapters/supabase/database.types.ts`. Flags: `--lang typescript|go|swift|python` (default typescript), `--local`, `--linked`, `--db-url`, `--project-id`, `--schema/-s` (comma list), `--postgrest-v9-compat`, `--query-timeout`. Source: https://supabase.com/docs/reference/cli/supabase-gen-types and https://github.com/supabase/cli/blob/develop/apps/cli/src/legacy/commands/gen/types/types.command.ts
- The older positional form `supabase gen types typescript --local` still appears in the current setup-cli README (v3). Confidence that the positional form is accepted by the 2.116 CLI: uncertain; use `--lang`.
- CI check for drift (from the setup-cli README): regenerate and `git diff --exit-code` the generated file.

## 5. Anonymous sign-ins and realtime settings in config.toml

Verified keys and defaults (current template):

- `[auth] enable_anonymous_sign_ins = false` (default) -> set `true`.
- `[auth.rate_limit] anonymous_users = 30` "Number of anonymous sign-ins that can be made per hour per IP address. Requires enable_anonymous_sign_ins = true." Local tests all come from 127.0.0.1, so raise it (e.g. 1000) for the local/CI profile. `sign_in_sign_ups = 30` "(excludes anonymous users)". `token_refresh = 150` per 5 minutes per IP.
- `[auth] jwt_expiry = 3600` (max 604800), `enable_refresh_token_rotation = true`, `refresh_token_reuse_interval = 10`, `enable_signup = true` (must stay true for anonymous sign-ins? Not verified; hosted docs only require the anonymous toggle. Mark uncertain; keep `enable_signup = true` locally).
- `[realtime] enabled = true`, `ip_version` (template comment says default IPv4; the docs page says IPv6), `max_header_length = 4096`. These three are the only realtime keys; hosted-only settings (`private_only`, quotas) have no config.toml counterpart. Sources: template above and https://github.com/supabase/cli/blob/develop/packages/config/src/project-config/registry.ts (comment listing the 12 unmapped hosted `realtime.*` fields)
- `[db] major_version = 17` in the current template (docs reference still says 15). Must match the hosted project (`show server_version;`).
- `[analytics] enabled = true` in the current template (docs say default false). Set `false` to avoid starting Logflare and Vector.
- `[api] schemas = ["public", "graphql_public"]`, `extra_search_path = ["public", "extensions"]`, `max_rows = 1000`.
- `[db.migrations] enabled = true`, `schema_paths = []` (declarative schemas; leave empty).

Hosted anonymous sign-ins: dashboard toggle, or `supabase config push` (the CLI maps `auth.enable_anonymous_sign_ins` -> Management API `external_anonymous_users_enabled` and `auth.rate_limit.anonymous_users` -> `rate_limit_anonymous_users`), or `PATCH /v1/projects/{ref}/config/auth`. Sources: https://supabase.com/docs/guides/auth/auth-anonymous , https://github.com/supabase/cli/blob/develop/apps/cli/src/legacy/commands/config/push/config-sync/auth.sync.ts , https://supabase.com/docs/reference/api/v1-update-auth-service-config

Anonymous-user semantics relevant to RLS: the JWT carries `is_anonymous`; `auth.users.is_anonymous` column exists; policies can require permanent users with `(select (auth.jwt()->>'is_anonymous')::boolean) is false`. Hosted rate limit "30 requests per hour" per IP by default, and Supabase "strongly recommend[s]" CAPTCHA/Turnstile for anonymous sign-ins (impractical for a CLI adapter; accept the IP rate limit instead, and keep one anonymous principal per adapter profile with refresh-token persistence so sign-in happens once). https://supabase.com/docs/guides/auth/auth-anonymous

Proposed `supabase/config.toml` (only the lines that differ from `supabase init` output; keep the rest of the template):

```toml
project_id = "brigade"

[api]
enabled = true
port = 54321
schemas = ["public", "graphql_public"]
extra_search_path = ["public", "extensions"]
max_rows = 1000

[db]
port = 54322
shadow_port = 54320
major_version = 17          # must equal the hosted project's major version

[db.migrations]
enabled = true
schema_paths = []

[db.seed]
enabled = true
sql_paths = ["./seed.sql"]

[realtime]
enabled = true
# ip_version = "IPv4"
# max_header_length = 4096

[studio]
enabled = false             # not needed for CLI/adapter work; re-enable locally if you want the UI

[local_smtp]
enabled = false             # no email flows in Brigade

[storage]
enabled = false

[edge_runtime]
enabled = false

[analytics]
enabled = false             # template default is true; avoids Logflare + Vector containers

[auth]
enabled = true
site_url = "http://127.0.0.1:3000"
jwt_expiry = 3600
enable_refresh_token_rotation = true
refresh_token_reuse_interval = 10
enable_signup = true
enable_anonymous_sign_ins = true
minimum_password_length = 6

[auth.rate_limit]
anonymous_users = 1000      # local/CI only: every test client shares 127.0.0.1 (hosted default 30/h/IP)
token_refresh = 150
sign_in_sign_ups = 30

[auth.email]
enable_signup = false        # Brigade principals are anonymous; no email accounts
enable_confirmations = false

[auth.sms]
enable_signup = false
```

Whether `[studio] enabled = false` etc. is honoured by `supabase start` exactly like `-x`: the status code checks `Config.Studio.Enabled`, `Config.Auth.Enabled`, `Config.Storage.Enabled`, `Config.EdgeRuntime.Enabled`, `Config.Inbucket.Enabled` (verified in status.go), so `enabled = false` sections are not started. Keep `-x` in the Makefile as belt-and-braces for CI.

## 6. Testing

### 6.1 pgTAP with `supabase test db`

Verified behaviour (CLI side-effects doc, current branch):

- Discovery: `supabase/tests/**/*.{sql,pg}` recursively (or explicit paths). Runs `pg_prove --ext .pg --ext .sql -r` inside `public.ecr.aws/supabase/pg_prove:3.36` attached to the local Docker network (`PGHOST=db`). Needs only the database container (`supabase start` or `supabase db start`).
- Before running it executes `create extension if not exists pgtap with schema extensions`, and drops it afterwards if it did not pre-exist. You do not need pgtap in a migration (keep it out of production schemas).
- "Each test is wrapped in its own transaction, it will be individually rolled back regardless of success or failure."
- psql `\ir`/`\i` includes resolve relative to the test file's own directory (a test file is mounted via its containing directory for this reason). This is the supported way to share helpers between test files.
- `supabase test new <name>` writes `supabase/tests/<name>.sql` with the template `BEGIN; SELECT plan(1); ... SELECT * FROM finish(); ROLLBACK;`.

Sources: https://github.com/supabase/cli/blob/develop/apps/cli/src/legacy/commands/test/db/SIDE_EFFECTS.md , https://supabase.com/docs/reference/cli/supabase-test-db , https://supabase.com/docs/guides/database/testing , https://github.com/supabase/cli/blob/develop/apps/cli/src/legacy/commands/test/new/new.template.ts

### 6.2 Simulating principals in SQL (how RLS is actually evaluated)

Current definitions (from the Auth service migrations that own the `auth` schema):

```sql
-- auth.uid()  (supabase/auth migrations/20211202183645_update_auth_uid.up.sql)
select nullif(coalesce(
  current_setting('request.jwt.claim.sub', true),
  (current_setting('request.jwt.claims', true)::jsonb ->> 'sub')), '')::uuid

-- auth.jwt()  (supabase/auth migrations/20220531120530_add_auth_jwt_function.up.sql)
select coalesce(
  nullif(current_setting('request.jwt.claim', true), ''),
  nullif(current_setting('request.jwt.claims', true), ''))::jsonb
```

Sources: https://github.com/supabase/auth/blob/master/migrations/20211202183645_update_auth_uid.up.sql , https://github.com/supabase/auth/blob/master/migrations/20220531120530_add_auth_jwt_function.up.sql

Consequences for tests:

- Official docs simulate a user with `set local role authenticated; set local request.jwt.claim.sub = '<uuid>';` (https://supabase.com/docs/guides/local-development/testing/overview and https://supabase.com/docs/guides/database/postgres/row-level-security). That only feeds `auth.uid()`. Policies that read `auth.jwt()` (for `is_anonymous`, `role`, custom claims) need the JSON GUC `request.jwt.claims`.
- Set both consistently. Because `coalesce` prefers `request.jwt.claim.sub`, once that GUC has been set to a non-empty value in a transaction, the JSON `sub` is ignored; and an empty string yields NULL rather than falling through. Use one helper for all identity switches:

```sql
-- supabase/tests/helpers/auth.sql  (included with \ir from each test file, inside its transaction)
create or replace function pg_temp.login(uid uuid, anon boolean default true, label text default null)
returns void language plpgsql as $$
begin
  perform set_config('request.jwt.claim.sub', uid::text, true);
  perform set_config('request.jwt.claims', jsonb_build_object(
    'sub', uid, 'role', 'authenticated', 'aud', 'authenticated',
    'is_anonymous', anon, 'user_metadata', jsonb_build_object('human_label', label))::text, true);
  execute 'set local role authenticated';
end $$;

create or replace function pg_temp.logout() returns void language plpgsql as $$
begin
  perform set_config('request.jwt.claim.sub', '', true);
  perform set_config('request.jwt.claims', '', true);
  execute 'set local role anon';
end $$;

create or replace function pg_temp.new_user(anon boolean default true) returns uuid language plpgsql as $$
declare uid uuid := gen_random_uuid();
begin
  insert into auth.users (id, aud, role, is_anonymous) values (uid, 'authenticated', 'authenticated', anon);
  return uid;
end $$;
```

The insert into `auth.users` with only a few columns follows the official example (`insert into auth.users (id, email) values (...)`); `is_anonymous` is a real column per the anonymous-auth docs. Confidence: likely (not executed today). `set local role` is permitted because pg_prove connects as `postgres`, which can `set role` to the API roles; the official examples rely on the same.

Alternative: the basejump helpers (`tests.create_supabase_user`, `tests.authenticate_as`, `tests.authenticate_as_service_role`, `tests.clear_authentication`, `tests.rls_enabled(schema)`, `tests.freeze_time`), installable via dbdev or by copying the SQL into the tests tree; version 0.0.6. They do not model `is_anonymous`, so Brigade would still need the custom helper above. Sources: https://supabase.com/docs/guides/local-development/testing/pgtap-extended , https://github.com/usebasejump/supabase-test-helpers

Example Brigade test (structure mirrors the official todos example):

```sql
-- supabase/tests/rls_team_isolation.sql
begin;
\ir helpers/auth.sql
select plan(6);

-- fixtures as service_role (bypasses RLS)
set local role service_role;
insert into public.teams (id, name, join_secret_hash) values
  ('11111111-1111-1111-1111-111111111111', 'alpha', extensions.digest('alpha-secret', 'sha256')),
  ('22222222-2222-2222-2222-222222222222', 'beta',  extensions.digest('beta-secret',  'sha256'));
select pg_temp.logout();

-- alice (anonymous principal) joins alpha through the RPC, never by inserting a membership row
select pg_temp.login(pg_temp.new_user(), true, 'alice@example.com');
select lives_ok($$ select public.join_team('alpha-secret') $$, 'alice can join alpha with the secret');
select throws_ok($$ insert into public.team_members (team_id, principal_id)
                    values ('22222222-2222-2222-2222-222222222222', auth.uid()) $$,
                 '42501', null, 'alice cannot self-insert a membership in beta');

select lives_ok($$ select public.register_session('payments-api', 'idem-1') $$, 'alice registers a session');

-- bob joins beta
select pg_temp.login(pg_temp.new_user(), true, 'bob@example.com');
select public.join_team('beta-secret');
select results_eq($$ select count(*) from public.sessions $$, ARRAY[0::bigint],
                  'bob cannot see sessions in alpha');
select is_empty($$ update public.sessions set session_name = 'x' returning 1 $$,
                'bob cannot update alpha sessions');
select throws_ok($$ select public.send_message((select id from public.sessions limit 1), 'hi', 'idem-2') $$,
                 'bob cannot send to a session he cannot see');

select * from finish();
rollback;
```

Assertions used: `results_eq`, `lives_ok`, `throws_ok('42501', 'new row violates row-level security policy for table "x"')`, `is_empty` with `returning` (docs recommend this to prove a denied write changed nothing), `policies_are`, `results_ne`. Sources: RLS docs "Testing policies" section and https://supabase.com/docs/guides/database/extensions/pgtap

### 6.3 supabase-js integration tests with several anonymous principals

What pgTAP cannot cover: GoTrue's `signInAnonymously`, JWT issuance and refresh, PostgREST behaviour with a real bearer token, Realtime delivery under RLS, and the adapter CLI end to end. The official "Application-Level testing" guidance: do not reset the DB per test; make tests independent with unique IDs. https://supabase.com/docs/guides/local-development/testing/overview

Pattern (Node 24, vitest):

```ts
import { createClient } from '@supabase/supabase-js'
const url = process.env.SUPABASE_URL!             // http://127.0.0.1:54321
const key = process.env.SUPABASE_PUBLISHABLE_KEY! // sb_publishable_... (local: well-known default)

function principal() {
  return createClient(url, key, { auth: { persistSession: false, autoRefreshToken: false } })
}
const alice = principal(); const bob = principal(); const mallory = principal()
await alice.auth.signInAnonymously()   // distinct auth.users row + JWT per client
await bob.auth.signInAnonymously()
await mallory.auth.signInAnonymously()
await alice.rpc('join_team', { secret: alphaSecret })
await bob.rpc('join_team',   { secret: alphaSecret })
await mallory.rpc('join_team', { secret: betaSecret })
// assertions: alice/bob list each other; mallory lists nothing; mallory.send -> error; etc.
```

- `createClient(url, key, options)`; the key may be a publishable key or the legacy anon key; for server-side/Node use `persistSession: false`. https://supabase.com/docs/reference/javascript/initializing
- `supabase.auth.signInAnonymously({ options: { data, captchaToken } })` "Creates a new anonymous user" and returns `data.user`, `data.session` (access + refresh token). https://supabase.com/docs/reference/javascript/auth-signinanonymously
- `@supabase/supabase-js` latest `2.112.4` (2026-08-24), `engines.node >= 22` (fine for Node 24). https://registry.npmjs.org/@supabase/supabase-js
- Realtime in tests: with a user JWT the client must call `supabase.realtime.setAuth()` (or the client does so after sign-in) so RLS applies; `postgres_changes` requires the table in the `supabase_realtime` publication (`alter publication supabase_realtime add table public.messages;`), authenticated subscribers "only receive rows they can SELECT under RLS", and "Realtime performs ... authorization checks — one per user" per change, so prefer Broadcast for scale. Broadcast from the database: `realtime.send(payload jsonb, event text, topic text, private boolean)` / `realtime.broadcast_changes(...)` from a trigger, client subscribes with `config: { private: true }`; access is governed by RLS on `realtime.messages` using `realtime.topic()`; rows there "will be deleted after 3 days". Sources: https://supabase.com/docs/guides/realtime/postgres-changes , https://supabase.com/docs/guides/realtime/broadcast , https://supabase.com/docs/guides/realtime/authorization . The local Realtime image (`v2.129.9`) is recent enough to include `realtime.send` (migrations in the realtime repo date from 2024-11 onward); confidence: likely, not executed.
- Env for tests comes from `supabase status -o env` (section 10), never hard-coded.

## 7. Extensions locally: pgcrypto and pg_cron

pgcrypto:

- `create extension if not exists pgcrypto with schema extensions;` (`extensions` is on the default `extra_search_path`). Provides `gen_random_bytes(count)`, `digest(data, 'sha256')`, `hmac(...)`, `crypt`/`gen_salt`. `gen_random_uuid()` is a core function (pgcrypto's copy is marked obsolete), so UUID primary keys need no extension. `uuidv7()` exists only in PostgreSQL 18, not in the local/hosted 17 default. Sources: https://www.postgresql.org/docs/current/pgcrypto.html , https://www.postgresql.org/docs/current/functions-uuid.html
- pgcrypto is in the image's `supautils.privileged_extensions` list, so the `postgres` role can create it in a migration. https://github.com/supabase/postgres/blob/develop/ansible/files/postgresql_config/supautils.conf.j2
- Use: `gen_random_bytes(32)` for join secrets/idempotency tokens generated server-side; store `digest(secret, 'sha256')` not the secret.

pg_cron:

- Local availability: the CLI's Postgres config template has `shared_preload_libraries = 'pg_stat_statements, pg_cron, pg_net, pgsodium, supabase_vault, supautils'` and `cron.database_name = 'postgres'`; the local database is `postgres`, so the extension can be created in a migration. `supautils.extensions_parameter_overrides = '{"pg_cron":{"schema":"pg_catalog"}}'` forces the schema. Sources: https://github.com/supabase/postgres/blob/develop/nix/packages/cli-config/postgresql.conf.template , https://github.com/supabase/postgres/blob/develop/docker/pgctld/postgresql.conf.tmpl , supautils.conf.j2 above
- Official install SQL: `create extension pg_cron with schema pg_catalog; grant usage on schema cron to postgres; grant all privileges on all tables in schema cron to postgres;` https://github.com/supabase/supabase/blob/master/apps/docs/content/guides/cron/install.mdx (rendered at https://supabase.com/docs/guides/cron)
- Scheduling: `select cron.schedule('brigade-expire-leases', '30 seconds', $$ ... $$);` Sub-minute intervals require "Postgres version 15.1.1.61 or later" (local 17.6.x qualifies). `cron.unschedule(name)`, `cron.job`, `cron.job_run_details`. Recommended no more than 8 concurrent jobs, each under 10 minutes. https://supabase.com/docs/guides/cron/quickstart , https://supabase.com/docs/guides/cron
- Known friction: creating pg_cron via migration has historically left grants/dashboard state inconsistent (https://github.com/supabase/cli/issues/1591, closed issue #158 on local install). Write the migration idempotently (`create extension if not exists pg_cron with schema pg_catalog;` followed by the grants) and treat cron as optional: `describe` capabilities should not depend on it, and lease/retention semantics must be enforced by predicates in views and RPCs.

## 8. Hosted project: link, push, config

- Authentication: `supabase login` (PAT from https://supabase.com/dashboard/account/tokens ; stored in the OS keyring, else `~/.supabase/access-token`) or `SUPABASE_ACCESS_TOKEN` env var for CI/scripts. `apps/cli/docs/supabase/login.md`
- `supabase link --project-ref <ref> [-p <db password>]` validates PostgREST config against local `config.toml`, saves the DB password in the keyring, writes `supabase/.temp/project-ref` (gitignore `supabase/.temp/`); `SUPABASE_DB_PASSWORD` avoids the prompt. https://supabase.com/docs/reference/cli/supabase-link
- `supabase db push [--dry-run] [--include-seed] [--include-roles] [--linked | --db-url <conn>]`: applies unapplied local migrations; history table `supabase_migrations.schema_migrations`; `--db-url` works without linking (self-hosted or scripted). https://supabase.com/docs/reference/cli/supabase-db-push
- `supabase migration list`, `migration repair --status applied|reverted <version>`.
- `supabase config push [--project-ref] [--yes]`: diffs and PATCHes `api` (`/v1/projects/{ref}/postgrest`), `db` settings, `auth` (`/v1/projects/{ref}/config/auth`), `storage`; it reads `supabase/.env` / `.env.local` for `env()` values. This is how to enable anonymous sign-ins on the hosted project from `config.toml`. https://github.com/supabase/cli/blob/develop/apps/cli/src/legacy/commands/config/push/SIDE_EFFECTS.md
- Per-environment overrides: `[remotes.<name>]` blocks in config.toml (all root options supported) for staging vs production. https://supabase.com/docs/guides/local-development/cli/config
- Official deploy workflow uses `SUPABASE_ACCESS_TOKEN`, `SUPABASE_DB_PASSWORD`, `SUPABASE_PROJECT_ID` secrets and `supabase link` + `supabase db push`. https://supabase.com/docs/guides/deployment/managing-environments

### What an end user must do to self-host a team backend

Minimum manual steps (dashboard path):

1. Create a Supabase account and organization; create a project (choose region, set DB password).
2. Enable anonymous sign-ins (Authentication -> Providers) and optionally raise the anonymous rate limit.
3. Apply Brigade's migrations.
4. Copy the project URL and publishable key; distribute them plus a Brigade join secret to the team.

CLI/scriptable path (all verified as existing commands/endpoints):

- Create project: `supabase projects create <name> --org-id <org> --db-password <pw> --region <region> [--size]` (https://supabase.com/docs/reference/cli/supabase-projects-create) or `POST /v1/projects` with `name`, `organization_slug`, `db_pass`, `region_selection`... (https://supabase.com/docs/reference/api/v1-create-a-project). Provisioning is asynchronous; poll `supabase projects list` / `GET /v1/projects/{ref}` until healthy (exact status enum not verified today).
- Keys: `supabase projects api-keys --project-ref <ref> [--reveal]` -> `GET /v1/projects/{ref}/api-keys[?reveal=true]`; response items have `name`, `type` (`legacy|publishable|secret`), `api_key`; the `default` publishable key is what the adapter needs. https://supabase.com/docs/reference/api/v1-get-project-api-keys and the CLI's recorded fixture https://github.com/supabase/cli/blob/develop/apps/cli-e2e/fixtures/recorded/GET_v1_projects___PROJECT_REF___api_keys/default.response.json
- Config: `supabase config push` with `enable_anonymous_sign_ins = true`, or `PATCH /v1/projects/{ref}/config/auth {"external_anonymous_users_enabled": true, "rate_limit_anonymous_users": N}`.
- Schema: `supabase link --project-ref <ref>` + `supabase db push`, or `supabase db push --db-url postgresql://postgres.<ref>:<pw>@...` without linking.

So yes, a `brigade backend install` (or `make backend-install project=<ref>`) can drive everything through the CLI given `SUPABASE_ACCESS_TOKEN` and `SUPABASE_DB_PASSWORD`. Recommendation: make "existing project ref" the default path (creating projects touches billing/org membership and async provisioning), and print the URL + publishable key at the end. The PAT and DB password are needed only by the team administrator at install time; team members receive only URL + publishable key + join secret. Shipping the migrations inside the plugin/adapter package (not requiring a git checkout) keeps this a one-command operation.

## 9. GitHub Actions

Verified facts:

- `supabase/setup-cli` latest is `v3.0.0` (2026-07-07): installs the CLI from the npm package; `version` accepts `latest`, `beta`, or a fixed npm version; if omitted it reads the `supabase` version from `bun.lock` / `pnpm-lock.yaml` / `package-lock.json` (verifying integrity against the registry) and falls back to `latest`; requires Node 20+; `github-token` input removed. v1 tags still exist and are maintained, and the Supabase docs pages still show `@v1`. Sources: https://github.com/supabase/setup-cli (README) and https://github.com/supabase/setup-cli/releases
- Docker Engine 28.0.4 and Compose 2.38.2 are preinstalled on `ubuntu-24.04` (`ubuntu-latest`); Node 22 default, 24.19 cached. https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md
- Official CI examples: `supabase db start` then `supabase test db` for pgTAP; `supabase start` for integration tests; export env with `supabase status -o env --override-name api.url=SUPABASE_URL ... >> .env.test`. https://supabase.com/docs/guides/deployment/ci/testing and setup-cli README
- Timing: `supabase start` on a clean runner takes roughly 2-3 minutes, mostly image pulls (https://github.com/supabase/cli/issues/2724). Caching images with `docker save`/`docker load` + `actions/cache` is possible, but a maintainer noted "data transfer for the cache seems about equal to just running supabase start directly" (https://github.com/orgs/supabase/discussions/20081). Excluding services (`-x`) and `[analytics] enabled = false` are the effective levers; image caching is not recommended.

Proposed `.github/workflows/ci.yml`:

```yaml
name: ci
on:
  push: { branches: [master] }
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: 24, cache: npm }
      - run: npm ci --ignore-scripts
      - uses: supabase/setup-cli@v3          # version resolved from package-lock.json
      - run: make typecheck lint build test-unit
      - name: Start local Supabase (db, auth, rest, realtime, kong only)
        run: make supabase-start
      - name: pgTAP RLS tests
        run: make supabase-test
      - name: Export local env for integration tests
        run: make supabase-env               # writes .env.test via `supabase status -o env`
      - name: Integration tests (supabase-js + adapter CLI)
        run: make test-integration
      - if: always()
        run: npx supabase stop --no-backup

  deploy-staging:                           # optional; only if a hosted staging project exists
    if: github.ref == 'refs/heads/master'
    needs: test
    runs-on: ubuntu-latest
    env:
      SUPABASE_ACCESS_TOKEN: ${{ secrets.SUPABASE_ACCESS_TOKEN }}
      SUPABASE_DB_PASSWORD: ${{ secrets.SUPABASE_DB_PASSWORD }}
      SUPABASE_PROJECT_ID: ${{ secrets.SUPABASE_PROJECT_ID }}
    steps:
      - uses: actions/checkout@v4
      - uses: supabase/setup-cli@v3
      - run: supabase link --project-ref "$SUPABASE_PROJECT_ID"
      - run: supabase db push --dry-run
      - run: supabase db push
      - run: supabase config push --yes
```

Notes: `setup-cli` needs Node/npm to be present (it provisions its own if missing); running `actions/setup-node` first keeps a single Node version. `SUPABASE_YES=1` also auto-confirms prompts (config push side-effects doc).

## 10. Secrets and `.env` handling

Verified CLI behaviour: when loading `config.toml`, the CLI walks from the `supabase/` directory up to the repository root and, in each directory, loads `.env.<SUPABASE_ENV>.local`, `.env.local` (skipped when `SUPABASE_ENV=test`), `.env.<SUPABASE_ENV>`, `.env` (`SUPABASE_ENV` defaults to `development`). Values are available to `env(VAR)` references in `config.toml`. Source: https://github.com/supabase/cli/blob/develop/apps/cli-go/pkg/config/config.go (`loadNestedEnv`, `loadDefaultEnv`). The docs show `.env` and `.env.example` next to `supabase/` and say not to commit `.env`. https://github.com/supabase/supabase/blob/master/apps/docs/content/guides/local-development/managing-config.mdx

Recommended split for Brigade:

- `.env.example` (committed): documents variables only.

```dotenv
# Local Supabase stack (values printed by `make supabase-env`; the local keys are well-known dev constants)
SUPABASE_URL=http://127.0.0.1:54321
SUPABASE_PUBLISHABLE_KEY=
# Admin-only, used by test fixtures; never commit real values
SUPABASE_SECRET_KEY=
SUPABASE_SERVICE_ROLE_KEY=
SUPABASE_DB_URL=postgresql://postgres:postgres@127.0.0.1:54322/postgres
# Hosted project operations (maintainers/CI only)
SUPABASE_ACCESS_TOKEN=
SUPABASE_PROJECT_ID=
SUPABASE_DB_PASSWORD=
```

- `.env.test` (gitignored, generated): `supabase status -o env --override-name api.url=SUPABASE_URL --override-name auth.publishable_key=SUPABASE_PUBLISHABLE_KEY --override-name auth.secret_key=SUPABASE_SECRET_KEY --override-name auth.service_role_key=SUPABASE_SERVICE_ROLE_KEY --override-name db.url=SUPABASE_DB_URL > .env.test`. The `--override-name` keys are the `env:` tags in status.go (`api.url`, `db.url`, `auth.publishable_key`, `auth.secret_key`, `auth.anon_key`, `auth.service_role_key`, `auth.jwt_secret`, ...).
- `.gitignore`: add `.env`, `.env.*`, `!.env.example`, `supabase/.temp/`, `supabase/.branches/`, `supabase/.env`.
- Runtime adapter configuration (project URL, publishable key, team ref, anonymous-session refresh token) is per-user profile data, not repo `.env`: store under the plugin data dir (`CLAUDE_PLUGIN_DATA`) or OS keychain per the logical plan. The publishable key is "Safe to expose online: web page, mobile or desktop app, GitHub actions, CLIs, source code" (API keys doc); secret/service_role keys must never ship in the adapter or plugin.
- Never print keys in normal command output (logical plan requirement); `supabase status` output should only be redirected to gitignored files.

## 11. Proposed Makefile targets (fits the existing conventions: lowercase vars, per-target `.PHONY`, `##` doc comments)

```make
# ========== Variables (alphabetical) ==========
env_test        := .env.test
supabase        := npx supabase
supabase_exclude := studio,postgres-meta,imgproxy,storage-api,edge-runtime,mailpit,logflare,vector,supavisor
types_out       := src/adapters/supabase/database.types.ts

# ========== Setup ==========
setup: ## npm ci, .env scaffold, verify Docker + CLI
	npm ci --ignore-scripts
	@cp -n .env.example .env || true
	docker version >/dev/null
	$(supabase) --version

# ========== Supabase (local stack) ==========
supabase-start: ## Start the minimal local stack (db, auth, rest, realtime, kong) and apply migrations + seed
	$(supabase) start -x $(supabase_exclude)

supabase-stop: ## Stop the local stack, keep data
	$(supabase) stop

supabase-clean: ## Stop the local stack and delete its data
	$(supabase) stop --no-backup

supabase-status: ## Show URLs and keys of the running stack
	$(supabase) status

supabase-env: ## Write $(env_test) from the running stack (SUPABASE_URL, SUPABASE_PUBLISHABLE_KEY, ...)
	$(supabase) status -o env \
	  --override-name api.url=SUPABASE_URL \
	  --override-name auth.publishable_key=SUPABASE_PUBLISHABLE_KEY \
	  --override-name auth.secret_key=SUPABASE_SECRET_KEY \
	  --override-name auth.service_role_key=SUPABASE_SERVICE_ROLE_KEY \
	  --override-name db.url=SUPABASE_DB_URL > $(env_test)

supabase-reset: ## Recreate the local database from migrations + seed
	$(supabase) db reset

migration-new: ## Create supabase/migrations/<timestamp>_$(name).sql (usage: make migration-new name=add_messages)
	$(supabase) migration new $(name)

supabase-diff: ## Print schema drift between the local DB and migrations
	$(supabase) db diff --local -s public

supabase-types: ## Regenerate $(types_out) from the local database
	$(supabase) gen types --lang typescript --local -s public > $(types_out)

supabase-types-check: ## Fail if $(types_out) is stale (CI)
	$(MAKE) supabase-types && git diff --exit-code -- $(types_out)

supabase-test: ## Run pgTAP tests in supabase/tests against the local database
	$(supabase) test db

# ========== Supabase (hosted project) ==========
supabase-link: ## Link to a hosted project (usage: make supabase-link project=<ref>; needs SUPABASE_ACCESS_TOKEN)
	$(supabase) link --project-ref $(project)

supabase-push-dry: ## Show migrations that would be applied to the linked project
	$(supabase) db push --dry-run

supabase-push: ## Apply migrations to the linked project
	$(supabase) db push

supabase-config-push: ## Push config.toml settings (e.g. anonymous sign-ins) to the linked project
	$(supabase) config push

backend-install: supabase-link supabase-push supabase-config-push ## One-shot hosted backend setup for a team admin
	$(supabase) projects api-keys --project-ref $(project)

# ========== Build / Test / Lint ==========
test-unit: ## Unit tests (no Docker)
	npx vitest run --project unit

test-integration: ## Integration tests against the running local stack (reads $(env_test))
	npx vitest run --project integration

test: test-unit supabase-test test-integration ## All tests (requires `make supabase-start` first)
```

`clean` should also remove `$(env_test)`. The existing `commit` chain (`typecheck pull build test`) will then require a running local stack; if that is too heavy for every commit, keep `test` = `test-unit` and add `test-all` for the Docker-backed suites, and run `test-all` in CI.

## 12. Gotchas collected

- Docs drift: several docs pages lag the CLI (init creating `seed.sql`, `major_version` 15 vs template 17, `[analytics]` default, `setup-cli@v1` vs `@v3`, `status` docs listing only legacy key names). Trust the CLI's own docs/source cited above.
- The local publishable/secret keys are constants shared by every Supabase local project on every machine. They are harmless locally but must never be mistaken for real credentials, and tests must not embed them (use `status -o env`).
- Secret key vs admin API locally (issue #4524): keep `SERVICE_ROLE_KEY` in `.env.test` as a fallback for `auth.admin.*` fixtures.
- Anonymous sign-in rate limit is per IP per hour (30 hosted default, configurable). All local test clients share 127.0.0.1: raise `anonymous_users` in the local config or tests will start failing after 30 principals per hour. The hosted value must be sized for the team (each adapter profile signs in once and then refreshes).
- `auth.uid()` prefers `request.jwt.claim.sub` over the JSON claims; pgTAP tests must set both or use one helper consistently (section 6.2).
- `supabase test db` creates and then drops `pgtap` itself; do not add pgtap to migrations.
- `set local role` inside pgTAP works because the test connection is the `postgres` superuser-ish role; RPCs marked `security definer` are executed as their owner regardless of the simulated role, so test both the RPC path and direct table access.
- Realtime `postgres_changes` runs one RLS check per subscriber per change; for a messaging system use Broadcast-from-database with private topics plus cursor catch-up from the durable table, exactly as the logical plan requires.
- `pg_cron` schema is forced to `pg_catalog`; sub-minute schedules OK locally (PG 17); grants may need re-application; never depend on it for correctness.
- `supabase link` writes `supabase/.temp/`; `supabase init` only adds `supabase/.gitignore` when run inside a git repo. This working directory is not yet a git repo (verified-facts), so run `git init` before `supabase init`, or add the ignore entries manually.
- Docker 29.6.1 vs CLI: the CLI talks to the Docker Engine API; the runner images use 28.0.4. No incompatibility is documented, but it was not exercised today (uncertain).
- First `supabase start` pulls roughly a dozen images; budget several minutes and a few GB.

## 13. Open questions (to settle in the first spike)

1. Does `supabase status -o env` still emit the deprecated `ANON_KEY`/`SERVICE_ROLE_KEY`/`JWT_SECRET` lines in 2.116? (Source suggests yes; confirm by running.)
2. Is the local `sb_secret_...` key accepted by GoTrue admin endpoints now (issue #4524 was closed without fix)?
3. Does the local Realtime image expose `realtime.send`/`realtime.broadcast_changes` and enforce `realtime.messages` RLS for `private: true` channels out of the box (no hosted "Allow public access" toggle exists locally)?
4. Exact `auth.users` columns required for a minimal insert in pgTAP fixtures (`aud`, `role`, `instance_id`?).
5. Whether `enable_signup = true` is required for anonymous sign-ins to work (GoTrue semantics); if `false` breaks `signInAnonymously`, keep it `true` and rely on `auth.email/sms enable_signup = false`.
6. Provisioning status enum for polling after `projects create` (for the optional create path of the install script).

## 14. Sources

- https://supabase.com/docs/guides/local-development/cli/getting-started
- https://supabase.com/docs/guides/local-development/overview
- https://supabase.com/docs/guides/local-development/cli/config
- https://github.com/supabase/cli (README; `apps/cli-go/pkg/config/templates/config.toml`; `apps/cli/docs/supabase/*.md`; `apps/cli/src/legacy/commands/{init,test/db,test/new,gen/types,config/push,projects/api-keys}/SIDE_EFFECTS.md`; `apps/cli-go/internal/status/status.go`; `apps/cli-go/pkg/config/{auth.go,config.go}`; `packages/stack/src/{JwtGenerator.ts,ServiceCatalog.ts}`; `packages/config/src/realtime.ts`)
- https://registry.npmjs.org/supabase ; https://github.com/supabase/cli/releases ; https://formulae.brew.sh/api/formula/supabase.json
- https://supabase.com/docs/reference/cli/supabase-start ; -status ; -db-reset ; -migration-new ; -gen-types ; -test-db ; -link ; -db-push ; -projects-create ; -projects-api-keys ; -config-push
- https://supabase.com/docs/guides/local-development/seeding-your-database
- https://supabase.com/docs/guides/database/testing ; https://supabase.com/docs/guides/database/extensions/pgtap ; https://supabase.com/docs/guides/local-development/testing/overview ; https://supabase.com/docs/guides/local-development/testing/pgtap-extended ; https://github.com/usebasejump/supabase-test-helpers
- https://supabase.com/docs/guides/database/postgres/row-level-security
- https://github.com/supabase/auth (migrations `20211202183645_update_auth_uid.up.sql`, `20220531120530_add_auth_jwt_function.up.sql`)
- https://supabase.com/docs/guides/auth/auth-anonymous ; https://supabase.com/docs/reference/javascript/auth-signinanonymously ; https://supabase.com/docs/reference/javascript/initializing ; https://registry.npmjs.org/@supabase/supabase-js
- https://supabase.com/docs/guides/getting-started/api-keys ; https://supabase.com/docs/guides/getting-started/migrating-to-new-api-keys ; https://github.com/supabase/cli/issues/4524
- https://supabase.com/docs/guides/realtime/postgres-changes ; https://supabase.com/docs/guides/realtime/broadcast ; https://supabase.com/docs/guides/realtime/authorization ; https://github.com/supabase/realtime
- https://supabase.com/docs/guides/cron ; https://supabase.com/docs/guides/cron/quickstart ; https://github.com/supabase/supabase/blob/master/apps/docs/content/guides/cron/install.mdx ; https://github.com/supabase/cli/issues/1591 ; https://github.com/supabase/postgres (nix/packages/cli-config/postgresql.conf.template, docker/pgctld/postgresql.conf.tmpl, ansible/files/postgresql_config/supautils.conf.j2, ansible/files/postgresql_extension_custom_scripts/pg_cron/after-create.sql)
- https://www.postgresql.org/docs/current/pgcrypto.html ; https://www.postgresql.org/docs/current/functions-uuid.html
- https://supabase.com/docs/guides/deployment/managing-environments ; https://supabase.com/docs/guides/deployment/ci/testing ; https://github.com/supabase/setup-cli ; https://github.com/supabase/setup-cli/releases ; https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md ; https://github.com/supabase/cli/issues/2724 ; https://github.com/orgs/supabase/discussions/20081
- https://supabase.com/docs/reference/api/v1-create-a-project ; https://supabase.com/docs/reference/api/v1-get-project-api-keys ; https://supabase.com/docs/reference/api/v1-update-auth-service-config
- https://github.com/supabase/supabase/blob/master/apps/docs/content/guides/local-development/managing-config.mdx
