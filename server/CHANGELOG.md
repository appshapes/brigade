# Changelog of the Brigade server

The server, `brigade-server`, is released apart from the Brigade plugin, with its own version and its own tags
(`server-v<version>`). The plugin's changes are in [the repository's changelog](../CHANGELOG.md). The contract
between the two is [`docs/backend-rest-v1.md`](../docs/backend-rest-v1.md).

## [0.7.0] — 2026-11-14

### Added

- **A running server takes a team in by request** (work item 328, ADR 0005). Until now a team that lives on
  another backend was taken in by the operator alone, with `--import-team` on a stopped server. Now
  `/v1/team/take-in` does it on a server that runs: the caller presents the join secret the team has on the
  backend it leaves, a name and, when it has one, a label. It needs no credential, as a creation needs none,
  and it is allowed exactly as a creation is: a server started with `--create-token-file` asks for that token
  in the `Brigade-Create-Token` header, and refuses a take-in without it in a creation's words, before it
  reads anything of the request beyond that its body is one JSON object. A team the server does not hold is
  taken in under the reference inside the secret, with the secret's hash and never the secret, and in the
  same entry of the log the caller becomes a member and the team's owner.
- **The reference of a team that is taken in is held to 256 code points.** The caller chose it, the server
  keeps it, in its state, in every record of the team and in every snapshot, and BAP/1 gives it no cap:
  without one, a caller the server does not know could have it keep a megabyte with one request. A longer
  one is `invalid_input` naming `join_secret`, in fixed words. A join keeps nothing of what it presents and
  is answered as before, and `--import-team` is the operator's own and has no cap.
- **Whoever takes a team in by request owns it.** The server cannot check who administered a team where it
  came from, so the rule is the one a creation has. The rule of the first join, by which the first member to
  join a team that has no owner owns it, stays for a team `--import-team` took in and for one `--take-back`
  took back.
- **A take-in is safe to repeat, and a team that is here is joined.** Where the server holds the team, the
  request is a join by the rules a join has: the join secret and an open move ticket admit, a ticket never
  makes an owner, a team that has no owner is owned by the first caller whose join may own it, and a refusal
  is counted against a known principal as a failed join is. The answer says whether the team was taken in
  now, `taken_in`, and whether the caller owns it, `owner`. Where the server holds a team of that reference
  under another join secret the request is refused with `conflict` (409) and the reason `other_join_secret`,
  in the words `--import-team` is refused in, and a principal the team banned reads the same. A request that
  is made again without the credential its first answer carried joins as a member, unless it carries the
  retry key of the first (next).
- **A take-in whose answer was lost is made again by the machine that made it.** A take-in needs no
  credential, and the credential of the owner it makes is in its answer alone: when the answer never arrives,
  nobody holds it, and the request made again would join as a stranger. So `/v1/team/take-in` takes one more
  member, `retry_key`: 22 to 128 characters of a credential's alphabet, which the client minted from at least
  128 random bits. The server keeps the key's hash, never the key, when the request made a principal and made
  it the team's owner, by taking the team in or by the first join of a team that has no owner, one
  `--import-team` took in or `--take-back` took back. A take-in
  for that team with its join secret, that key and no credential the server knows is answered for the
  principal the first request made, with a new credential, and every credential that named it before is
  unknown from then on. `owner` says whether it owns the team when the request is answered. Another key and
  no key are the join they were, and no answer says whether a team keeps a key; another secret is `conflict`;
  a banned principal is refused in the same bytes; the creation token is asked for where it was. A rotation of
  the team's join secret ends the key, and so does `--take-back`, which rotates. A key that is not of that
  form is `invalid_input` naming `retry_key`. A request without the member is answered as before.
- **A team's owner opens and closes its move by request.** `/v1/team/open-move` and `/v1/team/close-move` do
  what `--open-move`, with `--ticket-days`, and `--close-move` do, on a server that runs, and answer the
  team's owner only: every other caller reads what a stranger to the team reads. The ticket is in the answer
  of the request that opens the move, once, and the `rest` adapter writes it to a file of mode 0600 on the
  owner's machine, so no ticket file changes hands. The number of days is `ticket_days`, from 1 to 365, and
  30 when the request names none. A second open puts a new ticket in the place of the one before and says so.
  The requests and the operator's commands are one move: what one opens the other closes or replaces.
- **A team that is taken in counts against the principal that takes it in** as a team it created does: five
  in an hour, and then `rate_limited` with the reason `team_create` (0.6.0, ADR 0004).
- **The server says who took a team in and who moved it.** A take-in leaves `member <reference> took in team
  <name> (<team reference>) with the join secret it has on another backend, and owns it`, and the owner's two
  requests leave `member <reference> opened the move of team <name> (<team reference>): its ticket admits
  until <time>` and `member <reference> closed the move of team <name> (<team reference>): its ticket admits
  nobody from now on`. A take-in made again leaves `member <reference> made its take-in of team <name> (<team
  reference>) again with the retry key of the first: it has a new credential, and the one it had is not known
  any more`. No line holds a join secret, a ticket, a retry key, a credential or the hash of any of them.
- **`/v1/info` lists `team.take_in`** beside `team.admin`. The three requests are an addition to version 1 of
  the contract that a server may leave out (`docs/backend-rest-v1.md`, section 3b); the bundled `rest`
  adapter asks for the capability before each of its three commands, `team take-in`, `team open-move` and
  `team close-move`, and the conformance suite holds a server to them with cases C-53 to C-55.

### Changed

- **A creation token now says who may take a team in as well.** On a server with no creation token, whoever
  can reach it and holds a team's join secret can take that team in and own it there. That moves no member:
  a member's session follows a team only on the record on the backend the team leaves. And because a team's
  reference is in its committed team file, a stranger can take in a team under a reference that is not
  theirs, with a secret of their own, after which the real team's take-in is refused. A creation token closes
  both. No command removes a team from a server yet (`docs/security.md`, section 20).

Nothing about the data directory changes. A take-in by request is the record a creation writes, with one
member more that is written only for a team that was taken in, and the record of a join, with one member more
that is written only when the take-in carried a retry key: the key's hash. A take-in made again is the records
of a credential made unknown, a credential issued and a join, without the first when its principal had made
its own credential unknown before. There is no new kind of record and the
snapshot's format stays 3; a team that keeps a key has one member more in it until its secret is rotated. A
0.6.0 starts on a 0.7.0 directory and a 0.7.0 on a 0.6.0 one. A 0.6.0 reads past the key's hash and writes
none in its own snapshot, so a take-in whose answer was lost, made again after a 0.6.0 wrote one, joins as a
member. The operator's commands are unchanged, and `--import-team` writes the record it always wrote.

## [0.6.0] — 2026-11-14

### Added

- **One member's installation is bounded, with the numbers a team in a Supabase project meets** (work item
  313, ADR 0004). A principal the server knows by its credential creates at most five teams in an hour, and
  opens at most 120 sessions in an hour over all its teams; a session that is resumed opens none and is never
  refused for it. After five joins that failed for their secret within fifteen minutes, over all teams, its
  next join is refused before the secret is looked at, the right one included, until the oldest of them is
  fifteen minutes old; a join that succeeds clears them. Each refusal is `rate_limited` (429) with the reason
  the Supabase backend sends, `team_create`, `register_session` or `join_attempts`, and the wait in
  `retry_after_ms`, which for a join is never under a minute. The numbers are the server's own: there is no
  option for them.
- **Where each is counted.** The teams and the sessions are counted from the record times of the log, so a
  restart keeps the count and a snapshot carries it. The snapshot's format stays 3: a server before 0.6.0
  reads such a snapshot and drops the counts, having no bound to hold against them, and a snapshot written
  before 0.6.0 reads as nobody having created a team or opened a session in the last hour. The failed joins
  are counted in the memory of the process, because a request that is refused makes no record: a restart
  forgets them.
- **What is not counted as a failed join.** A join that cannot be read is told `invalid_input`, held or not.
  And a gateway's join is never counted: a gateway that was put out of its team tries again each minute with
  the file its operator keeps, and joins the minute that file holds the right secret, as before.
- **A caller with no credential is held by none of the three.** It is a new member with each request, so a
  server that strangers can reach can still be filled by them. What holds a stranger is a creation token, a
  server that is not on the public internet, or a limit by address in the proxy in front of it
  (`docs/project-owner/server/run-the-server.md`, "What one member may do in an hour").

## [0.5.0] — 2026-11-13

### Added

- **A team's members can follow it to the server with no command of their own: the move ticket** (work item
  273). A team that is taken in keeps its join secret, but no member's machine keeps that secret, so every
  member ran one join with the secret file. `brigade-server --data <dir> --open-move <team_ref> --ticket-file
  <file>`, run with the server stopped, opens the team's move and ends. The team gets a move ticket: a second
  join secret that lets a member in until the ticket ends, 30 days later unless `--ticket-days <days>` names
  another number from 1 to 365. The ticket is written to the file, readable by its owner alone, and to nothing
  else: it is on no command line and in no line the server writes, and the server keeps its hash only. The
  file must not be there yet, and its place is checked before anything is changed. The operator hands the file
  to the team's admin, who records the ticket on the backend the team leaves; a member's session reads it there
  and joins the server with it. That half is the Brigade plugin's, in the release that carries work item 273.
- **A ticket never makes a team's owner.** A join the ticket lets in is a join in everything else: the same
  request, the same answer, a member under the secret the team has now, and a member the owner banned is
  refused as it is with the secret. But on a team that has no owner it leaves the team without one, as a
  gateway's join does. Every member of the team on the backend it leaves can read the ticket, so the team's
  admin joins first, with the secret file, and owns the team. A ticket that has ended, one whose move was
  closed or replaced and one of another team are refused in the words a wrong secret is.
- **A move ends with its ticket, and is closed by the operator, by another move and by a rotation.**
  `brigade-server --data <dir> --close-move <team_ref>`, run with the server stopped, closes it: the ticket
  admits nobody from then on, and every member that came in with it stays a member. Opening a move for a team
  that has one puts a new ticket in the place of the one before, which is how a ticket whose file was lost is
  replaced, and the command says that the earlier ticket no longer admits. `brigade team rotate-secret` closes
  the team's move in the same entry of the log, and so does `--take-back`: a rotation is how an owner ends
  everything that let anyone in before it. The operator opens another move when this one is to go on.
- **The server says who came in with a ticket.** A member's join with one leaves the line `member <reference>
  joined team <name> (<team reference>) with its move ticket, labelled "<label>"`, and a rotation that closes
  a move leaves `the move of team <name> (<team reference>) is closed by the rotation of its join secret: its
  ticket admits nobody from now on`. Each of the two commands prints one line, as the operator's other
  commands do: the team, the file and when the ticket ends, in UTC. No line holds any part of a ticket.

Nothing about the contract changes: `/v1/team/join` carries a ticket as it carries a join secret, and no answer
has a new member.

### Changed

- **Do not go back to 0.4.x on a data directory on which 0.5.0 has opened a move.** The opening and the closing
  of a move are records of kinds 0.4.x does not know, and 0.4.x refuses to start on a log that holds one. The
  snapshots 0.5.0 writes are of a new format, 3, which 0.4.x passes over: it would otherwise read a team whose
  move is open as a team with none. On a directory where no move was ever opened, 0.4.x passes those snapshots
  over and starts from the newest snapshot of its own that is still kept and the log after it, or from the
  log's first entry where none is kept, and it refuses a log that no longer begins there. 0.5.0 reads the
  snapshots and the log of 0.4.x as they are.

## [0.4.1] — 2026-11-13

### Added

- **The server says who joined, and what its gateways were asked** (work item 313). Until now its lines were its
  start, its snapshots, its stop and its gateways' own turns, so neither a member's first join nor a request
  from Slack could be confirmed from the log. Now a member's own request that creates a team, joins one or makes
  a member its owner leaves one line each, once the log holds it: `member <reference> joined team <name>
  (<team reference>), labelled "<label>"`, with `created` and `owns … from now on` beside it. And every request
  to a gateway's address leaves one: which gateway, whether it took the request, and the status, as in `the
  slack gateway took a request (200)` or `the slack gateway refused a request its Slack app did not sign (401)`.
  The reason is one of ten fixed sentences chosen by the status. Nothing a gateway answered with is repeated,
  because some of it comes from the request, and no line carries a credential, a join secret, a token or a
  message's text. A label is cut at 128 characters and a control character in it is written `?`, so a label
  cannot start a line of its own. The label is what the team's roster shows, often an email address: the
  server's journal is as private as its data directory.

Nothing about the data directory or the contract changes: 0.4.1 starts on a 0.4.0 directory, and a 0.4.0 on a
0.4.1 one.

## [0.4.0] — 2026-11-12

### Added

- **A team's owner administers it.** The three administration commands of the Brigade plugin work on a server:
  `brigade team rotate-secret` gives the team a new join secret and ends the old one, `brigade team
  revoke-member` ends one member's membership, bans it or lifts its ban, or ends every membership that joined
  with an old secret, and `brigade team transfer` hands the team to another member. They are the endpoints
  `/v1/team/rotate-secret`, `/v1/team/revoke-member` and `/v1/team/transfer` of the contract's section 3a, and
  they answer the team's owner only: anyone else reads what a stranger to the team reads. The owner is the
  member that created the team, until a transfer names another. A member that is revoked has its sessions
  closed, and a receive of its that waits is refused within a second.
- **A team that was taken in gets its owner at the first join.** The server cannot check who administered the
  team on the old backend, so the first member to join it over HTTP owns it. A gateway joins inside the server
  and never becomes an owner. A team that 0.3.0 took in and that already has members gets its owner the same
  way, at the first join after the update: a member that joins again with the secret it has owns the team.
- **The operator names an owner.** `brigade-server --data <dir> --set-owner <team_ref> --principal
  <principal_ref>`, run with the server stopped, makes an active member of a team its owner and ends. It is for
  a team whose first join was not its admin's, and for a team whose owner lost its credential. No address of the
  server names an owner this way.
- **The operator takes a team back.** `brigade-server --data <dir> --take-back <team_ref> --secret-file
  <file>`, run with the server stopped, is for a team whose join secret leaked and whose owner is whoever joined
  with it first: that owner can rotate the secret and revoke everyone, which leaves no member to name. The
  command gives the team a new join secret, ends every membership and leaves the team with no owner, all in one
  entry of the log, and ends. The new secret is written to the file, readable by its owner alone, and to
  nothing else: it is on no command line and in no line the server writes. The file must not be there yet, and
  its place is checked before anything is changed. The first member to join with the new secret over HTTP owns
  the team, so the file goes to the team's admin first. A member that was banned stays banned.
- **`brigade team members` says which secret each member joined with**, as `joined_secret_version`: 1 for the
  secret the team was created or taken in with, and one more with every rotation.
- **`/v1/info` lists `team.admin` in `capabilities`**, which the plugin's `rest` adapter asks for before it
  sends an administration command.
- **A gateway comes back by itself after a rotation, as the member it was.** A gateway whose membership ends
  reads its `join_secret_file` again before it joins again, once a minute for as long as it is refused. After
  the owner has rotated the secret and revoked everyone who joined with the old one, the operator replaces that
  file and restarts nothing. The gateway joins with the credential it holds, so it is the same member with the
  same session and leaves no dead member behind, and a gateway the team's owner banned stays out until the
  owner lifts the ban. Only a gateway whose credential the server no longer knows comes back as a new member.
- **A gateway whose first join was refused needs no restart either.** It reads its `join_secret_file` again
  before each attempt, so a file the operator mended is joined with, and it asks a team that refused it once a
  minute instead of every two seconds.
- **One team cannot stop Slack for the others.** A team's owner can now end the membership of the team's
  Slack gateway, by revoking it. While that gateway is out, a message from Slack to a session of any other team
  the gateway serves is carried as before. The roster a person is shown lists the teams that can be reached and
  says under them that one team's gateway is not connected, without naming the team, and so does the answer to
  a message that names a session nobody can find. For as long as a team is left out, a person whose message was
  carried is always told which session it went to and on which team, with the same sentence, however they
  named the session: a name that sessions of two teams answer to would otherwise go to the team that is reached
  without a word. A gateway that reaches none of its teams answers Slack a failure, as it always did, so that
  Slack delivers again once the gateway is back. Mail needs no such rule: a mail belongs to the one team whose
  inbox it was sent to.

### Changed

- **A membership that ends no longer brings back a session that was long gone.** A member that leaves, or is
  revoked, has its open sessions closed. A session whose lease ran out more than a week before, which retention
  had let go of, was closed with them and so kept for another week; it is now left to go.
- **Do not go back to 0.3.0 on a data directory 0.4.0 has run on.** A rotation, a revocation, a ban and a change
  of owner are records of kinds 0.3.0 does not know, and 0.3.0 refuses to start on a log that holds one. The
  snapshots 0.4.0 writes are of a new format, 2, which 0.3.0 passes over: it would otherwise read them without
  the bans and the secret versions they hold. 0.4.0 reads the snapshots and the log of 0.3.0 as they are.

## [0.3.0] — 2026-11-12

### Added

- **A team that exists elsewhere can be taken in with its join secret.** `brigade-server --data <dir>
  --import-team <name> --join-secret-file <file>`, run with the server stopped, writes the team with the
  reference inside the secret and the secret's hash, and ends. The team's members then join the server with the
  secret file they already have, so a team moves from a Supabase project to a server without a new secret and
  keeps its reference. It is an operator's command: no address of the server takes a team in. Members, sessions
  and waiting messages do not come over.

## [0.2.0] — 2026-11-12

### Added

- **The mail and Slack gateways, inside the server.** A person's mail or Slack message becomes a Brigade message
  to the session they name, and a session's message to the gateway becomes a mail or a post, as with the
  gateways of a Supabase project and with the same words. There is nothing more to install: one JSON file,
  named with `--gateway-config`, says where the Slack app's and the mail provider's secrets are and which teams
  each gateway serves, and the gateways are off without it. Slack reaches the server through the Events API at
  `/gateway/slack`; mail goes through Resend or Postmark, whose webhook reaches `/gateway/mail/resend` or
  `/gateway/mail/postmark`. A gateway joins a team with its join secret and is one more member: the plugin's
  skills find it on the roster as they do today, and no plugin release is needed.
- **A reply to a mail finds its thread by the address it was sent to**, on a server too, whose message ids have a
  form of their own.

### Changed, against the gateways of a Supabase project

- A mail or a Slack message is marked as done only once it is, so one that could not be carried, or whose carry
  a stop cut short, is carried when the provider delivers it again; the functions answer such a second delivery
  as already done.
- The mail gateway never answers a mail that comes from its own inbox, public address or From, so an address
  that forwards to the inbox cannot make it answer itself without end.
- Nothing of a request to the two public addresses is read before it is authenticated, beyond Slack's small
  handshake; at most 64 are worked on at once.

### Known limits

- Slack waits three seconds for an answer: past that, a slash command shows a timeout although the message was
  sent, and Slack's next delivery of an event is told to come again until the first is done.
- A Slack signing secret shorter than 32 characters is refused.
- The gateways' own records are in the data directory and are not yet given to a standby.

## [0.1.0] — 2026-11-12

The first release. Experimental: one instance, no standby, and no mail or Slack gateway.

### Added

- **A backend for Brigade with no Supabase project.** One program that keeps every team on its own machine's
  disk: `brigade-server --data <dir>`. It implements version 1 of the backend REST contract, which the `rest`
  adapter bundled with the Brigade plugin (0.52.0 or later) speaks: `brigade team create --adapter rest --url
  https://<host>:8743 …`.
- **What it keeps.** Every request that changes something becomes one entry in an ordered log, a recorded
  [Cavalry](https://github.com/appshapes/cavalry) feed, and is on the disk before it is answered. The state is
  held in memory and written as a snapshot from time to time, so a start reads little. A server that is killed
  keeps everything it had answered.
- **HTTPS three ways.** `--tls-self-signed` has the server make its own certificate on its first start and keep
  it; `--tls-cert` and `--tls-key` take the operator's own; `--tls-in-front` is for a proxy that ends TLS. The
  start line names the certificate's fingerprint, which a team file can pin so that members trust that
  certificate and no other. Plain HTTP is served on loopback only.
- **Open by design.** Anyone who can reach the server can create a team; `--create-token-file` requires a token
  for that. A team is closed by its join secret, as on any backend.
- **Three ways to install.** Binaries for Linux (x86-64 and ARM, glibc 2.36 or later) and macOS (Apple silicon)
  on this release; `cargo install --git https://github.com/appshapes/brigade --tag server-v0.1.0 brigade-server
  --locked`; and the image `ghcr.io/appshapes/brigade-server:0.1.0`.

### Known limits

- One instance. The server's disk is the only copy of its teams.
- No mail or Slack gateway for a team on a server (added in 0.2.0).
- Nothing bounds the teams a server holds, the members a join secret admits or the sessions a member registers.
- Cavalry is pinned to a commit before its first release and needs 201 MiB of shared memory for its driver
  directory; a container needs `--shm-size=256m`.

The operator's pages are [Project Owner › Server](../docs/project-owner/server/README.md).
