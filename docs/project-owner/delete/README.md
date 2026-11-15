# Delete

Deleting the Supabase project deletes every team in it, every message, every membership and every gateway at once,
and nothing restores them. Members' machines keep credentials that no longer open anything.

## If you want to

| If you want to | Go to |
| --- | --- |
| delete the project cleanly | [Delete the project](#delete-the-project) |
| keep a team when the project goes: it moves to a Brigade server first, with one command | [Delete the project](#delete-the-project) |

## Delete the project

1. A team that is to go on moves to a Brigade server first: its Team Admin runs one command,
   `brigade team move --to <url> --secret-file <path>`, and its members run nothing
   ([Move a team from Supabase](../server/README.md#move-a-team-from-supabase)). Wait until no checkout is left
   to follow.
2. Tell the Team Admin of every other team to end it, so members leave and reset while the backend can still
   record it.
3. Stop the keep-alive and remove the gateways' external pieces (webhooks, the Slack app) that no moved team took
   along.
4. Delete the project in the dashboard (Project Settings → General → Delete project), then revoke the access token.

Details: [Delete the project](delete-the-project.md).
