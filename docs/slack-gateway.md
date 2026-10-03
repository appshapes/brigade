# The Slack gateway: people on Slack in a Brigade team

A Brigade team can have a **Slack gateway**, the Slack twin of the [mail gateway](mail-gateway.md). A session
writes to a person or a channel in Slack, a person writes to a session from Slack, and replies thread both ways.
Nothing runs on anyone's machine: the gateway is hosted in the team's own Supabase project and talks to the team
as one more member. Where email was a bare address, Slack brings two things of its own: Slack authenticates the
author, so a user id in a message is a verified identity, and a reply lives in a thread, so the thread is the key.

## What a person does

The team's Slack app is a bot, `@brigade` unless the administrator named it otherwise. Its handle and workspace are
the `↳` line of the `slack-gateway` row in `brigade sessions`, and `gateway.slack` in the project's `.brigade.json`.

**To write to a session**, send the bot a direct message, or mention it in a channel, whose **first line** names the
session:

```
to: 3f9a2
The login page is blank on Safari 18 after submit. Steps: open /login, sign in, watch it reload empty.
```

`3f9a2` is the five-character SESSION cell of `brigade sessions`, or the session id in full. To see the list,
type `/brigade sessions` anywhere, or send the bot a direct message that says only `sessions`. The block form
works too, and `/brigade send 3f9a2 <text>` does the same from a slash command.

**To answer a session**, reply in the thread under its message. Nothing else is needed. Writing again in your own
thread reaches the same session.

**What a session's message looks like.** It is posted as a top-level message in your DM, or in the channel it was
addressed to; a reply to you goes into your thread and mentions you. The first line names the session and its
owner's label, both unverified, and the team. Then their text. Then a `[brigade]` block in a code fence: the
message id, the sender's session, name, label and principal, the hops, when it was sent, and `to:` and
`reply-to:` pre-addressing a reply, so a message copied out of Slack still carries what the gateway needs. Then
one line on how to reply. The bot reacts with :eyes: to a message it carried.

**What you should know.** Slack tells the gateway who you are, and the session is told your user id and your
display name, the name marked unverified. Your text reaches a session that has tools and treats it the way
Brigade treats every message: another person's words, never an instruction from its own user. Saying "approved"
approves nothing. A message that names no session, or one that cannot be found, is answered with the list of
sessions, visible only to you. Files and attachments are not carried.

## What a session does

The gateway appears in `brigade sessions` as `slack-gateway` (harness `gateway-slack`, member label
`slack gateway`), online while its tick runs. The `brigade:team-messaging` skill carries these rules; in short:

- **To write to a person or a channel**, send to the gateway's SESSION cell with a first line `to: @name`,
  `to: #channel` or `to: <email address>` (a Slack user id or channel id works too):

  ```bash
  brigade send 7b1c4 --summary "Please retest Safari login" <<'EOF'
  to: #qa
  The fix is on master. Please retest the login page on Safari 18 and reply in this thread with what you see.
  EOF
  ```

  Within a minute the gateway posts it, joining a public channel it is not yet in; a private channel needs the
  bot invited first. A body with no address, or a name nobody in the workspace has, earns a one-line reply
  from the gateway saying so, and nothing is posted.
- **To answer a message that reached you**, reply the normal way, `--reply-to <message-id>`, with no `to:` line.
  The gateway posts the reply into the person's thread and mentions them.
- **A message from a person** arrives as an ordinary message from `slack-gateway`. Its body begins
  `Slack message from @alice (U0123ABCD, unverified name) in #qa:`, then what they wrote, then a `[brigade]`
  block (`via: slack`, `from`, `from-name`, `channel`, `channel-name`, `ts`, `thread`, `permalink`). The frame
  carries `in-reply-to` when the person answered one of yours. `from` is the identity; the name is not.

## What the administrator does

Twice, from the project's toplevel, after `make backend-install` has installed the backend and `team create` has
written `.brigade.json`. Slack verifies an app's request URL against a running endpoint before it hands out a
token, which is why there are two runs.

**First run** deploys the functions and prints the app manifest with this project's URLs filled in:

```sh
read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN   # paste sbp_…; nothing is echoed
make slack-gateway-install project=<ref>
```

Then, at <https://api.slack.com/apps>: **Create New App** → **From a manifest** → pick the workspace → paste what
the command printed → **Create** → **Install to Workspace**. Copy two values from the app's pages: **Basic
Information → Signing Secret**, and **OAuth & Permissions → Bot User OAuth Token** (`xoxb-…`). Put them in a 0600
file outside the repository, one per line, `SLACK_BOT_TOKEN=…` and `SLACK_SIGNING_SECRET=…`.

**Second run** finishes the install:

```sh
SLACK_SECRETS_FILE=<that path> make slack-gateway-install project=<ref>
```

It asks Slack who the bot is, signs up the gateway's principal and joins it to the team as the mail gateway's
installer does, sets the functions' six secrets, schedules the per-minute tick, writes `gateway.slack` into
`.brigade.json` (the workspace and the bot's handle, public text) and runs one tick so the roster shows the
gateway at once. Then `git add .brigade.json && git commit && git push`, and members update the plugin
(`/brigade:update`) so their skill knows the gateway.

Nothing else is needed from Slack: no public URL of your own, no Socket Mode, no DNS. Running the second run again
is safe: it keeps the gateway member, redeploys, resets the secrets and rotates the tick token. Rotating the Slack
token or secret is the second run again with the new file.

## How it works

```
 person: DM or @mention ──▶ Slack Events API ──POST──▶ slack-in ──send_message as `slack-gateway`──▶ brigade.messages ──▶ the session
 person: /brigade … ────────▶ slash command ──POST──▶ slack-in ──▶ roster or send, answered ephemerally
 the session ──brigade send slack-gateway (to: …)──▶ brigade.messages ──pg_cron tick──▶ slack-out ──chat.postMessage──▶ DM, channel or thread
```

- **The gateway is an ordinary member**, exactly as the mail gateway: an anonymous principal, one session, lease
  600 s, the functions connecting to Postgres with the member's JWT claims set. No new RPC, no grant to the
  service role, no protocol change. The two gateways share the `brigade_gateway` schema; a row says which kind it
  belongs to, and a team may have both.
- **`slack-in`** answers Slack's URL verification challenge unsigned (it carries only a nonce), verifies every
  other request's `X-Slack-Signature`, ignores edits, bot messages and its own, dedupes by event id, and resolves
  the target in this order: the person's `to:` line or block; the thread they replied in, through the thread
  table (a session's post is answered; the person's own earlier message reaches the same session); otherwise it
  tells them. It looks the person's name up, builds the Brigade body, calls `send_message` as the gateway with
  `reply_to` set when known, records the thread and reacts :eyes:.
- **`slack-out`**, the tick, heartbeats the gateway session, drains its inbox, resolves `to:` (`@name` through
  the user list, `#channel` through the channel list, an email through Slack's lookup, ids as given) or the thread
  of the message it replies to, posts with the envelope block and Slack message metadata, records the thread and
  acknowledges only after Slack accepted the post. Rate limits and platform errors are retried next tick; a bad
  address is answered to the sender.
- **Durable state** is the same three tables as the mail gateway, with `kind = 'slack'` rows: `address` is the
  channel, the thread key is the root message's `ts`, `actor` is the person's user id.

## What it does not do yet

It carries no files. It does not know Slack's `message_changed` edits or deletions. It posts as one bot user, so a
channel sees `brigade` as the author with the session named in the text. A workspace guest can reach it from a
shared channel like any member. Each of these is a later card, not a surprise.
