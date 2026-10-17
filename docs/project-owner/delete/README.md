# Delete

Deleting the Supabase project deletes every team in it, every message, every membership and every gateway at once,
and nothing restores them. Members' machines keep credentials that no longer open anything.

## If you want to

| If you want to | Go to |
| --- | --- |
| delete the project cleanly | [Delete the project](#delete-the-project) |

## Delete the project

1. Tell each Team Admin to end their team first, so members leave and reset while the backend can still record it.
2. Stop the keep-alive and remove the gateways' external pieces (webhooks, the Slack app).
3. Delete the project in the dashboard (Project Settings → General → Delete project), then revoke the access token.

Details: [Delete the project](delete-the-project.md).
