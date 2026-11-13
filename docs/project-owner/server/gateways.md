# Open the gateways

The mail and Slack gateways let people without a session take part: a person's mail or Slack message becomes a
Brigade message to the session they name, and a session's message to the gateway becomes a mail or a post. On a
Brigade server they are part of `brigade-server` itself, from 0.2.0: there is nothing more to install, and they
stay off until you name a configuration file. What people and sessions do is the same as with a Supabase project
([People on email and Slack](../../team-member/use/people-on-email-and-slack.md)).

## What you need

| | |
| --- | --- |
| A public name | Slack and a mail provider call the server, so it must be reachable from the internet at an HTTPS name whose certificate a public authority signed: `--tls-cert` and `--tls-key` with such a certificate, or a proxy in front that holds one. The certificate a server makes for itself serves members and neither of these. |
| The team's join secret | The file `team create` wrote. A gateway joins the team with it, as one more member. |
| For Slack | A Slack app in the workspace: its bot token and its signing secret. |
| For mail | A Resend account or a Postmark server: its API key, a secret for its webhook, a receiving address and a sending address. |

Each secret goes in a file of its own that only the server's user can read. The configuration names the files and
never holds a secret, and no secret is ever an argument.

## The configuration file

```json
{
  "slack": {
    "bot_token_file": "/etc/brigade-server/slack-bot-token",
    "signing_secret_file": "/etc/brigade-server/slack-signing-secret",
    "bot_handle": "brigade",
    "workspace": "example.slack.com",
    "teams": [{"join_secret_file": "/etc/brigade-server/ops.secret"}]
  },
  "mail": {
    "provider": "resend",
    "api_key_file": "/etc/brigade-server/resend-key",
    "webhook_secret_file": "/etc/brigade-server/resend-webhook-secret",
    "teams": [{
      "join_secret_file": "/etc/brigade-server/ops.secret",
      "inbox": "ops@in.example.com",
      "public_address": "ops@example.com",
      "from": "Ops <ops@example.com>",
      "allowed_senders": ["@example.com"]
    }]
  }
}
```

Start the server with `--gateway-config <file>` beside its other options. It reads the file when it starts, so a
change takes a restart. Leave `slack` or `mail` out to have only the other. A member the server does not know is
refused, so a misspelt name never leaves a gateway half configured.

| Member | |
| --- | --- |
| `bot_handle`, `workspace` | what the roster's line under the gateway tells people: the bot's handle and the workspace's host |
| `teams` | one entry for each team the gateway serves; one Slack app, or one mail account, can serve several |
| `provider` | `resend` or `postmark` |
| `inbox` | the address the provider receives the team's mail at |
| `public_address` | the address people are told to write to; the same as `inbox` unless you forward one to the other |
| `from` | the From of every mail the gateway sends for the team. The gateway never answers a mail that comes from its own inbox, public address or From, so that an address which forwards to the inbox cannot make it answer itself |
| `allowed_senders` | who may write in: addresses, or `@domain` for everyone at a domain. Leave it out and anyone who knows the address can write to any session |

## Slack

1. Start the server with the Slack gateway configured. Slack checks the address when you save it, and the server
   answers that check only once the gateway is on. Until the app exists and you have its signing secret, put a
   random value in that file, `openssl rand -hex 32 > <file>`: nobody can then sign a request the server would
   take, and the server refuses a signing secret shorter than Slack's own. The bot token's file can hold any text
   until then; nothing is posted to Slack without the real one.
2. At <https://api.slack.com/apps>: Create New App, From a manifest, pick the workspace, paste the manifest below
   with your server's name in both `url` lines, Create, then Install to Workspace.
3. Copy Basic Information › Signing Secret and OAuth & Permissions › Bot User OAuth Token into the two files, and
   restart the server.

```yaml
display_information:
  name: Brigade
  description: Messages between this workspace and the team's Claude Code sessions
  background_color: "#1f2a44"
features:
  app_home:
    messages_tab_enabled: true
    messages_tab_read_only_enabled: false
  bot_user:
    display_name: brigade
    always_online: false
  slash_commands:
    - command: /brigade
      url: https://<host>/gateway/slack
      description: Brigade sessions and messages
      usage_hint: sessions | send <session> <text> | help
      should_escape: false
oauth_config:
  scopes:
    bot:
      - app_mentions:read
      - channels:history
      - channels:join
      - channels:read
      - chat:write
      - commands
      - groups:history
      - groups:read
      - im:history
      - im:read
      - im:write
      - reactions:write
      - users:read
      - users:read.email
settings:
  event_subscriptions:
    request_url: https://<host>/gateway/slack
    bot_events:
      - app_mention
      - message.channels
      - message.groups
      - message.im
  interactivity:
    is_enabled: false
  org_deploy_enabled: false
  socket_mode_enabled: false
  token_rotation_enabled: false
```

It is the manifest of the Slack gateway of a Supabase project with the server's address in it, and
[what its scopes allow](../configure/slack-gateway.md#the-scopes-said-plainly) is said there. An app that already
serves a Supabase project is moved by changing the two `url` lines in its manifest; its token and signing secret
stay the same.

## Mail

- **Resend.** Add a webhook for the event `email.received` with the address `https://<host>/gateway/mail/resend`.
  Its signing secret, which starts with `whsec_`, goes in `webhook_secret_file`, and a full-access API key in
  `api_key_file`. `inbox` is the receiving address Resend gave you.
- **Postmark.** Choose a secret, put it in `webhook_secret_file`, and set the inbound stream's webhook to
  `https://brigade:<secret>@<host>/gateway/mail/postmark`. The server token goes in `api_key_file`, and `inbox` is
  the stream's inbound address.

## Check it worked

The server says when each gateway has joined its team. `brigade sessions` in a checkout of the team then lists
`slack-gateway` and `mail-gateway`, each with a line that says how to write to it. A direct message `sessions` to
the bot is answered with the roster, and so is a mail whose only text is `sessions`.

## After the join secret is rotated

A gateway is a member that joined with the secret in `join_secret_file`. When the team's owner rotates the secret
and then evicts everyone who joined with the old one (`brigade team revoke-member --max-version`,
[Rotate a leaked secret](../../team-admin/members/rotate-the-secret.md)), the gateways are evicted with them.

1. The owner sends you the new secret file, as they send it to every member they keep.
2. Replace the file `join_secret_file` names, with the same owner and mode.
3. The gateway joins again with it by itself, within about a minute, as the member it was, and the server says
   so. No restart is needed.

Until then the gateway carries nothing for that team. A mail to the team is not lost: the server answers the
provider's delivery with an error, and the provider delivers it again later. On Slack the server's other teams
are served as before, and someone who writes to a session of that team is told that one team's sessions cannot
be reached. A rotation alone, with no eviction, changes nothing for a gateway.

A team's owner can also put a gateway out by itself. Revoked, it joins again within a minute with the secret its
file holds. Banned, it stays out until the owner lifts the ban: that is how a team says it wants no gateway.

## What it does and does not do

A gateway is a member of the team like any other: it can do what a member can, the same limits apply to it, and
its messages are kept and dropped like a session's. What a leaked bot token or signing secret lets its holder do,
and who can read a mail on its way, is [docs/security.md, section 14](../../security.md#14-the-mail-and-slack-gateways);
the limits of each gateway are on [the mail gateway's](../configure/mail-gateway.md#limits) and
[the Slack gateway's](../configure/slack-gateway.md#limits) pages. On a server, also:

- Slack waits three seconds for an answer. When the server takes longer, a slash command shows a timeout although
  the message was sent, and Slack delivers the event again, which the server answers without carrying it twice.
- The gateway's own records (which thread a message belongs to, and the credential it is a member by) are in
  `gateway/` of the data directory, and are part of what a backup holds.
