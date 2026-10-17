# Install the Slack gateway

The Slack twin of the mail gateway. A person writes to a session by messaging a bot, and a session writes to a
person or a channel; replies thread both ways. Slack authenticates the author, so a user id in a message is a
verified identity. You run one command twice, with a step at Slack in between.

## What you need

A Slack workspace where you may create and install an app (one Slack gateway per Supabase project: a second
team's install takes it over); the personal access token you used for `make backend-install`; `curl`, `jq` and
Node. No public URL of your own, no Socket Mode, no DNS.

## The two passes

1. **First pass**, from your Brigade clone:

   ```sh
   read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN   # paste sbp_…; nothing is echoed
   make slack-gateway-install project=<ref> team_file=<path>/.brigade.json
   ```

   It pushes the migrations, deploys two Edge Functions (`slack-in`, `slack-out`), stops, and prints a Slack app
   manifest with this project's URLs filled in.
2. **At <https://api.slack.com/apps>:** Create New App → From a manifest → pick the workspace → paste what the
   command printed → Create → Install to Workspace.
3. **Copy two values** from the app's pages, Basic Information → Signing Secret and OAuth & Permissions → Bot User
   OAuth Token (`xoxb-…`), into a file outside any repository, readable by you alone
   (`(umask 077; cat > ~/brigade-slack.env)`, type the two lines, Enter, Ctrl-D):

   ```
   SLACK_BOT_TOKEN=xoxb-…
   SLACK_SIGNING_SECRET=…
   ```

4. **Second pass:**

   ```sh
   SLACK_SECRETS_FILE=~/brigade-slack.env make slack-gateway-install project=<ref> team_file=<path>/.brigade.json
   ```

   It asks Slack who the bot is, checks that the token carries every scope the manifest asks for (and stops before
   deploying when one is missing), joins the gateway to the team as a member, stores the token and signing secret
   as function secrets, schedules the `pg_cron` job `brigade_slack_gateway_tick` every minute, writes
   `gateway.slack` into `.brigade.json` (workspace and bot handle, public text) and runs one tick so the roster
   shows the gateway at once. Neither pass prints a token or a secret.
5. **Commit and push** the project's `.brigade.json`; tell people the bot's handle on the `workspace and bot:` line.
   What they do and see: [Write to people on email or Slack](../../team-member/use/people-on-email-and-slack.md).

## The scopes, said plainly

The manifest declares the `/brigade` slash command, the direct-message, mention and channel-message events, and the
bot scopes to read those, post, open direct messages, look people and channels up, join public channels and add a
reaction. The channel-message events (`channels:history`, `groups:history`) are how a reply in a thread reaches the
gateway without a mention; with them the bot can read every message in the channels it is in. The code carries
only a reply inside a thread the gateway is part of and drops the rest unread; the permission is still the wider
one, and whoever approves the app should know it.

## Running it again

The second pass is safe to repeat: it keeps the gateway member, redeploys, resets the secrets and rotates the tick
token. To rotate the Slack token or signing secret, put the new values in the file and run it again. After a
release whose notes say the manifest changed, update the app first (api.slack.com → the app → App Manifest →
replace with what the first pass prints → Save Changes → Install App → Reinstall to Workspace); the second pass
refuses to deploy until the token carries the new scopes, and until then a channel post's footer keeps asking
people to mention the bot.

If people cannot message the bot: Slack refuses direct messages to a bot whose Messages tab is off. The manifest
turns it on; for an app created from an older manifest, open App Home and turn on **Messages Tab** and **Allow
users to send Slash commands and messages from the messages tab**.

## How it works

```
 person: DM, @mention or a reply in a Brigade thread ──▶ Slack Events API ──POST──▶ slack-in ──send_message as slack-gateway──▶ brigade.messages ──▶ session
 person: /brigade … ───────▶ slash command ──POST──▶ slack-in ──▶ roster or send, answered to them alone
 session ──brigade send <gateway> (to: …)──▶ brigade.messages ──pg_cron, every minute──▶ slack-out ──chat.postMessage──▶ DM, channel or thread
```

An ordinary member, exactly as the mail gateway, sharing the `brigade_gateway` schema with a `kind` column; a team
may have both. `slack-in` answers the URL verification challenge unsigned (a nonce only), verifies every other
request's signature, ignores edits, bot messages and its own, dedupes by event id, and finds the target in the
`to:` line or the thread table; a channel message that mentioned nobody is carried only when it is a reply in a
thread the table knows. `slack-out`, the tick, drains the gateway's inbox, resolves `to:` (`@name`, `#channel`, an
email, an id), turns `@handle` into a mention, posts, records the thread and acknowledges only after Slack accepted
the post.

## Limits

A reply chain stops after 32 hops. No files; edits and deletions in Slack are not carried; the bot posts as one
user, so a channel sees `brigade` as the author with the session named in the text; a workspace guest can reach the
bot from a shared channel like any member. What a leaked bot token or signing secret lets its holder do:
[docs/security.md, section 14](../../security.md#14-the-mail-and-slack-gateways).
