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
| back it up, restore it, update it, move it, watch it, replace its certificate | [Keep the server](#keep-the-server) |
| let people write to the team by email or from Slack | [Open the gateways](#open-the-gateways) |
| move a team here from a Supabase project, keeping its join secret | [Take in a team that exists](#take-in-a-team-that-exists) |
| say who administers a team, name a new owner, or take a team back | [Name a team's owner](#name-a-teams-owner) |

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
3. Losing the directory with no copy loses every team in it: there is one instance and no standby yet. With a
   copy the teams return as of the copy, and the files outside the directory are placed again.
4. A monitor asks `POST /v1/info`, which needs no credential.

Details: [Keep the server](keep-the-server.md).

## Open the gateways

1. The server must be reachable from the internet at an HTTPS name with a certificate a public authority signed.
2. Write one JSON file that names the Slack app's and the mail provider's secrets by file, and the join secret
   file of each team; start the server with `--gateway-config <file>`.
3. Point the Slack app's two request URLs at `https://<host>/gateway/slack`, and the mail provider's webhook at
   `https://<host>/gateway/mail/resend` or `/gateway/mail/postmark`.

Details: [Open the gateways](gateways.md).

## Take in a team that exists

1. Stop the server, then
   `brigade-server --data <dir> --import-team <name> --join-secret-file <file>`, then start it.
2. In each repository of the team, in `.brigade.json`: `adapter` becomes `rest`, `url` the server's, and
   `publishable_key` goes; `team_ref` stays.
3. Each member updates the plugin to 0.53.1 or later, pulls, and joins with the secret file they already have,
   once in each checkout that was joined to the team on the old backend.

Details: [Take in a team that exists](take-in-a-team.md).

## Name a team's owner

1. A team's owner is whoever created it on the server. A team that was taken in is owned by the first member to
   join it there.
2. To name another: `brigade team members --json` on a member's machine gives the new owner's `principal_ref`.
3. Stop the server, then
   `brigade-server --data <dir> --set-owner <team_ref> --principal <principal_ref>`, then start it.
4. If whoever holds the team has locked the others out: `--take-back <team_ref> --secret-file <path>` in place
   of step 3 gives the team a new secret in that file, ends every membership and leaves it to the first member
   who joins with the file.

Details: [Name a team's owner](name-the-owner.md).
