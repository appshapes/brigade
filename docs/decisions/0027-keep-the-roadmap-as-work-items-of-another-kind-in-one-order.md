# 0027. Keep the roadmap as work items of another kind, in one order

- **Status:** Accepted
- **Date:** 2026-11-23
- **Supersedes:** none

## Context

The Brigade Board held work items in lists and nothing larger. What stood in for a roadmap was a list of more
than 80 open work items of five repositories in no order, a plan's section of session hand-offs, and an
execution log whose "Start here" was months old (the repository review of 2026-11-21, section 6). The owner
asked for "one more Brigade Board feature: a roadmap item" (work item 404, 2026-11-22), and for a design first,
with each choice brought to him.

The board's store sets the terms (`internal/harness/workitem`). It is a folder of changes that file sync shares
whole between machines: each change is one small file, written once; a work item is the fold of its changes,
the last one that sets a field winning; a reader ignores a member of a change it does not know and never
rewrites a change; and the order of the board's lists is one change that names them all, the last such change
being the order. No server hands out numbers or decides between two writers.

## Decision

The owner chose, on 2026-11-23, the option the design recommended in each of six questions.

1. **A roadmap item is a work item of another kind.** It has a number of the board's one sequence, and its
   changes are a work item's: a title, a description, a list, comments, a history. One field, `kind`, says
   what it is. Its list says how far the goal is.
2. **A work item is under one roadmap item at most**, by a field, `roadmap`, that holds that item's id. Roadmap
   items do not nest. One roadmap item spans the team's repositories, as the board does.
3. **One change names the whole order.** A change under `roadmap/` in the board's folder names the roadmap
   items in order, and the last such change is the order. A roadmap item it does not name follows, by number.
   The work items under one roadmap item go by number.
4. **One command, `brigade roadmap`.** Alone it prints the roadmap; `add` adds a roadmap item, `put` puts a work
   item under one or under none, `order` names the order. Every other change of a roadmap item is
   `brigade work-item`'s. No skill is added: `work-items` shows the roadmap and `work-item` documents the
   verbs. The board's page has a view grouped by roadmap item.
5. **`work-item-next` puts work under way first, then the roadmap.** After its rules for work in progress, the
   candidates go by their roadmap item's place, then by number; a work item under no roadmap item comes last.
   A project's own instruction still overrides.
6. **The roadmap replaces the texts that stood in for one**: section 11 of the server's plan and the execution
   log's "Start here" each become a few lines that name it.

What the working session added to make these whole, each a routine call:

- **Finished** is closed, or in the board's list for finished work, which is the list keyed `done` or else the
  last list the board shows. It is the convention the skills already gave a session for where finished work goes.
- **A listing of work leaves roadmap items out** and counts them in its last line, so that a session reading
  the board for work does not take a goal for a task, and nothing is left out silently.
- **A closed roadmap item is off the roadmap**, and the work items under it read as under none until it is
  reopened. `put` and `order` refuse one.
- **The refusals are the commands', never the reader's.** A reader passes over an id in the order that is no
  open roadmap item, and reads a work item whose `roadmap` names no roadmap item as under none.

## Alternatives considered

Each lost in the owner's answers; the words are the design's.

- **A record of its own**, in its own folder with its own numbers. Clean in every listing and invisible to an
  older plugin; it costs a second store with its own fold, numbering and repair, and its own show, edit and
  comment.
- **A tag with a title and a place.** Nothing new for membership; but a tag is a bare word, a work item carries
  several, and renaming one means retagging every work item.
- **A work item under several roadmap items.** It then counts toward several goals and has several places in
  the order, so "7 of 12 finished" is no longer true of anything.
- **Each roadmap item carries its own place**, a value between its neighbours'. Two machines that move different
  items both keep their move; it costs the arithmetic and a re-spacing when values run out, for a roadmap of
  tens of items that one or two people reorder.
- **No stored order: by number.** Nothing to build, and no way to say that one goal comes before an older one.
- **Flags on `work-item` and no new command.** Less to name, harder to find.
- **A skill of its own in each package.** The clearest for a member; four packages, four package checks and
  four pages more.
- **The roadmap before work under way**, or **only what is on the roadmap is a candidate.** The first starts a
  new piece while another lies half done; the second never proposes work nobody placed.

## Evidence

- The design and the owner's six answers, comments of 2026-11-23 on work item 404, each question with its
  options as put to him.
- `internal/harness/workitem/roadmap_test.go`: a roadmap item takes every change of a work item; a work item is
  under one at most; what is no roadmap item holds nothing; the order is the last one named, and two machines
  that reorder at once agree on the later one, with a roadmap item added meanwhile after it; an order read from
  the folder is cleaned. `symlink_test.go`: the roadmap's directory is not read or written through a symlink.
- `internal/harness/commands/roadmap_test.go` and `workitem_page_test.go`: the command's words, its refusals,
  its JSON, the listing of work that leaves roadmap items out, and the page's grouping under Node.
- The built binary in a throwaway board, 2026-11-23: add, put, order, the listing, three refusals, the order's
  change file, and the page's two views in a headless browser.
- None measured: a roadmap of hundreds of roadmap items; two machines reordering over a slow sync.

## Consequences

- A member on a Brigade from before this sees each roadmap item as one more work item in its list, and their
  `work-item-next` may propose it as work. It does no harm and ends when they update. The owner was told this
  before he answered the first question.
- Two machines that reorder at the same moment keep the later order, and the other is in the folder and nowhere
  else: no command shows an earlier order.
- The order's changes keep to the cap of the lists' changes: 2,000 of them, after which `order` is refused.
  Nothing removes a change, so a roadmap can be reordered 2,000 times.
- A third directory of changes is a third place a teammate's machine can write anything. It is read as the
  other two are: a change's name, size and shape, no symlink followed, every id checked. A forged order can
  reorder the roadmap, as a forged change can already move any work item; `docs/security.md` section 19 says so.
- `work-item-next` is a skill, so its order is the model's reading of two listings, not code.
- Revisit when a team wants an order inside a roadmap item, a roadmap item under another, or dates: the design
  left each out.

## References

- Work item 404 on the Brigade Board; work item 353 (the board's page), work item 300 (`work-item-next`),
  work item 269 (a listing that leaves a list out and says so).
- `internal/harness/workitem/roadmap.go`, `internal/harness/commands/roadmap.go`,
  `internal/harness/commands/workitem_page.go`; the sources `skills/work-item.md`, `skills/work-items.md` and
  `skills/work-item-next.md`.
- `docs/team-member/use/work-items.md`, "Keep a roadmap"; `docs/security.md` section 19.
