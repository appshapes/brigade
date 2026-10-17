# Run the local stack

`make test` needs no stack. The live Supabase suites do: the adapter integration tests, `conformance(supabase)`,
the pgTAP tests and the Phase 4 proof.

```sh
make supabase-start supabase-env     # db, auth, rest, realtime, kong; migrations and seed applied; .env.test written
make test-all                        # test, test-db, advisor-lints, test-integration, e2e
make supabase-stop                   # keep the data
make supabase-clean                  # stop and delete the data
```

- `supabase-start` refuses when `supabase/migrations/` is empty (`migrations-check`); `supabase-reset` recreates the
  database from migrations and seed; `make migration-new name=<add_x>` creates a migration.
- `supabase-env` writes `.env.test` from the running stack, in both name sets; never commit it.
- The live tests are opt-in behind `BRIGADE_TEST_LIVE=1`, which only `make test-integration` sets.
- CI's `supabase` job runs the same recipes with the setup-cli binary (`supabase=supabase`), so the version is
  pinned in one place per context.
- To run the conformance suite against the bundled adapter and the local stack:
  `make conformance adapter=bin/brigade args="-- adapter supabase"` with `--env BRIGADE_SUPABASE_URL=… --env
  BRIGADE_SUPABASE_PUBLISHABLE_KEY=…`; `team create`/`team join` honour that pair only when the profile names no
  backend.
- `make supabase-config-push` is **dangerous** against a production project and refuses without `i_know=1`
  ([Deploy the backend](../../project-owner/install/deploy-the-backend.md) says why).

Run `make test-all` before pushing anything that touches `supabase/` or `internal/adapters/supabase`.
