# Members

Four commands, for the creator only. `rotate-secret` runs in a session or a terminal, its new secret to a file;
`revoke-member` and `transfer` run in **your own terminal** and refuse inside a session. Those three
are pass-throughs to the adapter, so what you see is the adapter's own JSON envelope. They work on a team in a
Supabase project and, from 0.53.0, on a team on a Brigade server of 0.4.0 or later, where the creator is called
the team's owner. `move` takes the team from a Supabase project to a server: from plugin 0.56.0 and server 0.7.0
with one command, in a terminal or a session, while the server's operator and your members run nothing.

## If you want to

| If you want to | Go to |
| --- | --- |
| remove a member at once, or bar their return | [Revoke or ban a member](#revoke-or-ban-a-member) |
| replace a leaked secret and evict whoever joined with it | [Rotate a leaked secret](#rotate-a-leaked-secret) |
| hand the team to another member | [Transfer the team](#transfer-the-team) |
| move the team to a Brigade server, from a Supabase project or from another server: one command, and your members run none | [Move the team to a server](#move-the-team-to-a-server) |
| know what the creator's credential is, and what losing it costs | [Know where the authority lives](#know-where-the-authority-lives) |

## Revoke or ban a member

1. `brigade team members --json`; copy `.members[].principal_ref` of the member. Write it down.
2. `brigade team revoke-member --principal <ref>`; add `--ban` to make a rejoin fail exactly as a wrong secret does.

Details: [Revoke or ban a member](revoke-a-member.md).

## Rotate a leaked secret

1. `brigade team rotate-secret --secret-file ~/brigade-<team>.secret`: the old secret dies now, nobody is evicted.
2. Hand the new secret to every member you keep, over a password-grade channel.
3. `brigade team revoke-member --max-version <the result's secret_version minus one>` evicts everyone else.

Details: [Rotate a leaked secret](rotate-the-secret.md).

## Transfer the team

1. `brigade team members --json`; the new creator must be an active member.
2. `brigade team transfer --principal <ref>`.

Details: [Transfer the team](transfer-the-team.md).

## Move the team to a server

1. In a checkout of the team: `brigade team move --to <url> --secret-file <path>`, with the server's URL and
   the file `team create` wrote. In a session, `/brigade:move` asks you for both and runs it.
2. Add `--certificate sha256:<fingerprint>` for a server that made its own certificate, and
   `--create-token-file <path>` for one that asks for a creation token. Answer `y`.
3. Commit and push `.brigade.json` in every repository of the team. Your members run nothing: each one's next
   session follows, and a session that is running follows at its next prompts.
4. From a Supabase project it needs a server of 0.7.0 or later, plugin 0.56.0 or later on your machine and
   0.55.0 or later on each member's (0.56.0 for a running session to follow), and the migration
   `20261114120000_team_move` on the project. The server's operator runs nothing, and gives you its URL.
5. From another Brigade server it needs that server at 0.8.0 or later and plugin 0.57.0 or later on every
   machine, and you own the team there. The server it leaves keeps the team as it is: nothing is ended there.
6. `brigade team move --show` reads the record; `brigade team move --clear` removes it, and undoes nothing
   else: whoever followed stays on the new server.
7. A server older than 0.7.0: its operator takes the team in and opens the move, and you join and record it
   with `brigade team move --ticket-file <path>`.

Details: [Move the team to a server](move-the-team.md).

## Know where the authority lives

1. One member administers a team; your credential directory is the only key to being that member.
2. `team leave` keeps it; `team reset` or a lost credential ends it: for good on Supabase, until the server's
   operator names you again on a Brigade server.

Details: [Know where the authority lives](where-the-authority-lives.md).
