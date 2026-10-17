# Apply a release

From your Brigade clone, with `SUPABASE_ACCESS_TOKEN` in the environment as [Deploy the backend](../install/deploy-the-backend.md) shows:

1. **Pull the clone** (`git pull`), so what you deploy is the release's code.
2. **Apply the migrations:** `make backend-install project=<ref>`. Idempotent: it applies the migrations not applied
   yet and leaves everything else alone; `dry=1` first lists them and changes nothing.
3. **Redeploy the mail gateway, if the team has one:** the same `make gateway-install …` command you installed it
   with, with the same values ([Install the mail gateway](../configure/mail-gateway.md), "Running it again").
4. **Redeploy the Slack gateway, if the team has one:** the second pass,
   `SLACK_SECRETS_FILE=<path> make slack-gateway-install project=<ref> team_file=<path>/.brigade.json`. When the
   release notes say the app manifest changed, update the app at api.slack.com first
   ([Install the Slack gateway](../configure/slack-gateway.md)).
5. **Check:** `npx --yes supabase@2.116.0 migration list --project-ref <ref>` shows every migration on both sides;
   `npx --yes supabase@2.116.0 functions list --project-ref <ref>` shows each function's version and when it was
   last deployed.

Members need nothing from this: a plugin version and a migration are independent ([Know which migration adds
what](migrations.md)), and a gateway's functions belong to the project, so every team on the project has the new
ones once you deploy them. The Slack app and the mail provider need nothing either unless the release notes say a
scope, an endpoint or a provider setting changed.
