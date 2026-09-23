# Working on Brigade

The developer's page: how a clone is set up, how the repository is laid out, what gates a change, how a release is
cut, and what we ask of an adapter author. Users and administrators want [`docs/setup.md`](setup.md) instead; the
front [`README.md`](../README.md) has the seven journeys in short form.

## If you want to

| If you want to | Go to |
| --- | --- |
| set up a fresh clone | [Setup](#setup) |
| see every target | `make help` |
| run the tests | `make test` |
| run the live Supabase suites | `make supabase-start supabase-env`, then `make test-all` |
| run the real-Syncthing test | `BRIGADE_TEST_SYNCTHING=1 go test -run TestRealSyncthingIntegration ./internal/syncadapters/syncthing` |
| run the two-session real-Syncthing smoke | `BRIGADE_TEST_SYNCTHING=1 go test -count=1 -timeout 15m -run TestTwoSessionsSyncAFolderThroughSyncthing ./internal/harness/e2e/ -v` |
| run the whole gate | [Gates](#gates) |
| format, fix lint | `make fmt`, `make lint-fix` |
| try the plugin against your build | `make plugin-dev`, `make plugin-dev-off` — [`plugin/README.md` › Status](../plugin/README.md#status) |
| add a migration | `make migration-new name=<add_x>` |
| regenerate the protocol schema | `make schema` |
| commit and push | `make push message="<card>: …"`, or `/commit <card>` |
| read or create a Trello card | [`docs/claude-code-usage.md` › Trello CLI](claude-code-usage.md#trello-cli) |
| hand work to the agentic loop | [`docs/claude-code-usage.md` › Daily Workflow](claude-code-usage.md#daily-workflow) |
| cut a release | [Releases](#releases) |
| write an adapter | [For adapter contributors](#for-adapter-contributors) |
| write a sync adapter | [`docs/sync-adapters.md`](sync-adapters.md) |
| see what a CI script does, run shellcheck as CI does | [`scripts/ci/README.md`](../scripts/ci/README.md) |

## Setup

Go 1.27.0, `make`, `git` and `curl`, then `make setup`: module downloads, the pinned golangci-lint and goreleaser
into `./bin`, shellcheck (Homebrew on macOS), `.env` from `.env.example`, `push.autoSetupRemote`. It stops if `gh`
is missing or logged out, or if Docker is not running.

| For | You also need |
| --- | --- |
| `make test`, the gate, `make push` | nothing more |
| the live Supabase suites, `make e2e` | Docker, Node (`npx` runs the pinned Supabase CLI), `jq` |
| `make plugin-dev`, `make harness-smoke`, `make proof` | a logged-in `claude`, `jq` |
| `make release`, `make release-dry-run`, `make checksums-check` | `gh`, logged in |
| `make plugin-check` | shellcheck; Docker for CI's 0.9.0 — [`scripts/ci/README.md`](../scripts/ci/README.md) |
| the real-Syncthing test | `syncthing` on `PATH` (`brew install syncthing`); measured with 2.1.5 |
| the Trello skills | [trello-cli](claude-code-usage.md#trello-cli) |

## Gates

The acceptance gate is `make typecheck lint build test vuln deps-check schema-check tidy-check`. `make test` is
Docker-free and stack-free; the suites that need the local Supabase stack are `make supabase-start supabase-env`
followed by `make test-all`. `make help` lists every target, and a trailing ` (CI)` marks the ones a workflow step
invokes — [`scripts/ci/README.md`](../scripts/ci/README.md) says what each CI script does and what calls it.

`make lint`'s forbidigo rule forbids `exec.Command` everywhere but the spawn helpers, `procutil`, `testutil` and
the conformance suite, plus one file: `internal/syncadapters/syncthing/instance.go`, which starts `syncthing serve`
detached in its own session to outlive the adapter process — the one long-running daemon Brigade starts, so it
cannot go through `adapterkit.Spawn`, which runs a child to completion. Its carve-out in `.golangci.yml` lifts only
the spawn half of the rule (and gosec's G204 for that file); the spawn is still an argument array with
`adapterkit.ChildEnv`, never a shell. The package's tests use a fake REST server and a fake `syncthing` script
launched as `/bin/sh <script>`; the two tests that run the real `syncthing` on `PATH` — the adapter's
`TestRealSyncthingIntegration` and the two-session smoke `TestTwoSessionsSyncAFolderThroughSyncthing` in
`internal/harness/e2e` (two personas, two instances, a file each way, a conflict, the stop with the last
session; a few minutes) — are skipped unless `BRIGADE_TEST_SYNCTHING=1`, so `make test` starts no daemon and
opens no port:

```sh
BRIGADE_TEST_SYNCTHING=1 go test -count=1 -timeout 15m -run TestTwoSessionsSyncAFolderThroughSyncthing ./internal/harness/e2e/ -v
```

Go 1.27.0 is pinned in `go.mod` with no `toolchain` line, and [`docs/allowed-deps.txt`](allowed-deps.txt) binds
only the shipped `brigade` binary, not your adapter. `master` only, merges only, never rebase; commit messages
`<card>: <Imperative summary>`, through `make push message="<card>: …"` — the number is the card on the AppShapes
Trello board the work belongs to, and [`docs/claude-code-usage.md`](claude-code-usage.md) sets up the Trello CLI
and lists the skills that read and create cards. Plans live in `.context/plans/`, ephemeral scratch in `.ignored/`
(gitignored).

To run the conformance suite against the bundled adapter and a local stack, pass the backend as
`--env BRIGADE_SUPABASE_URL=… --env BRIGADE_SUPABASE_PUBLISHABLE_KEY=…` — `team create`/`team join` honour that pair
only when the profile names no backend, never overriding a configured profile. The suite provisions its own teams,
so no `--setup` hook is needed.

## Releases

**The current release is the one `plugin/bin/VERSION` pins.** `make release version=<v> card=<n>` pins
`plugin/bin/VERSION` and the plugin manifest to that version, writes the sha256 of each published binary into
`plugin/bin/checksums.txt`, and pushes the matching tag; the release workflow builds the four binaries from that
tag and publishes them beside their `checksums.txt`, then the release-notes gate drafts, lints and publishes the
notes. A tree in which that command has not run carries the pre-release `0.0.0` and an empty checksums file, and
its plugin has nothing to download. There is no Homebrew tap and no Linux package, and the credential is a 0600
file rather than an operating-system keychain — [`docs/security.md`](security.md) says what that costs. The single
source of truth for where the work stands is `.context/plans/brigade-execution-log.md`.

## Layout

| Path | What |
| --- | --- |
| [`CHANGELOG.md`](../CHANGELOG.md) | what changed in each release |
| `docs/protocol-v1.md`, `docs/protocol-v1.schema.json` | the frozen protocol and its advisory JSON Schema |
| `docs/adapter-authors.md` | how to write and prove an adapter |
| [`docs/setup.md`](setup.md) | how to set up Brigade for a team |
| [`docs/security.md`](security.md) | what Brigade protects, what it does not, and what was measured |
| [`docs/sync.md`](sync.md), [`docs/sync-adapters.md`](sync-adapters.md) | file sync for a team, and the protocol a sync adapter speaks |
| [`docs/claude-code-usage.md`](claude-code-usage.md) | how the repository is worked on with Claude Code: agents, skills, the daily workflow, the Trello CLI |
| [`docs/experiments/`](experiments/README.md) | the dated experiment writeups: what was measured, and what each run does *not* prove |
| [`docs/research/`](research/README.md) | the research digests the plan was written from, and the security threat model that defines the test ids |
| `internal/protocol`, `internal/adapterkit` | the wire types with `Validate()`, and the shared adapter plumbing |
| `internal/adapters/fs`, `cmd/brigade-adapter-fs` | the reference adapter (dev and test only) |
| `internal/conformance`, `cmd/brigade-conformance` | the conformance suite (dev and test only) |
| `cmd/brigade` | the one shipped binary: the plugin harness, the bundled Supabase adapter and the bundled Syncthing sync adapter |
| `internal/harness/foldersync`, `internal/syncadapters/syncthing` | the harness side of the sync-adapter protocol, and the Syncthing adapter (`brigade sync-adapter syncthing`) |
| `plugin/` | what the Claude Code plugin ships: the manifest, the lifecycle hooks, the five skills, the sh bootstrap and the release pins |
| `supabase/` | the Supabase backend: migrations, pgTAP tests, local stack config |
| `scripts/` | the proof scripts behind `make e2e` and `make proof`, the headless smoke test and the release sequence |
| [`scripts/ci/`](../scripts/ci/README.md) | the CI checks (plugin tree, secrets, release pins, keep-alive), their fixtures and the Go drift tests that pin them |
| `.context/plans/` | the implementation plan (an index plus one file per section under implementation/), the execution log and its archive |
| [`.context/plans/brigade-proof-results.md`](../.context/plans/brigade-proof-results.md) | the Phase 4 exit record: the ten success criteria, each with a verdict and the run that proves it |

## Status

Phases 1 to 4 and 6 are complete: the protocol is frozen, and the shared library, the reference filesystem adapter,
the conformance suite, the bundled Supabase adapter, the plugin and its harness — the lifecycle hooks, the
session-bound commands and the detached watcher — all exist, are green and were driven end to end through real
Claude Code sessions with no person at a keyboard ([the proof results](../.context/plans/brigade-proof-results.md):
ten success criteria met, eight open findings, none blocking). Phase 5 delivered the rest: the backend is deployed on
a hosted project with the daily keep-alive and the conformance suite green against it, team administration and the
`hold` inbox ship, retention is verified live, the two-hour soak has run, the frame's instruction text ships as
levels, and the user documentation is written.

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
   directory every test principal must share, and `--setup <cmd>` if principals are provisioned out of band). 49
   cases, three principals in two teams, an environment built from scratch. An adapter is correct when the suite
   is green; the suite's own positive control is four deliberately broken builds of the reference adapter, each of
   which must fail exactly its own cases.

Facts that changed since the early brief some of you read: the ten open questions about section 4 are decided, the
lease range is each adapter's own and is advertised in `describe`, the suite reads no poll interval — so a polling
adapter gets the same 5 s delivery deadline as a push one — and the project's team file names the adapter while a
session option may override it. [`docs/protocol-v1.md`](protocol-v1.md) Appendix C names each decision and the
sentence that honours it, and the wording questions a second implementation may surface are collected as BAP/1.x
items in [the execution log](../.context/plans/brigade-execution-log.md), where an answer changes a minor revision
and never the frozen major.

What we still ask of you: read the frozen core — `describe`, `session *`, `message *` and the semantics of 4.5 —
against your backend and tell us, naming section numbers, whether there is anything you cannot express without
GoTrue, PostgREST and Realtime. A real gap is a BAP/1.x question; a change an existing conforming adapter would
fail is a new major version.

Brigade integrates with the harness, not with a model: a member is a Claude Code session, whatever model answers
in it. To use OpenAI models, keep Claude Code as the session and add OpenAI's official plugin for Claude Code,
[`openai/codex-plugin-cc`](https://github.com/openai/codex-plugin-cc), which lets the session delegate to Codex;
Brigade needs nothing for that. A Codex plugin of Brigade's own is not planned: Codex has no supported way to wake a
live conversation, and a member that cannot be woken is not a peer.
