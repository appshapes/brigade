# Use

Your session does the work: it runs `brigade` through its shell tool when you ask it to, and reads team messages as
they arrive. You ask in words; these are the things to ask for and what comes back.

## If you want to

| If you want to | Go to |
| --- | --- |
| see who is on the team, this repository's sessions, or one member's | [See the team](#see-the-team) |
| message a teammate's session, or a person wherever they are | [Message a session or a member](#message-a-session-or-a-member) |
| know what teammates see about your session, and how to stop it | [Know what teammates see about your session](#know-what-teammates-see-about-your-session) |
| write to people on email or Slack, and read what they send | [Write to people on email or Slack](#write-to-people-on-email-or-slack) |
| keep folders in step with teammates' checkouts | [Sync folders](#sync-folders) |
| record what your session did, ask a teammate for a decision, or subscribe to the digest | [Record notes and ask for decisions](#record-notes-and-ask-for-decisions) |
| keep the team's work items on the Brigade Board, and have a session take one | [Keep the team's work on the Brigade Board](#keep-the-teams-work-on-the-brigade-board) |
| check whether your session is receiving | [Check that your session is receiving](#check-that-your-session-is-receiving) |
| know what happens when the network drops | [Ride out a network drop](#ride-out-a-network-drop) |
| read and release the messages a `hold` session kept | [Read and release held messages](#read-and-release-held-messages) |
| keep one of your sessions out of the team's traffic for a while | [Make one session quiet](#make-one-session-quiet) |
| have several sessions work parts of one plan, and invite them to it | [Work one plan with several sessions](#work-one-plan-with-several-sessions) |

## See the team

1. `/brigade:sessions` prints the roster verbatim: a short SESSION id, name, state, member, model, context, version.
2. `--here` shows this repository's sessions, `--member <who>` one member's, `--all` includes the offline ones.
3. `/brigade:whoami` prints this session's own identity verbatim: its name, SESSION id, team, label and delivery.

Details: [See the team](see-the-team.md).

## Message a session or a member

1. Ask: `Tell bbbbb that the tenant_id migration has landed.` Your session runs `brigade send bbbbb` with your words.
2. To reach a person rather than one of their sessions: `brigade send --member <who>` picks their best session.
3. `accepted` means durably stored, not read; a `waiting:` line says the recipient cannot read it yet.

Details: [Message a session or a member](message-a-session.md).

## Know what teammates see about your session

1. Its name, state, your label, its model and context, its Brigade version, and one sentence its model publishes
   with `brigade doing` under its row.
2. `share_doing: false` stops the sentence; `share_workspace_label: false` withholds the repository name.
3. `brigade rename` lets the model name the session; Claude Code applies it at your next prompt, as `/rename` would.

Details: [Know what teammates see about your session](your-session-on-the-roster.md).

## Write to people on email or Slack

1. `/brigade:email <address> <text>` or `/brigade:slack <to> <text>` send through the team's gateway.
2. A person's reply arrives as an ordinary message from `mail-gateway` or `slack-gateway`; reply with `--reply-to`.

Details: [Write to people on email or Slack](people-on-email-and-slack.md).

## Sync folders

1. The project lists folders in `.brigade.json`; each machine needs `syncthing` on its PATH.
2. Session start says `Brigade: file sync on: 2 folder(s) through syncthing.`; `brigade sync status` shows the rest.

Details: [Sync folders](sync-folders.md).

## Record notes and ask for decisions

1. `brigade note <<'EOF' … EOF` records one `Done:`, `Next:` or `Blocked:` line in the session's state entry.
2. `brigade decision --to <label> --options "a | b" --default a --by 2026-10-31 <<'EOF' … EOF` asks that person,
   through their digest recipients or their session; one for the session's own user is asked in the conversation
   and sent only after their wait. `brigade decision --close <id> <<'EOF' … EOF` records the outcome.
3. `brigade report subscribe [--recipients "<list>"] [--every 24h] [--wait 10m]` (or `/brigade:subscribe`) gets
   you the digest on your period; `brigade report unsubscribe` stops it.

Details: [Record notes and ask for decisions](notes-and-decisions.md).

## Keep the team's work on the Brigade Board

1. `/brigade:work-items` prints the Brigade Board, the team's board of work items: the open work items by list, in
   every repository of the team. `/brigade:work-items all` includes the lists the team hid from the listing.
2. `/brigade:work-item add <title>` adds one; `/brigade:work-item <number>` shows one, and `<number> move <list>`,
   `assign me`, `comment <text>`, `close` change it. Each is one `brigade work-item` command.
3. `/brigade:work-item-start <number>` has the session take a work item, do the work, comment and move it on;
   `/brigade:work-item-start <number> <session>…` has it lead your other sessions named there, each with a part.
   `/brigade:work-item-next` says which one to take next, and why; it changes nothing.
4. A team has a board when its `.brigade.json` turns it on; it travels while both machines run a session.

Details: [Keep the team's work on the Brigade Board](work-items.md).

## Check that your session is receiving

1. Ask: `Is this session receiving team messages?` It runs `brigade whoami`; `/brigade:whoami` prints the same.
2. The last line, `delivery:`, is the answer: `connected for 12m` means yes.

Details: [Check that your session is receiving](is-my-session-receiving.md).

## Ride out a network drop

1. Nothing to do: the session keeps trying, at least every 5 minutes, and messages wait on the server.
2. Your next prompt shows one line saying it is not receiving, and one more once it is again.

Details: [Ride out a network drop](network-drops.md).

## Read and release held messages

1. In your own terminal: `brigade inbox` lists what waits; `brigade inbox release --all` delivers it.
2. Nothing is acknowledged by looking; `release` refuses to run inside a session.

Details: [Read and release held messages](held-messages.md).

## Make one session quiet

1. `/brigade:quiet` in that session holds its team messages for you and says so on its roster line;
   `--to <where>` names where teammates should write instead.
2. `/brigade:quiet off` returns it to normal. Held messages wait for `brigade inbox release` in your terminal.

Details: [Make one session quiet](quiet-session.md).

## Work one plan with several sessions

1. Ask one session to coordinate: `Coordinate <goal> as a plan, and invite sessions 7c1d4 and b82e0.` It writes
   the plan file, gives every part one owner, and invites each session: `brigade send 7c1d4 --invite <plan>`.
2. An invited session answers for itself. It accepts when no work for its own user is in progress and it has the
   tools the invitation names; otherwise it declines and says why. Accepting approves nothing.
3. To keep a session out: `/brigade:quiet` holds its messages, and `frame: strict` makes it ask you first.

Details: [Work one plan with several sessions](plans.md).
