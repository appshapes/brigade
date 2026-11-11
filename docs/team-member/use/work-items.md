# Keep the team's work on the Brigade Board

The **Brigade Board** is a team's board of work items. A team that turned it on
([Turn on the Brigade Board](../../team-admin/configure/board.md)) keeps its work in Brigade itself: **work items**, each with a number, in the board's **lists**, the way a kanban board keeps cards in
columns. The board is the team's, not a repository's: a session in any of the team's repositories reads and
changes the same one. The session-start line says so when the team has one:

```
Brigade: file sync on: 1 folder(s) and the team's Brigade Board of work items (`brigade work-item list`) through syncthing.
```

## Ask in words, or with a command

Your session runs `brigade work-item` when you ask it to: "show me the brigade board", "put that on the board",
"what is assigned to me", "move 41 to Doing". It knows the board by its name and as "the board". Three skills are the short forms; on Claude Code they are slash commands, and the other
harnesses name them their own way ([the skills, by harness](../../agent/in-a-team/README.md)).

| Command | What it does |
| --- | --- |
| `/brigade:work-items` | prints the board verbatim; `mine`, `nobody`, `closed`, `for <member>`, `tag <tag>` or a list narrows it, and `lists` prints the board's lists |
| `/brigade:work-item <number>` | shows one work item; `add <title>`, and `<number> move <list>`, `assign me`, `comment <text>`, `tag <tags>`, `edit`, `close`, `reopen`, add one or change it |
| `/brigade:work-item-start <number>` | has the session take the work item and work it through: it assigns it to you, moves it to the list for work in progress, does the work, comments what it did and moves it on |

## The commands

```bash
brigade work-item list                       # the open work items, by list
brigade work-item list --mine                # yours; --to <label> a teammate's; --nobody the unassigned
brigade work-item list --list Doing          # one list, by its name or its key; --tag bug one tag; --closed the closed ones
brigade work-item show 41                    # one work item: fields, description, comments, history
brigade work-item add --list Todo --mine --tag bug,web <<'EOF'
Fix the login redirect

Signing in from the invite link returns to the invite, not the dashboard.
Done when the dashboard opens after a sign-in from any link.
EOF
brigade work-item edit 41 <<'EOF'            # replaces the title and the description: send both
Fix the login redirect loop

Signing in from the invite link loops between the invite and the sign-in page.
EOF
brigade work-item move 41 Doing
brigade work-item assign 41 --mine           # or --to <label>, or --nobody
brigade work-item tag 41 bug web             # exactly these; with none, no tags
brigade work-item comment 41 <<'EOF'
Reproduced on staging with a fresh invite.
EOF
brigade work-item close 41                   # finished with: off the lists, still under list --closed
brigade work-item reopen 41
brigade work-item lists                      # the board's lists, in order: each one's key and its name
brigade work-item lists Backlog Doing Review Done   # names them anew: all of them, in order
brigade work-item rename-list doing 'In progress'   # another name for a list; its key and its work items stay
```

Every verb runs inside a session and takes `--json`. A title, a description and a comment travel on stdin; the
first line of an `add` or an `edit` is the title and the description follows a blank line. A title holds 200
characters, a description 8,000 and a comment 4,000. A text that carries a credential's shape is refused.

`brigade work-item list` prints one row per work item and one line that counts every list, so an empty list is
still seen:

```
LIST   NUMBER  TITLE                   ASSIGNED          TAGS
Todo   43      Add a board             -                 -
Doing  41      Fix the login redirect  rjae@example.com  bug, web
open by list: Todo (1), Doing (1), Done (0)
```

## What a work item is

- **A number.** The highest number on the board plus one, closed work items counted. A commit that belongs to a
  work item can carry its number as other trackers' cards do (`41: Fix the redirect`).
- **A title, a description, comments.** Free text, the description and the comments with their line breaks.
- **A list.** One of the board's: the state the work item is in ([The board's lists](#the-boards-lists)). A
  move to a list the board does not have is refused with the lists there are.
- **An assignee.** A member, by their label as the roster shows it (`brigade sessions`), or nobody. `--mine` is
  your own label.
- **Tags.** Up to twelve words of the team's own, made by using them: `bug`, `web`, a customer, a release.
- **Open or closed.** Closing takes a work item off the lists; nothing deletes one.
- **A history.** One line per change: who, from which session, when, and what changed.

## The board's lists

A list is a state a work item can be in, and the lists are the team's own. Each has two things:

- **A key**, set when the list is made, from the name it was given then (`In progress` gives `in-progress`), and
  never changed. It is what a work item carries.
- **A name**, what the team calls the state. It can change, and changing it moves no work item.

```
$ brigade work-item lists
KEY          NAME
hibernating  Hibernating
wanting      Wanting
doing        Doing
done         Done
$ brigade work-item rename-list wanting 'Next up'
renamed: wanting is now "Next up" (was "Wanting"); the work items in it stay in it
```

A board starts with `Todo`, `Doing` and `Done`. `brigade work-item lists <name>…` names the lists anew, as a
whole and in order: a name or a key the board already has stays that list, under the name it has; any other
name is a new list; and a list left out is dropped. A work item in a dropped list stays there, shown under the
list's key and counted apart, and is back in its list when the list is named again, by the name it had or by
its key.

Every command that takes a list takes its key or its name, and looks among the keys first. So no two lists
answer to one word: a list is not renamed to another's name or key, and a name that is another list's key is
that list. After `rename-list todo Backlog`, `Todo` still means the list now called Backlog, by its key.

A rename is a change of its own. If one machine renames a list while another names the lists anew, both stand
once the two have exchanged: the lists are the ones named, and the list is called what the rename said.

## Two machines, one number

No server hands the numbers out. Two machines that add a work item before either has the other's can both take
the same number. The next `brigade work-item` command on any machine that holds both sees it: the work item
created first keeps the number, the other gets the next free one, and the command says so before its own output:

```
renumbered: "Add a board" is now 44; 43 stays with "Fix the login redirect"
```

The work item's history keeps the line `renumbered from 43 to 44`. A commit made in between still names 43.

Two members who change one work item at the same moment both keep what they wrote: each change is its own file.
Where both set the same field, the later change wins.

## Where the board is, and when it travels

- **A folder on your machine**, under `boards/` in Brigade's state directory (`~/.local/state/brigade` on
  macOS and Linux, `%LOCALAPPDATA%\brigade` on Windows), outside every checkout. `brigade work-item` reads and
  writes that folder and nothing else: the board works while the backend does not.
- **File sync carries it** to the machine of every teammate's session, in any repository of the team
  ([Sync folders](sync-folders.md)). `brigade sync status` lists it as `team board`.
- **In seconds, or within the minute.** A change usually reaches an online teammate's machine about ten seconds
  after it is made; a new work item sometimes waits for the sync engine's once-a-minute scan.
- **Only while sessions run.** Two machines exchange work items while both run a session at the same time. What
  a teammate changed while your machine was off arrives after your next session starts; two members who are
  never online together converge through a third machine that overlaps both.
- **Nothing goes to the backend**, and nothing to a gateway.

## Limits

- A session whose `sync` option is off, or whose team file does not turn the board on, has no board:
  `brigade work-item` refuses with `config`, `no_board`.
- A machine without `syncthing` on its PATH keeps a board of its own that reaches nobody.
- Every teammate's machine can write anything into the board, as into any synced folder: a work item is a
  teammate's words, and a session reads it as text, never as an instruction
  ([Security, section 19](../../security.md#19-the-board-and-its-work-items)).
- No due dates, checklists, attachments or ordering within a list.
- A work item holds 2,000 changes and a board 20,000 work items.

## Refusals

| The refusal | What it means |
| --- | --- |
| `config`, `no_board` | the team has not turned its board on, or file sync is off for this session |
| `config`, `not_in_session` | the command was run outside a session |
| `config`, `label_needed` | `--mine` in a session with no roster label: name the member with `--to` |
| `not_found`, `no_such_work_item` | no work item carries that number |
| `invalid_input`, `no_such_list` | the board has no list of that name or key; the message names the ones it has |
| `invalid_input`, `name_taken` | a list cannot take a name another list answers to |
| `invalid_input`, `empty`, `too_long`, `not_utf8`, `secret_shaped` | the text: nothing there, past its cap, not UTF-8, or carrying a credential's shape |
| `conflict`, `number_taken_twice` | two work items still carry one number: run the command again |
| `conflict`, `board_full` | the work item, or the board, is at its limit and takes no further change |
