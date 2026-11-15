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
4. **A take-in can be made again by the machine that made it, with a key that machine minted.** A take-in
   needs no credential, and the credential of the owner it makes is in its answer alone. An answer that is
   lost between the server's sync and the adapter's write of the credential (the command's deadline, a proxy's
   502, an interrupt, a disk with no room) would leave a team whose owner is a principal nobody holds a
   credential for. So the request may carry `retry_key`: 22 to 128 characters of a credential's alphabet, which
   the client minted from at least 128 random bits.
   - The server keeps a hash of the key, never the key, in the record that makes the caller a member, when the
     request made a principal and made it the team's owner: by taking the team in, or, on a team the operator
     took in, by the first join. No other request keeps one, so a later take-in cannot put another key in the
     place of the owner's.
   - A take-in for a team the server holds, that carries the team's current join secret, that key and no
     credential the server knows, is not the join of a new principal. It is answered for the principal the
     first request made, with a new credential, and every credential that named that principal before is
     unknown from then on. `owner` says whether the principal owns the team when the request is answered: true
     unless the team was transferred since.
   - Everything else is as point 2 has it. A wrong key and no key, with the right secret, are the join they
     were, and no answer says whether a team keeps a key. Another secret is `conflict`, the ticket of a move is
     the join the ticket is, a caller the server knows is the principal its credential names, a banned
     principal is refused in the same bytes as before, and the creation token is asked for where it was. A
     request without the member is answered as one always was.
   - The key is honoured for as long as the team's join secret is the one it was taken in with: a rotation
     ends it, and it has no other end. The key lives in a file with the protection of the credential it stands
     in for, on the machine that owns the team, and the adapter removes that file as soon as the credential is
     written, so the key outlives the take-in only when the answer was lost. A clock of its own would add
     nothing to that, and a person makes a request again when they come back to it.
   - The `rest` adapter mints the key before the request, or uses the one an earlier attempt for the same
     server and the same team left. It keeps it in `take-in-key.json` beside `credential.json` (mode 0600,
     written by rename, never under the project directory), registers it for redaction, sends it, and on an
     answer writes the credential first and removes the key file only then. BAP/1 does not change: the key is
     in no request and no result of the adapter protocol.
5. **`/v1/team/open-move` and `/v1/team/close-move`** answer the team's owner only, by its credential, and do
   what the operator's `--open-move`, with `--ticket-days`, and `--close-move` do. The ticket is in the answer
   of the request that opens the move, once. The adapter writes it to a file of mode 0600 that the caller
   names, having proved before the request that a file can be written there, and it is in no argument, no log
   line, no result and no error. A second open replaces the ticket and says so.
6. **The protocol.** BAP/1 gets the capability `team.take_in` and three commands, `team take-in`, `team
   open-move` and `team close-move` (4.4.10, conformance cases C-53 to C-55). The `rest` adapter carries them.
   The fs adapter and the Supabase adapter do not: a team is taken in by the backend it moves to. An adapter
   that does not advertise the capability is asked none of the three.
7. **The operator's commands stay** and work as before.
8. **The log.** A take-in by request is a `team.created` record that names its owner and says that the team was
   taken in, and a `member.joined` record, in one entry. With a retry key the `member.joined` record carries
   the key's hash, a member that is written only then; a take-in made again is one entry of the records there
   already were, `credential.revoked`, `credential.issued` and `member.joined`. There is no new kind of record
   and no new snapshot format: a server 0.6.0 reads what a 0.7.0 wrote. It reads past the key's hash, which it
   has no use for, and a snapshot it writes holds none.

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
- **The operator's `--set-owner` as the only way back** from a take-in whose answer was lost. It is a command on
  a stopped server, which stops every team on it, and it needs the operator. The requests exist so that an
  administrator who is not an operator moves a team with one command, and a timeout is the commonest way for
  that command to fail.
- **Let the next take-in with the secret replace an owner that was never seen**, one whose credential the
  server never saw again. Any holder of the join secret could race the administrator's second attempt and own
  the team, and every member of the team on the backend it leaves holds the secret.
- **A credential minted by the client** and sent with the take-in, so that a lost answer loses nothing. It
  changes how every principal is made: the server issues every credential and shows it once, and a credential
  of the client's choosing would be a second way to make one, on the three requests that make a principal, for
  the sake of one of them.
- **An end of its own for the key**, an hour or a day. The key gives nothing that the credential beside it in
  the same directory does not give, the adapter removes it when the take-in is done, and nobody can name the
  hour in which a person makes a request again.
- **The key's hash on `team.created`.** A team the operator took in gets its owner by the first join, in an
  entry that has no `team.created`, and a lost answer leaves the same owner with nobody there. `member.joined`
  is in both entries.

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
- The gap a lost answer left. The review of that build confirmed it on the built server: a take-in made again
  without the credential of the first answer joined as a new principal, `owner: false`; its `open-move` was
  answered 403; and `--set-owner` on the stopped server was the only way back. The rules still hold that
  outcome for a request that carries no key.
- The retry key in the rules: four tests added, 57 in the file. The request made again with the secret and
  the key is answered for the principal the first one made, in one entry of a credential made unknown, a
  credential issued and a join; the credential before it is unknown, and the new one opens the move. Another
  key and no key are the join of a new principal, with answers of the same members; the key with another
  secret is the conflict it was, byte for byte; with the ticket of a move it is the ticket's join; with a
  known credential it is that principal's. A transfer is told truly, a banned principal reads the conflict
  and gets no credential, and a rotation and the operator's take-back end the key. The key is kept only where
  the request made the owner, on a team it took in and on one the operator took in, and a member's key never
  takes the owner's place. The state, a snapshot and the log hold the key's hash and never the key.
- The retry key on a running server (`a_take_in_is_made_again_with_its_key`, on the file log and on the
  Cavalry feed). The server is killed right behind the answer of the first take-in. Started again, it knows
  the credential of that answer, answers the request made again for the same principal with a new one, and
  refuses the first with 401. After a stop and a start from the snapshot the key is honoured still. After a
  rotation it is not, and a kill does not bring it back. The newest snapshot holds the key's hash while the
  key is honoured and nothing of it once the secret was rotated; no snapshot holds the key, and no line of the
  server's holds the key, a credential or the hash of either. The log's entries are sealed, so the log's bytes
  were not searched: that the hash is in the log is what the start after the kill shows.
- The two built programs together
  (`a_take_in_whose_answer_is_lost_is_run_again_by_the_built_adapter_and_owns_the_team`, which `make
  server-test` runs). `bin/brigade`'s `rest` adapter asks `brigade-server` through a front that hands each
  request on, reads the server's whole answer and, for the first take-in, closes the adapter's connection
  without a byte. The first run ends with status 9, `unavailable`, holds no credential and has kept its key
  in a file of mode 0600. The second run is answered `taken_in: false` and `owner: true`, and the key file is
  gone. `team open-move` then writes a ticket that admits a member, and the team has two members: its owner
  and that one. The key is on no stream of the three runs at debug level and in no line of the server's.
- The adapter against the stand-in server (`internal/adapters/rest/retrykey_test.go`), with the answer lost
  as a proxy's 502 and as a closed connection: the key file's mode, the same key sent again, the owner's
  answer, the credential accepted for `open-move`, and the file gone once the credential is written. The key
  stays through a refusal, an answer that is not the command's and a credential that could not be written. A
  file that names another server, another pin or another team is replaced, one that others can read is
  refused, and a profile that cannot keep the key sends nothing.
- Older servers again, with a take-in that kept a key and one made again in the directory: 0.6.0 and 0.5.0,
  each built from its tag, 30 checks each. Each starts on what this 0.7.0 wrote, after a kill and after a
  stop, knows the credential the repeat issued and not the one it replaced, admits the ticket and answers a
  take-in `not_found`. This 0.7.0 starts again on what each wrote. After one of them wrote a snapshot the key
  is forgotten, and the request made again is a member's join.
- Mutation of the retry key: 54 one-line changes, each caught. 25 to the server's rules, 7 more run against
  the server as a process on both logs, 14 to the adapter and 8 to the stand-in server. One of the adapter's,
  which let a key of 21 characters in a kept file be used, passed its first run: the test had measured the key
  with the function the change altered. The test now counts the characters itself. Four were first written in
  a form that did not build, and were run again in one that does.
- The two hardenings of the same review. A ticket file and a secret file are proved writable before the
  request that needs them (`adapterkit.CanWriteAtomic`): 8 one-line changes, each caught. The secret half of a
  join secret and of a ticket is registered for redaction: 8 one-line changes, each caught, the two for a
  rotation and an opened move by a test that hands the command its redactor, because nothing the server chose
  is written after those answers but the result.
- `make server-check server-test` and the acceptance gate pass on the final tree. `make server-deps-check` did
  not run: the machine has no `cargo-deny`. No crate was added.
- Not measured: a move of a real team from a hosted project by these requests, and an answer lost by a real
  proxy or a real timeout. The command that makes the requests from the administrator's machine is the next
  part of work item 328.

## Consequences

- Whoever holds a team's join secret, and may create a team on a server, can own that team there. That moves no
  member: a member's session follows a team only on the record on the backend the team leaves, and the join
  secret does not let anyone write it.
- On a server with no creation token a stranger can take in a team under a reference that is not theirs, with a
  secret they invented. The real team's take-in then answers `conflict`. A creation token closes that. No
  command of the operator's removes such a team yet.
- A take-in whose answer is lost is made again by the same machine, and owns the team. That holds while the
  machine has its key file and the team has the secret it was taken in with. Without the key the request made
  again joins as a member, and the operator names the owner with `--set-owner`: a client that sends no key, a
  key file that went with the machine, a rotation by someone the team was transferred to, and a server 0.6.0
  that wrote a snapshot of the directory in between, which drops the hash.
- Whoever reads the key file while it exists, and holds the join secret, can own the team. The file is in the
  adapter's private directory beside the credential, which gives the same, and is there only between a request
  and its answer.
- A server keeps one hash more for a team that was taken in with a key, until the team's secret is rotated.
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
and 21; `.context/plans/brigade-server.md` sections 4.7 to 4.9; `server/CHANGELOG.md`, 0.7.0;
`internal/adapters/rest/retrykey.go`; ADR 0001.
