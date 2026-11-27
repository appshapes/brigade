# Add a harness

A harness is a program that runs a model's session and gives Brigade three things: a way to run a hook at session
start and at a prompt, an environment variable that marks a session, and a way to put text in front of the model
while it is idle. Claude Code has an inbox socket; Muse Code has a monitor tool that reads a file; Codex CLI has a
command that queues a turn into a session from outside (`codex queue`); Antigravity CLI has a hook before each
model call, which adds a message to the conversation but does not wake an idle session; a harness with none of
these is a mailbox until its next prompt (`poll_on_prompt`). A harness whose hooks carry no marker of their own
(Codex, Grok Build, Antigravity CLI) names itself
on the hook's argv instead: `brigade hook <event> --host <name> --plugin-root <dir>`. The research question for a new harness is always
the third: can anything outside the session wake it? Measure it first, as [the Muse live test](../../experiments/muse-participation.md)
did, and record the answer under `docs/experiments/`.

## What a package is

A directory beside `plugin/`, `muse/brigade/`, `codex/brigade/`, `grok/brigade/` and `antigravity/brigade/` holding the harness's native
manifest, its hooks (one wrapper per hook that execs `bin/brigade hook <event>`, or hook commands that run it), the
fifteen skills, which are generated from the sources under `skills/` once the harness has a row in the table
of `internal/skillgen` ([Change a skill](skills.md)), a `README.md`, and `bin/` copies of `plugin/bin`'s bootstrap, `VERSION` and
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
  queues a turn, a private sink read in pages, and `next-call`: the sink file a followed sink has, read by the
  host's own hook at the next model call), and `internal/harness/watch`, the detached watcher
  (`brigade watch`), has one implementation a kind, in a file of its own (`deliver.go` for the socket and the
  interface, `deliver_muse.go`, `codexqueue.go`, `grok.go`; `next-call` shares the followed sink's implementation
  in `deliver_muse.go`, and the hook reads the file through `internal/harness/inbound`'s `drain.go`). The hook's spawn of the watcher, the `inbox` commands
  and `whoami` ask the table for the kind and name no host. A harness that is reached as another is shares that
  kind; one that is reached its own way adds a kind and an implementation.
- **Its local facts**, a session's name, model and context size read from the harness's own history: the table
  names the kind of history (`config.HistoryKind`), and `internal/harness/watch` has one reader a kind, in a file
  of its own (`hostfacts_codex.go`, `hostfacts_muse.go`, `grokname.go`), behind the `history` interface of
  `hostfacts.go`. A harness that files its sessions as another does shares that kind and adds nothing; one that
  files them its own way adds a kind and a reader.
- **Its hook's dialect**, what its hook is handed and may print and how its session is keyed: the table names the
  kind (`config.DialectKind`: a plugin's hook, the same with a monitor and a cap in bytes, a hook named on its
  argv and keyed by a thread, the host's own JSON, and `inject-steps`: the host's own JSON, where what the hook says
  reaches the model as a step that the hook adds to the conversation), and `internal/harness/hook` has one implementation a kind,
  in a file of its own (`dialect.go` for the plugin's and the interface, `dialect_muse.go`, `dialect_codex.go`,
  `grok.go`, `dialect_antigravity.go`). A session that binds a receiver, as Grok Build's does, is asked of the table too
  (`config.Host.BindsReceiver`), and the line a start prints about how messages arrive goes by the delivery.
- **The by-host pointer**, `internal/harness/sessionmap`: from the native id to the by-pid map. It sits below
  `config` and names no host constant; where it must tell a host, it compares the map's `harness` string.

No file of the watcher, the hook or the commands names a host: the lint rule's list holds the table, the tests
and the frame's own names, and nothing else. A change that needs a host's name anywhere else fails `make lint`
with the rule's message: add the column, or, when it is more than a value, add a kind and its implementation
([ADR 0020](../../decisions/0020-put-delivery-local-facts-and-the-hooks-dialect-behind-three-interfaces.md)).

## The checks and the release

- Its rules in the one package check, `internal/packagecheck`: a file of its own beside `muse.go` and `codex.go`
  for what only this package has (its manifest's form, how its hooks are worded, its marketplace file), calling
  what the packages share (the allow-list, assets equal to `plugin/bin`'s, the bootstrap's mode in git, the
  version, the shells, no skill that takes the model's reach away). It is registered under the harness's id in
  `docs/harnesses.json`, with its cases in `scripts/ci`, a Makefile target (`go run ./cmd/brigade-packages check
  <id>`) and a step in `ci.yml`'s `fast` job.
- `scripts/release-prep.sh` bumps the manifest's version and copies the pins into the package's `bin/`;
  `checksums-check.sh` rule (a) and `release.yml`'s guard compare them.
- A harness that installs from a catalog carries it at the repository root (`.claude-plugin/marketplace.json`,
  `.agents/plugins/marketplace.json`, `.grok-plugin/marketplace.json`, `marketplace.json` for Muse). A harness
  that reads the catalog from the default branch and takes the package's files as they are there would give its
  members every push: Codex CLI's and Grok Build's catalogs therefore name the package at the commit of the last
  published release, which the release sequence's last step moves and the package checks hold (work item 424,
  [ADR 0030](../../decisions/0030-pin-the-codex-cli-and-grok-build-packages-to-the-last-release-in-their-marketplace-files.md)).
  Antigravity CLI has no catalog: a member installs the package folder at a release tag, so it needs no pin
  ([ADR 0039](../../decisions/0039-take-antigravity-cli-in-as-a-harness-that-gets-a-message-at-its-next-model-call.md)).
  A new harness of that kind is pinned the same way, in `internal/packagecheck/pin.go`. When the harness pins a package digest, as Muse
  does, `internal/musedigest` and `cmd/brigade-muse-digest` are the model: the repository computes the digest the
  harness computes (tests pinned to digests the harness reported), `make release` rewrites it, the package check
  and `make test` verify it on every machine, and where the harness is installed a test has it install from the
  catalog.

## Trying an unreleased package

The two pinned catalogs fetch the package from GitHub at the last release's commit, from a clone too. To run
what the working tree has:

- **Grok Build**: `grok plugin install ./grok/brigade --trust` installs the directory itself, with no
  marketplace. Remove it afterwards with `grok plugin uninstall brigade`.
- **Antigravity CLI**: `agy plugin install ./antigravity/brigade` installs the folder itself. Remove it afterwards
  with `agy plugin uninstall brigade`.
- **Codex CLI**: in a throwaway clone, put the catalog's entry back to
  `"source": {"source": "local", "path": "./codex/brigade"}`, then `codex plugin marketplace add <that clone>`
  and `codex plugin add brigade@brigade`. Never commit that form: `make codex-plugin-check` refuses it.

Do each under a throwaway `HOME`, so that the harness's own configuration keeps the released package.

## The documentation

An entry in `docs/harnesses.json` and six pages, which `make docs-check` requires:
[Add a harness to the documentation](../documentation/add-a-harness.md). The package's `README.md` links to the
member's install page; the agent page names every skill by path.
