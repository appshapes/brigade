# Server

A Brigade server is the backend you run yourself: one program on an always-on machine of your own, with no
Supabase project and no cloud account. It keeps every team on that machine's disk, so whoever can read its data
directory can read every message. It is experimental: one instance with no standby. From 0.2.0 it carries the mail
and Slack gateways itself.

## If you want to

| If you want to | Go to |
| --- | --- |
| install the server from a release, by crate or as a container | [Install the server](#install-the-server) |
| start it, choose how it serves HTTPS, run it as a service | [Run the server](#run-the-server) |
| give a Team Admin what they need, and nothing more | [Hand over the values](#hand-over-the-values) |
| back it up, update it, move it, replace its certificate | [Keep the server](#keep-the-server) |
| let people write to the team by email or from Slack | [Open the gateways](#open-the-gateways) |

## Install the server

1. A machine that stays on: Linux (x86-64 or ARM) or macOS (Apple silicon). Members' machines can be anything.
2. Take one of:
   - the release binary `brigade-server_<version>_<os>_<arch>` from the release `server-v<version>`, checked
     against its `checksums.txt`;
   - by crate: `cargo install --git https://github.com/appshapes/brigade --tag server-v<version> brigade-server --locked`;
   - the image: `docker pull ghcr.io/appshapes/brigade-server:<version>`.
3. `brigade-server --version` prints the version.

Details: [Install the server](install-the-server.md).

## Run the server

1. `brigade-server --data /var/lib/brigade-server --listen 0.0.0.0:8743 --tls-self-signed`
2. Its start line names the address and the fingerprint of the certificate it made for itself:
   `listening on https://0.0.0.0:8743 (certificate sha256:…)`.
3. Run it as a service, so that it starts with the machine and is stopped with `SIGTERM`.

Details: [Run the server](run-the-server.md).

## Hand over the values

1. The server's URL as members reach it, `https://<host>:8743`, and the fingerprint from its start line. Both
   are public.
2. The file that holds the creation token, only if you started the server with `--create-token-file`.

Details: [Hand over the values](hand-over-the-values.md).

## Keep the server

1. Everything is in the data directory. To back it up: stop the server, copy the directory, start the server.
2. To update: stop it, replace the binary, start it on the same directory.
3. Losing the directory loses every team in it: there is one instance and no standby yet.

Details: [Keep the server](keep-the-server.md).

## Open the gateways

1. The server must be reachable from the internet at an HTTPS name with a certificate a public authority signed.
2. Write one JSON file that names the Slack app's and the mail provider's secrets by file, and the join secret
   file of each team; start the server with `--gateway-config <file>`.
3. Point the Slack app's two request URLs at `https://<host>/gateway/slack`, and the mail provider's webhook at
   `https://<host>/gateway/mail/resend` or `/gateway/mail/postmark`.

Details: [Open the gateways](gateways.md).
