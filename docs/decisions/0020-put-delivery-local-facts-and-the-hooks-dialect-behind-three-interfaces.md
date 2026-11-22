# 0020. Put delivery, local facts and the hook's dialect behind three interfaces

- **Status:** Accepted
- **Date:** 2026-11-22
- **Supersedes:** none

## Context

ADR 0019 put what differs between the harnesses and is a value, or a yes or a no, into one table, and had `make
lint` refuse a host's name outside it. Sixteen files stayed on the rule's list of exceptions, because what they
decide by host is more than a value (work item 381, the repository review's section 1):

- delivery, how a message reaches the model: Claude Code takes a post on its inbox socket, Muse Code a sink file
  that a monitor follows, Codex CLI a `codex queue` to the session's thread, Grok Build a private sink file read
  in pages. The watcher's injector chose among them by conditions in line, with Grok Build's check of its
  receiver and the repair of its sink beside the shared code;
- local facts, a session's name, model and context size from the harness's own history, read by one reader that
  asked its host at seven places;
- the hook's dialect, what a hook is handed and what it may print: Grok Build's hook speaks its own JSON, Codex
  CLI's is named on its argv and keyed by its thread, Muse Code's is capped in bytes, and Grok Build's session has
  a receiver the hook claims, checks and retires.

A fifth harness (work item 267) and the usage of each harness (work item 347) are mostly a new answer to these
three. The tests of the four hosts pin many of the functions by name and by form, and were to pass unchanged.

## Decision

Each of the three is an interface, with one implementation for each kind of answer, in a file of its own. The
host table says which kind a host has, in a column each, and the kinds are named for what they are and not for a
host:

| | the table's column | the interface | the implementations |
| --- | --- | --- | --- |
| local facts | `history`, a `config.HistoryKind` | `history` in `watch/hostfacts.go` | `datedStore` (`hostfacts_muse.go`), `rollouts` (`hostfacts_codex.go`), `summaries` (`grokname.go`) |
| delivery | `delivery`, a `config.DeliveryKind` | `delivery` in `watch/deliver.go` | `socket` (`deliver.go`), `followedSink` (`deliver_muse.go`), `queue` (`codexqueue.go`), `pagedSink` (`grok.go`) |
| the hook's dialect | `dialect`, a `config.DialectKind` | `dialect` in `hook/dialect.go` | `plugin` (`dialect.go`), `monitor` (`dialect_muse.go`), `thread` (`dialect_codex.go`), `nativeJSON` (`hook/grok.go`) |

The shared code asks the interface and names no host. The first kind of each is the plain form, which the
others start from and which a host the table does not hold is given.

Two things the hook asks are not behind its interface, and are the table's:

- whether a session binds a receiver (`config.Host.BindsReceiver`), which only Grok Build's does. The steps the
  hook takes with one, to claim it at a start, to hold a prompt and an end to it, to publish and to retire it,
  stay where they are in the start, prompt and end hooks, each behind that one question;
- the line a start prints after its first, which says how a message arrives and so goes by the delivery.

Outside the watcher, what was asked of delivery was a yes or a no, and asks the table for the kind: the hook's
spawn of the watcher, `brigade inbox follow`, `brigade inbox read` and the monitor lines of `brigade whoami`.

The lint rule's list of exceptions is the table, the tests and the frame's own names, and nothing else.

## Alternatives considered

- **A package a harness**, holding its three implementations. Lost: the three are asked by two packages, the
  watcher and the hook, that import each other one way only, and each implementation leans on the shared code
  of the package it is in (the bounded reader, the injector, the run). A file a harness in each package is the
  same thing without the import cycle.
- **Registered by the host's name**, the implementation saying which host it is. Lost: it puts the four names
  back into the watcher and the hook, which the lint rule is there to keep out. The table names a kind and the
  package registers under the kind.
- **Kinds named for the hosts.** Lost: a second set of host names. A kind says how the thing is done, so a
  harness that does it as another does shares the kind and adds nothing.
- **The receiver's steps as methods of the dialect.** Lost for now: there are about a dozen, each at a
  different place of the start, prompt and end hooks, and an interface that wide is the conditions under
  another name. With one host that has a receiver, one question says as much. A second such host is the time to
  give the steps a form.
- **Change the functions the tests call by host**, to be rid of the forms that survive for them. Lost: the
  tests were to pass unchanged, which is what shows that nothing a host does changed.

## Evidence

- `go test` of the harness, the app and the binary passes after each of the three parts with no existing test
  file changed. The new test files hold that every kind the table names has an implementation, that a host the
  table does not hold is given the plain one, and how a session is keyed.
- `make lint` on darwin, linux and windows with the sixteen files off the list: no issue. No file of
  `internal/harness/watch`, `internal/harness/hook` or `internal/harness/commands` writes one of the four names.
- The agent review of each part traced the replaced conditions against the code they replaced and found no
  change of behaviour.
- A message delivered in a live session of each harness: Claude Code's is the session that did the work. Grok
  Build's, Codex CLI's and Muse Code's wait on the owner's word and are recorded on the work item when they are
  run.

## Consequences

- A new harness adds a row to the table, its wording in the frame, and for each of the three either nothing,
  where it does the thing as another harness does, or a kind and one file.
- Each kind has one host today, so a kind and a host are one to one, and two functions that are asked by kind
  and no host say so and are tested: `HistoryKind.Root`, and the watcher's `--host`.
- The rule sees a name and not a string that spells a host's value. `sessionmap/grok.go`, which sits below the
  table, tells Grok Build's receiver by the string, and the watcher registers a Claude Code session under the
  harness's word in `watch/reopen.go`; a few calls in Grok Build's own files pass `"grok"` to the pointer store.
  None is in shared code that chooses by it.
- The functions that kept a form for their tests say so where they are: the watcher's environment with its two
  yeses, the retiring of the receiver with its own guard, and `findHostLog`, which is a helper of the tests now.
- Harder: an answer that differs by harness can no longer be written where it is first needed. It is a column,
  or a kind and a file.
- Revisit when a second harness binds a receiver, or when a kind comes to have two hosts.

## References

- Work item 381 on the Brigade Board; work item 380 and ADR 0019, the table and the rule this completes; work
  items 267, 347 and 249, which add to it.
- `internal/harness/config/host.go`, the three kinds and their columns; `internal/harness/watch/hostfacts.go`,
  `internal/harness/watch/deliver.go` and `internal/harness/hook/dialect.go`, the three interfaces.
- `.golangci.yml`, the rule and its list; `docs/developer/change/harness-packages.md`, what a new harness adds.
