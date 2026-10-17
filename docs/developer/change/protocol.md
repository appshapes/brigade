# Change the protocol

[docs/protocol-v1.md](../../protocol-v1.md) is BAP/1, the normative protocol between the harness and an adapter,
**frozen**. Every MUST cites a conformance case; section 4.7 is the capabilities registry (omitting a capability
is a first-class answer); section 4.8 lists what the protocol deliberately does not say; Appendix C names each
decision and the sentence that honours it.

- **A wire change** means changing `internal/protocol` (types and `Validate()`), regenerating
  `docs/protocol-v1.schema.json` with `make schema` (`make schema-check` fails when it is stale), the conformance
  suite under `internal/conformance`, and both adapters (`internal/adapters/fs`, `internal/adapters/supabase`), in
  one commit. `cmd/brigade-schema` writes the schema from the Go types and must stay byte-stable.
- **A wording question** a second implementation surfaces is a BAP/1.x item in the execution log: an answer
  changes a minor revision and never the frozen major.
- **A change an existing conforming adapter would fail** is a new major version.
- **Every string from another principal** is sanitised before a model sees it (`internal/protocol/sanitize.go`);
  a new display field goes through the same path and gets a cap.
- **The frame** (`internal/harness/frame`) is the model-facing text around a message; its bytes are pinned by
  golden files per host, and a change there is a change to what every measured number was measured against
  ([docs/security.md, section 3](../../security.md#3-a-message-is-untrusted-text)).
