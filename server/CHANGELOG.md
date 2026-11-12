# Changelog of the Brigade server

The server, `brigade-server`, is released apart from the Brigade plugin, with its own version and its own tags
(`server-v<version>`). The plugin's changes are in [the repository's changelog](../CHANGELOG.md). The contract
between the two is [`docs/backend-rest-v1.md`](../docs/backend-rest-v1.md).

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
