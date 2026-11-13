# Changelog of the Brigade server

The server, `brigade-server`, is released apart from the Brigade plugin, with its own version and its own tags
(`server-v<version>`). The plugin's changes are in [the repository's changelog](../CHANGELOG.md). The contract
between the two is [`docs/backend-rest-v1.md`](../docs/backend-rest-v1.md).

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
