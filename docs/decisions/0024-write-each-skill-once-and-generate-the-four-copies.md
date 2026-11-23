# 0024. Write each skill once and generate the four copies

- **Status:** Accepted
- **Date:** 2026-11-23
- **Supersedes:** none

## Context

The fifteen skills were kept as four copies edited by hand, one for each harness's package. With the invocation
form, the tool's word and the frontmatter set aside, the copies kept about 95% of each other's words, and in a
month 33 commits changed 200 skill files. Drift shipped: a sentence garbled by search and replace stood in Muse
Code's `team-messaging` for five weeks; Muse Code's `setup` denied and used the `!` prefix; six updates of
`team-messaging` reached three packages and not Grok Build's (work item 393, the repository review of
2026-11-21). The only pins were a few command strings in one test and checks that a file exists.

Four copies have to ship, because each harness loads its own package.

## Decision

A skill is written once, in `skills/<name>.md` at the repository's root, and the four packages' copies are
generated from it and never edited.

- The source is the Claude Code package's text as a Go `text/template`, with a mark wherever a harness
  differs. The marks are a handful of functions: the invocation form, what a skill's arguments came after, a few
  words that differ, and a block that some harnesses alone have.
- What differs in every skill the same way is the table `Harnesses` in `internal/skillgen`: how a skill is
  invoked and which frontmatter keys the harness's loader takes.
- `make skills-generate` writes the copies. `make skills-check`, which CI's `fast` job runs and `make test`
  repeats, fails on a copy that is not what its source renders.
- A difference between two copies is one of three things, and is handled as that: a fact of the harness is a
  mark; the same thing in other words gets one wording, Claude Code's; two copies that disagree, one of them
  wrong, is a choice that is written down.

## Alternatives considered

- **Keep four copies and pin more of their words in tests.** Lost: a test names what somebody thought to pin, and
  the drift that shipped was in sentences nobody pinned.
- **One text and no generator**, each skill asking the binary what the harness is (`work-item-start` is close to
  this: the command's own answer carries the difference). Lost as the general answer: a skill's description is
  read before any command runs, and it is where the invocation form matters most. It stays the way to remove a
  block: a difference the binary can state needs no mark.
- **Generate at release.** Lost: Grok Build and Codex CLI read their package from the default branch, so the
  copies have to be right in every commit, and a reviewer has to see what a change does to each harness.
- **A template language of our own, or includes.** Lost: `text/template` is in the standard library, and a
  source stays one file that reads from top to bottom.
- **A source that is no harness's text**, neutral, with every harness's words as marks. Lost: the Claude Code
  copy is the one most read and most proven, and a source that renders to it byte for byte could be shown to
  change nothing there.

## Evidence

- The sources were written against the copies as they stood: rendered for Claude Code, each source gave the
  package's copy byte for byte, and tests held that until the switch. Of the sixty copies, 34 came out byte for
  byte, Claude Code's fifteen among them, and the other 26 differ in 86 lines, each one read: a rewrap, one
  wording where there were two, or one of the nine choices; `.context/plans/skills-one-source.md` lists both.
- A review of the sources before the switch found two places where "Claude Code's wording" would have changed
  what a copy told a model: Muse Code's copies name a skill in words where Claude Code's give a typed form, and
  that is the harness's fact, kept in a block.
- `internal/skillgen/skillgen_test.go`: the committed copies are current; a changed copy, a missing one and a
  skill a package carries with no source are each found, and a write puts the first two back; a mark that names
  no harness, or leaves one without words, is an error; the table's rows are the registry's harnesses.
- Not yet shown: each harness loading the generated skills in a live session. It waits on the owner's word.

## Consequences

- A change to a skill is one edit, and all four packages have it or the gate fails.
- A new harness is a row of the table and its words in the sources' marks; a `say` that has no words for it
  fails until it has, and so does a registry entry with no row.
- `setup` and `team-messaging` still carry many blocks, where the copies differed by more than a mark. Each
  block is a difference nobody has yet decided is a fact; narrowing one changes no copy.
- Every change of a source changes four packages at once, Codex CLI's among them, and Codex asks its members
  to review Brigade's hooks after any change of its package.
- A reader of a package's `SKILL.md` sees no mark and no notice that it is generated: a skill's text is read by
  a model, and a comment in it would be read too. The gate and the rule are what say it.
- Revisit when a harness can load a skill from outside its package, which would end the four copies.

## References

- Work item 393 on the Brigade Board; work item 383, which brought Grok Build's skills level by hand; work item
  382, the one check of the four packages.
- `internal/skillgen/skillgen.go`, `cmd/brigade-skills`, `skills/`.
- `docs/developer/change/skills.md`, how a skill is changed; `.context/plans/skills-one-source.md`, the plan
  and its choices.
