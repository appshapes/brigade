# Install the mail gateway

People without a session write to a session by email, and the session writes back; replies thread both ways. Mail
travels through a connector for the team's provider: Resend and Postmark ship with Brigade, and any other provider
is a small connector of the team's own ([docs/mail-connectors.md](../../mail-connectors.md)).

## What you need

- **Resend:** an account whose sending domain is verified; the `from` address must be on it; a full-access API key
  (`re_…`, the installer creates the inbox and the webhook) in `RESEND_API_KEY`.
- **Postmark:** a server, a confirmed sender signature or domain for the `from` address, and the server's API token in
  `POSTMARK_SERVER_TOKEN`. Until Postmark approves the account, it refuses recipients outside the `from` domain
  (error 412); receiving works regardless.
- **Another provider:** a connector of your own, installed with `provider=external`
  ([docs/mail-connectors.md](../../mail-connectors.md)).
- The personal access token you used for `make backend-install`; `curl`, `jq` and Node (for `npx`). No MX record,
  no mailbox, no database password, no `supabase link`.

## The command

From your Brigade clone, with `team_file=` the project's `.brigade.json` by its full path:

```sh
read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN   # paste sbp_…; nothing is echoed
read -rs RESEND_API_KEY && export RESEND_API_KEY                 # paste re_…; nothing is echoed
make gateway-install project=<ref> team_file=<path>/.brigade.json from='Brigade <brigade@your-domain.com>'
```

For Postmark, `read -rs POSTMARK_SERVER_TOKEN && export POSTMARK_SERVER_TOKEN` and add `provider=postmark`.
`dry=1` shows the plan and changes nothing.

It applies the gateway's migration, deploys the gateway and your provider's connector, points the provider's
webhook at the connector, joins the gateway to the team as a member, stores the keys as function secrets (prints
none), schedules the once-a-minute `pg_cron` job `brigade_gateway_tick` that sends the sessions' mail, and adds the
address to the project's `.brigade.json`. Its summary names the address people write to, the receiving address,
the sender and who may write.

## Then

1. Commit and push the project's `.brigade.json`, which the command changed; it carries only public values
   ([Name the gateways](../../team-admin/configure/gateways.md)).
2. Give people the address on the `people write to:` line. They email it with a first line `to: <session>` or
   `to: <member>`; what they do and see is [Write to people on email or Slack](../../team-member/use/people-on-email-and-slack.md).
3. [Choose who may write](mail-senders.md). Until you do, anyone who knows the address can write to the gateway.

## Running it again

Safe, and what a release asks for when its changelog says the gateway changed (`git pull`, then the same command).
It keeps the gateway member, its receiving address, the public address and the allowed-senders list, redeploys the
functions, resets the provider's webhook and rotates the tick token and the connector secret. To rotate the
provider's key, run it again with the new key in the environment.

## An address of the team's own

By default people write to the provider's receiving address. For `brigade-team@your-domain.com`:

- **Route an address of yours to the receiving address.** In Google Workspace: Admin console › Apps › Google
  Workspace › Gmail › Default routing › Add another rule; envelope recipient `brigade-team@your-domain.com`;
  Change envelope recipient to the receiving address and Bypass spam filter; perform on recognised and
  non-recognised addresses. Then run the command again with `public_address=brigade-team@your-domain.com`. Use an
  address with no mailbox; replies go straight to the receiving address through their Reply-To tag.
- **Receive on a subdomain** such as `mail.your-domain.com` (an MX record on a domain that already receives mail
  takes that mail away). Resend: enable receiving on the subdomain and add the MX record it shows. Postmark: set the
  server's inbound domain to the subdomain and add `inbound.postmarkapp.com`, priority 10. Then
  `inbox=brigade-team@mail.your-domain.com`.

## Several teams

Run the command once per team, each with its own `team_file=` and `from=`. Each team gets its own inbox and
address, and a mail is routed to the team whose inbox it reached. The teams share the provider's key, the
connector secret and the tick: all use one Resend account; only one can use Postmark; only one can have a
connector of its own, and it runs its command again after any other team's run.

## How it works

```
 person ──mail──▶ provider ──webhook──▶ connector ──▶ mail-in ──send_message as mail-gateway──▶ brigade.messages ──▶ session
 session ──brigade send <gateway> (to: …)──▶ brigade.messages ──pg_cron, every minute──▶ mail-out ──▶ connector ──provider──▶ person
```

The gateway is an ordinary member (an anonymous principal, one session, lease 600 s) whose functions connect to
Postgres with the member's JWT claims set, so every RPC, limit and check of the protocol applies; the service role
holds nothing in the `brigade` schema. `mail-in` checks the connector secret, drops a mail whose From is not on the
list when one is set, splits what the person typed from what their client quoted, finds the target (a `to:` line,
the message id in the Reply-To address, the thread table, a quoted `[brigade]` block) and calls `send_message`.
`mail-out`, the tick, heartbeats the gateway session, drains its inbox, POSTs each message to the connector and
acknowledges only after the connector accepted it. State lives in the `brigade_gateway` schema (`gateways`,
`threads` kept 90 days, `received`), off the Data API, with no policy and no grant. Every outbound mail is
`text/plain` with an `Auto-Submitted` header; threading rides on the Reply-To tag because a relay may replace the
`Message-ID`.

## Limits

No auto-reply detection: an out-of-office answer reaches a session as a message. A reply chain stops after 32 hops,
which also bounds an auto-reply loop. Attachments are not carried. What the gateway opens, and who can then read the
mail: [docs/security.md, section 14](../../security.md#14-the-mail-and-slack-gateways).
