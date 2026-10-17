# Write a sync adapter or a mail connector

## A sync adapter

The bundled sync adapter is `brigade sync-adapter syncthing` (`internal/syncadapters/syncthing`); the harness side
is `internal/harness/foldersync`. Another engine is an executable `brigade-sync-<name>` on each member's PATH,
named by `"adapter": "<name>"` in the project's `sync` member. It speaks five verbs, one JSON document in and one
out: `describe`, `attach`, `apply`, `status`, `detach`. Folder ids are derived by the harness and handed to the
adapter. [docs/sync-adapters.md](../../sync-adapters.md) is the protocol, with the failure shapes and the id rule.
Sessions are only introduced to sessions that use the same adapter name.

## A mail connector

The mail gateway's core (`mail-in`, `mail-out`) knows no provider. A connector is two endpoints under one secret:
an inbound one that receives the provider's webhook, verifies it the provider's way and POSTs the mail to `mail-in`
in the contract's shape; and a send endpoint `mail-out` calls. Resend and Postmark ship as
`supabase/functions/mail-connector-resend` and `mail-connector-postmark`; a connector of the team's own is
installed with `provider=external`, and `make mail-connector-check send_url=<url> secret_file=<path>` checks its
send endpoint. [docs/mail-connectors.md](../../mail-connectors.md) is the contract, both directions, with how a
whole gateway of your own would run.
