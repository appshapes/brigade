# Change

## If you want to

| If you want to | Go to |
| --- | --- |
| change a wire shape of the adapter protocol | [Change the protocol](#change-the-protocol) |
| write an adapter for another backend | [Write an adapter](#write-an-adapter) |
| write a sync adapter or a mail connector | [Write a sync adapter or a mail connector](#write-a-sync-adapter-or-a-mail-connector) |
| add a migration, a function, or change the Supabase adapter | [Change the backend](#change-the-backend) |
| change the Brigade server, the backend with no Supabase project | [Change the server](#change-the-server) |
| add Brigade support for another harness | [Add a harness](#add-a-harness) |
| know where things are | [Know the layout](#know-the-layout) |

## Change the protocol

1. Protocol v1 is frozen. A wire change moves `internal/protocol` (types and `Validate`), `make schema`, the
   conformance suite and both adapters in one commit.
2. A wording question is a BAP/1.x item in the execution log; a change a conforming adapter would fail is a new major.

Details: [Change the protocol](protocol.md).

## Write an adapter

1. Read [docs/adapter-authors.md](../../adapter-authors.md), then the spec, then `internal/adapters/fs/`.
2. `make build`, then `bin/brigade-conformance --adapter /abs/path/to/your-adapter`; green is correct.

Details: [Write an adapter](adapters.md).

## Write a sync adapter or a mail connector

1. A sync adapter is `brigade-sync-<name>` on each member's PATH speaking five verbs ([docs/sync-adapters.md](../../sync-adapters.md)).
2. A mail connector is two Edge Function endpoints under one secret ([docs/mail-connectors.md](../../mail-connectors.md)).

Details: [Write a sync adapter or a mail connector](sync-and-mail.md).

## Change the backend

1. `make migration-new name=<add_x>`; a migration only appends RPC parameters with defaults.
2. `make supabase-start supabase-env && make test-all` before pushing; `make functions-check` for `supabase/functions/`.

Details: [Change the backend](backend.md).

## Change the server

1. `server/` is a Rust workspace of its own: no code shared with the Go side, outside the acceptance gate.
2. `make server-check server-test` before pushing; a new crate goes on `server/deny.toml` (`make server-deps-check`).
3. The oracle is the conformance suite through the `rest` adapter against a running server.

Details: [Change the server](server.md).

## Add a harness

1. A package directory with its manifest, wrappers, the fourteen skills and `bin/` copies of the pins.
2. An entry in `docs/harnesses.json`, six pages, a CI check, a release-prep step.

Details: [Add a harness](harness-packages.md).

## Know the layout

1. One shipped binary, `cmd/brigade`; the rest is dev-only.

Details: [Know the layout](layout.md).
