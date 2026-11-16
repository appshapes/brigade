# 0012. Show the board in a browser from a page on the member's own machine

- **Status:** Accepted
- **Date:** 2026-11-16
- **Supersedes:** none

## Context

A member wants to see the Brigade Board in a browser, with filters. The page shows every list and every work
item's whole title, assignee and tags (work item 344; on 2026-11-15 it was divided into 353 and the work items
after it). The board is a folder of change files that file sync keeps on each member's machine
(`internal/harness/workitem`). It never reaches the backend, also for a team on a Brigade server
(`.context/plans/brigade-server.md`, "Folder sync and the board: unchanged").

A page on the server needs three things that do not exist: the board on the server, a new endpoint in the frozen
contract (`docs/backend-rest-v1.md`), and a browser login. On 2026-11-15 Frank first chose one team URL on the
server, and the owner agreed to an ADR for it. Later that day Frank changed his decision. The first page is on the
member's own machine, with no login. The team page on the server comes later (work item 345).

## Decision

1. `brigade work-item page` writes the board as one HTML file and prints its `file:` URL. It reads the board
   folder on this machine and nothing else. It writes nothing to the board and sends nothing to the backend.
2. The file is the whole page: its style, its script and the board's data are inside it. Its
   Content-Security-Policy allows only that style and that script, by their hashes. So the page loads nothing
   and needs no network. The board's words travel only as escaped JSON, and the script puts each one in the page
   as text.
3. The page is read-only. The filters (assignee, tag, list, the hidden lists, the closed work items) run in the
   browser and stay in the page's address. For a later change, the member runs the command again and reloads.
4. The file goes under `pages/` in Brigade's state directory, and only its owner can read it. `--out` names
   another file, but never one in the board folder.
5. The verb runs in a terminal too. There it finds the board from the checkout's `.brigade.json`, as the
   session-start hook does. Every other verb of `brigade work-item` stays inside a session.

## Alternatives considered

- **One team page on the Brigade server, behind a login.** Every member sees the same page at one URL. It lost
  for now on cost. The board must reach the server first, the frozen contract changes, and the server needs a
  login. A team on Supabase has no server at all. It stays open as work item 345.
- **A server on 127.0.0.1 that serves the page and updates it.** It gives live updates. It lost on what it
  adds: a process to start and stop, a port, and a defence against requests from other pages for any action. A
  member who runs the command again gets the same data.
- **A page that loads its script or its data from a file beside it.** Browsers refuse many reads from a `file:`
  page. Also, a member can separate two files by mistake.

## Evidence

- The investigation of work item 344, on the comments of work items 344 and 345 (2026-11-15). The server has no
  browser page and no login, and the owner's roadmap session knew of no plan for one.
- The proof of work item 353 (row P105-1 of the execution log). The page came from the team's board of
  2026-11-16 (83 open and 38 closed work items) and ran in Chromium. Each filter gave the count that the data
  gives. The page's policy refused a `fetch`. A title that tries to close the script element showed as text and
  ran nothing.

## Consequences

- A member sees the board without the backend, a login or a network, and a terminal can make the page.
- The page is a snapshot. Its footer gives the time of the newest change in the machine's copy.
- The usage of each agent and the budget of each work item (work items 346 to 351) can go on the same page, from
  data on the member's machine.
- The page holds the whole board, with the comments. A member who gives the file to someone gives them the
  board.
- Revisit this when work item 345 starts. One URL for the team needs the server, its contract and a login.

## References

Work items 344, 345 and 353; `internal/harness/commands/workitem_page.go`;
`docs/team-member/use/work-items.md`, "See the board in a browser".
