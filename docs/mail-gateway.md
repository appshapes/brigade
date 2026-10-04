# The mail gateway: people on email in a Brigade team

A team can have a mail gateway. People without a coding session, such as QA or product, write to a session by
email, and the session writes back. Replies thread both ways. The gateway runs in the team's own Supabase project
as one more member. Nothing runs on anyone's machine.

## TL;DR for administrators

1. Have a [Resend](https://resend.com) account with a verified sending domain and an API key (`re_…`).
2. Install the backend and create the team, if not done yet:
   [docs/setup.md › Administrator: create a team](setup.md#administrator-create-a-team).
3. From the top of the project checkout, run:

   ```sh
   read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN   # paste sbp_…; nothing is echoed
   read -rs RESEND_API_KEY && export RESEND_API_KEY                 # paste re_…; nothing is echoed
   make gateway-install project=<ref> from='Brigade <brigade@your-domain.com>'
   ```

4. Commit and push `.brigade.json`, which the command changed. It carries only public values.
5. Members need plugin 0.18.0 or later: `/brigade:update`, then `/reload-plugins`.
6. Give people the address the command printed last. They email it with a first line `to: <session>`.

`dry=1` shows the plan and changes nothing. Running the command again is safe.

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

- A Resend account. The `from` address must be on a domain verified in Resend, which only sends to arbitrary
  recipients from a verified domain. The free plan's 3,000 mails a month is plenty.
- The Supabase personal access token (`sbp_…`) you used for `make backend-install`.
- `curl`, `jq` and Node (for `npx`) on the machine you run the command from.

You do not need an MX record, a mailbox, the database password or `supabase link`.

### What the command does

`make gateway-install` runs `scripts/gateway-install.sh`, which:

1. Pushes the `brigade_gateway` migration (`supabase db push`).
2. Deploys two Edge Functions, `mail-in` and `mail-out`, bundled server-side. No Docker.
3. In Resend, finds or creates an inbox for the `from` address, reads its Resend-managed receiving address
   (`<id>@<account>.resend.app`, no DNS), and points an `email.received` webhook at `mail-in`.
4. Joins the gateway to the team as an anonymous member with one session, the way `team join` does.
5. Sets the functions' secrets from a 0600 file.
6. Schedules a `pg_cron` job, `brigade_gateway_tick`, that calls `mail-out` every minute.
7. Writes `"gateway": {"email": "<address>"}` into `.brigade.json` and runs one tick, so the roster shows the
   gateway at once.

It prints no token, key or secret. Its last lines name the address people write to, the receiving address, the
sending address and the functions.

### Running it again

Safe. It keeps the gateway member and its receiving address, redeploys the functions, replaces the webhook and
rotates the tick token. To rotate the Resend key, run it again with the new key in the environment.

### An address of the team's own

By default people write to the Resend-managed receiving address. For `brigade-team@your-domain.com` instead,
do one of these:

- **Route an address you already have.** Make it an alias that also delivers to the receiving address (in Google
  Workspace: an alias plus a routing rule that adds the receiving address as a recipient), then run the command
  with `public_address=brigade-team@your-domain.com`. The roster, the mails and the bounces name your address.
  Replies keep going straight to the receiving address through their Reply-To tag, which a forwarder would strip.
  Do not route a mailbox that also receives other mail, such as replies to a newsletter: the gateway would answer
  each with the roster.
- **Receive on your domain in Resend.** Enable receiving on the domain in Resend, add the one MX record it shows,
  and run the command with `inbox=brigade-team@your-domain.com`.

### Several teams on one Resend account

Fine. Each team's gateway has its own inbox and reads only the mail addressed to it.

## How it works

```
 person ──mail──▶ Resend inbox ──webhook──▶ mail-in ──send_message as mail-gateway──▶ brigade.messages ──▶ session
 session ──brigade send <gateway> (to: …)──▶ brigade.messages ──pg_cron, every minute──▶ mail-out ──Resend──▶ person
```

- **The gateway is an ordinary member**: an anonymous principal with a membership and one session (lease 600 s).
  The functions connect to Postgres directly and set the JWT claims PostgREST would set, so every RPC, limit
  and check of [docs/protocol-v1.md](protocol-v1.md) applies to it. The service role holds nothing in the
  `brigade` schema. No RPC was added or changed.
- **`mail-in`** verifies the webhook's signature, fetches the mail, splits what the person typed from what their
  client quoted, and finds the target in this order: a `to:` line or block they typed; the message id in the
  address they replied to (every mail the gateway sends has `Reply-To: <inbox>+<message id>@…`); the mail
  headers against the thread table; a block quoted from the mail they answer. It then calls `send_message` as
  the gateway, with `reply_to` set when known.
- **`mail-out`**, the tick, heartbeats the gateway session, drains its inbox, finds each message's address from
  its `to:` line or from the thread it replies to, mails it, and acknowledges only after Resend accepted the
  mail. A message it cannot deliver is answered to its sender and acknowledged.
- **State** lives in the `brigade_gateway` schema: `gateways`, `threads` (which Brigade message is which mail,
  kept 90 days) and `received` (the webhook's dedupe set). Not on the Data API; RLS with no policy and no grant.
- **Text rules.** A person's text is escaped so it cannot forge a `[brigade]` block. A body is cut to the
  protocol's 16 KiB with a marker. Every outbound mail is `text/plain` with `Auto-Submitted: auto-generated`.
  Resend's relay assigns the `Message-ID`, which is why threading rides on the Reply-To tag.

## Writing your own gateway

The Resend functions are the default implementation, not the contract. Anything that meets these four points is
a gateway, whatever it is written in and wherever it runs:

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

To use another mail provider inside the default functions, add one file under
`supabase/functions/_shared/providers/` exporting the `Provider` interface of `resend.ts` (parse the inbound
webhook, fetch a mail, send a mail) and name it in the `BRIGADE_MAIL_PROVIDER` secret.

## Limits

- The gateway does not verify who sent a mail. Anyone who knows its address can write to it, and every message
  tells the session so.
- No attachments.
- No auto-reply detection. An out-of-office answer reaches a session as a message. The protocol's hop cap of 32
  bounds any loop, and bounds a very long reply thread the same way: start a new mail, not a reply, to begin a
  fresh chain.
- One provider, Resend.
