# Add a harness

A harness is a program that runs a model's session and gives Brigade three things: a way to run a hook at session
start and at a prompt, an environment variable that marks a session, and a way to put text in front of the model
while it is idle. Claude Code has an inbox socket; Muse Code has a monitor tool that reads a file; Codex CLI has a
command that queues a turn into a session from outside (`codex queue`); a harness with none of these is a mailbox
until its next prompt (`poll_on_prompt`). A harness whose hooks carry no marker of their own (Codex, Grok Build)
names itself
on the hook's argv instead: `brigade hook <event> --host <name> --plugin-root <dir>`. The research question for a new harness is always
the third: can anything outside the session wake it? Measure it first, as [the Muse live test](../../experiments/muse-participation.md)
did, and record the answer under `docs/experiments/`.

## What a package is

A directory beside `plugin/`, `muse/brigade/`, `codex/brigade/` and `grok/brigade/` holding the harness's native
manifest, its hooks (one wrapper per hook that execs `bin/brigade hook <event>`, or hook commands that run it), the
fifteen skills worded for the harness, a `README.md`, and `bin/` copies of `plugin/bin`'s bootstrap, `VERSION` and
`checksums.txt`, which the release sequence keeps equal.

## The code

One binary serves every harness, and what differs between them is decided inside it. What is a value, or a yes or a
no, is a column of one table, and `make lint` refuses a host's name anywhere else
([ADR 0019](../../decisions/0019-keep-what-differs-between-the-harnesses-in-one-host-table.md)).

- **A row of the table**, `internal/harness/config/host.go`: the harness's name, the variable that marks a session
  of it, whether its session is found by its native id, its options file, where its hook learns the package's
  root, which wording a message is framed in, where it keeps its own history of a session, and a yes or a no for
  each of: it can be renamed from inside, it runs a Stop hook, it shows what a prompt hook prints, a running
  session follows its team's move, `whoami` names it. A fact that comes to differ for the new harness is a new
  column, with a no for the others. `host_test.go` reads the whole table.
- **Its wording**, `internal/harness/frame`: the frame variant, pinned by a golden file; one sentence may differ
  per host.
- **Its delivery**, how a message reaches its model while the session is idle: the table names the kind
  (`config.DeliveryKind`: a socket the host owns, a sink file a monitor follows, a command of the host's own that
  queues a turn, a private sink read in pages), and `internal/harness/watch`, the detached watcher
  (`brigade watch`), has one implementation a kind, in a file of its own (`deliver.go` for the socket and the
  interface, `deliver_muse.go`, `codexqueue.go`, `grok.go`). The hook's spawn of the watcher, the `inbox` commands
  and `whoami` ask the table for the kind and name no host. A harness that is reached as another is shares that
  kind; one that is reached its own way adds a kind and an implementation.
- **Its local facts**, a session's name, model and context size read from the harness's own history: the table
  names the kind of history (`config.HistoryKind`), and `internal/harness/watch` has one reader a kind, in a file
  of its own (`hostfacts_codex.go`, `hostfacts_muse.go`, `grokname.go`), behind the `history` interface of
  `hostfacts.go`. A harness that files its sessions as another does shares that kind and adds nothing; one that
  files them its own way adds a kind and a reader.
- **Its hook's dialect**, what its hook is handed and may print and how its session is keyed: the table names the
  kind (`config.DialectKind`: a plugin's hook, the same with a monitor and a cap in bytes, a hook named on its
  argv and keyed by a thread, the host's own JSON), and `internal/harness/hook` has one implementation a kind,
  in a file of its own (`dialect.go` for the plugin's and the interface, `dialect_muse.go`, `dialect_codex.go`,
  `grok.go`). A session that binds a receiver, as Grok Build's does, is asked of the table too
  (`config.Host.BindsReceiver`), and the line a start prints about how messages arrive goes by the delivery.
- **The by-host pointer**, `internal/harness/sessionmap`: from the native id to the by-pid map. It sits below
  `config` and names no host constant; where it must tell a host, it compares the map's `harness` string.

No file of the watcher, the hook or the commands names a host: the lint rule's list holds the table, the tests
and the frame's own names, and nothing else. A change that needs a host's name anywhere else fails `make lint`
with the rule's message: add the column, or, when it is more than a value, add a kind and its implementation
([ADR 0020](../../decisions/0020-put-delivery-local-facts-and-the-hooks-dialect-behind-three-interfaces.md)).

## The checks and the release

- A `scripts/ci/<harness>-plugin-check.sh` like `muse-plugin-check.sh` or `codex-plugin-check.sh` (allowlist,
  manifest, hooks, assets equal to `plugin/bin`'s), its Go test, a Makefile target and a step in `ci.yml`'s `fast`
  job.
- `scripts/release-prep.sh` bumps the manifest's version and copies the pins into the package's `bin/`;
  `checksums-check.sh` rule (a) and `release.yml`'s guard compare them.
- A harness that installs from a catalog carries it at the repository root (`.claude-plugin/marketplace.json`,
  `.agents/plugins/marketplace.json`, `marketplace.json` for Muse). When the harness pins a package digest, as Muse
  does, `internal/musedigest` and `cmd/brigade-muse-digest` are the model: the repository computes the digest the
  harness computes (tests pinned to digests the harness reported), `make release` rewrites it, the package check
  and `make test` verify it on every machine, and where the harness is installed a test has it install from the
  catalog.

## The documentation

An entry in `docs/harnesses.json` and six pages, which `make docs-check` requires:
[Add a harness to the documentation](../documentation/add-a-harness.md). The package's `README.md` links to the
member's install page; the agent page names every skill by path.
