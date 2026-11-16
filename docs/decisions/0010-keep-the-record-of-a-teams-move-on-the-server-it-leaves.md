# 0010. Keep the record of a team's move on the server it leaves

- **Status:** Accepted; superseded in part by 0011
- **Date:** 2026-11-15
- **Supersedes:** ADR 0003, in part: in its Decision point 3, that the `rest` adapter does not carry the two
  commands of a move's record. The rest of ADR 0003 stands.

## Context

A member's session follows a team that moved on one thing only: the record that the backend the member joined
the team on holds of exactly that move ([ADR 0003](0003-follow-a-team-that-moved-on-the-old-backends-record.md)).
The Supabase adapter and the filesystem adapter kept that record. The `rest` adapter did not, and ADR 0003 said
why: "a record is kept by the backend a team leaves", and a Brigade server was only ever the backend a team went
to. So a team that lived on a server could not be moved with the one command
([ADR 0007](0007-let-a-session-move-a-team.md)). From a checkout whose team was on a server the command refused
with "the backend this team was joined on keeps no record of where a team goes", and its members joined the new
server one machine at a time, with the secret file (work item 341, found on 2026-11-15 while the pages of the
one-command move were revised).

Facts read from the code on 2026-11-15:

- The adapter protocol needed nothing. BAP/1 has `team move` and `team moved` under the capability `team.move`
  since work item 273, with conformance cases C-51 and C-52, and says that an adapter may carry `team.move`,
  `team.take_in`, both or neither (`docs/protocol-v1.md`, 4.4.10).
- The follow asks no question about which backend it reads: `internal/harness/teammove` and the two hooks
  compare a pin with a team file and ask the pin's adapter, whatever it is. A team's key is made of the adapter,
  the address and the reference, so two servers are two profiles on a member's machine.
- A server keeps a hash of every secret it is given: a credential, a join secret, a move ticket it mints, a retry
  key. It cannot keep a hash of the ticket of a move away, because it hands that ticket to its members.
- A server's log refuses an entry of a kind it does not know, and its snapshot names a format
  (`server/crates/brigade-server/src/snapshot.rs`).
- No request and no command of the operator's removes a team from a server.

The lead of work item 341, the session `89-server-9-second-team-and-roadmap`, decided the points below on
2026-11-15 on the building session's recommendations, under the owner's standing rule that a session decides on
its own recommendation.

## Decision

A Brigade server keeps the record of where a team went, as the reference backend does, and the `rest` adapter
carries the two commands that write and read it.

1. **Two requests**, an addition to REST contract version 1 that a server may leave out; the version stays 1. A
   server that carries them lists the capability `team.move` in `/v1/info` (`docs/backend-rest-v1.md`, section
   3c). BAP/1 does not change.
2. **`/v1/team/move`** records where the team went (the adapter, the address and the certificate pin of the
   backend it went to, and the move ticket that backend made) or, with `clear` alone, clears the record. A
   second record takes the place of the first, and a clear is safe to repeat. **`/v1/team/moved`** answers the
   record, and carries the ticket only to a request that asks for it.
3. **The record is the team's owner's alone to write and to clear.** Every other caller reads the one
   `unauthorized` a stranger to a team that does not exist reads, checked before any member of the request but
   its `team_ref` is acted on. What is answered before that check is the same for every caller and says nothing
   about any team: a body over the cap, a body that is not one JSON object, and a `team_ref` that is missing,
   empty or not a string are `invalid_input`, and a caller with no credential the server knows is
   `unauthenticated`. **Every active member reads it** and may take the ticket; a member that left, was revoked
   or was banned reads nothing. The refusals are the ones BAP/1 fixes for the other backends.
4. **The record changes nothing else on the server the team leaves.** The team stays there as it is: no
   membership ends, no session is closed, and no join, registration or message is refused for it. A member who
   has not followed still reaches the team there, which is how its session reads the record. Ending the team
   there is a later act of its administrator's and no part of a move, and a server has no command that removes
   a team: what its owner can do is rotate the join secret there, so that nobody new joins, and revoke its
   members once they have followed.
5. **The ticket is kept as it was given**, in the server's state, in the record of the log and in every
   snapshot that holds the record: the one secret a server keeps whole. Its type prints as nothing of itself,
   so no line the server writes and no debug rendering of a record or a state holds it. The server holds a
   recorded ticket to 1024 code points.
6. **Who outlives what.** A rotation of the join secret and a transfer leave the record: a rotation ends what
   let anyone in on that server, and the record lets nobody in there; the next owner has the record to clear.
   The operator's `--take-back` clears it, in the take-back's own entry: the record is the owner's word, and
   the owner is who the team is taken from.
7. **The retry key.** The request that records where a team went, and the one that clears a record, are the
   owner's own, with its credential: each leaves a record that ends the retry key the team keeps for that
   owner, by the rule of [ADR 0006](0006-take-a-team-in-and-open-its-move-by-request.md).
8. **The log and the snapshot.** Two new kinds of record, `team.move_recorded` and `team.move_cleared`. A
   server before 0.8.0 refuses a log that holds either. A snapshot names format 4 only when its state holds
   such a record, and format 3 otherwise, whose state it then is member for member. So a server 0.7.0 starts
   on a data directory that a 0.8.0 ran on until the first move is recorded there, and no later.
9. **The two sides that do not match.** An older `rest` adapter does not advertise `team.move` and asks a
   newer server nothing. A newer adapter asks an older server `/v1/info` and nothing else: `team moved`
   answers that the team did not move, as BAP/1 has it for a backend that cannot say, and `team move` answers
   `config` with the reason `server_lacks_team_move`. The one command turns that into a refusal that names
   server 0.8.0, before the new server is asked anything.
10. **A session whose team is on a server follows from plugin 0.57.0**, the first whose `rest` adapter reads a
    server's record. The one command names the sessions that are online on an older plugin by that number when
    the team leaves a server, and by 0.55.0 when it leaves any other backend.
11. **A team that comes back.** The one command asks the server the team moves to whether that server itself
    records that the team left it, and clears such a record, after the server answered that the caller owns
    the team there and before the backend the team leaves is asked to record anything. A clear that is refused
    or fails stops the move with nothing recorded on the backend the team leaves, and the refusal says which
    server holds what. So two servers never each say that the team is on the other.
12. **A chain is followed one move at a time.** Nothing is built for a member who missed a move: the page says
    what such a member does.

## Alternatives considered

- **The record as any member's to write**, or as the operator's from the command line. It lost to the rule the
  other backends have: a record sends every member's sessions to a server of its writer's choosing, which is
  the team's administrator's to decide. An operator's command that writes it would be a second way to do what
  the operator can already do by naming an owner.
- **A recorded move that closes the old server to joins or sessions.** It lost on three counts. A checkout that
  has not pulled the new team file still starts sessions on the old server, rightly. A session's follow reads
  the record with the membership it holds there. And `--clear` would then have to undo a second change of
  state. The owner who wants the old server closed to new members rotates its join secret, which is explicit
  and leaves the record.
- **The ticket kept as a hash, or sealed under a key of the members'.** A hash cannot be presented. The server
  holds only the hash of each member's credential and no machine keeps the join secret, so there is no key that
  every member has and the server has not.
- **The ticket in every answer of `/v1/team/moved`**, as the Supabase function returns it. It lost to a request
  that asks: `team move --show` and the one command's checks read the record often and need no ticket.
- **Snapshot format 4 for every snapshot of 0.8.0**, the 0.5.0 precedent. Simpler by one function, and it would
  keep a 0.7.0 from starting on a directory a 0.8.0 had written three snapshots on, whether or not a move was
  ever recorded there. The format by content costs one function and one test, and a state that holds no record
  is format 3's state.
- **Leaving the destination's own record to `team move --clear --from`.** A team that came back would leave two
  servers that each name the other, and a team file flipped between them, by a commit or a branch, would send
  a session back and forth: each follow is behind a record, so the comparison of ADR 0003 would not stop it.
- **Clearing the destination's record after the source has recorded.** A failure between the two would leave
  exactly the state the step exists to prevent.
- **Following a chain in one step**, by reading the record of each backend in turn. It would send a session to
  a backend its member's machine never joined on the word of a backend it never consented to.

## Evidence

All on 2026-11-15, on this machine (macOS, Apple silicon), in the worktree of work item 341.

- Conformance cases C-51 and C-52 pass through `bin/brigade adapter rest` against the stand-in server
  (`make test`: 56 passed, 0 failed, 1 skipped, the slow case) and against the built server on the file log and,
  with `--slow`, on the Cavalry feed (`the_conformance_suite_is_green_on_the_file_log` and
  `the_conformance_suite_is_green_on_the_cavalry_feed`, which name the two cases so that a suite which skipped
  them again would fail).
- Through the built `brigade` and two servers on loopback, the stand-in twice under `make test` and the built
  server twice over TLS under `make server-test`
  (`internal/harness/e2e/rest_server_move_test.go`): a team moved from one server to another by its owner's one
  command, with a member's next session following at its start
  (`TestATeamMovesFromOneServerToAnotherByOneCommandThroughTheRealBinary`); the sessions that were running
  following at their next prompts (`TestRunningSessionsFollowAMoveFromOneServerToAnotherThroughTheRealBinary`);
  a team that came back, the first run stopped while the server could not clear its record and the second
  finishing (`TestATeamThatComesBackToAServerClearsTheRecordOfItsLeavingThroughTheRealBinary`); and a chain
  (`TestAMemberWhoMissedAMoveDoesNotFollowTheNextThroughTheRealBinary`).
- What the chain showed: a member whose machine was on the first backend, with a team file that named the third,
  followed nothing and was told that the file does not match. With the file of the first move it followed to
  the second backend, and with the file of the second move it followed from there to the third.
- A server started from a copy of the old server's data directory, at another address, as the destination
  (`TestAServerStartedFromACopyOfTheDataIsNoPlaceToMoveATeamToThroughTheRealBinary`, the built server only): the
  one command stopped because the team is owned by someone else there, nothing was recorded, and the
  administrator was on the copy's roster twice. The operators' page says so.
- The server's rules: `server/crates/brigade-core/tests/rules.rs`, four tests of the record, and the two that
  walk every request against the rule of a retry key, which now ask the two new requests. The walk's seed
  changed with its list of requests.
- The server as a process, on both logs (`where_a_team_went_is_kept_across_a_kill_on_the_file_log` and
  `..._on_the_cavalry_feed`): the record is there after a kill right behind the answer and after a stop and a
  start from a snapshot; the snapshot is format 3 before the record, format 4 with it and format 3 again after
  `--take-back` cleared it; and the ticket's bytes are in no line the server wrote.
- The snapshot's two branches (`a_snapshot_names_format_4_only_when_it_holds_where_a_team_went`): a state
  with no record is written in the bytes a server 0.7.0 wrote, header and checksum included, and a reader of
  formats 1 to 3 refuses the one that holds a record by its format.
- The released server 0.7.0, built from the tag `server-v0.7.0`, run by hand on data directories this build had
  written. On one with no record it started and answered. On one that held a record it wrote `the snapshot …
  is passed over: it is of format 4, and this build reads formats 1 to 3` and `the log could not be read from
  the snapshot's position, 704: not an entry: unknown variant `team.move_recorded`, expected one of …`, and
  ended with status 1. After the record was cleared and the server stopped, it started and answered again. Its
  words name the kind of record and the format, and no version to run: they were released before this.
- The one command between the released 0.7.0 and this build's server, run by hand through the built `brigade`
  in both directions. From a team on the 0.7.0 it was refused with "keeps no record of where a team goes: it is
  older than 0.8.0", and the new server, behind a front that counts requests, was asked none. From a team on
  this build's server to the 0.7.0 the team moved, and its member's next session followed.
- The record's older road, `brigade team move --ticket-file`, for a team that comes back: the built `brigade`
  cleared the record on the server the team returned to before the other server recorded anything, on two
  stand-in servers and on two built servers.
- Not measured: a move between two servers on two machines, and a team with gateways on the server it leaves.

## Consequences

- A team on a server moves as a team on Supabase does: one command of its owner's, and no command of its
  members'. A server's URL can change, and a team can change operators, without a join on every machine.
- The owner of a team on a server can now send every member's sessions to a server of its choosing. On a
  server the owner is not always the team's creator: it is whoever took the team in, the first member to join a
  team the operator took in, or the principal the operator named. The last two were ways to administer a team;
  they are now ways to redirect it. `docs/security.md` sections 20 and 21 state that.
- A server's data directory holds one secret that is not a hash, for as long as a record stands and, after it
  is cleared, in the snapshots still kept and in the log's earlier entries. The server sets no end to that
  stay: the file log keeps every entry, and the feed lets a recording go only once it has been rolled and a
  snapshot is due, so what ends a cleared ticket's use is closing the move at the backend the team went to, or
  the ticket's own end.
- A server before 0.8.0 does not start on a data directory on which a move was recorded.
- The one command has one step more, which asks the server a team moves to two things it was never asked.
- The old server keeps a team that moved, with its messages and its gateways, until someone ends it there, and
  a server still has no way to end one.
- We would revisit this if a server gained a way to end a team, which would decide what becomes of the record;
  if chains became common enough to follow in one step; or if the ticket at rest had to be kept from whoever
  reads the data directory.

## References

Work item 341; `docs/backend-rest-v1.md` section 3c; `docs/protocol-v1.md` 4.4.10; `docs/security.md` sections 20
and 21; `server/CHANGELOG.md` section 0.8.0; `.context/plans/brigade-server.md` section 11;
`docs/project-owner/server/move-to-another-server.md` and `docs/team-admin/members/move-the-team.md`; ADRs 0003,
0006, 0007 and 0008.
