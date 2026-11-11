# E18: a team's board of work items, across two repositories and in live sessions (card 263)

**Date** 2026-11-11. **Drivers** the end-to-end test `TestABoardSpansTheTeamsRepositories`
(`internal/harness/e2e/board_test.go`, no model) and `scripts/experiments/E18-work-items.sh` (three real headless
sessions), run from this checkout on the dev build `0.49.0-dev` over `8a95471` plus the card's working tree, as
it was committed. **Machine** this one: macOS, Syncthing from Homebrew, Claude Code 2.1.322, the sessions' model
Opus 5.5. **Backend** the fs adapter in a throwaway root, both times: no hosted project and no gateway was
involved, and nothing was sent to anyone.

## What the card added

A team that sets `"board": true` in the `sync` member of its `.brigade.json` keeps its work in Brigade: work
items in the lists of one board (each list a state with a key that never changes and a name that can), read and
changed with `brigade work-item`, in a folder under each machine's state directory that file sync shares with every teammate's machine whatever repository their session is in
([Keep the team's work on the board](../team-member/use/work-items.md)).

## Part 1: two machines, two repositories, no model

`BRIGADE_TEST_SYNCTHING=1 go test ./internal/harness/e2e/ -run TestABoardSpansTheTeamsRepositories -count=1 -v`.
One team; alice with a session in the repository `web` and bob with a session in the repository `api`, each
with a config directory, a state directory and so a Syncthing instance of their own; both team files carry
`"sync": {"board": true}` and list no folder. The hooks run as real processes and the real detached watchers
drive the bundled sync adapter. It was run twice on the build as committed and passed both times, in 76 s and
213 s; the outcomes below give the two measurements.

| Step | Assertion | Outcome |
| --- | --- | --- |
| 1 | Both SessionStarts print ``Brigade: file sync on: the team's board (`brigade work-item list`) through syncthing.`` and freeze `board_folder`, a folder under `boards/` in each machine's own state directory, with no `sync_folders`; the two sessions' repositories are `web` and `api` | ok |
| 2 | `brigade sync status --json` on both shows one configured folder, the board: the id derived from the team alone, marked `team`, labelled `team board`, at that machine's path | ok |
| 2 | Both engines hold the folder and the two machines connect, though the sessions share no repository | ok; 15.8 s both times after the first SessionStart |
| 3 | alice runs `brigade work-item add --mine` in `web`: work item 1, in the list `todo` (`Todo`), created by and assigned to her label. It is on bob's board in `api` | ok; 10.9 s and 65.1 s after the add |
| 3 | bob runs `move 1 Doing`, `comment 1` and `rename-list doing 'Under way'`; alice's `show 1` has the work item in the list `doing`, now called `Under way`, with the comment and bob's session name, and its history reads `moved to Under way` | ok; 10.8 s and 71.3 s later |
| 4 | alice and bob each add a work item within five seconds, before either has the other's: both take number 2 | ok |
| 4 | Once the two have crossed, a `list` on either machine renumbers the one created later: both boards show 1, 2 and 3 with the same titles, and work item 3's history ends `renumbered from 2 to 3` on both | ok; 36.4 s and 59.4 s after the adds |
| 5 | Neither checkout holds anything but `.git` and `.brigade.json`; no file under the backend's root carries a work item's title; neither session's messaging token is anywhere | ok |

The waits are Syncthing's, and they come in two sizes. Its filesystem watcher gathers changes for 10 s before
it scans, which is the ten seconds; and it scans every folder once a minute whatever the watcher saw, which is
the minute. Across these two runs and six earlier ones on builds of this card, what crossed in about ten
seconds was most often a new change in a directory the other side already had, and what waited for the minute
was most often a new directory: a new work item, or the board's first change of its lists. Why the watcher is
late for a new directory was not looked into; the minute bounds it.

## Part 2: the three skills in live sessions

`scripts/experiments/E18-work-items.sh`. A throwaway project whose team file turns the board on, the plugin of
this checkout (`--plugin-dir`, the installed Brigade plugin switched off), one `claude -p` session a leg in the
default permission mode, each given one slash command. An observer, a second session of the same member driven
by the real hooks with a sleeper as `CLAUDE_PID`, read the board with `brigade work-item … --json` after each leg.
Twenty assertions, twenty ok.

| Leg | Assertion | Outcome |
| --- | --- | --- |
| setup | The team file carries `"sync": {"board": true}`; the observer's map names a folder under `boards/` in the state directory; the board starts empty with the lists `Todo`, `Doing`, `Done` under the keys `todo`, `doing`, `done` | ok |
| A | `/brigade:work-item add Try the board from a live session`, with `Skill` the only tool allowed on the command line: the session's start line named the board, and it ran one command, `brigade work-item add` with the title on stdin in a quoted heredoc, under the skill's own `allowed-tools` | ok, 2 turns |
| A | The observer's board holds work item 1 with that title in `Todo`; the session's answer carries the command's line, `added: 1 "Try the board from a live session" in Todo` | ok |
| B | `/brigade:work-items`: the session ran exactly one command, `brigade work-item list` | ok, 2 turns |
| B | Its answer is the table as the command printed it: the header, the row, and `open by list: Todo (1), Doing (0), Done (0)` | ok |
| C | The observer adds work item 2, "Add a HELLO.md that says hello", with a description that says when it is done. `/brigade:work-item-start 2`: the session ran `show 2`, `assign 2 --mine`, `lists`, `move 2 doing`, a `brigade doing` line naming the work item, then `comment 2` and `move 2 done`, naming both lists by their keys | ok, 11 turns |
| C | The work was done: `HELLO.md` holds `hello` | ok |
| C | Work item 2 is in the list `done`, assigned to the member, open, with one comment that names its session; its history reads `created in Todo`, `assigned to …`, `moved to Doing`, `commented`, `moved to Done` | ok |
| C | The comment the session left: "Added HELLO.md at the top of the repository, containing the single word "hello". As the work item asks, it is not committed: it is an untracked file in the working tree on branch master. Checked by reading the file back, which shows "hello". Nothing is still open." No path on the machine | ok |
| all | No work item is in the backend's files; the board is six change files under `boards/` | ok |

The session did not close the work item: it said it would when the user says it is finished with, as the skill
asks.

**What the first run changed.** In the first run of leg C the session, looking for the board's lists before its
move, tried `brigade work-item list --help` and `brigade work-item list lists` before `brigade work-item lists`.
The skill named the command only in the `work-item` skill it points at; its second step now says
`brigade work-item lists` prints the board's lists. The run recorded above is the last of five, on the build
as committed: after that change, after the two agent reviews' fixes, and after the lists gained their keys and a
work item's labels became tags. The four before it were twenty of twenty as well.

## What this did not measure

- **Two physical machines, and the network between them.** Both parts ran on one machine; in part 1 each
  instance was given the other's loopback address, as the other sync tests do, because a Syncthing already
  running on the machine holds the local discovery port.
- **A hosted backend.** The board asks nothing of the backend but the roster's `sync_peer`, which the fs adapter
  and the Supabase adapter both carry (C-47).
- **The Muse Code, Codex CLI and Grok Build packages' skills in a live session.** The three skills are the same
  text worded for each harness; only the Claude Code package's ran.
- **A hostile work item.** Nothing here tested a work item written to steer the session that reads it; the
  injection test set has not been run against work items
  ([docs/security.md, section 19](../security.md#19-the-board-and-its-work-items)).
- **A long-lived board.** A handful of work items; nothing measured a board of thousands.
