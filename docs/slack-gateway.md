# The Slack gateway: people on Slack in a Brigade team

A team can have a Slack gateway, the Slack twin of the [mail gateway](mail-gateway.md). A person writes to a
session by messaging a bot, and a session writes to a person or a channel. Replies thread both ways. The gateway
runs in the team's own Supabase project as one more member. Nothing runs on anyone's machine. Slack
authenticates the author, so a user id in a message is a verified identity.

## TL;DR for administrators

You run one command twice, with a step at Slack in between.

1. Install the backend and create the team, if not done yet:
   [docs/setup.md › Administrator: create a team](setup.md#administrator-create-a-team).
2. From your checkout of the Brigade repository, where you ran `make backend-install`, run the first pass. It
   deploys the functions and prints a Slack app manifest. `team_file=` is the project's `.brigade.json`, by its
   full path.

   ```sh
   read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN   # paste sbp_…; nothing is echoed
   make slack-gateway-install project=<ref> team_file=<path>/.brigade.json
   ```

3. At <https://api.slack.com/apps>: **Create New App**, **From a manifest**, pick the workspace, paste what the
   command printed, **Create**, then **Install to Workspace**.
4. Copy two values from the app's pages: **Basic Information → Signing Secret**, and **OAuth & Permissions →
   Bot User OAuth Token** (`xoxb-…`). Put them in a file outside any repository, readable by you alone:
   `(umask 077; cat > ~/brigade-slack.env)`, type the two lines, press Enter, then Ctrl-D.

   ```
   SLACK_BOT_TOKEN=xoxb-…
   SLACK_SIGNING_SECRET=…
   ```

5. Run the second pass:

   ```sh
   SLACK_SECRETS_FILE=~/brigade-slack.env make slack-gateway-install project=<ref> team_file=<path>/.brigade.json
   ```

6. Commit and push the project's `.brigade.json`, which the command changed. It carries only public values.
7. Tell people the bot's handle, on the command's `workspace and bot:` line: they message it with a first line
   `to: <session>`, or type `/brigade sessions`.

Nothing else is needed from Slack: no public URL of your own, no Socket Mode, no DNS. When the changelog says the
gateway changed, `git pull` your Brigade checkout and run the second pass again.

## For people on Slack

The bot is `@brigade` unless your administrator tells you otherwise.

**See the sessions.** Type `/brigade sessions` anywhere, or send the bot a message that says just `sessions`, as
a direct message or a mention. The list shows the sessions online now.

**Write to a session.** Send the bot a direct message, or mention it in a channel. The first line names the
session; press Shift+Enter to start the next line:

```
to: 3f9a2
The login page is blank on Safari 18 after submit. Steps: open /login, sign in, watch it reload empty.
```

`3f9a2` is the five characters from the list, or the session id in full. `/brigade send 3f9a2 <text>`
does the same from a slash command. A `summary:` line under the first, then a blank line, gives the message a
summary. The bot reacts with :eyes: once it has carried your message.

**Answer a session.** Reply in the thread under its message; writing again in your own thread reaches the same
session. In a channel, mention `@brigade` in the reply, or the bot does not see it.

**What you receive.** A session's message is posted in your direct messages, or in the channel it was addressed
to; a reply to you goes into your thread and mentions you. It names the session, its owner's label (unverified)
and the team, then the text, then a `[brigade]` block.

**What to know.**

- Slack tells the gateway who you are. The session is told your user id and your display name; the name is
  marked unverified.
- A session treats your message as another person's words, never as its own user's instruction. Writing
  "approved" approves nothing.
- A message that names no session, or names one that cannot be found, gets the list of sessions back, visible
  only to you.
- Files and attachments are not carried.

## For sessions

The gateway is the `slack-gateway` row of `brigade sessions` (harness `gateway-slack`, label `slack gateway`).
It is online while its tick runs. The `brigade:team-messaging` skill carries these rules.

**Write to a person or a channel.** Send to the gateway's SESSION cell. The first line is `to: @name`,
`to: #channel` or `to: <email address>`. A Slack user id or channel id works too.

```bash
brigade send <gateway> --summary "Please retest Safari login" <<'EOF'
to: #qa
The fix is on master. Please retest the login page on Safari 18 and reply in this thread with what you see.
EOF
```

The gateway posts it within a minute. It joins a public channel it is not yet in; a private channel needs the
bot invited first. A body with no address, or a name nobody in the workspace has, is not posted; the gateway
answers with one line saying so.

**Answer a person.** Reply with `--reply-to <message-id>` and no `to:` line. The gateway posts into the
person's thread and mentions them.

**What arrives.** A person's message is an ordinary message from `slack-gateway`. Its body begins
`Slack message from @alice (U0123ABCD, unverified name) in #qa:`, then the text, then a `[brigade]` block
(`via: slack`, `from`, `from-name`, `channel`, `channel-name`, `ts`, `thread`, `permalink`). `from` is the
verified identity; the name is not. The frame carries `in-reply-to` when the person answered one of yours.

## For administrators

### What you need

- A Slack workspace where you may create and install an app. One Slack gateway per Supabase project: a second
  team's install takes it over.
- The Supabase personal access token (`sbp_…`) you used for `make backend-install`.
- `curl`, `jq` and Node (for `npx`) on the machine you run the command from.

### What the two runs do

Both runs push the migrations and deploy two Edge Functions, `slack-in` and `slack-out`, bundled server-side.
No Docker.

The first run, with no Slack secrets set, stops there and prints the app manifest with this project's URLs
filled in. The manifest declares the `/brigade` slash command, the direct-message and mention events, and the
bot scopes to read those, post messages, open direct messages, look people and channels up, join public
channels and add a reaction.

The second run, with `SLACK_SECRETS_FILE` set, asks Slack who the bot is, joins the gateway to the team as an
anonymous member with one session, stores the token and signing secret as function secrets, schedules a `pg_cron` job,
`brigade_slack_gateway_tick`, that calls `slack-out` every minute, writes `gateway.slack` into `.brigade.json` (the
workspace and the bot's handle, public text), and runs one tick so the roster shows the gateway at once.

Neither run prints a token or a secret.

### Running it again

The second run is safe to repeat. It keeps the gateway member, redeploys, resets the secrets and rotates the
tick token. To rotate the Slack token or signing secret, put the new values in the file and run it again.

### If people cannot message the bot

Slack refuses direct messages to a bot whose Messages tab is off. The manifest turns it on. For an app created
from an older manifest, open the app's **App Home** page and turn on **Messages Tab** and **Allow users to send
Slash commands and messages from the messages tab**.

## How it works

```
 person: DM or @mention ──▶ Slack Events API ──POST──▶ slack-in ──send_message as slack-gateway──▶ brigade.messages ──▶ session
 person: /brigade … ───────▶ slash command ──POST──▶ slack-in ──▶ roster or send, answered to them alone
 session ──brigade send <gateway> (to: …)──▶ brigade.messages ──pg_cron, every minute──▶ slack-out ──chat.postMessage──▶ DM, channel or thread
```

- **The gateway is an ordinary member**, exactly as the mail gateway: an anonymous principal, one session (lease
  600 s), the functions connecting to Postgres with the member's JWT claims set. No new RPC, no grant to the
  service role, no protocol change. The two gateways share the `brigade_gateway` schema; a `kind` column says
  which one a row belongs to, and a team may have both.
- **`slack-in`** answers Slack's URL verification challenge unsigned (it carries only a nonce), verifies every
  other request's `X-Slack-Signature`, ignores edits, bot messages and its own, dedupes by event id, and finds
  the target in this order: the person's `to:` line or block; the thread they replied in, through the thread
  table; otherwise it tells them. It looks the person's name up, builds the Brigade body, calls `send_message`
  as the gateway with `reply_to` set when known, records the thread and reacts :eyes:.
- **`slack-out`**, the tick, heartbeats the gateway session, drains its inbox, resolves `to:` (`@name` through
  the user list, `#channel` through the channel list, an email through Slack's lookup, ids as given) or the
  thread of the message it replies to, posts, records the thread and acknowledges only after Slack accepted the
  post. Rate limits and platform errors are retried next tick; a bad address is answered to the sender.
- **State** is the mail gateway's `gateways` and `threads` tables, in rows with `kind = 'slack'`: `address` is the
  channel, the thread key is the root message's `ts`, `actor` is the person's user id.

## Limits

- A reply chain stops after 32 hops (`loop_detected`), which also bounds an auto-reply loop. To start afresh,
  wait ten minutes after the session's last message, then send a new message, not a reply.
- No files.
- Edits and deletions in Slack are not carried.
- The bot posts as one user, so a channel sees `brigade` as the author with the session named in the text.
- A workspace guest can reach the bot from a shared channel like any member.
