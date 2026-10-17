# Remove a gateway

There is no `make gateway-remove`. A gateway is a team member, functions in the project, a `pg_cron` job, function
secrets, a line in `.brigade.json` and, for mail, a webhook at the provider. Take them away in this order; the
dashboard or the Supabase CLI does what the installers did.

1. **Revoke the member** (the Team Admin, in a terminal): `brigade team members --json`, copy the gateway's
   `principal_ref` (its label is `mail gateway` or `slack gateway`), then `brigade team revoke-member --principal
   <ref>`. The roster drops it and sessions can no longer reach it.
2. **Unschedule the tick.** In the SQL editor: `select cron.unschedule('brigade_gateway_tick');` for mail,
   `select cron.unschedule('brigade_slack_gateway_tick');` for Slack. Until this runs, the tick keeps calling a
   function every minute for a member that no longer exists.
3. **Delete the functions.** `npx --yes supabase@2.116.0 functions delete <name> --project-ref <ref>` for `mail-in`,
   `mail-out` and the connector (`mail-connector-resend` or `mail-connector-postmark`), or for `slack-in` and
   `slack-out`. Delete their secrets under Edge Functions → Secrets.
4. **At the provider.** Mail: delete the webhook and the inbox at Resend or Postmark, and any routing rule you added
   for an address of your own. Slack: at api.slack.com, delete the app, or uninstall it from the workspace.
5. **The team file.** Remove `email` or `slack` from the `gateway` member of the project's `.brigade.json` (the
   member itself when both are gone) and commit; sessions stop announcing the gateway at their next start.
6. **The rows** in the `brigade_gateway` schema (`gateways`, `threads`, `received`) are inert once the functions are
   gone; delete the team's row from `brigade_gateway.gateways` if you want the schema clean.

Several teams share the connector and the tick: remove a team's gateway member and its `gateways` row, and leave
the functions and the job while another team still uses them.
