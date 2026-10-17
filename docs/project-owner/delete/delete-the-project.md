# Delete the project

1. **Warn the teams.** Each Team Admin runs [End a team](../../team-admin/delete/end-a-team.md), and each member
   [Leave the team](../../team-member/leave/leave-the-team.md), while the backend is still there to record the
   leave and the credential revocation. After the deletion a member's `brigade team leave` and `brigade team
   reset` fail at the backend; they then remove `~/.config/brigade/teams/<key>` by hand.
2. **Stop the keep-alive** ([Stop the keep-alive](../remove/stop-the-keep-alive.md)), or its run turns red on the
   health rung from the next day on.
3. **Remove the gateways' pieces outside the project**: the provider's webhook and inbox, the Slack app
  ([Remove a gateway](../remove/remove-a-gateway.md), steps 4 and 5). What is inside the project goes with it.
4. **Delete the project** in the Supabase dashboard: Project Settings → General → Delete project. Every table,
   function, secret and cron job goes with it; there is no undo.
5. **Revoke the personal access token** you used for it (Account Preferences → Access Tokens) if it was scoped to
   this project, and remove `.brigade.json` from the repositories that named it.

A paused project is not a deleted one: Supabase keeps a paused free-plan project restorable for up to a year, and
its teams come back with it.
