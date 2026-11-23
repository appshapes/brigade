# One source per skill (work item 393)

Started 2026-11-23 by the session `393-write-each-skill-once-and-generate`. The work item is the proposal; nothing
in it is ruled by the owner. This file says how it is built and, after each part, where the work stands.

## What is being built

The fifteen skills are four hand-edited copies each. They become one source each, `skills/<name>.md` at the
repository's root, and a generator that writes the four packages' copies from it. Four copies still ship, because
each harness loads its own; none is edited by hand any more, and a gate fails on a copy that is stale.

- **The source** is the Claude Code package's text with marks where a harness differs. It is a Go
  `text/template`.
- **The table** is `skillgen.Harnesses` (`internal/skillgen/skillgen.go`): for each harness, how a skill is
  invoked there and which frontmatter keys its loader takes.
- **A mark** is one of a few functions, and nothing else:

  | Mark | What it gives |
  | --- | --- |
  | `{{invoke "join"}}` | `/brigade:join` in Claude Code and Grok Build, `$brigade:join` in Codex CLI, `brigade:join` in Muse Code |
  | `{{argsOf "join"}}` | what a skill's arguments came after: "the command name, `/brigade:join`" in Claude Code, "the skill's name, `brigade:join`" in Muse Code, the invocation alone in the other two |
  | `{{argsOfCommand "join"}}` | the same where Claude Code's text names no invocation: "the command name" in Claude Code and Muse Code |
  | `{{say "claude" "…" "*" "…"}}` | a few words that differ by harness; every harness must have words |
  | `{{if in "muse" "codex"}}…{{end}}`, `{{if notin "claude"}}…{{end}}` | a block one or more harnesses alone have |

- **The frontmatter** is the source's, with the keys a harness does not take left out: Claude Code takes
  `user-invocable`, `argument-hint` and `allowed-tools`; Muse Code the first two; Codex CLI and Grok Build none.
- **The command** is `go run ./cmd/brigade-skills write | check`. `write --out <dir>` writes into another
  directory, to look at.

## The rule for a difference between two copies

Each difference a source meets is one of three, and is handled as that and no other way:

1. **A fact of the harness** (how a skill is invoked, the tool's word, where an option is set, where `brigade`
   is found, how a message arrives): a row of the table, a mark, or a block for that harness.
2. **The same thing said in two wordings** (the copies were substituted by hand, and drifted apart in phrasing):
   the source's wording, which is Claude Code's with the table's forms, is the one that stays. The meaning does
   not change.
3. **Two copies that disagree, one of them wrong**: chosen, and listed below under "Choices".

A change of what a skill tells a model is not this work item's.

## Parts, and where the work stands

1. **The generator, the table, the first sources, their tests.** No package file changes.
   Done in this part: `internal/skillgen`, `cmd/brigade-skills`, eight sources, the tests below.
2. **The other seven sources**: `email`, `slack`, `join`, `move`, `update`, `setup`, `team-messaging`. The last
   three carry the blocks one harness alone has (the receive path, the install and update commands).
3. **The switch**, in one commit: `brigade-skills write` rewrites the sixty copies, `make skills-generate` and
   `make skills-check` are added, the check joins `make test` and CI's `fast` job, `make muse-digest` rewrites the
   catalog, the rule files say how a skill is changed, and the interim tests give way to the check. This is the
   one change of the four packages: Codex CLI members review Brigade's hooks once more, and Grok Build and
   Codex CLI take the new text from the default branch at once.
4. **Each harness loads its skills once in a live session**: waits on the owner's word on live sessions.

### The sources so far

| Skill | Source | Its four copies |
| --- | --- | --- |
| `work-item`, `work-item-next`, `work-item-start`, `work-items` | written | reproduced word for word (`TestSourcesKeepTheWordsOfTheirCopies`) |
| `sessions`, `whoami` | written | the other three packages said "the `sessions` skill … with `--all` it includes" where Claude Code names the invocation each time; the source keeps Claude Code's wording (rule 2) |
| `subscribe`, `quiet` | written | where an option is set is marked for each harness (rule 1); "the `team-messaging` skill" in two packages is `brigade:team-messaging`, the skill's own name, as in the other two (rule 2) |
| `email`, `slack`, `join`, `move`, `update`, `setup`, `team-messaging` | not yet | |

Held by tests until the switch (`internal/skillgen/skillgen_test.go`): every source renders for every harness with
that harness's frontmatter keys and no other; rendered for Claude Code it is the package's copy byte for byte; and
the four skills of the first row keep the words of all four copies.

## Choices (rule 3)

- `sessions`, in the Muse Code, Codex CLI and Grok Build copies: a sentence begins "with `--all` it includes",
  lower case after a full stop, left by a substitution. Gone with the source's wording.
- `subscribe` and `quiet`, in Grok Build's copies: "the `label` option" and "the `team_inbound` option" named no
  options file where the other two packages' copies name theirs. The source names Grok Build's, as it has one.

To be added as the other seven are written: the sentence garbled by search and replace in Muse Code's
`team-messaging`, the two statements about the `!` prefix in Muse Code's `setup`, the unbalanced parenthesis in
Muse Code's `email` and `slack`, and Codex CLI's "never the binary by its full path" (the manager's comment on the
work item).

## What it will cost members, at the switch

Every package's skills change in one commit. Codex CLI asks its members to review Brigade's hooks again;
`marketplace.json` gets a new digest; Grok Build and Codex CLI read the new text from the default branch at once,
before a release. The changelog's opening lines say so when the switch lands.
