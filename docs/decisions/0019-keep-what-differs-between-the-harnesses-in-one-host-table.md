# 0019. Keep what differs between the harnesses in one host table

- **Status:** Accepted
- **Date:** 2026-11-22
- **Supersedes:** none

## Context

Brigade serves four harnesses, Claude Code, Muse Code, Codex CLI and Grok Build, from one binary. The four
packages are thin: a manifest, three or four hook commands that all run `bin/brigade hook <event>`, the skills
and the pins. Every difference between the harnesses is decided inside the binary.

The repository review of 2026-11-21 was asked whether harness-specific code lives only in each harness's own
package, and found that it does not (its section 1, work item 380). On 2026-11-22, before this decision, 125
lines of 25 Go files outside the tests named one of the four host constants, in the `config`, `hook`, `watch`,
`frame` and `commands` packages: `if f.host == config.HostGrok`. Each harness had cost more of the core than the
one before; adding Grok Build touched 33 files. One rule had been written five times under three conditions,
and that was a defect (work item 379, ADR 0014). Three work items wait that each add a harness or a fact about
one (347, 267, 249).

Most of those conditions ask for a value (which variable marks a session, which file holds the options) or for a
yes or a no (can this host be renamed from inside). Some ask for more: how a message reaches the model, what a
hook of this host reads and prints. Those are work item 381's.

## Decision

Keep one binary. What differs between the harnesses and is a value, or a yes or a no, is a column of one table,
in one file: `internal/harness/config/host.go`. A row is a host. The rest of the tree asks a `config.Host` for
the fact and does not name a host.

The columns are the host's name, the variable that marks its session, whether its session is found by its
native id, whether its directories are resolved from `HOME` alone, its options file, where its hook learns the
package's root, the wording a message is framed in, where it keeps its own history, and a yes or a no for: it
can be renamed from inside, it runs a Stop hook, it shows nothing a prompt hook prints, a running session stays
when its team moves, `whoami` names it, and three that only Grok Build says yes to today: another host's marker
beside its own is refused, inherited plugin options are no defaults, and its map must bind a receiver. Every yes
or no is worded so that no is what a host without a row gets.

`make lint` holds it: a forbidigo rule refuses the four names, `HostClaude`, `HostMuse`, `HostCodex` and
`HostGrok`, wherever one is written. The files it does not bind are listed by name beside it in `.golangci.yml`:
the table, the tests, the frame's own names for its wordings, and the sixteen files that still decide by host
because what they decide is more than a value, each under the reason it is there. A file that is not on the
list fails the lint when it writes one of the four names.

The rule sees a name and not a string. A host's value spelled as a string, `"grok"`, passes it, and two files
that are not on the list hold one today: `sessionmap/grok.go`, which sits below `config` and cannot ask it, for
Grok Build's receiver; and `watch/reopen.go`, the harness a watcher registers a Claude Code session under. Both
are delivery's, and work item 381's.

No behaviour changes. The tests of the four hosts pass as they were written.

## Alternatives considered

- **A package of code per harness**, each implementing one interface, which is what the question asked about.
  Lost for the facts of this decision: an interface with four implementations of `OptionsFile() string` is a
  table written four times. For delivery and the hook's dialect it is the right question, and work item 381 asks
  it there.
- **The table in a package of its own**, `internal/harness/host`, with `config` asking it. Lost: the tests name
  `config.HostClaude` and its three siblings in 25 files, and they were to pass unchanged; the type and its
  names stay where they are, and the table is a file beside them.
- **A struct of fields, exported, for the tree to read.** Lost: a caller would read `row.Rename` of a host with no
  row and get a zero it did not ask about. A method answers for every `Host`, and says in its comment what no
  means.
- **The frame's wording in the table as text.** Lost: the frame's tests pin its pieces by name and its goldens
  byte for byte. The table says which wording a host reads; the text stays the frame's.
- **A test that walks the tree for the names**, in place of the lint rule. Lost: `make lint` already carries the
  rules of this kind, with their exceptions by path and their reasons beside them, and a developer meets it
  before the tests.
- **No list of exceptions: move everything now.** Lost: delivery, a session's facts from the host's own
  history, and the hook's dialect each need a design, not a column, and moving them without one would change
  what a host does.

## Evidence

- On 2026-11-22, before: 125 lines in 25 non-test Go files named a host constant. After: 74 lines in 17, each
  one of the files the rule lists, and none in `config` outside the table.
- `go test ./internal/harness/... ./internal/app/ ./cmd/brigade/` passes with no test file changed.
- `host_test.go` reads the table through what the tree asks of it, and holds the plugin root, the history root,
  the three columns only `config` asks, the order of `HostOf`, and that a host with no row gets no. The receiver
  column is held by the tests of Grok Build's receiver.
- The rule, drilled: `f.host != config.HostClaude` put back into `hook/rename.go`, a file that is not listed,
  fails `make lint` with the rule's message.

## Consequences

- A new harness adds a row, its wording in the frame, and whatever of delivery and the hook's dialect it needs
  in the listed files. A fact that comes to differ adds a column with a no for the others.
- The exceptions are a list a reviewer can read, and it can only shrink without a word: adding a file to it is
  a change to `.golangci.yml`, with its reason.
- Work item 381 takes the list's third part: delivery, local facts and the hook's dialect.
- Harder: a condition on a host can no longer be written where it is first needed. That is the point.
- The rule matches the four names at the end of an identifier, so a fifth host's constant must be added to its
  pattern; `host_test.go` and the table's comment say so. Nothing checks that it was.
- The rule does not see a string that spells a host's value. A reviewer does.
- Revisit when work item 381 has given delivery and the hook's dialect a place: the list's third part should
  then be empty.

## References

- Work item 380 on the Brigade Board; work item 381, which follows it; work item 379 and ADR 0014, the defect
  the scattered rule cost.
- `internal/harness/config/host.go` and `host_test.go`; `.golangci.yml`, the forbidigo rule and its exclusions.
- `docs/developer/change/harness-packages.md`, what a new harness adds.
