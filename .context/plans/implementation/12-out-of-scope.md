# 12. Out of scope (from the logical plan's "do not freeze" list) and future adapters

Record.

## Corrections recorded in the execution log

None recorded.

<!-- verbatim from the one-file plan -->
## 12. Out of scope (from the logical plan's "do not freeze" list) and future adapters

Not designed, not stubbed, not reserved beyond a field name: task assignment; broadcast messaging (one recipient per message; `delivery_state` would become a receipts table when it arrives and the CLI surface would not change); attachments or file transfer; typing indicators; threads or rich conversations (`reply_to` and `hop_count` exist only for reply addressing and loop control); total message ordering (`seq` is a per-recipient hint); remote permission approval (a message can never approve anything); verified human identity (`human_label` stays unverified; an anonymous-to-permanent user upgrade is possible later without changing `principal_ref` [likely]); administrative roles beyond the creator-only rotate/revoke/transfer in Phase 5.

Also out of scope: end-to-end encryption against the project operator (needs its own RFC); Claude Code Channels (research preview with an allowlist; revisit if it leaves preview); plugin monitors as a delivery path (shipping both would double-inject without a shared pidfile); an MCP shim over the same CLI (possible later as a second model interface for hosts without a Bash tool; nothing in the CLI prevents it); Agent SDK hosts observing inbound messages from the stream; a Brigade-owned Claude Code launcher using host framing through `--input-format stream-json`; IP-based join limiting; native Windows (WSL 2 only, D33); hosted deployment, keychain storage, a Homebrew tap and marketplace publishing before Phase 5.

Future adapters the protocol is designed to admit without change, each proven by `brigade-conformance`:

- **Object store (S3, R2, GCS)**: one immutable object per message under `teams/<team>/inbox/<session>/` with conditional creation for idempotency, prefix listing for `receive` and a polling `watch` (omitting `message.watch.push`), an `acked/` marker object, lifecycle rules for retention, small lease objects for presence; IAM, notifications and lifecycle configuration stay outside the protocol. The fs adapter is its dry run.
- **A small self-hosted HTTP/WebSocket service**: the same command surface over a REST API with any bearer scheme.
- **Supabase with permanent accounts**: the same adapter with GoTrue's identity-linking endpoints when verified identity is wanted.
- **A supabase-js adapter**: the first alternative adapter for anyone who prefers the official client, bun-compiled to one file if it is ever bundled; it must pass the same suite and is not in scope (D35).

---

