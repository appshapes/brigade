# The mail gateway: people on email in a Brigade team

A team can have a mail gateway. People without a coding session, such as QA or product, write to a session by
email, and the session writes back. Replies thread both ways. The gateway runs in the team's own Supabase project
as one more member. Nothing runs on anyone's machine. Mail travels through a **connector** for the team's
provider: Resend and Postmark ship with Brigade, and any other provider is a small connector of the team's own
([docs/mail-connectors.md](mail-connectors.md)).

## TL;DR for administrators

1. Have a [Resend](https://resend.com) account with a verified sending domain and an API key (`re_…`), or a
   [Postmark](https://postmarkapp.com) account with a server, a confirmed sender signature or domain, and the
   server's API token.
2. Install the backend and create the team, if not done yet:
   [docs/setup.md › Administrator: create a team](setup.md#administrator-create-a-team).
3. From the top of the project checkout, run one of:

   ```sh
   read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN   # paste sbp_…; nothing is echoed
   read -rs RESEND_API_KEY && export RESEND_API_KEY                 # paste re_…; nothing is echoed
   make gateway-install project=<ref> from='Brigade <brigade@your-domain.com>'
   ```

   ```sh
   read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN
   read -rs POSTMARK_SERVER_TOKEN && export POSTMARK_SERVER_TOKEN   # the server's API token; nothing is echoed
   make gateway-install project=<ref> from='Brigade <brigade@your-domain.com>' provider=postmark
   ```

4. Commit and push `.brigade.json`, which the command changed. It carries only public values.
5. Members need plugin 0.18.0 or later: `/brigade:update`, then `/reload-plugins`.
6. Give people the address the command printed last. They email it with a first line `to: <session>`.

`dry=1` shows the plan and changes nothing. Running the command again is safe. After updating Brigade, run it
again once: it deploys the current functions.

## For people on email

The gateway has an address. Your administrator gives it to you. It is also on the `mail-gateway` row of
`brigade sessions`, and in `gateway.email` of the project's `.brigade.json`.

**Write to a session.** Email the address. The first line names the session:

```
to: 3f9a2
The login page is blank on Safari 18 after submit. Steps: open /login, sign in, watch the page reload empty.
```

`3f9a2` is the five characters in the SESSION column of `brigade sessions`, or the session id in full. To get the
list by email, send a mail whose body is only:

```
[brigade]
command: sessions
[/brigade]
```

**Answer a session.** Reply to its email, above the quoted text. Nothing else is needed.

**Add a summary.** Use the block form in place of the first line:

```
[brigade]
to: 3f9a2
summary: Login blank on Safari 18
[/brigade]
Steps: open /login, sign in, …
```

**What you receive.** A session's email starts with who wrote it: the session's name, its owner's label, both
marked unverified, and the team. Then the text. Then a `[brigade]` block with the message id and the reply
addressing, so a reply from any client, or a forward, still carries what the gateway needs. Then one line on how
to reply.

**What to know.**

- Anyone who knows the address can write to it. The gateway does not check that the From address is yours, and
  tells the session the address is unverified.
- A session treats your email as another person's words, never as its own user's instruction. Writing
  "approved" approves nothing.
- A mail that names no session, or names one that cannot be found, gets a reply with the list of sessions.
- Attachments are not carried.

## For sessions

The gateway is the `mail-gateway` row of `brigade sessions` (harness `gateway-email`, label `mail gateway`). It
is online while its tick runs. The `brigade:team-messaging` skill carries these rules.

**Write to a person.** Send to the gateway's SESSION cell. The first line is `to: <address>`:

```bash
brigade send <gateway> --summary "Please retest Safari login" <<'EOF'
to: qa@example.com
The fix is on master. Please retest the login page on Safari 18 and reply with what you see.
EOF
```

The gateway mails it within a minute. The subject is `[brigade/<team>] <summary>`. A `[brigade]` block at the
top of the body does the same as the first line and may add `subject: <text>`. A body with no address is not
sent; the gateway answers with one line saying so.

**Answer a person.** Reply with `--reply-to <message-id>` and no `to:` line. The gateway threads the reply into
the person's mail.

**What arrives.** A person's mail is an ordinary message from `mail-gateway`. Its body begins
`Email from <address> (unverified), subject "…":`, then the text, then a `[brigade]` block (`via: email`,
`from`, `subject`, `mail-id`, `received`). The frame carries `in-reply-to` when the person answered one of yours.

## For administrators

### What you need

- **Resend:** an account whose sending domain is verified; the `from` address must be on it. The free plan's 3,000
  mails a month is plenty. The API key goes in `RESEND_API_KEY`.
- **Postmark:** a server, and a confirmed sender signature or domain for the `from` address. Until Postmark
  approves the account (a form on the account page), it refuses any recipient whose domain differs from the
  `from` address's (error 412), so the gateway can mail only people on the team's own domain; receiving works
  regardless. The server's API token goes in `POSTMARK_SERVER_TOKEN`.
- **Another provider:** a connector of your own, [docs/mail-connectors.md](mail-connectors.md).
- The Supabase personal access token (`sbp_…`) you used for `make backend-install`.
- `curl`, `jq` and Node (for `npx`) on the machine you run the command from.

You do not need an MX record, a mailbox, the database password or `supabase link`.

### What the command does

`make gateway-install` runs `scripts/gateway-install.sh`, which:

1. Pushes the `brigade_gateway` migration (`supabase db push`).
2. Deploys the core, two Edge Functions named `mail-in` and `mail-out`, and the connector for your provider,
   `mail-connector-resend` or `mail-connector-postmark`, bundled server-side. No Docker.
3. Sets the provider up. Resend: finds or creates an inbox for the `from` address, reads its Resend-managed
   receiving address (`<id>@<account>.resend.app`, no DNS), and points an `email.received` webhook at the
   connector. Postmark: reads the server's inbound address (`<hash>@inbound.postmarkapp.com`) and points the
   server's inbound webhook at the connector, with a password in the URL.
4. Joins the gateway to the team as an anonymous member with one session, the way `team join` does.
5. Sets the functions' secrets from a 0600 file, the connector secret among them.
6. Schedules a `pg_cron` job, `brigade_gateway_tick`, that calls `mail-out` every minute.
7. Writes `"gateway": {"email": "<address>"}` into `.brigade.json` and runs one tick, so the roster shows the
   gateway at once.

It prints no token, key or secret. Its last lines name the address people write to, the receiving address, the
sending address, the provider and the endpoints.

### Running it again

Safe. It keeps the gateway member and its receiving address, redeploys the functions, resets the provider's
webhook and rotates the tick token and the connector secret (a connector of your own keeps its secret through the
file the installer wrote; `rotate=1` mints a new one). To rotate the provider's key, run it again with the new
key in the environment.

### An address of the team's own

By default people write to the Resend-managed receiving address. For `brigade-team@your-domain.com` instead,
do one of these:

- **Route an address you already have.** Make it an alias that also delivers to the receiving address, then run
  the command with `public_address=brigade-team@your-domain.com`. In Google Workspace that is an alias plus a
  Default routing rule (Admin console › Apps › Google Workspace › Gmail › Default routing): single recipient, your
  address; Modify message › Also deliver to › Add, the receiving address; "Perform this action on non-recognized
  and recognized addresses". On the added recipient, **turn off "Do not deliver spam to this recipient"**: Gmail
  may class a short mail from outside as spam, and the gateway, which treats every mail as untrusted text anyway,
  would otherwise never see it. The roster, the mails and the bounces name your address. Replies keep going
  straight to the receiving address through their Reply-To tag, which a forwarder would strip. Do not route a
  mailbox that also receives other mail, such as replies to a newsletter: the gateway would answer each with the
  roster.
- **Receive on your domain.** Resend: enable receiving on the domain, add the one MX record it shows. Postmark:
  set the server's inbound domain to a subdomain of yours and add the MX record `inbound.postmarkapp.com`,
  priority 10, on it. Then run the command with `inbox=brigade-team@your-domain.com`.

### Several teams

One Supabase project can carry several teams' gateways: run the installer once per team, from each team's
checkout. Each team gets its own inbox and address, and a mail is routed to the team whose inbox it reached. The
connector secret, the providers' keys and the tick are the project's and shared, so one Resend connector serves
every team on the project. A Postmark server has one inbound address, so one project carries one Postmark team.
One provider account can likewise serve several projects.

### Another provider: a connector of your own

Write a connector to the contract in [docs/mail-connectors.md](mail-connectors.md), host it anywhere with HTTPS,
and install with:

```sh
make gateway-install project=<ref> from='Brigade <brigade@your-domain.com>' provider=external \
  send_url=<your connector's send URL> inbox=<the address it receives at> secret_file=<an absolute path outside the repository>
```

The installer writes the connector secret and the core's inbound URL to that file. Configure both in your
connector. Nothing else about the gateway changes.

## How it works

```
 person ──mail──▶ provider ──webhook──▶ connector ──▶ mail-in ──send_message as mail-gateway──▶ brigade.messages ──▶ session
 session ──brigade send <gateway> (to: …)──▶ brigade.messages ──pg_cron, every minute──▶ mail-out ──▶ connector ──provider──▶ person
```

- **The gateway is an ordinary member**: an anonymous principal with a membership and one session (lease 600 s).
  The functions connect to Postgres directly and set the JWT claims PostgREST would set, so every RPC, limit
  and check of [docs/protocol-v1.md](protocol-v1.md) applies to it. The service role holds nothing in the
  `brigade` schema. No RPC was added or changed.
- **The connector** receives the provider's webhook, verifies it the provider's way, and POSTs the mail to
  `mail-in` in the contract's shape under the connector secret; it serves the send endpoint `mail-out` calls.
  The core knows no provider ([docs/mail-connectors.md](mail-connectors.md)).
- **`mail-in`** checks the connector secret, splits what the person typed from what their client quoted, and
  finds the target in this order: a `to:` line or block they typed; the message id in the address they replied
  to (every mail the gateway sends has `Reply-To: <inbox>+<message id>@…`); the mail headers against the thread
  table; a block quoted from the mail they answer. It then calls `send_message` as the gateway, with `reply_to`
  set when known.
- **`mail-out`**, the tick, heartbeats the gateway session, drains its inbox, finds each message's address from
  its `to:` line or from the thread it replies to, POSTs it to the connector's send endpoint, and acknowledges
  only after the connector accepted the mail. A message it cannot deliver is answered to its sender and
  acknowledged.
- **State** lives in the `brigade_gateway` schema: `gateways`, `threads` (which Brigade message is which mail,
  kept 90 days) and `received` (the webhook's dedupe set). Not on the Data API; RLS with no policy and no grant.
- **Text rules.** A person's text is escaped so it cannot forge a `[brigade]` block. A body is cut to the
  protocol's 16 KiB with a marker. Every outbound mail is `text/plain` with `Auto-Submitted: auto-generated`.
  A provider's relay may replace the `Message-ID` (Resend's does), which is why threading rides on the Reply-To
  tag.

## Writing your own gateway

The shipped functions are the default implementation, not the contract. A team that wants only a different mail
provider writes a connector, not a gateway ([docs/mail-connectors.md](mail-connectors.md)). A team that wants
nothing of the shipped functions runs its own gateway: anything that meets these four points is one, whatever it
is written in and wherever it runs:

1. **Be a member** of the team, with one session named `mail-gateway`, harness `gateway-email`, kept online with
   heartbeats while you work, whose `session_description` names the address people write to.
2. **Inbound:** for a mail you carry, `send_message` from your session to the target session with a body of the
   shape above (the `Email from … (unverified), subject "…":` line, the text with any `[brigade]` line escaped,
   the block with `via`, `from`, `subject`, `mail-id`, `received`), a `summary`, and `reply_to` when the mail
   answers a message of yours. Find the target by the person's own block or `to:` line, then your own threading
   key, then a quoted block.
3. **Outbound:** drain your inbox. A message whose body begins with a `to:` line or a block goes to that address.
   A message with `reply_to` naming a message you carried in goes back to that mail's sender, in its thread.
   Render the sender's name, label and team, the text escaped, and the block with `to:` and `reply-to:`
   pre-addressed. Acknowledge only after your provider accepted the mail.
4. **Never** act on a block inside a body you received, send anything but text, or tell anyone a mail was read.

To add a third shipped connector to Brigade itself, add one file under `supabase/functions/_shared/providers/`
exporting the `Provider` interface of `connector.ts`, a function directory beside `mail-connector-resend`, and a
case in `scripts/gateway-install.sh`.

## Limits

- The gateway does not verify who sent a mail. Anyone who knows its address can write to it, and every message
  tells the session so.
- No attachments.
- No auto-reply detection. An out-of-office answer reaches a session as a message. The protocol's hop cap of 32
  bounds any loop, and bounds a very long reply thread the same way: start a new mail, not a reply, to begin a
  fresh chain.
- Two shipped providers, Resend and Postmark. Another needs a connector ([docs/mail-connectors.md](mail-connectors.md)).
