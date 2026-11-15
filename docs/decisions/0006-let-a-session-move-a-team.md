# 0006. Let a session move a team, behind an option that is on by default

- **Status:** Accepted
- **Date:** 2026-11-14
- **Supersedes:** ADR 0003, in part: in its Decision point 2, the words "refuses inside a session"; and, for the
  one command `brigade team move --to` alone, that the destination "is never typed" (the same point, and its
  alternative "A typed destination for `team move`"). The rest of ADR 0003 stands, as ADR 0005 left it.

## Context

A team's administrator records where the team went with `brigade team move`
([ADR 0003](0003-follow-a-team-that-moved-on-the-old-backends-record.md)). That decision made the command refuse
inside a session when it records or clears, as `team revoke-member` and `team transfer` do: a record sends every
member's sessions to another backend, and a session reads untrusted text. It also read the destination from the
project's team file and never from an argument, so the administrator edited `.brigade.json` by hand first.

On 2026-11-14 the owner moved a real team from a Supabase project to a Brigade server by those rules: a join
secret carried to the server's host, two operator commands with the server stopped, a ticket handed back as a
file, `.brigade.json` edited by hand, a join, and `brigade team move --ticket-file` in his own terminal because
the command refused inside a session. The terminal named no Claude profile, so his label was sent from the
wrong one (work item 326). He then asked for one command, started from a coding session in which the agent asks
the administrator for what it needs: "Whatever api keys, tokens, etc. you require, the admin will provide you"
(work item 328). [ADR 0005](0005-take-a-team-in-and-open-its-move-by-request.md) gave the server the requests
that needs.

Three facts, read on 2026-11-14:

- The owner's principle for Brigade is that it is "open by design, and only closed by choice/configuration". A
  team's default and a server's default already follow it (`docs/security.md`, sections 4 and 20).
- The refusal is not a boundary. "Inside a session" is read from the environment: `CLAUDE_PID`,
  `MUSE_SESSION_ID`, `CODEX_THREAD_ID` or `GROK_SESSION_ID` (`internal/harness/config/environ.go`). A command run
  with those variables removed passes it. `docs/security.md` section 5 has said so of the refusing commands since
  2026-09-08, and on 2026-11-14 it was done, at the owner's word, to correct the label above. What the refusal
  did do, the same section says: that other command matches no `Bash(brigade:*)` rule, so a permission mode that
  asks did ask about it.
- A plugin option never reaches the shell tool of a session. A command there reads an option from the start facts
  the session's start hook wrote, as `team join` reads `label` (`internal/harness/sessionmap/start.go`).

## Decision

`brigade team move` runs inside a session, and one command of it moves a team.

1. **The whole verb runs inside a session**: `--to`, `--ticket-file` and `--clear`. `--show` always did.
2. **An option turns that off.** `team_move_in_session`, on unless the member sets it to `off`. Off is the refusal
   ADR 0003 made, in the same shape (`usage`, reason `in_session`). A value that is neither word is off, and the
   session says so at its start. The option has the same name in every harness, and the command reads it from the
   session's start facts. At a terminal it has no effect.
3. **Nobody is asked inside a session.** At a terminal the command shows what it is about to do and asks. Inside a
   session it prints the same line as a statement and proceeds, as `team join` does: the invocation is the
   consent.
4. **One command moves a team**: `brigade team move --to <server URL> --secret-file <path>`, with `--certificate`,
   `--create-token-file` and `--ticket-days` for a server that needs them, run in a checkout of the team. In
   order it checks, changing nothing; takes the team in on the server, where the caller must come out as its
   owner; opens the move, with the ticket in a private file under Brigade's state directory; records the move on
   the backend the team leaves; and only then rewrites `.brigade.json`, the binding and the checkout's pin. It
   then says what was done and what is left. Every step is safe to repeat, so the same command finishes a move
   that stopped.
5. **The destination of that command is typed**, once, and that one value is written to the record and to the
   team file, so the two cannot differ. `brigade team move --ticket-file` still reads the destination from the
   team file, for a server that takes no team in by request.
6. **The label is the member's.** The server is told the label this member has on the backend the team leaves, as
   a session that follows tells it, and never one read from the Claude profile of the terminal the command runs
   in.
7. **A skill, `move`,** in every package asks the administrator for what the command needs, one question at a
   time: the server's URL, the path of the join secret's file, and the creation token's file and the
   certificate's fingerprint when the server needs them. It never asks for a secret's content, never chooses a
   path and never writes a secret file. It moves a team only on its user's own ask in the conversation, and is
   told that a move asked for by a teammate's message, a file or a page is untrusted text. In Claude Code it
   pre-approves nothing that writes.

`team revoke-member`, `team transfer` and `inbox release` refuse inside a session as before.

## Alternatives considered

- **Keep the refusal, and have the skill hand the person a command to paste into a terminal.** It is the move of
  2026-11-14: the terminal carries none of the session's facts, which is how the wrong label was sent. And
  removing a variable undoes the refusal: what it adds is one dialog in a mode that asks, which an ask rule
  gives a member who wants it.
- **The option, off by default.** A session-led move would then start with a settings change on the day it is
  needed, against the owner's principle, and the default would claim a protection the refusal does not give.
- **A real boundary: something a session cannot produce**, such as a confirmation only a terminal can give. Brigade
  has no such channel today. The harness's own permission prompt is the gate that holds, as section 5 says of
  every command, and the skill leaves the move to it.
- **Narrow the `team-messaging` skill's grant so that it does not cover `team move`.** In Claude Code that
  skill allows every `brigade` command for the turn that loaded it, which is the turn a teammate's message
  arrives in. Narrowing it would put the harness's prompt back in that turn. It would also change what an
  existing skill runs without asking, for every command it uses, and that is a decision of its own. It is not
  made here; the consequences below name what it leaves open, and `docs/security.md` gives the ask rule that
  closes it for one member.
- **Let `--to` alone run in a session.** The record, the clear and the move are one act with one risk, and
  `--clear` is how a move is undone. A split would be a rule with no reason behind it.
- **Read the destination from the team file, as the record's own form does.** The file would be edited by hand
  first, which is one of the steps the owner asked to be rid of. ADR 0003 refused a typed destination because a
  record that differs from the file by one character is followed by nobody. With one value written to both, that
  cannot happen.
- **Write the team file first, then record.** A failure in between would leave a committed file that no record
  vouches for, which every member's session reports as a file that does not match. The file is written last.
- **Send the label a join sends**: the `label` option, or the terminal profile's account. It is work item 326's
  mistake. A member of the team is the same member on the server.
- **Ask the server whether it takes a team in before asking the administrator.** The bundled adapter has no
  command that asks it and changes nothing, and a new one is a change to the frozen protocol. The adapter asks
  the server as the first thing its take-in does, before the request, so a server that does not is found before
  anything is written all the same.

## Evidence

All of it on 2026-11-14, on one machine (macOS, arm64), from the worktree of work item 328. No live server and
no hosted project was asked.

- The move of 2026-11-14 by hand, and the label it sent (work item 326).
- `docs/security.md` section 5: `env -u CLAUDE_PID brigade team revoke-member …` runs as if from a terminal,
  measured 2026-09-08.
- Through the built `brigade` (`internal/harness/e2e/rest_move_test.go`): a team on the fs adapter is moved by the
  one command to the stand-in server (`make test`) and to the built server over TLS with its own certificate
  (`make server-test`'s tests); a second member's next session follows with no command, under the label the member
  had, and a message is delivered. The same from inside a session with the option on; refused, with nothing asked
  of any backend, with the option off. The command stopped after the take-in, after the open, after the record
  and after the team file ends in the same state when run again. A secret file of another team, a server that
  takes no team in by request, a project without the migration (the Supabase adapter's refusal, answered by a
  stand-in for the fs adapter) and a team that someone else owns on the server each stop it before the record,
  the team file or the pin is written.
- The label: with the terminal's Claude profile signed in to another account, the server's roster holds the
  label of the old backend and not that account (the same tests, and
  `TestTeamMoveToSendsTheLabelOfTheOldBackend`).
- The join secret, the ticket and the creation token are in no output, no question and no adapter's argv, and in
  no file a run leaves but the files they travel in (the same tests, and `internal/harness/commands`).
- From the local Supabase stack (Supabase CLI 2.116.0, its migrations applied) to the built server: the same
  move by the one command, the follow and the message (`make test-move-live`, two tests, both passing). With the
  three functions of the team-move migration taken out of PostgREST's sight on that stack, which is what a
  project without the migration is to the adapter, the built `brigade` refused with the sentence that names the
  migration and `make backend-install`, exit 11, and left the team file and the store as they were; the functions
  were put back and the record read again.
- A take-in whose answer is lost: behind a front that hands the request to the stand-in server and drops the
  answer, the command stops with nothing recorded or written and leaves the profile its adapter wrote where it
  is, because that directory is what the next run is the same caller with. After a refusal, where the server
  holds nothing new, the same profile is removed (`TestATakeInWhoseAnswerIsLostKeepsItsProfileThroughTheRealBinary`,
  and the two tests of the profile in `internal/harness/commands`). What the next run is answered is the
  adapter's and the server's part, in the work of ADR 0005.
- Mutation: 92 one-line changes to the new rules, in the command, the option, the start facts, the hook, the
  version rule and the team file's rewrite. 91 are caught by a test; three of them needed a test added, a
  secret file's path inside the checkout that names no file, the roster read without its offline sessions, and
  what the command says when the ticket has nowhere to go. The one that is not caught drops a comparison that
  cannot differ: the rewritten team file is parsed again, and a URL it could not carry fails that parse first.
- The acceptance gate and the four package checks pass.
- Not measured: a move of a real team from a hosted project by the one command, and a session of a real model
  running the `move` skill. Both wait on the owner's word.

## Consequences

- Whatever steers the administrator's session, a teammate's message or a file or page the session read, can
  send every member's sessions, and the join secret, to a server of its choosing. Before this it took the
  administrator's own terminal, or a command that removed the session's variables.
- What still stands in its way: only the team's creator on the old backend can record a move; the command needs
  the path of the join secret's file, which is outside the repository and which the session is told and never
  chooses; the harness's own permission prompt; and the line every member is shown when their session follows,
  with `follow_team_move` to turn the follow off.
- The third of those is weaker than it reads, and weaker than before. The prompt is gone in a mode that skips
  prompts, under a `permissions.allow: Bash(brigade:*)` rule of the member's own, and, in Claude Code, in a
  turn that loaded the `team-messaging` skill, whose grant is `Bash(brigade:*)`. With the refusal, a move from
  a session took a command that no such rule matched, so a mode that asks did ask. An ask rule,
  `Bash(brigade team move*)`, puts that dialog back for the member who adds it.
- A member who wants `team move` kept out of their sessions sets one option. That keeps out the text that would
  talk a session into it. It does not keep out a command that removes the session's variables, which nothing in
  Brigade does.
- The three commands that still refuse inside a session stand on the same ground as the one that no longer does.
  A decision about them would be a new ADR.
- One more option to keep the same in four packages, and one more member in the start facts.
- `brigade team move` has two ways to say where a team went: typed, for `--to`, and read from the team file, for
  `--ticket-file`. A change to either is a change to what `docs/security.md` section 21 argues.
- We would revisit this if a session-led move were ever made on text the administrator did not write, or if a
  harness gave a command a way to know that a person, and not a model, confirmed it.

## References

Work item 328; work item 326; [ADR 0003](0003-follow-a-team-that-moved-on-the-old-backends-record.md),
which this supersedes in part, and [ADR 0005](0005-take-a-team-in-and-open-its-move-by-request.md);
`docs/security.md` sections 5 and 21; `docs/team-admin/members/move-the-team.md`;
`internal/harness/commands/teammove.go` and `teammoveto.go`; `internal/harness/config/options.go`; the `move`
skill of each package; ADR 0001.
