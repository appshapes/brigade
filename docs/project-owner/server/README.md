# Server

A Brigade server is the backend you run yourself: one program on an always-on machine, your own or a cloud
instance, with no Supabase project. It keeps every team on that machine's disk, so whoever can read its data
directory can read every message. It is experimental: one instance with no standby. From 0.2.0 it carries the mail
and Slack gateways itself. From 0.7.0 a team in a Supabase project moves to it with one command, which its Team
Admin runs: you run nothing. From 0.8.0 a team moves from one server to another the same way.

## If you want to

| If you want to | Go to |
| --- | --- |
| install the server from a release, by crate or as a container | [Install the server](#install-the-server) |
| start it, choose how it serves HTTPS, run it as a service | [Run the server](#run-the-server) |
| start from a setup that runs: a machine of your own behind a router, or a cloud instance | [Sample setups](#sample-setups) |
| give a Team Admin what they need, and nothing more | [Hand over the values](#hand-over-the-values) |
| back it up, restore it, update it, move it, watch it, replace its certificate | [Keep the server](#keep-the-server) |
| let people write to the team by email or from Slack | [Open the gateways](#open-the-gateways) |
| have a team move here from a Supabase project: one command, which its Team Admin runs, and none of yours | [Move a team from Supabase](#move-a-team-from-supabase) |
| have a team move from one server to another, or change a server's URL without a join on every machine | [Move a team to another server](#move-a-team-to-another-server) |
| take a team in yourself: on a server older than 0.7.0, or before its Team Admin acts | [Take in a team that exists](#take-in-a-team-that-exists) |
| open, replace or close a team's move yourself | [Let a team's members follow it](#let-a-teams-members-follow-it) |
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

## Sample setups

1. Both run the server on loopback, `--listen 127.0.0.1:8750`, behind Caddy on the same machine. Caddy holds a
   Let's Encrypt certificate for the server's name, so members need no fingerprint and the gateways can be opened.
2. A machine of your own behind a router: two user units, the router forwards port 443 to Caddy, and a second
   machine pulls the nightly copy.
3. A cloud instance: the data on a volume of its own, the secret files fetched into memory before each start,
   the nightly copy put in a bucket, and a monitor outside the instance.

Details: [Sample setups](sample-setups.md).

## Hand over the values

1. The server's URL as members reach it, `https://<host>:8743`, and the fingerprint from its start line. Both
   are public.
2. The file that holds the creation token, only if you started the server with `--create-token-file`.
3. With these a Team Admin creates a team here, or moves one here from a Supabase project with one command. You
   run nothing more.

Details: [Hand over the values](hand-over-the-values.md).

## Keep the server

1. Everything is in the data directory. To back it up: stop the server, copy the directory, start the server.
2. To update: stop it, replace the binary, start it on the same directory. To move it to another machine at the
   same URL: copy the directory. To another URL: a fresh server there, and each team moved with one command.
3. Losing the directory with no copy loses every team in it: there is one instance and no standby yet. With a
   copy the teams return as of the copy, and the files outside the directory are placed again.
4. A monitor asks `POST /v1/info`, which needs no credential.
5. Pull the copies to a second machine and let a failed pull post to a Slack webhook from there: nobody else
   tells you that a copy failed.

Details: [Keep the server](keep-the-server.md).

## Open the gateways

1. The server must be reachable from the internet at an HTTPS name with a certificate a public authority signed.
2. Write one JSON file that names the Slack app's and the mail provider's secrets by file, and the join secret
   file of each team; start the server with `--gateway-config <file>`.
3. Point the Slack app's two request URLs at `https://<host>/gateway/slack`, and the mail provider's webhook at
   `https://<host>/gateway/mail/resend` or `/gateway/mail/postmark`.

Details: [Open the gateways](gateways.md).

## Move a team from Supabase

1. The Team Admin runs one command in a checkout of the team, or asks a session to (`/brigade:move`):
   `brigade team move --to <url> --secret-file <path>`
2. You run nothing. You give the Team Admin the server's URL, its certificate's fingerprint when the server made
   its own (`--certificate`), and the creation token's file when the server asks for one (`--create-token-file`).
3. The Team Admin commits and pushes `.brigade.json` in every repository of the team. The members run nothing:
   each one's next session follows, and a session that is running follows at its next prompts.
4. It needs the server at 0.7.0 or later, the migration `20261114120000_team_move` on the Supabase project,
   plugin 0.56.0 or later on the Team Admin's machine and 0.55.0 or later on each member's: 0.56.0 for a running
   session to follow.
5. After: open the gateways on the server and take them out of the Supabase project. When no checkout is left to
   follow, close the move, and the Team Admin ends the team on the old backend, from a checkout that still names
   it.
6. A server older than 0.7.0, from 0.5.0: update it, or take the team in and open its move yourself with the
   server stopped. The Team Admin then joins and records the move with `brigade team move --ticket-file <path>`.

Details: [Move a team from Supabase](move-from-supabase.md).

## Move a team to another server

1. The Team Admin, who owns the team on the server it leaves, runs one command in a checkout of the team, or
   asks a session to (`/brigade:move`): `brigade team move --to <url> --secret-file <path>`
2. Neither operator runs a command. The operator of the new server gives the Team Admin its URL, its
   certificate's fingerprint when it made its own, and the creation token's file when it asks for one.
3. The server the team leaves is 0.8.0 or later and stays up until every member has followed: it holds the
   record they follow on, and it keeps the team as it is. The server the team goes to is 0.7.0 or later and a
   fresh one, never a copy of the old one's data directory at a new URL.
4. The Team Admin commits and pushes `.brigade.json`. Each member's next session follows, on plugin 0.57.0 or
   later, and a session that is running follows at its next prompts.
5. After: open the gateways on the new server, point the Slack app and the mail provider at it, and take the
   team out of the old server's gateway configuration.
6. Do not go back to 0.7.0 on the old server's data directory while it records a move.

Details: [Move a team to another server](move-to-another-server.md).

## Take in a team that exists

1. From server 0.7.0 you run none of this: the Team Admin's one command,
   `brigade team move --to <url> --secret-file <path>`, has the running server take the team in, with the Team
   Admin as its owner ([Move a team from Supabase](#move-a-team-from-supabase)).
2. To do it yourself, on a server older than 0.7.0 or for a team that is to be here before its Team Admin acts:
   stop the server, then `brigade-server --data <dir> --import-team <name> --join-secret-file <file>`, then
   start it.
3. In each repository of the team, in `.brigade.json`: `adapter` becomes `rest`, `url` the server's, and
   `publishable_key` goes; `team_ref` stays. The Team Admin joins first, and so owns the team.
4. Each member updates the plugin to 0.53.1 or later, pulls, and joins with the secret file they already have,
   once in each checkout that was joined to the team on the old backend. With a move opened and recorded they
   do nothing instead ([Let a team's members follow it](#let-a-teams-members-follow-it)).

Details: [Take in a team that exists](take-in-a-team.md).

## Let a team's members follow it

1. From server 0.7.0 you run none of this: the Team Admin's one command,
   `brigade team move --to <url> --secret-file <path>`, opens the move by request, and no ticket file changes
   hands ([Move a team from Supabase](#move-a-team-from-supabase)).
2. To do it yourself, on a server from 0.5.0: stop the server, then
   `brigade-server --data <dir> --open-move <team_ref> --ticket-file <file>`, then start it.
3. Give the ticket's file to the Team Admin, who joins the server first and then records the move:
   `brigade team move --ticket-file <path>`.
4. Once the changed `.brigade.json` is pushed, each member's next session follows; nobody runs a join.
5. `--close-move <team_ref>`, between a stop and a start, ends a move however it was opened. So does the
   ticket's end, after 30 days unless another number was chosen.

Details: [Let a team's members follow it](open-a-move.md).

## Name a team's owner

1. A team's owner is whoever created it on the server, or moved it here: the Team Admin whose
   `brigade team move --to` took it in. A team you took in yourself is owned by the first member to join it.
2. To name another: `brigade team members --json` on a member's machine gives the new owner's `principal_ref`.
3. Stop the server, then
   `brigade-server --data <dir> --set-owner <team_ref> --principal <principal_ref>`, then start it.
4. If whoever holds the team has locked the others out: `--take-back <team_ref> --secret-file <path>` in place
   of step 3 gives the team a new secret in that file, ends every membership and leaves it to the first member
   who joins with the file. From 0.8.0 it also clears the record of where the team went, if its owner made one.

Details: [Name a team's owner](name-the-owner.md).
