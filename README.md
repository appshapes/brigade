# Brigade

Team messaging between the Claude Code sessions of different people and machines. A session registers itself as an
addressable *session* in a *team*; another person's session sends it a text message; the message is stored durably by
a backend and injected into the recipient session's context without a human in the loop. Identity is a *principal*
(one person's credential on one backend) and an opaque *session id*; display names are unverified labels. Delivery
is at least once with an explicit acknowledgement point. Everything a model sees from another principal is
sanitised, and a message can never grant permission, approve a prompt or represent human consent.

Brigade ships as a Claude Code plugin. The plugin talks to its backend through an **adapter**: a separate executable
that speaks the Brigade Adapter Protocol (BAP/1) on argv, stdin and stdout. The bundled adapter targets Supabase;
any other backend is its own adapter, selected per profile.

**Status (2026-09-02).** Phase 1 is complete: the protocol is frozen, the shared Go library, the reference filesystem
adapter, the conformance suite, the plugin bootstrap and the CI scripts exist and are green. Phase 2 (the bundled
Supabase adapter) is in progress. Nothing runs inside a live Claude Code session yet; the plugin manifest and hooks
are Phase 3. The single source of truth for where the work stands is `.context/plans/brigade-execution-log.md`.

## For adapter contributors

If you are writing an adapter for another backend (a raw PostgreSQL or MySQL database, an object store, your own
service), everything you need is in this repository, in this order:

1. **`docs/adapter-authors.md`** — the authoring guide. Its "Start here" section carries `describe` and `session
   list` end to end; three independent implementers built both from that page alone and passed the suite's first
   four cases. Read it before the spec.
2. **`docs/protocol-v1.md`** — BAP/1, the normative protocol, **frozen**. Every MUST cites a conformance case.
   Section 4.8 lists what the protocol deliberately does not say — how you authenticate, store credentials,
   represent membership or transport events, the shape of your identifiers, your profile file — and that freedom is
   yours. Section 4.7 is the capabilities registry: omitting a capability is a first-class answer (a polling
   adapter omits `message.watch.push`; an adapter whose membership is managed elsewhere omits `team.join`).
3. **`internal/adapters/fs/`** — the reference adapter, a complete BAP/1 implementation over a directory on disk,
   insecure and test-only by design. Its README documents the store layout; it is the worked example the guide
   walks through and the dry run for an object-store adapter.
4. **`brigade-conformance`** — the arbiter. `make build`, then
   `bin/brigade-conformance --adapter /abs/path/to/your-adapter` (with `--shared-env <VAR>` if your backend is a
   directory every test principal must share, and `--setup <cmd>` if principals are provisioned out of band). 45
   cases, three principals in two teams, an environment built from scratch. An adapter is correct when the suite
   is green; the suite's own positive control is four deliberately broken builds of the reference adapter, each of
   which must fail exactly its own cases.

Facts that changed since the early brief some of you read: the ten open questions about section 4 are decided
(the spec's Appendix C names each decision and the sentence that honours it); the lease range is each adapter's own,
advertised in `describe`; the suite reads no poll interval, so a polling adapter gets the same 5 s delivery deadline
as a push one; a profile carries its default adapter and a session may override it (plan decision D36, landing with
the Phase 3 plugin); and the wording questions that a second implementation may surface are collected as BAP/1.x
items in the execution log, where answers change a minor revision, never the frozen major.

What we still ask of you: read the frozen core — `describe`, `session *`, `message *` and the semantics of 4.5 —
against your backend and tell us whether there is anything you cannot express without GoTrue, PostgREST and
Realtime. Name section numbers. A real gap is a BAP/1.x question; a change an existing conforming adapter would
fail is a new major version.

Practicalities: `master` only, merges only, never rebase; commit messages `15: <Imperative summary>`; the
acceptance gate is `make typecheck lint build test vuln deps-check schema-check tidy-check`; Go 1.27.0 is pinned in
`go.mod` with no `toolchain` line; `docs/allowed-deps.txt` binds only the shipped `brigade` binary, not your adapter.
To run the suite against the bundled adapter and a local Supabase stack (`make supabase-start supabase-env`, then
`make test-integration`), pass the backend as `--env BRIGADE_SUPABASE_URL=… --env BRIGADE_SUPABASE_PUBLISHABLE_KEY=…`
— `team create`/`team join` honour that pair only when the profile names no backend, never overriding a configured
profile — and the suite provisions its own teams, so no `--setup` hook is needed.
Plans live in `.context/plans/`, ephemeral scratch in `.ignored/` (gitignored).

## Layout

| Path | What |
| --- | --- |
| `docs/protocol-v1.md`, `docs/protocol-v1.schema.json` | the frozen protocol and its advisory JSON Schema |
| `docs/adapter-authors.md` | how to write and prove an adapter |
| `internal/protocol`, `internal/adapterkit` | the wire types with `Validate()`, and the shared adapter plumbing |
| `internal/adapters/fs`, `cmd/brigade-adapter-fs` | the reference adapter (dev and test only) |
| `internal/conformance`, `cmd/brigade-conformance` | the conformance suite (dev and test only) |
| `cmd/brigade` | the one shipped binary: the plugin harness and the bundled Supabase adapter |
| `plugin/` | what the Claude Code plugin ships: the sh bootstrap and the release pins |
| `supabase/` | the Supabase backend: migrations, pgTAP tests, local stack config |
| `scripts/ci/` | the CI checks (plugin tree, secrets, release pins) |
| `.context/plans/` | the implementation plan and the execution log |
