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
     request made a principal and made it the team's owner: by taking the team in, or, on a team that has no
     owner, one the operator took in or took back, by the first join. No other request keeps one, so a later
     take-in cannot put another key in the place of the owner's.
   - A take-in for a team the server holds, that carries the team's current join secret, that key and no
     credential the server knows, is not the join of a new principal. It is answered for the principal the first
     request made, with a new credential, and every credential that named that principal before is unknown from
     then on. `owner` is true: a team keeps a key for its owner alone. The server keeps the key through it, so
     an answer that is lost a second time is made again as the first was.
   - Everything else is as point 2 has it. A wrong key and no key, with the right secret, are the join they
     were, and no answer says whether a team keeps a key. Another secret is `conflict`, the ticket of a move is
     the join the ticket is, a caller the server knows is the principal its credential names, and the creation
     token is asked for where it was. A request without the member is answered as one always was.
   - **The key ends with the first record that its principal's own request makes**, with its credential: a
     session registered, its credential made unknown, the team's move opened or closed by it, a member revoked,
     the team transferred or left, a team joined or created with that credential. From that record on the owner
     holds a credential, and the key has nothing left to stand in for: the same take-in is the join of a new
     principal, in the bytes it has on a team that never kept a key. What the owner reads and what it is refused
     leave no record and end nothing; neither does a request of any other principal, nor a command of the
     operator's that names no owner. A team that gets another owner keeps no key, and a rotation of the team's
     join secret ends it, as it ends everything that let anyone in before it. The rule is in `State::apply`,
     with no new kind of record, so the state stays what the log makes it: a record names the principal that
     acted, or is one that only the team's owner is answered for, and the two records of a move say whether its
     owner asked for them. The contract asks a server for no less than this and allows no more: no later than
     the first such request that changes what the server holds, and no earlier than the first that carries the
     credential.
   - The key lives in a file with the protection of the credential it stands in for, on the machine that owns
     the team. The `rest` adapter mints it before the request, or uses the one an earlier attempt for the same
     server and the same team left. It keeps it in `take-in-key.json` beside `credential.json` (mode 0600,
     written by rename, never under the project directory), registers it for redaction, sends it, and on an
     answer writes the credential first and removes the key file only then. One answer leaves the file where it
     is: a take-in that carried a key an earlier run kept, and was answered `owner: false` for a credential the
     profile holds. The server did not look at that key, and it is the key of the owner the earlier run made
     when the machine joined the team in between; the adapter says on stderr that `profile revoke-credentials`
     and the same take-in make that take-in again. A clock of its own would add nothing to the key: a person
     makes a request again when they come back to it, and the key ends when its owner first acts. BAP/1 does not
     change: the key is in no request and no result of the adapter protocol.
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
   taken in, and a `member.joined` record, in one entry. With a retry key the `member.joined` record carries the
   key's hash, a member that is written only then. A take-in made again is one entry of the records there
   already were, `credential.revoked`, `credential.issued` and `member.joined`, and its `member.joined` carries
   the hash again: the credential made unknown ended the key, and the join keeps it for the answer that may be
   lost in its turn. `team.move_opened` and `team.move_closed` have one member more, `by_owner`, written only
   when the team's owner asked by request, so the operator's records are the records they were. There is no new
   kind of record and no new snapshot format: a server 0.6.0 reads what a 0.7.0 wrote. It reads past the members
   it has no use for, and a snapshot it writes holds no key.

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
- **An end of its own for the key by the clock**, an hour or a day. Nobody can name the hour in which a person
  makes a request again, and the key has an end that needs no clock: its owner's first request.
- **No end but a rotation**, which this decision had at first. A copy of the key file then stayed good after the
  file was gone and after the owner made its own credential unknown, and a take-in that arrived a second time
  took the credential out of the hands of an owner that had long used it. The verification of the change showed
  both (Evidence).
- **End the key at the owner's first request of any kind**, a read included. A read leaves no record, so either
  the state would stop being what the log makes it, or a read would have to write.
- **A word on every entry for who asked for it**, so that any request of the owner's ends the key by one rule.
  It is a member more on the entry, which every record of every request would carry or the rule would read. The
  records that a principal's own request leaves already name it, or are ones only an owner is answered for; the
  two of a move were the only ones that did not say.
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
- The retry key in the rules (`rules.rs`, 65 tests in the file). The request made again with the secret and the
  key is answered for the principal the first one made, in one entry of a credential made unknown, a credential
  issued and a join that carries the key's hash again; the credential before it is unknown, and the request is
  made again a second time, carrying a credential the server does not know. Another key and no key are the join
  of a new principal, with answers of the same members; the key with another secret is the conflict it was, byte
  for byte; with the ticket of a move it is the ticket's join; with a known credential it is that principal's.
  The key is kept only where the request made the owner, on a team it took in and on one the operator took in or
  took back, and a member's key never takes the owner's place. The state, a snapshot and the log hold the key's
  hash and never the key.
- The end of the key in the rules. Twelve requests of the owner's own, each on a server of its own: a session
  registered, its credential made unknown, the move opened, the move the operator opened closed, a member
  revoked, the members of a secret revoked, the team transferred, the team left, the team joined again and taken
  in again with the credential, a team created, the secret rotated. Before each the take-in is made again; after
  each the team keeps no key, the same request is a stranger's join in the two records such a join has, and
  nothing of the owner's is touched. The key outlives the take-in made again twice, ten requests of another
  member's, strangers' take-ins, the operator's opened and closed move and its naming of the owner the team has,
  and the owner's own reads, refusals and requests that change nothing. A team the operator gives another owner
  keeps none. Two servers that differ only in whether the team keeps a key answer sixteen requests in the same
  bytes and write the same records; once the owner's session has ended the key they hold the same state and
  answer eighteen in the same bytes, the one with the kept key among them. A log in the records a 0.7.0 of
  before the key had an end wrote reads to one state however often it is read, and a server that goes on from it
  answers the same. And a walk of twenty thousand requests on two dozen teams, chosen by numbers that are the
  same in every run: a key is kept 132 times, each for the owner a take-in made; a take-in is made again 44
  times, each one that was due and no other; 110 keys end with their owner's own request, each key that such a
  request found; and no request of anybody else's, no command of the operator's that names no owner and no
  take-in ends a key or changes an owner.
- The retry key on a running server (`a_take_in_is_made_again_with_its_key`, on the file log and on the Cavalry
  feed). The server is killed right behind the answer of the first take-in. Started again, it knows the
  credential of that answer, answers the request made again for the same principal with a new one, and refuses
  the first with 401. After a stop, a move the operator opens on the stopped server and a start from the
  snapshot, the key is honoured still. The owner then opens the move itself, and the same request is a
  stranger's join from there on, after a kill as well: the log says whose request opened the move. The newest
  snapshot holds the key's hash while the key is kept and nothing of it once it ended; no snapshot holds the
  key, and no line of the server's holds the key, a credential or the hash of either. Two tests more start from
  the snapshot and end the key by a credential the owner gives up and by a rotation
  (`an_owners_own_request_ends_its_retry_key`). The log's entries are sealed, so the log's bytes were not
  searched: that the hash is in the log is what the start after the kill shows.
- The built server as a process, by a script of the verification's own: 106 checks on the file log and 95 on the
  Cavalry feed. The status and the body of eleven requests, and the headers of five of them, are the same on a
  team that keeps a key and on one that keeps none. Sixteen take-ins at once with one key leave one principal,
  one member and one credential the server knows; with sixteen keys they leave one owner, whose key is the one
  the team keeps. On the file log, a log cut at three places inside the entry of a take-in made again holds none
  of it, and the request is answered again. Seven of the owner's own requests end the key, each across a kill,
  and the newest snapshot then holds nothing of it; another member's requests and the owner's reads do not.
- The two built programs together
  (`a_take_in_whose_answer_is_lost_is_run_again_by_the_built_adapter_and_owns_the_team`, which `make
  server-test` runs). `bin/brigade`'s `rest` adapter asks `brigade-server` through a front that hands each
  request on, reads the server's whole answer and, for the first take-in, closes the adapter's connection
  without a byte. The first run ends with status 9, `unavailable`, holds no credential and has kept its key in a
  file of mode 0600. The second run is answered `taken_in: false` and `owner: true`, and the key file is gone.
  `team open-move` then writes a ticket that admits a member, and the team has two members: its owner and that
  one. The request of the first run, made once more after that with the key, is a stranger's join, the owner's
  credential still closes the move, and the snapshot holds nothing of the key. The key is on no stream of the
  runs at debug level and in no line of the server's.
- The adapter against the stand-in server (`internal/adapters/rest/retrykey_test.go`), which keeps the same end
  of the key, with the answer lost as a proxy's 502 and as a closed connection: the key file's mode, the same
  key sent again, the owner's answer, the credential accepted for `open-move`, and the file gone once the
  credential is written. The key stays through a refusal, an answer that is not the command's and a credential
  that could not be written. After a `team join` between two runs the take-in is the member's, the file stays
  with the same key in it and stderr says what to do; `profile revoke-credentials` and the same command then own
  the team. A file that names another server, another pin or another team is replaced, and stderr says so; one
  that others can read, a link and a directory in its place are refused; a profile that cannot keep the key
  sends nothing.
- Older servers again: 0.6.0 and 0.5.0, each built from its tag, on the file log, 61 checks each. Each starts on
  what this 0.7.0 wrote, after a kill and after a stop: with a key kept and a move the operator opened, and with
  a key that the owner's own requests ended, whose records say whose they were. Each knows the credential the
  repeat issued and not the one it replaced, admits the ticket and answers a take-in `not_found`. This 0.7.0
  starts again on what each wrote: the key is honoured still after one of them wrote a member's join and no
  snapshot, forgotten after one wrote a snapshot, and ended still where the owner had ended it.
- Mutation of the retry key as it was first built, with no end but a rotation: 54 one-line changes, each caught,
  and with them 8 to the write probe and 8 to the hiding of a secret's half. 25 to the server's rules, 7 more
  run against the server as a process on both logs, 14 to the adapter and 8 to the stand-in server. One of the
  adapter's, which let a key of 21 characters in a kept file be used, passed its first run: the test had
  measured the key with the function the change altered. The test now counts the characters itself. Four were
  first written in a form that did not build, and were run again in one that does. The verification ran them all
  again, each caught, and sixteen of its own, of which five were caught by no test before it: a credential the
  server does not know that stops a take-in made again, a take-in made again that is counted against its
  principal, a key file that cannot be removed and is left in silence, a key that is minted before the server is
  asked whether it takes teams in, and a link to a directory that is read as one. Each is caught now. The test
  of the two built programs fails under four changes more: an adapter that sends no key, one that mints a key
  for every run, one that never removes its key file, and a server that honours no key.
- Mutation of the end of the key: 31 one-line changes to the rule, the records and the state. 30 are caught as
  the tests first stood. One, which ended a principal's key though the team kept another's, was caught by none,
  because no record leaves a state in which that differs: a test now applies the records by hand and holds a
  team to one key and a principal to one team. 5 more run against the server as a process, with the 7 of the
  first build. Two of the twelve were caught by none as the process tests first stood, a rotation that leaves
  the key and a snapshot without the way from an owner back to its key: two tests now end the key after a start
  from the snapshot, by a credential given up and by a rotation, and all twelve are caught. 5 to the stand-in
  server, each caught. The 32 changes to the rules of the key as it was first built still fail the tests that
  were written again for its end. Of the 8 to the stand-in server 5 still do. Three change nothing a request can
  show any more, because the stand-in has no operator: no ticket is open while a team keeps a key, a caller the
  server knows ends its own key with the request that would keep it, and only the owner rotates, whose request
  ends the key before the rotation is asked about. Each of the three holds on the real server, where the
  operator's commands reach it, and is tested there.
- The key file a member's answer leaves: 10 one-line changes to the adapter, each caught. The names of a success
  passed through the redactor: 7, each caught, one for each of the four commands and three more. The probe as
  long as the file it stands in for: 4, of which 3 are caught. A probe one byte shorter than a long name passes
  the tests, because the write's own temporary name ends in one to ten digits: what holds for certain is that
  the probe passes every name of 239 bytes or less and refuses every name of 249 or more, as the write does
  where a name has 255 bytes, and the test holds both ends.
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
  machine has its key file and nobody has asked for anything as the owner. Without the key the request made
  again joins as a member, and the operator names the owner with `--set-owner`: a client that sends no key, a
  key file that went with the machine, and a server 0.6.0 that wrote a snapshot of the directory in between,
  which drops the hash.
- Whoever reads the key file while it exists, and holds the join secret, can be the principal that owns the
  team, and the request that does it makes the credential before it unknown. That is open from the take-in until
  the owner's first request that leaves a record, and no longer: after it a copy of the file opens nothing,
  whether the file is gone or not. In the move of a team by one command that request follows the take-in at
  once. An owner that takes a team in and then asks for nothing leaves it open until it does, or until the
  team's secret is rotated.
- Inside that time a take-in that arrives a second time, as a proxy that sends a request again would deliver it,
  is a take-in made again: it makes unknown the credential its client has just kept, and the client has removed
  its key file by then. The owner is a principal nobody holds a credential for, and the operator's `--set-owner`
  is the way back. The time is what lies between the answer and the owner's next request that leaves a record;
  after it such a request is a stranger's join and takes nothing from the owner.
- A machine that ran `team join` between a lost answer and the take-in run again holds a member's credential,
  and a request with a credential the server knows is that principal's whatever key it carries. The adapter
  keeps the key file through that answer and says what to do: `profile revoke-credentials`, and the take-in
  again, which is then answered for the owner.
- A server keeps one hash more for a team that was taken in with a key, and one reference more for its owner,
  until that owner's first request that leaves a record.
- The rule is as whole as the list of records it reads. A request of the owner's that leaves a record
  `State::apply` does not read for it would not end the key. There is none today: each request that can be an
  owner's first is held to the rule in the tests, and a request that is added later needs its arm there and its
  step in the walk of the rules.
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
