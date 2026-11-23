# A roadmap item on the Brigade Board (work item 404)

Started 2026-11-23 by the session `404-give-the-brigade-board-a-roadmap` (f605b), finished by the session of
the same name that followed it (d6a53). The design and the owner's six answers
(1a to 6a) are the comments of 2026-11-23 on the work item; they are the authority, and this file says how they
are built and, after each part, where the work stands. Nothing of it is pushed before the manager session says
the plugin release that follows 0.57.0 is out.

## What is ruled

1. A roadmap item is a work item of another kind: same numbers, change files and verbs.
2. A work item belongs to one roadmap item at most, by a field. Roadmap items do not nest. One spans the five
   repositories.
3. The order of the roadmap items is one change that names them all; the last such change is the order. A
   roadmap item it does not name goes to the end. The work items under one go by number.
4. One command, `brigade roadmap`: alone the listing; `add`; `put <work item> <roadmap item | none>`;
   `order <numbers…>`. Everything else about a roadmap item is `work-item show | edit | comment | move | close`.
   No sixteenth skill: `work-items` shows the listing, `work-item` documents the verbs. The board's page gets a
   roadmap view.
5. `work-item-next`: work under way first, then by the roadmap item's place, then by number; a work item under
   no roadmap item last. A project's own instruction still overrides.
6. Section 11 of `.context/plans/brigade-server.md` and the execution log's "Start here" each become a few
   lines that name the roadmap.

Left out, as the design says: nesting, more than one roadmap, dates, an order inside a roadmap item, "waits on"
as data, who may reorder, the digest and the gateways.

## The store (`internal/harness/workitem`)

- `Fields.Kind *string` (`"kind"`): `"roadmap"` or `""`. Any other value a reader drops. `Fields.Roadmap
  *string` (`"roadmap"`): the id of the roadmap item the work item is under, `""` for none; a value that is no
  id is dropped.
- `Item.Kind`, `Item.Roadmap` (the id). History words, fixed: "made a roadmap item", "put under a roadmap
  item", "taken off its roadmap item". The fold knows ids only, so the words name no number.
- A third directory, `roadmap/`, holds the order's changes: `Change.Order []string` (`"order"`), the roadmap
  items' ids in order, at most `MaxRoadmap`. Sequence and file name as every change. A reader from before it
  never reads the directory.
- `Board.Roadmap()`: the open roadmap items in order: those the last order names, in its order, then every
  other by number. An id in the order that is no open roadmap item is passed over. `Board.Under(id)`: the work
  items under a roadmap item, by number, closed ones included. `Board.Finished(it)`: closed, or in the list
  for finished work, which is the list with the key `done` or else the last list the board shows (the convention the
  skills already state).
- `Store.AddRoadmap`, `Store.SetOrder`; `put` is `Store.Change` with `Fields.Roadmap`.
- Refused by the commands, never by the reader: putting a roadmap item under one; putting under something that
  is no open roadmap item; ordering a number that is no open roadmap item, or one twice.

## The command (`internal/harness/commands/roadmap.go`, `internal/cli/command.go`)

- `brigade roadmap`: each roadmap item in order: number, title, its list, "n of m finished", then its open
  work items (number, list, assignee, title). A last group, "under no roadmap item", gives a count only.
  `--json`: the same as data, each roadmap item with `place`, and each work item row with its roadmap item's
  number, which is what `work-item-next` reads.
- `brigade roadmap add [--list <list>]`: title, a blank line and the description on stdin, as `work-item add`.
- `brigade roadmap put <work item> <roadmap item | none>`.
- `brigade roadmap order <number>…`: the whole order; a roadmap item left out follows, by number.
- `work-item list` leaves roadmap items out and says how many in one line; `work-item show` says "roadmap
  item" for one and lists what is under it, and for a work item under one prints `roadmap: <number> "<title>"`.
- In a session only, like every verb but `page`.

## The page (`workitem_page.go`)

A roadmap view beside the lists: the cards grouped by roadmap item, in order, each with its count.

## The skills (sources in `skills/`, then `make skills-generate`, `make muse-digest`)

- `work-items`: `roadmap` as one more word, which runs `brigade roadmap`. Its description is at 1,019 of 1,024
  characters: something goes for it.
- `work-item`: the three verbs, and that a roadmap item is otherwise a work item.
- `work-item-next`: reads `brigade roadmap --json`; rule 5 above.
- Claude Code's `allowed-tools` where a skill has them.

## Pages, decision, records

- `make docs-generate` (the command reference), the team member's page on the board, the team admin's if it
  speaks of the board's folder, `docs/agent/in-a-team/*`.
- ADR 0027 (the six choices and why), the changelog (with "Who must act": a member on an older plugin sees a
  roadmap item as one more work item), the execution log's row.
- Check `docs/security.md` section 19 against a third directory of changes; say so either way.

## Parts, and where the work stands

1. **The store**: fields, the order's change, the fold, the reads, the writer; tests. Built 2026-11-23, not
   committed: `internal/harness/workitem/{roadmap,roadmap_test}.go`, and `Fields`, `Item`, `Change.Order`,
   `Board.order` and `Store.SetOrder` in the package's other files.
2. **The command and the listings**; tests; the command table and `make docs-generate`. Built 2026-11-23, not
   committed: `internal/harness/commands/{roadmap,roadmap_test}.go`; in `workitem.go` the board's opening
   steps are `boardSession` and `readBoard`, shared by both commands, `list` leaves roadmap items out and
   counts them in its last line (`roadmap_items`), a row carries `roadmap`, and `show` prints what a roadmap
   item carries and what a work item is under (`under`, `carries`). As built: `brigade roadmap` takes no
   `show` word; `put` and `order` refuse a closed roadmap item; `order` writes the named ones first and then
   every other open one in the order it had.
3. **The page's roadmap view**; tests. Built 2026-11-23, not committed: a "Group by" choice (`#by=roadmap`),
   one column for each roadmap item in order with its count, a last one for the work items under none; a
   roadmap item is a card with a flag in the view by list and a heading in the other. The data carries
   `roadmap`, and each card `kind`, `roadmap` and `finished`. `TestWorkItemPageRoadmapView` runs the grouping
   under Node. Seen in a headless browser from a throwaway board (the rig below), both views, no console error.

   **The rig for the built binary**: `.ignored/381/live-pointed-setup.sh <dir>`, then `"sync": {"board": true}`
   added to `<dir>/proj/.brigade.json` with jq, then the observer's session-start hook once more (the last
   lines of the setup script); `<dir>/observer bin/brigade roadmap …` then works on a board of its own.
   Parts 1 to 3 were walked there on 2026-11-23: add, put, order, the listing, `--json`, three refusals, the
   order's change file under `roadmap/`.
4. **The skills** in the four packages, the pages, ADR 0027, the changelog, the log row. Built 2026-11-23, not
   committed, but for the last two: the sources `skills/{work-items,work-item,work-item-next}.md` and their
   twelve generated copies (`work-items`' description is at 1,021 of 1,024 characters after two trims),
   `marketplace.json`'s digest; `docs/team-member/use/work-items.md` ("Keep a roadmap", the page's view, the
   limits, three refusals), the TL;DR in `docs/team-member/use/README.md`, `docs/team-admin/configure/board.md`
   (the third directory), `docs/developer/reference/commands.md` (generated); ADR 0027 and the index.
   `docs/security.md` section 19: two sentences name the roadmap (what a forged change can do; the third
   directory no symlink is followed through). Old and new go in the report, for the owner.
   **Not written yet, on purpose**: the changelog's entry and its "Who must act" lines, and the execution log's
   row. The plugin release that follows 0.57.0 is being cut in the other lane and rewrites the head of
   `CHANGELOG.md`; both are written after it is out and pulled, under the new `## [Unreleased]`. The entry
   says: a roadmap on the board; a member on an older Brigade sees a roadmap item as one more work item
   (Team Member's line: update when the team starts a roadmap); Codex CLI members review Brigade's hooks once
   more, because the package changed.
5. **Proof.** On the built binary in a throwaway board: done 2026-11-23 (part 3's note). In a live Claude Code
   session on the checkout's plugin, headless, in the same board, done 2026-11-23, three sessions:
   `/brigade:work-items roadmap` ran `brigade roadmap` and printed it verbatim; `/brigade:work-item-next 3`
   read the lists, the listing, the roadmap and the roster and named 4, 5 and 2 in that order (the two under
   the first roadmap item, then the one under the second), with the one under none after them, each with its
   roadmap item and place; and "Put work item 3 on the roadmap under the sign-in goal, and make the sign-in
   goal first", in words, ran `brigade roadmap`, `roadmap put 3 6` and `roadmap order 6 7`. The rig's
   headless session needs `Skill(brigade:work-item)` among its allowed tools for a request in words.
   **Still to do**: Muse Code from the checkout before the push; Codex CLI and Grok Build read the package
   from the default branch, so theirs follow the push (the owner's standing word on live sessions, a comment
   on work item 249).
   **The review before the push** (2026-11-23, an agent review of the working tree) gave seven findings; six
   are fixed, each with a test: a carried board (`internal/boardcarry`) now carries `roadmap/` and compares the
   order; `brigade roadmap` names the closed roadmap items in its last line and `list --closed` says where they
   are named; the count of roadmap items in a listing goes by the listing's filters; `add` refuses at 500 open
   roadmap items and `order` refuses with the same words; the count under no roadmap item is of the shown
   lists, the hidden lists' beside it; the page shows a closed roadmap item as a group with the closed work
   items. The seventh, no changelog entry and no log row, is part 4's deliberate wait for the release.

   **Parked 2026-11-23** in the stash `404: parked for 425` (tracked and untracked files alike), at the
   manager session's word: work item 425 comes first and changes the same three skill sources. To go on:
   `git stash list`, pop that stash by its message, resolve the three sources by keeping both changes, then
   `make skills-generate` and `make muse-digest` (never merge a generated copy or the digest by hand), and
   run the gates again.

   **Brought back 2026-11-23** by the session `404-give-the-brigade-board-a-roadmap` (d6a53), which took the
   work item over when 425 was in Done: the stash popped onto master with one conflict, the catalog's digest
   (master's taken, then regenerated); the three sources merged clean. The changelog's entry with its Team
   Member line and the execution log's row P106-55 are written. **A second review**, of the merged tree, gave
   seven findings: four fixed (the list for finished work is never a hidden one, with its test; the listing's
   hint named `--all`, which the command does not take; the page's summary counted the work items under none as
   under a roadmap item; a comment), two left and said in the row and in ADR 0027 (the order's cap of 2,000
   changes; a work item of a hidden list under a roadmap item is listed with its list), the seventh the row
   itself. The built binary was walked again on the merged tree, and **Muse Code 1.4.25** on a throwaway home
   with the checkout's package showed the roadmap and changed it, each asked in words. The owner approved the
   two sentences of `docs/security.md` as worded, and the first use "now, from the built binary", once the
   commit is on master with CI green (comments of 2026-11-23 on the work item).

6. **First use**, after the push, on the owner's word (it was first planned for after the release): the review's work items grouped under roadmap items
   on the team's board, then choice 6's two texts. Commands and words, no code.
