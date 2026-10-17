# Delete

Brigade has no team-delete verb. A team ends when nobody can join it and nobody is left in it; the backend's
retention removes what remains.

## If you want to

| If you want to | Go to |
| --- | --- |
| end a team so that nobody can use it again | [End a team](#end-a-team) |

## End a team

1. `brigade team rotate-secret --secret-file <path>` to a file you then delete: no current secret exists outside it.
2. `brigade team revoke-member --max-version <the result's secret_version minus one>` evicts every other member.
3. `brigade team leave`, then `brigade team reset` on your own machine; remove `.brigade.json` from the repositories.

Details: [End a team](end-a-team.md).
