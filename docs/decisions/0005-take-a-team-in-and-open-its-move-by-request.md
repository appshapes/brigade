# 0005. Take a team in and open its move by request, and let whoever takes it in own it

- **Status:** Accepted
- **Date:** 2026-11-14
- **Supersedes:** ADR 0003, in part: in its Decision, that the server's operator alone opens a move (point 1) and
  that the administrator joins the server first to own the team (point 2); and its alternative "Recording before
  the administrator joins". The rest of ADR 0003 stands.

## Context

A team that lives on a Supabase project can move to a Brigade server and keep its join secret
([ADR 0003](0003-follow-a-team-that-moved-on-the-old-backends-record.md)). Until this decision the server's side
of that move was its operator's: two commands on the data directory with the server stopped, `--import-team` and
`--open-move`, and the move ticket handed to the team's administrator as a file. The administrator then joined the
server before anyone else, because the first member to join a team that was taken in owns it, and recorded the
move on the old backend. On 2026-11-14 the owner asked for the whole move as one command run from the
administrator's machine (work item 328). Of ADR 0003's rule that the administrator must join first he said that
it "was an arbitrary idea", and that an ADR must not stand in the way of a better one.

Four facts, read from the code on 2026-11-14:

- Taking a team in and opening its move were already jobs on the one thread that answers requests
  (`server/crates/brigade-server/src/engine.rs`), each a function with no I/O
  (`server/crates/brigade-core/src/execute.rs`). Neither needed a stopped server for what it does. It needed one
  only because the operator's command is a second process, and one process owns the log.
- The server already had what a request of this kind needs: `/v1/team/create` is held to a creation rule (no
  token configured, or the `Brigade-Create-Token` header matches), three requests answer a team's owner only,
  and a rotation returns a new secret in its answer, which the adapter writes to a file.
- A server cannot check who administered a team on the backend it leaves. The two backends share no identity:
  a member is an anonymous account there and a principal the server issues here
  (`.context/plans/brigade-server.md` section 4.8).
- A team's reference is public: the committed team file names it. And only the team's creator on the old
  backend, or whoever controls that backend, can record a move there (ADR 0003, point 2).

## Decision

A running server takes a team in, and opens and closes its move, by request.

1. **Three requests**, an addition to REST contract version 1 that a server may leave out; the version stays 1.
   A server that carries them lists the capability `team.take_in` in `/v1/info`
   (`docs/backend-rest-v1.md`, section 3b).
2. **`/v1/team/take-in`.** The caller presents the team's join secret, a name and optionally a label. It is
   allowed exactly as a team creation is: where the server asks for a creation token, the request carries it.
   - The server does not hold the team: it takes it in under the reference inside the secret, with the secret's
     hash, and in the same log entry the caller becomes a member and the team's owner.
   - The server holds the team under that secret: the request is a join, by the rules a join has. So on a team
     the operator took in, which has no owner, the first join still gives ownership.
   - The server holds that reference under another secret: `conflict`, as the operator's command answers.

   The answer says whether the team was taken in now and whether the caller owns it. The request is safe to
   repeat.
3. **Whoever takes a team in by request owns it**, in that request. The rule of the first join stays for a team
   the operator took in from the command line and for a team the operator took back.
4. **`/v1/team/open-move` and `/v1/team/close-move`** answer the team's owner only, by its credential, and do
   what the operator's `--open-move`, with `--ticket-days`, and `--close-move` do. The ticket is in the answer
   of the request that opens the move, once. The adapter writes it to a file of mode 0600 that the caller
   names, and it is in no argument, no log line, no result and no error. A second open replaces the ticket and
   says so.
5. **The protocol.** BAP/1 gets the capability `team.take_in` and three commands, `team take-in`, `team
   open-move` and `team close-move` (4.4.10, conformance cases C-53 to C-55). The `rest` adapter carries them.
   The fs adapter and the Supabase adapter do not: a team is taken in by the backend it moves to. An adapter
   that does not advertise the capability is asked none of the three.
6. **The operator's commands stay** and work as before.
7. **The log.** A take-in by request is a `team.created` record that names its owner and says that the team was
   taken in, and a `member.joined` record, in one entry. There is no new kind of record and no new snapshot
   format: a server 0.6.0 reads what a 0.7.0 wrote.

This supersedes three things in ADR 0003. In its Decision point 1, that a move is opened by the server's
operator alone, with the server stopped: the team's owner opens it by request as well. In its point 2, that the
administrator joins the server first, with the secret file, to own the team: the administrator owns the team by
taking it in. And its alternative "Recording before the administrator joins", whose reason was that the team
would stay without an owner: a team taken in by request has its owner from its first entry.

## Alternatives considered

- **The operator's two commands, run for the administrator over a shell on the server's host.** It needs the
  team's administrator to have a shell there, and it stops the server, and every team on it, for each team that
  moves.
- **A token of its own for taking a team in**, beside the creation token. The creation rule already says who
  may put a team on a server, and taking one in puts one there. A second secret would be handed around for the
  same decision.
- **Take the team in with no owner, and keep the rule of the first join.** It is two requests with a moment
  between them in which anyone who holds the join secret takes the owner's place by joining first. It is the
  rule the owner called arbitrary. One request that takes in and owns has no such moment.
- **Answer another secret as a join answers a wrong one**, the uniform `unauthorized`. The caller has to tell a
  reference that is taken from a server that refuses it, because what it does next differs. The reference is
  public, so that a team of it exists says nothing new, and the operator's command already answers `conflict`.
- **A new kind of record for a team taken in by request.** A server 0.6.0 would refuse a log that holds it. The
  team is the team a creation makes, so the record is the one a creation makes, with one member more that is
  written only when it is true.

## Evidence

All of it on 2026-11-14, on one machine (macOS, arm64), from the worktree of work item 328. No live server and
no hosted project was asked.

- The rules, with the clock in the test's hands (`server/crates/brigade-core/tests/rules.rs`): six tests added,
  53 in the file, all passing. They hold a team taken in and owned in one entry of three records; a repeat by
  the same caller and by one with no credential; a team already here under the same secret, one the operator
  took in included; another secret refused as a `conflict`, the same bytes for a principal the team banned; the
  creation token; the bound on a principal's teams; the two requests of a move refused to every caller but the
  owner, and to a caller with no credential; a ticket that admits a member which does not own; and a second
  open that replaces the first.
- A running server over HTTP (`server/crates/brigade-server/tests/server.rs`,
  `a_team_taken_in_by_request_stays_owned`): a team taken in by request, its owner, its three members and its
  open move are there after the process is killed, and after a stop and a start from the snapshot, on the file
  log and on the Cavalry feed. The server's own lines hold neither the join secret nor a ticket.
- Older servers: 0.6.0 and 0.5.0, each built from its tag, start on a data directory that 0.7.0 wrote, after a
  kill (the log) and after a stop (the snapshot), with two teams taken in by request and a move opened by one
  in it. Each finds the teams, their members and their owners, lets the owner rotate, admits the ticket, and
  answers a take-in `not_found`. 0.7.0 starts again on what each of them wrote.
- The conformance cases C-53 to C-55 pass through the `rest` adapter against the stand-in server (`make test`:
  54 of the 57 cases run and pass, three are skipped) and against the built server, on the file log and, with
  `--slow`, on the Cavalry feed (`make server-test`).
- Mutation: 46 one-line changes to the new rules. Of the 18 to the server, each is caught by a test; two of
  them needed a test added, one that reaches the owner's check past the request's gate. Of the 28 to the
  stand-in server, the adapter and the protocol's types, 26 are caught. The two that are not take away the
  adapter's registration of the join secret and of the ticket for redaction, and change nothing that can be
  seen: the logger redacts every text of a join secret's form already. The review of this change removed
  both registrations for that reason. It made the adapter's tests look for a ticket's secret half as well,
  which is not of that form: for the whole ticket no such test could fail on a log line.
- `make server-check server-test` and the acceptance gate pass. `make server-deps-check` did not run: the
  machine has no `cargo-deny`. No crate was added.
- Not measured: a move of a real team from a hosted project by these requests. The command that makes them from
  the administrator's machine is the next part of work item 328.

## Consequences

- Whoever holds a team's join secret, and may create a team on a server, can own that team there. That moves no
  member: a member's session follows a team only on the record on the backend the team leaves, and the join
  secret does not let anyone write it.
- On a server with no creation token a stranger can take in a team under a reference that is not theirs, with a
  secret they invented. The real team's take-in then answers `conflict`. A creation token closes that. No
  command of the operator's removes such a team yet.
- A take-in whose answer is lost leaves a team owned by a principal whose credential nobody holds: the request
  made again joins as a member. The operator names the owner with `--set-owner`.
- A take-in that is refused for its secret tells that a team of that reference is on the server, where a join
  tells nothing. It is counted against a known principal as a failed join is.
- The reference of a team that is taken in is its caller's choice, and the server keeps it. `brigade-server`
  holds it to 256 code points, so that one request of a caller it does not know cannot have it keep a megabyte.
- The operator stops the server for a team's move no longer, and no ticket file changes hands.
- The contract has three more optional endpoints, the protocol three more commands and the conformance suite
  three more cases to keep compatible in both directions.
- We would revisit this if two backends came to share an identity, so that a server could check who
  administered a team where it came from, or once an operator can remove a team.

## References

Work item 328; [ADR 0003](0003-follow-a-team-that-moved-on-the-old-backends-record.md), which this supersedes in
part; `docs/backend-rest-v1.md` section 3b; `docs/protocol-v1.md` 4.4.10 and 4.7; `docs/security.md` sections 20
and 21; `.context/plans/brigade-server.md` sections 4.7 to 4.9; `server/CHANGELOG.md`, 0.7.0; ADR 0001.
