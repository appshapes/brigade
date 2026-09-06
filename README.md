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

# Table of Contents

| You want to… | Go to |
|---|---|
| **Get started** | [What Brigade is](#brigade) · [Layout](#layout) · [Set up a team](docs/setup.md) · [Security](docs/security.md) |
| **Write an adapter** | [For adapter contributors](#for-adapter-contributors) · [`docs/adapter-authors.md`](docs/adapter-authors.md) · [`docs/protocol-v1.md`](docs/protocol-v1.md) |
| **Build, test, release** | [Gates](#gates) · [`scripts/ci/README.md`](scripts/ci/README.md) |
| **Run the plugin locally** | [`plugin/README.md`](plugin/README.md) |
| **See what is proven** | [`docs/experiments/`](docs/experiments/README.md) · [the proof results](.context/plans/brigade-proof-results.md) |
| **Find where the work stands** | [the execution log](.context/plans/brigade-execution-log.md) · [`CHANGELOG.md`](CHANGELOG.md) |
| **Read the research** | [`docs/research/`](docs/research/README.md) |

## [For adapter contributors](#table-of-contents)

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

Facts that changed since the early brief some of you read: the ten open questions about section 4 are decided, the
lease range is each adapter's own and is advertised in `describe`, the suite reads no poll interval — so a polling
adapter gets the same 5 s delivery deadline as a push one — and a profile carries its default adapter while a
session may override it. [`docs/protocol-v1.md`](docs/protocol-v1.md) Appendix C names each decision and the
sentence that honours it, and the wording questions a second implementation may surface are collected as BAP/1.x
items in [the execution log](.context/plans/brigade-execution-log.md), where an answer changes a minor revision and
never the frozen major.

What we still ask of you: read the frozen core — `describe`, `session *`, `message *` and the semantics of 4.5 —
against your backend and tell us, naming section numbers, whether there is anything you cannot express without
GoTrue, PostgREST and Realtime. A real gap is a BAP/1.x question; a change an existing conforming adapter would
fail is a new major version.

## [Gates](#table-of-contents)

The acceptance gate is `make typecheck lint build test vuln deps-check schema-check tidy-check`. `make test` is
Docker-free and stack-free; the suites that need the local Supabase stack are `make supabase-start supabase-env`
followed by `make test-all`. `make help` lists every target, and a trailing ` (CI)` marks the ones a workflow step
invokes — [`scripts/ci/README.md`](scripts/ci/README.md) says what each CI script does and what calls it.

Go 1.27.0 is pinned in `go.mod` with no `toolchain` line, and [`docs/allowed-deps.txt`](docs/allowed-deps.txt) binds
only the shipped `brigade` binary, not your adapter. `master` only, merges only, never rebase; commit messages
`15: <Imperative summary>`, through `make push message="15: …"`. Plans live in `.context/plans/`, ephemeral scratch
in `.ignored/` (gitignored).

To run the conformance suite against the bundled adapter and a local stack, pass the backend as
`--env BRIGADE_SUPABASE_URL=… --env BRIGADE_SUPABASE_PUBLISHABLE_KEY=…` — `team create`/`team join` honour that pair
only when the profile names no backend, never overriding a configured profile. The suite provisions its own teams,
so no `--setup` hook is needed.

## [Layout](#table-of-contents)

| Path | What |
| --- | --- |
| [`CHANGELOG.md`](CHANGELOG.md) | what changed in each release |
| `docs/protocol-v1.md`, `docs/protocol-v1.schema.json` | the frozen protocol and its advisory JSON Schema |
| `docs/adapter-authors.md` | how to write and prove an adapter |
| [`docs/setup.md`](docs/setup.md) | how to set up Brigade for a team |
| [`docs/security.md`](docs/security.md) | what Brigade protects, what it does not, and what was measured |
| [`docs/experiments/`](docs/experiments/README.md) | the dated experiment writeups (Phases 0, 3 and 4): what was measured, and what each run does *not* prove |
| [`docs/research/`](docs/research/README.md) | the research digests the plan was written from, and the security threat model that defines the test ids |
| `internal/protocol`, `internal/adapterkit` | the wire types with `Validate()`, and the shared adapter plumbing |
| `internal/adapters/fs`, `cmd/brigade-adapter-fs` | the reference adapter (dev and test only) |
| `internal/conformance`, `cmd/brigade-conformance` | the conformance suite (dev and test only) |
| `cmd/brigade` | the one shipped binary: the plugin harness and the bundled Supabase adapter |
| `plugin/` | what the Claude Code plugin ships: the manifest, the lifecycle hooks, the two skills, the sh bootstrap and the release pins |
| `supabase/` | the Supabase backend: migrations, pgTAP tests, local stack config |
| `scripts/` | the proof scripts behind `make e2e` and `make proof`, the headless smoke test and the release sequence |
| [`scripts/ci/`](scripts/ci/README.md) | the CI checks (plugin tree, secrets, release pins, keep-alive), their fixtures and the Go drift tests that pin them |
| `.context/plans/` | the implementation plan (an index plus one file per section under implementation/), the execution log and its archive |
| [`.context/plans/brigade-proof-results.md`](.context/plans/brigade-proof-results.md) | the Phase 4 exit record: the ten success criteria, each with a verdict and the test that discharges it |

## [Status](#table-of-contents)

Phases 1 to 4 and 6 are complete: the protocol is frozen, and the shared library, the reference filesystem adapter,
the conformance suite, the bundled Supabase adapter, the plugin and its harness — the lifecycle hooks, the
session-bound commands and the detached watcher — all exist, are green and were driven end to end through real
Claude Code sessions with no person at a keyboard ([the proof results](.context/plans/brigade-proof-results.md):
ten success criteria met, eight open findings, none blocking). Phase 5 is all but done: the backend is deployed on
a hosted project with the daily keep-alive and the conformance suite green against it, team administration and the
`hold` inbox ship, retention is verified live, the two-hour soak has run, and the user documentation is written.
Not done: the release itself — there is no tag yet (`plugin/bin/VERSION` reads
`0.0.0` and `plugin/bin/checksums.txt` is empty). The single source of truth for where the work stands is
`.context/plans/brigade-execution-log.md`.
