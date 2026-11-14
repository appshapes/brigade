# Decisions

Brigade records a decision that a newcomer would ask "why" about as an ADR, an architecture decision record: one
file per decision, numbered, with the alternatives that lost and the evidence, and never rewritten once it is on
`master`. [ADR 0001](0001-record-decisions-as-adrs.md) is the decision to work this way and says why. The rules an
agent follows are in [documentation.md](../agent/repository/documentation.md) and
[conventions.md](../agent/repository/conventions.md); `make docs-check` holds them.

## The decisions

- [0001. Record decisions as ADRs in the repository](0001-record-decisions-as-adrs.md)
- [0002. Close a decision's ask from the asker's side, with the ruling as its outcome](0002-close-a-decisions-ask-from-the-askers-side.md)
- [0003. Follow a team that moved, on the old backend's record and a move ticket](0003-follow-a-team-that-moved-on-the-old-backends-record.md)
- [0004. Bound one principal on a server with the reference backend's numbers](0004-bound-one-principal-on-a-server-with-the-reference-backends-numbers.md)

## Record a decision

1. **While it waits on someone**, it is not a file. A decision that waits on the owner's own hand, on another
   session or on evidence not yet measured is a work item on the Brigade Board tagged `decision` beside `brigade`:
   the question, the options and the evidence in the sections of [the template](0000-template.md), and the ruling
   as a comment. Ask for the owner's ruling with
   `brigade decision --to <the owner's roster label> --card <work item>`, the question on stdin. The session that
   receives the answer records it with `brigade work-item comment <work item>`, the owner's words on stdin. The
   asking session, or another session of the member who asked, closes the ask with
   `brigade decision --close <the ask's id>`, the ruling as its outcome on stdin; a session of another member
   that receives the answer records it and tells the asker (ADR 0002).
2. **Once it is decided**, copy the template to `NNNN-short-title.md` with the next number, the title in the
   imperative, and write it in the same change as the code, the rule or the page it explains. Until it is on
   `master` it can still be revised, on a pull request's branch too. Its Status is
   `Accepted` (or `Rejected`, for a decision ruled against that a newcomer would still ask about); a file is never
   Proposed. Its References name the work item it was carried by or made in.
3. **List it** under "The decisions" above, by its number and title.
4. **Land it** with the change. The work item's landing comment names the ADR.

A decision a session makes on its own inside a work item needs no work item of its own: its ADR lands in that work
item's change.

## Change one that is on master

An ADR keeps its text. What can still change:

- **Its Status line**, when a later ADR supersedes it: `Superseded by NNNN`, or `Accepted; superseded in part by
  NNNN` when only part of it is replaced. A change of mind is always a new ADR.
- **A fact that was wrong when it was written** (a date, a count, a citation, who decided something): correct it
  in place and add one entry to the header, after the Supersedes line and any earlier entry:
  `- **Corrected:** YYYY-MM-DD: <what was wrong, and what the ADR says now>`, continued on lines indented two
  spaces when it is long. Earlier entries stay as they are.

The title, the other header lines and the Decision section never change, and an ADR is never removed or renamed.
Two things are not a change. A link is repointed when the page it names moves: a link's target is not part of an
ADR's text, its words are. And when two ADRs have landed under one number, one of them takes the next free number,
in its file name and its title line, with nothing else changed.

## What the gate holds

`make docs-check` (the `decisions` rule) fails when an ADR is not named `NNNN-short-title.md`, reuses a number,
does not start `# NNNN. `, has no valid Status or no `## Decision` section, names a superseding ADR that does not
exist, is not listed under "The decisions" above, or carries a Corrected entry that is misspelt or outside the
header. Where the tree is a git repository it also compares each ADR with its text on `master` as the tree has
it, and fails when a landed ADR has changed beyond what the section above allows, or is gone from the tree: in
the working tree, in a commit that is not on `master` yet, or in a pull request. Putting the landed text back
always passes. The gate does not look back at what `master` already did.
