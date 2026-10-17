# Remove

Two things an owner takes away: a keep-alive, which is a pair of repository variables, and a gateway, which is a
member, two or three functions and a cron job. Neither has an uninstaller of its own.

## If you want to

| If you want to | Go to |
| --- | --- |
| stop keeping a project awake | [Stop the keep-alive](#stop-the-keep-alive) |
| remove a mail or Slack gateway from a team | [Remove a gateway](#remove-a-gateway) |

## Stop the keep-alive

1. `gh variable delete BRIGADE_SUPABASE_URL` and `gh variable delete BRIGADE_SUPABASE_PUBLISHABLE_KEY` (or the pair
   named for the project).
2. Both unset is a green no-op; one left behind fails the run on purpose.

Details: [Stop the keep-alive](stop-the-keep-alive.md).

## Remove a gateway

1. The Team Admin revokes the gateway's member; you unschedule its cron job and delete its functions.
2. Remove the `gateway` member from `.brigade.json` and the provider's webhook or the Slack app.

Details: [Remove a gateway](remove-a-gateway.md).
