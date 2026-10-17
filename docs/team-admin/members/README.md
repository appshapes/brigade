# Members

Three commands, for the creator only. `rotate-secret` runs in a session or a terminal, its new secret to a file;
`revoke-member` and `transfer` run in **your own terminal** and refuse inside a session. All three are
pass-throughs to the adapter, so what you see is the adapter's own JSON envelope.

## If you want to

| If you want to | Go to |
| --- | --- |
| remove a member at once, or bar their return | [Revoke or ban a member](#revoke-or-ban-a-member) |
| replace a leaked secret and evict whoever joined with it | [Rotate a leaked secret](#rotate-a-leaked-secret) |
| hand the team to another member | [Transfer the team](#transfer-the-team) |
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

## Know where the authority lives

1. `teams.created_by` is the only administrative authority; your credential directory is the only key to it.
2. `team leave` keeps it; `team reset` or a lost `session.json` ends it, permanently.

Details: [Know where the authority lives](where-the-authority-lives.md).
