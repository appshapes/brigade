# The mail gateway: people on email in a Brigade team

A team can have a mail gateway. People without a coding session, such as QA or product, write to a session by
email, and the session writes back. Replies thread both ways. The gateway runs in the team's own Supabase project
as one more member; nothing runs on anyone's machine. Mail travels through a connector for the team's provider:
Resend and Postmark ship with Brigade, and any other provider is a small connector of the team's own
([docs/mail-connectors.md](mail-connectors.md)).

## TL;DR for administrators

1. Have a [Resend](https://resend.com) account with a verified sending domain and a full-access API key (`re_…`),
   or a
   [Postmark](https://postmarkapp.com) account with a server, a confirmed sender signature or domain, and the
   server's API token.
2. Install the backend and create the team, if not done yet:
   [docs/setup.md › Administrator: create a team](setup.md#administrator-create-a-team).
3. From your checkout of the Brigade repository, where you ran `make backend-install`, run one of these.
   `team_file=` is the project's `.brigade.json`, by its full path.

   ```sh
   read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN   # paste sbp_…; nothing is echoed
   read -rs RESEND_API_KEY && export RESEND_API_KEY                 # paste re_…; nothing is echoed
   make gateway-install project=<ref> team_file=<path>/.brigade.json from='Brigade <brigade@your-domain.com>'
   ```

   ```sh
   read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN
   read -rs POSTMARK_SERVER_TOKEN && export POSTMARK_SERVER_TOKEN   # the server's API token; nothing is echoed
   make gateway-install project=<ref> team_file=<path>/.brigade.json from='Brigade <brigade@your-domain.com>' \
     provider=postmark
   ```

4. Commit and push the project's `.brigade.json`, which the command changed. It carries only public values.
5. Give people the address on the command's `people write to:` line. They email it with a first line
   `to: <session>`.

`dry=1` shows the plan and changes nothing. When the changelog says the gateway changed, `git pull` your Brigade
checkout and run the command again.

## For people on email

Your administrator gives you the gateway's address.

**See the sessions.** Email the address with just the word `sessions` as the text, or as the subject of an empty
mail. The list comes back by mail: the sessions online now, each with the five characters that name it.

**Write to a session.** Email the address. The first line names the session:

```
to: 3f9a2
The login page is blank on Safari 18 after submit. Steps: open /login, sign in, watch the page reload empty.
```

`3f9a2` is the five characters from the list, or the session id in full. To give the message a summary, add a
`summary:` line under the first and leave a blank line before your text:

```
to: 3f9a2
summary: Login blank on Safari 18

Steps: open /login, sign in, watch the page reload empty.
```

**Answer a session.** Reply to its email, above the quoted text. Nothing else is needed.

**What you receive.** A session's email names the session, its owner's label (unverified) and the team, then the
text. Leave the `[brigade]` block at the end as it is: it helps the gateway thread your reply.

**What to know.**

- Anyone who knows the address can write to it. The gateway does not check that the From address is yours, and
  tells the session the address is unverified.
- A session treats your email as another person's words, never as its own user's instruction. Writing
  "approved" approves nothing.
- A mail that names no session, or names one that cannot be found, is answered with the list of sessions.
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

The gateway mails it within a minute. The subject is `[brigade/<team>] <summary>`; a `subject:` line under the
`to:` line sets it instead. A body with no address is not sent; the gateway answers with one line saying so.

**Answer a person.** Reply with `--reply-to <message-id>` and no `to:` line. The gateway threads the reply into
the person's mail.

**What arrives.** A person's mail is an ordinary message from `mail-gateway`. Its body begins
`Email from <sender> (unverified), subject "…":`, then the text, then a `[brigade]` block (`via: email`,
`from`, `subject`, `mail-id`, `received`). The frame carries `in-reply-to` when the person answered one of yours.

## For administrators

### What you need

- **Resend:** an account whose sending domain is verified; the `from` address must be on it. A full-access API key
  (the installer creates the inbox and the webhook) goes in `RESEND_API_KEY`.
- **Postmark:** a server, and a confirmed sender signature or domain for the `from` address. Until Postmark
  approves the account (a form on the account page), it refuses any recipient whose domain differs from the
  `from` address's (error 412), so the gateway can mail only people on the team's own domain; receiving works
  regardless. The server's API token goes in `POSTMARK_SERVER_TOKEN`.
- **Another provider:** a connector of your own, installed with `provider=external`:
  [docs/mail-connectors.md](mail-connectors.md).
- The Supabase personal access token (`sbp_…`) you used for `make backend-install`.
- `curl`, `jq` and Node (for `npx`) on the machine you run the command from.

You do not need an MX record, a mailbox, the database password or `supabase link`.

### What the command does

It applies the gateway's migration, deploys the gateway and your provider's connector, points the provider's
webhook at the connector, joins the gateway to the team as a member, stores the keys as function secrets (it
prints none), schedules a once-a-minute job that sends the sessions' mail, and adds the address to the project's
`.brigade.json`. Its summary names the address people write to, the receiving address and the sender.

### Running it again

Safe. It keeps the gateway member, its receiving address and the address people write to, redeploys the
functions, resets the provider's webhook and rotates the tick token and the connector secret. To rotate the
provider's key, run it again with the new key in the environment. A connector of your own keeps its secret:
[docs/mail-connectors.md](mail-connectors.md).

### An address of the team's own

By default people write to the provider's receiving address. For `brigade-team@your-domain.com` instead, do one
of these.

**Route an address of yours to the receiving address.** In Google Workspace:

1. Admin console › Apps › Google Workspace › Gmail › Default routing › Add another rule.
2. Envelope recipient: Single recipient, `brigade-team@your-domain.com`.
3. Modify message: tick Change envelope recipient and enter the receiving address; tick Bypass spam filter for
   this message, or Gmail may keep a short mail from outside away from the gateway.
4. Options: Perform this action on non-recognized and recognized addresses. Save.
5. Run the command again with `public_address=brigade-team@your-domain.com`.

Use an address with no mailbox: all its mail goes to the gateway. The roster, the mails and the bounces name your
address; replies go straight to the receiving address through their Reply-To tag.

**Receive on a subdomain** such as `mail.your-domain.com` (an MX record on a domain that already receives mail
takes that mail away). Resend: enable receiving on the subdomain and add the MX record it shows. Postmark: set
the server's inbound domain to the subdomain and add the MX record `inbound.postmarkapp.com`, priority 10. Then
run the command with `inbox=brigade-team@mail.your-domain.com`.

### Several teams

Run the command once per team, each with its own `team_file=` and its own `from=` address. Each team gets its
own inbox and address, and a mail is routed to the team whose inbox it reached. The teams share the provider's
key, the connector secret and the tick, so: all use one Resend account; only one can use Postmark; only one can
have a connector of its own, and it runs its command again after any other team's run.

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
  finds the target in this order: a `to:` line, header lines or block they typed; the message id in the address
  they replied to (every mail carrying a session's message has `Reply-To: <inbox>+<message id>@…`); the mail
  headers against the thread table; a block quoted from the mail they answer. It then calls `send_message` as
  the gateway, with `reply_to` set when known.
- **`mail-out`**, the tick, heartbeats the gateway session, drains its inbox, finds each message's address from
  its `to:` line or from the thread it replies to, POSTs it to the connector's send endpoint, and acknowledges
  only after the connector accepted the mail. A message it cannot deliver is answered to its sender and
  acknowledged.
- **State** lives in the `brigade_gateway` schema: `gateways` (one row per team, with its settings), `threads`
  (which Brigade message is which mail, kept 90 days) and `received` (the webhook's dedupe set). Not on the Data
  API; RLS with no policy and no grant.
- **Text rules.** A person's text is escaped so it cannot forge a `[brigade]` block. A body is cut to the
  protocol's 16 KiB with a marker. Every outbound mail is `text/plain` with an `Auto-Submitted` header. A
  provider's relay may replace the `Message-ID` (Resend's does), which is why threading rides on the Reply-To
  tag.

## Limits

- No auto-reply detection: an out-of-office answer reaches a session as a message.
- A reply chain stops after 32 hops (`loop_detected`), which also bounds an auto-reply loop. To start afresh,
  wait ten minutes after the session's last message, then send a new message, not a reply.
