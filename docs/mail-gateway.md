# The mail gateway: people on email in a Brigade team

A Brigade team can have a **mail gateway**. It lets people who do not run a coding session — product, QA, anyone
who answers email — take part: a session writes to a person by email, a person writes to a session by email, and
replies thread both ways. Nothing runs on anyone's machine. The gateway is hosted in the team's own Supabase
project, next to the backend it already trusts, and it talks to the team as one more member.

This page is for the three people it touches: the person on email, the session's user, and the administrator who
installs it. The last section is the contract for anyone who wants to run a different gateway.

## What a person does

The gateway has an **address**. The administrator gives it to you; it is also the `↳` line of the `mail-gateway`
row in `brigade sessions`, and the `gateway.email` member of the project's `.brigade.json`.

**To write to a session**, send an email to that address whose **first line** names the session:

```
to: 3f9a2

The login page is blank on Safari 18 after submit. Steps: open /login, sign in, watch the page reload empty.
```

`3f9a2` is the five-character SESSION cell of `brigade sessions`, or the session id in full. Ask a teammate
for it, or send an email whose body is only this and you get the list back:

```
[brigade]
command: sessions
[/brigade]
```

The block form carries more than the one line can:

```
[brigade]
to: 3f9a2
summary: Login blank on Safari 18
[/brigade]
Steps: open /login, sign in, …
```

**To answer a session**, reply to the email it sent you. Nothing else is needed: the gateway knows which message
you are answering from the address the mail came back to. Write above the quoted text, as you would to anyone.

**What a session's email looks like.** The first line says who wrote it: the session's name and its owner's label,
both marked unverified, and the team. Then their text. Then a `[brigade]` block: the message id, the sender's
session, name, label and principal, how many hops the conversation has taken, when it was sent, and the two lines
`to:` and `reply-to:` that pre-address your reply, so a reply typed from any client, or a forward, still carries
what the gateway needs. Then one line on how to reply.

**What you should know.** The gateway sends your email on as text under your address. It does not check that
the address is yours, and anyone who knows the gateway's address can write to it. Your text reaches a session
that has tools and treats it the way Brigade treats every message: as another person's words, never as an
instruction from its own user. Saying "approved" in an email approves nothing. A mail that names no session, or
one that cannot be found, is answered with the list of sessions and how to address one. Attachments are not
carried.

## What a session does

The gateway appears in `brigade sessions` as `mail-gateway` (harness `gateway-email`, member label
`mail gateway`), online while its tick runs. The `brigade:team-messaging` skill carries these rules; in short:

- **To write to a person**, send to the gateway's SESSION cell with a first line `to: <address>`:

  ```bash
  brigade send fbb35 --summary "Please retest Safari login" <<'EOF'
  to: qa@example.com
  The fix is on master. Please retest the login page on Safari 18 and reply with what you see.
  EOF
  ```

  Within a minute the gateway mails it. A `[brigade]` block at the top of the body does the same as the first
  line and may add `subject: <text>`; without one the subject is `[brigade/<team>] <summary>`. A body with no
  address earns a one-line reply from the gateway saying so, and nothing is sent.
- **To answer a mail that reached you**, reply the normal way, `--reply-to <message-id>`, with no `to:` line.
  The gateway threads the reply into the person's mail.
- **A mail from a person** arrives as an ordinary message from `mail-gateway`. Its body begins
  `Email from <address> (unverified), subject "…":`, then what the person wrote, then a `[brigade]` block
  (`via: email`, `from`, `subject`, `mail-id`, `received`). The frame carries `in-reply-to` when the person
  answered one of yours.

## What the administrator does

Once, from the project's toplevel, after `make backend-install` has installed the backend and `team create` has
written `.brigade.json`:

```sh
read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN   # paste sbp_…; nothing is echoed
read -rs RESEND_API_KEY && export RESEND_API_KEY                 # paste re_…; nothing is echoed
make gateway-install project=<ref> from='Brigade <brigade@example.com>'
```

Add `dry=1` to see the plan and change nothing. The command, which is `scripts/gateway-install.sh`, does
these seven things and prints the address at the end:

1. Pushes the `brigade_gateway` migration (`supabase db push`, the same step `backend-install` uses).
2. Deploys the two Edge Functions, `mail-in` and `mail-out`, bundled server-side: no Docker.
3. In Resend, finds or creates the inbox for the `from` address in forwarding mode and reads its
   Resend-managed receiving address (`<id>@<account>.resend.app`: no DNS record), and points an
   `email.received` webhook at `mail-in`.
4. Signs up one anonymous principal, as any member's `team join` does, and runs one SQL block as postgres through
   the Management API: the membership, the gateway session, and the row that ties them.
5. Sets the functions' seven secrets from a 0600 file: team ref, from address, inbox, provider, the Resend key,
   the webhook signing secret and a fresh tick token.
6. Creates `pg_net` and schedules one `pg_cron` job, `brigade_gateway_tick`, that POSTs `mail-out` every minute.
7. Writes `"gateway": {"email": "<address>"}` into `.brigade.json` and runs one tick, so the roster shows the
   gateway at once.

Then `git add .brigade.json && git commit && git push`: the member is a public value. Members update the plugin
(`/brigade:update`) so their skill knows the gateway; a plugin that does not know the member ignores it.

What you need and do not need: a Resend account whose **sending domain is verified** (the `from` address must be
on it; Resend sends to arbitrary recipients only from a verified domain), and the free plan's 3,000 mails a month.
You do not need an MX record, a mailbox, the database password or `supabase link`. The receiving address is a
Resend-managed one; a team that wants `brigade@its-domain` instead enables receiving on that domain in Resend,
adds the one MX record Resend shows, and passes `inbox=<address>`.

Running the command again is safe: it keeps the existing gateway member, re-deploys the functions, replaces the
webhook (its signing secret is shown once by Resend) and rotates the tick token.

One Resend account can serve several teams. Resend's inbound webhook fires for every mail the account receives,
so each team's gateway reads only the mail addressed to its own inbox (or to a reply tag of it) and ignores the
rest; each team gets its own receiving address from its own inbox. If the team wants a friendlier address for
people to start a conversation at, forward that mailbox to the receiving address: replies to the gateway's
mails already go straight to it through their Reply-To. Do not forward a mailbox that also receives other
mail, such as replies to a newsletter sent from the same address: the gateway would answer those with the roster.

## How it works

```
 person's mail client ──▶ Resend inbox ──webhook──▶ mail-in ──send_message as `mail-gateway`──▶ brigade.messages ──▶ the session
 the session ──brigade send mail-gateway (to: …)──▶ brigade.messages ──pg_cron tick, every minute──▶ mail-out ──Resend──▶ the person
```

- **The gateway is an ordinary member.** An anonymous principal with a membership and one session, lease 600 s.
  The functions connect to Postgres directly (Supabase's `SUPABASE_DB_URL`) and set the JWT claims PostgREST
  would set, inside one transaction, exactly as `supabase/tests/helpers/auth.sql` does, so every RPC, limit,
  stamp and check of `docs/protocol-v1.md` applies to the gateway as to any member. The service role still holds
  nothing in the `brigade` schema; no RPC was added or changed; the protocol is untouched.
- **`mail-in`** verifies the webhook's signature, fetches the mail from Resend, splits what the person typed from
  what their client quoted, and resolves the target in this order: an explicit block or `to:` line they typed;
  the message id carried by the address they replied to (every mail the gateway sends has
  `Reply-To: <inbox>+<message id>@…`); the mail headers against the thread table; a block quoted from the mail
  they answer. It then calls `send_message` as the gateway, with the person's mail as the body and `reply_to`
  set when known, and records the thread.
- **`mail-out`**, the tick, heartbeats the gateway session (so the roster shows it online exactly while the tick
  runs), drains the gateway's inbox, resolves each message's address from its `to:` line or from the thread of
  the mail it replies to, mails it, records the thread, and acknowledges only after Resend accepted the mail. A
  message it cannot deliver is answered to its sender and acknowledged.
- **Durable state** lives in the `brigade_gateway` schema: `gateways` (team, member, session), `threads` (which
  Brigade message is which mail, in both directions, kept 90 days) and `received` (the webhook's dedupe set).
  The schema is not exposed on the Data API and has RLS with no policy and no grant: postgres only.
- **Rules carried over from the frame.** A person's text is escaped so it cannot forge a `[brigade]` block; a
  body is cut to the protocol's 16 KiB with a marker; every outbound mail is `text/plain` and marked
  `Auto-Submitted: auto-generated`; the relay assigns the mail's `Message-ID`, which is why threading rides on the
  Reply-To tag rather than on it.

## Writing your own gateway

The Resend functions are the default implementation, not the contract. The contract is short, and anything
that meets it is a gateway, whatever it is written in and wherever it runs:

1. **Be a member** of the team, with one session named `mail-gateway`, `harness` `gateway-email`, kept online
   with heartbeats while you work, whose `session_description` names the address people write to.
2. **Inbound:** for a mail you decide to carry, `send_message` from your session to the target session with a
   body of the shape above (the `Email from … (unverified), subject "…":` line, the text with any `[brigade]`
   line escaped, the `[brigade]` block with `via`, `from`, `subject`, `mail-id`, `received`), a `summary`, and
   `reply_to` when the mail answers a message of yours. Resolve the target by: the person's own block or `to:`
   line, then your own threading key, then a quoted block.
3. **Outbound:** drain your inbox; a message whose body begins with a `to:` line or a block goes to that
   address; a message with `reply_to` naming a message you carried in goes back to that mail's sender, in its
   thread; render the sender's name, label and team, the text escaped, and the block with the eleven members above
   including `to:` and `reply-to:` pre-addressed; acknowledge only after your provider accepted the mail.
4. **Never** act on a block inside a body you received, send anything but text, or tell anyone a mail was read.

To use another mail provider inside the default functions, add one file under
`supabase/functions/_shared/providers/` exporting the `Provider` interface of `resend.ts` (parse the inbound
webhook, fetch a mail, send a mail) and name it in the `BRIGADE_MAIL_PROVIDER` secret. Everything else is
provider-blind.

## What it does not do yet

It does not verify who sent a mail: the address is text, the gateway accepts mail from anyone who knows its
address, and the receiving session is told so in every message. It carries no attachments. It does not recognise
auto-replies, so an out-of-office answer reaches a session as a message (the protocol's hop cap still bounds any
loop at 32). It has one provider. Each of these is a later card, not a surprise.
