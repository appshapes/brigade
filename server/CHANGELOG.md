# Changelog of the Brigade server

The server, `brigade-server`, is released apart from the Brigade plugin, with its own version and its own tags
(`server-v<version>`). The plugin's changes are in [the repository's changelog](../CHANGELOG.md). The contract
between the two is [`docs/backend-rest-v1.md`](../docs/backend-rest-v1.md).

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
- No mail or Slack gateway for a team on a server.
- Nothing bounds the teams a server holds, the members a join secret admits or the sessions a member registers.
- Cavalry is pinned to a commit before its first release and needs 201 MiB of shared memory for its driver
  directory; a container needs `--shm-size=256m`.

The operator's pages are [Project Owner › Server](../docs/project-owner/server/README.md).
