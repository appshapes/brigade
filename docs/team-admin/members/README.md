# Members

Four commands, for the creator only. `rotate-secret` runs in a session or a terminal, its new secret to a file;
`revoke-member`, `transfer` and `move` run in **your own terminal** and refuse inside a session. The first three
are pass-throughs to the adapter, so what you see is the adapter's own JSON envelope. They work on a team in a
Supabase project and, from 0.53.0, on a team on a Brigade server of 0.4.0 or later, where the creator is called
the team's owner. `move`, from 0.55.0, records that the team went from a Supabase project to a server.

## If you want to

| If you want to | Go to |
| --- | --- |
| remove a member at once, or bar their return | [Revoke or ban a member](#revoke-or-ban-a-member) |
| replace a leaked secret and evict whoever joined with it | [Rotate a leaked secret](#rotate-a-leaked-secret) |
| hand the team to another member | [Transfer the team](#transfer-the-team) |
| move the team to a Brigade server without asking members to join again | [Move the team to a server](#move-the-team-to-a-server) |
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

1. In one checkout, point `.brigade.json` at the server and join it: `/brigade:join ~/brigade-<team>.secret`.
   You join first, so you own the team there.
2. In your terminal: `brigade team move --ticket-file ~/<team>.ticket`, with the file the server's operator
   gave you. Answer `y`.
3. Commit and push `.brigade.json` in every repository of the team. Each member's next session follows.
4. `brigade team move --show` reads the record; `brigade team move --clear` removes it.

Details: [Move the team to a server](move-the-team.md).

## Know where the authority lives

1. One member administers a team; your credential directory is the only key to being that member.
2. `team leave` keeps it; `team reset` or a lost credential ends it: for good on Supabase, until the server's
   operator names you again on a Brigade server.

Details: [Know where the authority lives](where-the-authority-lives.md).
