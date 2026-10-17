# Change the backend

`supabase/` is the Supabase backend: migrations, pgTAP tests, Edge Functions, the local stack's `config.toml`
(which describes the **local** stack and is never pushed to a hosted project).

- **A migration**: `make migration-new name=<add_x>` creates `supabase/migrations/<timestamp>_<name>.sql`. A
  migration only ever *appends* RPC parameters with defaults, so plugin versions and migrations stay independent
  ([Know which migration adds what](../../project-owner/update/migrations.md)); the Supabase adapter's compat layer
  (`internal/adapters/supabase/compat.go`) drops a parameter the project refuses and retries without it. An
  applied migration is never edited. Add the pgTAP test under `supabase/tests`, the retention numbers to
  `describe` where they change, and the row to the owner's migration table.
- **An Edge Function** under `supabase/functions/`: Deno, with unit tests; `make functions-check deno=deno`
  format-checks, type-checks and tests them (CI runs it). The gateways' installers (`scripts/gateway-install.sh`,
  `scripts/slack-gateway-install.sh`) deploy them; a function change is "the gateway changed" in the changelog,
  which tells the owner to redeploy.
- **The Supabase adapter** (`internal/adapters/supabase`) is a child process of the harness, never imported by it
  (depguard). It links only the allowed modules. The live tests need the local stack:
  `make supabase-start supabase-env && make test-all` before pushing.
- **Security Advisor lints**: `make advisor-lints` mirrors them with the database container's `psql`; the schema's
  tables have RLS on, every grant is explicit, and `join_attempts` is server-only.
- **The hosted projects** are the owners' ([Project Owner](../../project-owner/README.md)); nothing here deploys to
  one. `make backend-install` is their command and `scripts/backend-settings.sh` applies the settings one field at
  a time.
