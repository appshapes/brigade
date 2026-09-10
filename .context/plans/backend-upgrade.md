# Brigade backend upgrade: administrators apply migrations from the plugin alone

**Status.** Brief, v1, 2026-09-10 — awaiting Rjae's charter. Nothing in §5 is to be started until then.

## 1. The requirement

Rjae, 2026-09-10: releases must be schema-compatible in both directions, because we do **not** control which
adapter version and which schema version an installation pairs; and "we need some way for users of Brigade to
apply migrations after they receive the update." The first half shipped as P10-5 (`internal/adapters/supabase/
compat.go`: appended-parameters-only migrations, omit-when-nil, one PGRST202 fallback, a ten-minute legacy
marker, capabilities withheld meanwhile). This brief is the second half: today a migration is applied with
`make backend-install project=<ref>` from a checkout of this repository, which a team administrator who installed
the plugin from the marketplace does not have.

## 2. What was measured (2026-09-10, both hosted projects)

- The Management API's query endpoint runs SQL with a personal access token and returns rows as JSON:
  `POST https://api.supabase.com/v1/projects/<ref>/database/query` with `{"query": "…"}` and
  `Authorization: Bearer sbp_…` (token in a 0600 header file, never argv). Read-only selects against
  `wmgtaraqmoufmrnyojzf` answered in well under a second.
- The CLI's bookkeeping is `supabase_migrations.schema_migrations (version text, statements text[], name text)`,
  one row per applied file, `version` the 14-digit timestamp prefix, `name` the rest of the file name without
  `.sql`, `statements` the file split into statements (83 / 7 / 3 / 4 / 14 / 11 for the six files today).
  `db push --dry-run` and `migration list` compare **versions only** — that is how P11-2's `migration list`
  showed `local == remote` for every row.
- `db push --project-ref` does not use that endpoint: it mints a login role
  (`POST /v1/projects/<ref>/cli/login-role`) and connects to Postgres directly (P11-2, `docs/setup.md` §1). A
  Postgres driver is not in `docs/allowed-deps.txt`, and adding one is a release-reproducibility and supply-chain
  decision, not a convenience.

## 3. Design (proposed)

A maintenance verb on the bundled Supabase adapter, run by a human in a terminal, never by the harness:

```
brigade adapter supabase backend upgrade --project-ref <ref> --token-file <path> [--dry-run]
```

- **The migrations travel in the binary.** `//go:embed` of `internal/adapters/supabase/migrations/*.sql`, a copy of
  `supabase/migrations/` kept identical by a `make migrations-embed-check` gate (go:embed cannot reach outside
  the package and follows no symlink). The plugin tree does not change — no `plugin/` allowlist edit.
- **Pending = embedded versions not in `schema_migrations`.** One select through the query endpoint; the
  embedded set is the source of truth for what this adapter can talk to.
- **Apply in version order, each file as ONE query**, then insert the bookkeeping row with the file's
  statements split the way the CLI splits them — **or**, if the P0 below shows the CLI does not care, with the
  whole file as a single statement. Failure stops at the first failing file; nothing is recorded for it.
- **The token is a secret and is handled like the join secret**: `--token-file <path>` outside the repository (mode
  and owner never checked — owner ruling 4) or stdin; never argv, never a log, never a file Brigade writes. The
  verb's stdout is one JSON document (the 4.3 envelope shape) naming the versions found, applied and now present.
- `--dry-run` lists what would be applied and touches nothing.

## 4. Open measurements (P0, before any code)

1. Does the query endpoint run a whole migration file — multiple statements, `$$` bodies, `create function`,
   `drop function` — in one request, and within what time and size limits? Measure with a throwaway project or
   a harmless multi-statement query (`select 1; select 2;`) plus one `create function`/`drop function` pair in a
   scratch schema on `thinktech-brigade` — the owner's call, since it writes.
2. Does `supabase db push --dry-run` treat a version Brigade recorded as applied — with `statements` as one
   element, or must the split match the CLI's? (`migration list` is versions-only; `db push` is believed to be
   too.)
3. Does a project-scoped token suffice for the query endpoint, as it does for `db push` (P11-2)?
4. PostgREST's schema cache after a migration applied this way: does it reload on its own (the CLI's push does
   not send `notify pgrst`), or must the verb issue `notify pgrst, 'reload schema'` as its last statement?

## 5. Work breakdown (after the charter)

| Row | Scope | Tier |
| --- | --- | --- |
| P12-1 | The four P0 measurements, recorded in `docs/experiments/E7-backend-upgrade.md` | Opus |
| P12-2 | The verb: embedded migrations + the embed-check gate, the Management API client (query endpoint only), the pending computation, the applier, the bookkeeping row, `--dry-run`, the secret path, tests against a fake Management API | Fable |
| P12-3 | Docs: `docs/setup.md` §1 gains the plugin-only path; `CHANGELOG.md`; the adapter's README; `docs/security.md`'s token sentence | Opus |

## 6. Risks

- **A second secret in a human's hands.** The personal access token can do far more than run migrations; the verb
  must never persist it and the docs must say to use a project-scoped token.
- **Two writers of `schema_migrations`.** Brigade and the CLI both record rows; they must agree on `version`
  and `name` or `db push` will try to re-apply. P0 (2) decides how `statements` is filled.
- **The query endpoint is not the CLI's path.** Its limits are undocumented for this use; P0 (1) measures them
  before anything is built on it.
