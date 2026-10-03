# Human sessions over Slack and email: what it takes

Date: 2026-10-03. Trello card 48. Status: **the email half is built and live (section "As built" below); Slack is not built.** Owner: Rjae. Asked for: an overview of what it
takes to let non-agentic team members (product, QA, anyone without a coding session) take part in a Brigade team
from Slack and from email, with an in-body protocol so that what travels is the message's object model, not a bare
body, and so that a reply can target the session it answers.

## As built, 2026-10-03 (read this first)

The email half shipped the same day as a **hosted mail gateway**, not as the bridge daemon of sections 2 to 7,
after Rjae ruled that a single poller is acceptable only when it is durable and hosted, that a session-side poller
and a leader election were not worth their cost, and that security controls and special cases (auto-replies,
allow-lists, sender verification) are set aside for now. The build, end to end:

- **Where:** in the team's own Supabase project. Two Edge Functions under `supabase/functions/` (`mail-in`, the
  Resend webhook; `mail-out`, the per-minute tick that heartbeats, drains and mails), one migration
  (`20261003160000_mail_gateway.sql`: schema `brigade_gateway` with `gateways`, `threads`, `received`; postgres
  only), a pgTAP file (`supabase/tests/gateway.sql`), Deno unit tests (`make functions-check`, also in CI).
- **Identity:** the gateway is an ordinary anonymous member with one session, `mail-gateway` (harness
  `gateway-email`). The functions connect to Postgres and set the JWT claims as the pgTAP fixtures do, so every
  RPC applies unchanged; the service role was granted nothing (neither of section 8's two parked options).
- **Install:** `make gateway-install project=<ref> from='Name <address>'` with `SUPABASE_ACCESS_TOKEN` and
  `RESEND_API_KEY` in the environment (`scripts/gateway-install.sh`): migration, functions, Resend inbox (a
  Resend-managed receiving address, no DNS) and webhook, the member and session, seven secrets, pg_net and the
  pg_cron tick, `.brigade.json`'s new `gateway.email` member. Idempotent. Measured on `wmgtaraqmoufmrnyojzf`.
- **The envelope block** of section 4 is as designed, with these measured facts: a person's reply resolves first
  by the **Reply-To tag** (`<inbox>+<message id>@…`), because Resend's relay (Amazon SES) replaces a custom
  `Message-ID`, so the mail headers are the second key and the quoted block the third; `In-Reply-To` on outbound
  mail is honoured and threads the reply in Gmail; `Auto-Submitted: auto-generated` is kept.
- **Measured:** Gmail → inbox → session in 3 s; session reply → tick → Gmail in under a minute, threaded; Gmail
  reply to the tagged address → session with `in-reply-to` and `hops=2`. A forged `[brigade]` block in a session's
  body arrived escaped.
- **Docs:** `docs/mail-gateway.md` (person, session, administrator, how it works, the gateway contract, what it
  does not do yet), `docs/security.md` §14, `docs/setup.md`, the team-messaging skill, CHANGELOG. Plugin 0.18.0
  teaches the skill and makes `.brigade.json`'s `gateway` member a known one.
- **Not built:** Slack (sections 6 of the original design), sender verification and allow-lists (section 7's
  controls), the frame wording variant and the `KIND` column (section 8), auto-reply detection. The Postmark
  provider file is the seam's proof and waits on an account.

Sections 1 to 12 below are the research and the bridge design as written before the ruling; section 4 (the
block) and section 9's dependency notes still describe what was built.

## 0. Verdict

**Viable, with no change to protocol v1 and no migration.** A human on Slack or email is a Brigade *session* like
any other: a row with an owner principal, a name, a lease, an inbox. What is new is the program that keeps that
row alive and moves text between the backend and the human's own messaging tool. In Brigade's vocabulary that
program is a **harness** (the third kind of process that speaks BAP/1 to the bundled adapter, beside the Claude
Code hooks and the watcher), not an adapter. The adapter protocol already carries everything it needs:

- `harness` is a free string of up to 32 characters on every session (`docs/protocol-v1.md` 4.4.2, 4.4.3;
  `supabase/migrations/20260830120000_brigade_schema.sql:59`). `slack` and `email` fit without a schema change.
- A message is `kind: text`, one recipient, a body of at most 16,384 bytes, a summary of at most 200 characters,
  `reply_to` and a server-computed `hop_count` (4.4.5). Slack and email both carry that comfortably.
- `session.resume` keeps one session id across restarts of the program, so a human's address is stable (4.5.8).
- `injected` means "handed to the transport, not read" (4.9). "Posted to Slack" and "accepted by the mail API" are
  exactly that.

The shape is a long-running **bridge** process per team, `brigade bridge slack` and `brigade bridge email`,
that runs on a member's machine or a small always-on host, makes only outbound connections (Slack Socket Mode;
email by polling), registers one Brigade session per enrolled human, relays inbound messages into a Slack DM or
an email with an **envelope block** in the body, and parses the human's replies back into `message send` with
`reply_to` set.

Two decisions change the work materially and need a ruling before P1 (section 10): whether each human is their
own principal, and which email transport to build on. One security question is real and has a workable answer:
email's `From:` is forgeable, so an email session may only *initiate* a message under one of the controls in
section 7.

The Codex ruling of 2026-09-09 (`.context/plans/codex-team-participation.md`) dropped Codex because it "could
only be a mailbox, never a peer". For a human, a mailbox is the right model: they read when they read. Nothing
here contradicts that ruling; it is the reason a human session needs no idle-wake experiment at all.

## 1. What a human session is, in Brigade's terms

| Member (4.4.2 / 4.4.3) | Claude Code session today | Human session |
| --- | --- | --- |
| `harness`, `harness_version` | `claude-code`, Claude Code's version (`internal/harness/hook/hook.go:120`) | `slack` or `email`, the bridge's version |
| `session_name` | the session's own name | the human's chosen name, e.g. `Rjae (Slack)`, ≤ 64 code points, unverified like every name |
| `session_description` (the `↳` line) | `brigade doing` | set by the human (`/brigade doing …` in Slack, a `doing:` directive by email); default `human on Slack; answers when they read it` |
| `activity` | busy / idle from the hooks | always `idle` (busy means "a model is working") |
| `inbound` | accept / hold / refuse | always `accept`: the human is the reviewer |
| lease | 90 s, heartbeat from the watcher | 600 s (the maximum), heartbeat from the bridge every ~4 min; **offline whenever the bridge is down**, and messages wait on the server (7-day unacknowledged retention, 4.5.9) |
| `model`, `context_used_tokens`, `brigade_version`, `sync_peer` | reported | omitted (blank cells in the roster) |
| delivery, ack | inject into the inbox socket, then `message ack` | post to Slack / send the email, then `message ack` (4.5.3: nothing is acknowledged before the hand-off) |

What the rest of the team sees today, with nothing else changed: a `brigade sessions` row with a blank MODEL and
VERSION cell and a human-looking name; a frame whose preamble says the message came "from another person's Claude
Code session". Section 8 lists the small receiving-side changes that make a human session recognisable.

## 2. Architecture

**Components and the two paths (labels A*, K*, B*, H*, F* are referred to throughout this plan).**

```
┌──────────────────────────── EXISTS TODAY: A TEAMMATE'S SESSION ───────────────────────────┐
│  A1  Claude Code session (the teammate's model)                                           │
│       │ runs `brigade send <human session> --reply-to …`      ▲ reads the frame           │
│  A2  Brigade plugin: hooks + watcher (the Claude Code harness; renders <brigade-message>) │
│       │ SendRequest                                          │ MessageEnvelope            │
│  A3  `brigade adapter supabase` child, spawned per command                                │
└───────┼──────────────────────────────────────────────────────┼────────────────────────────┘
        │ F1  message send                                     │ F4  message watch, then ack
        ▼                                                      │
┌──────────────────────────── EXISTS TODAY: THE BACKEND ────────────────────────────────────┐
│  K1  Supabase: brigade.sessions, brigade.messages                                         │
│      RPCs send_message / fetch_inbox / ack_messages; Realtime push on the inbox topic     │
└───────┬──────────────────────────────────────────────────────▲────────────────────────────┘
        │ F2  message watch (Slack) or receive on a timer (email), then ack                 
        ▼                                                      │ F3  message send, reply_to set
┌──────────────────────────── NEW: THE BRIDGE (one daemon per team) ────────────────────────┐
│  B1  `brigade bridge slack|email run`: long-running, outbound connections only            │
│  B5  `brigade adapter supabase --profile <human>` children, spawned per command           │
│      one profile per enrolled human = one principal each (option A; option B shares one)  │
│  B2  core: human ↔ session map, resume on start, heartbeat (lease 600 s), drain,          │
│      de-duplicate by message_id, record the post THEN ack                                 │
│  B3  envelope block: render `[brigade] … [/brigade]` outbound; parse directives inbound   │
│      (explicit block > native thread or mail headers via B4 > quoted block)               │
│  B4  state + secrets: $XDG_CONFIG_HOME/brigade/bridge/<team>/ (0700; files 0600)          │
│      state.json (session ids, post ↔ message tables, poll cursors), secrets.json          │
│  B6  Slack transport: Socket Mode websocket (events, slash commands) + Web API posts      │
│  B7  Email transport: poll Resend Inbound (GET /emails/receiving) + send (POST /emails)   │
└───────┬──────────────────────────────────────────────────────▲────────────────────────────┘
        │ F2  a DM or an email carrying the block              │ F3  a thread reply or a mail reply
        ▼                                                      │
┌──────────────────────────── OUTSIDE BRIGADE ──────────────────────────────────────────────┐
│  H1  a person in Slack: a DM thread per message; /brigade sessions|send|doing|whoami      │
│  H2  Slack, Inc. and the workspace admins: can read everything that reaches H1            │
│  H3  a person in their mail client: reply in thread, or a new mail with a block + token   │
│  H4  the team inbox at Resend (MX) and the mail provider: can read everything to H3       │
└───────────────────────────────────────────────────────────────────────────────────────────┘
```

**The outbound path, F1 → F2.** A1 runs `brigade send` → A2 validates the body → A3 calls K1 `send_message`
(recipient = the human's session; `accepted`, as today) → B5's watch emits the envelope → B2 drops a `message_id`
it has already posted → B3 renders the human header, the sender's text (escaped) and the block → B6 posts the DM
(or B7 sends the mail) → B2 records the transport id in B4 → B5 acknowledges → H1 or H3 reads it when they read it.

**The return path, F3 → F4.** H1 replies in the thread (or H3 replies to the mail) → B6/B7 hands the text and its
native facts (`thread_ts`; `In-Reply-To`/`References`) to B3 → B3 resolves `to` and `reply-to`: an explicit block
in the unquoted text first, then the native fact looked up in B4's post ↔ message table, then a quoted block →
B2 derives the idempotency key from the transport's own id → B5 calls K1 `send_message` with the human's session
as sender and `reply_to` set (K1 verifies the id and adds one hop) → A3's watch emits it → A2 renders the frame
with `in-reply-to` → A1's model reads it. Errors on the way back (`not_found`, `rate_limited`, `loop_detected`,
`waiting:`) go to H1/H3 in their own channel with the texts `brigade send` already uses.

**What changes in the boxes that exist today (section 8).** A2 only: a roster cache so the frame can say "Slack
account" or "email" for a sender whose session's `harness` says so, a `KIND` column in `brigade sessions`, and one
skill section. A3 and K1 are untouched: no protocol change, no migration.

**Components.**

- **Bridge core** (one Go package, `internal/bridge`): the human ↔ session map; registration with
  `resume.session_id` from state so an address survives restarts (and so two bridges for one team cannot both drain
  one inbox: the second gets `conflict: session_live`, C-19b); heartbeats; inbox drain; de-duplication by
  `message_id` (at-least-once, 4.5.2); the envelope-block renderer and parser (section 4); `message send` with an
  idempotency key derived from the transport's own message id; acknowledgement after the transport accepted the
  post; a state directory (0700) with a state file and a secrets file (0600); diagnostics through the redacting
  logger (`internal/adapterkit/log`), with the Slack and Resend token shapes added to its patterns; nothing on
  stdout but the documented human output of `brigade bridge status` (forbidigo).
- **Transports**: `internal/bridge/slack`, `internal/bridge/email`. Each implements: deliver(envelope) →
  transport id; a stream of inbound human messages with their native threading facts; a way to answer the human
  with an error or a roster.
- **Backend access** through the existing process boundary: `internal/harness/adapterclient` spawns
  `brigade adapter supabase …` with `--profile`, an allow-listed environment and the 20 s budgets, exactly as the
  plugin does (`internal/harness` must not import the adapter; depguard). The bridge reads the project's
  `.brigade.json` through `internal/harness/teamfile` (or takes `--team-file`) for the backend URL and key, and
  joins with `--secret-file` (never argv, never the chat).
- **Where it runs**: anywhere with outbound HTTPS. Slack Socket Mode is a websocket the bridge opens; Resend
  Inbound is polled; nothing listens, so a laptop daemon (launchd) or a $5 VM (systemd) both work. No public
  URL, no inbound firewall rule, no Supabase Edge Function.

## 3. Identity: who is the principal behind a human session?

Security.md §1 is explicit: "the principal, never the label, is the identity"; `from-principal` is the only
server-stamped fact a receiver may rely on; labels and names are free text anyone can copy.

**Option A — one principal per human (recommended).** The bridge keeps one adapter profile per enrolled human
(`--profile slack-U0123…`, `--profile email-<hash of address>`), each joined once with the team's join secret
by the bridge operator (`brigade bridge enrol slack <user-id>`; `brigade bridge enrol email <address>`), with
the human's display name or address as the label (unverified like every label, C-45). Consequences: every human
has a distinct `from-principal`; `brigade team members` lists each of them; `team revoke-member` removes one
human without touching the others; the per-principal send budgets (60/min, 600/h) are per human; the receiving
model can recognise the same person across their Slack and email sessions only if both are enrolled on one
profile (make `enrol` accept a second transport for an existing profile). Cost: N credential files under the
bridge's config dir, and enrolment is an operator step with a consent point (the human is told, in their own
channel, what they are now reachable as and who can read it; section 6 and 7 give the text).

**Option B — one bridge principal, N sessions.** Cheaper to build (one profile), but every human shares one
`from-principal`, so the only per-person signal a model gets is the session name, which is unverified text.
Revocation is all-or-nothing, and one chatty human spends everyone's budget. Fine for the P0 spike; not for
anything a teammate's model acts on.

The protocol does not care which: both are "a principal registers sessions". Ruling 1 in section 10.

## 4. The in-body protocol: the envelope block

**Purpose.** The transport's native threading (a Slack thread, an email's `In-Reply-To`) and the bridge's own
state table already let the bridge route most replies. The block is there for the cases they do not cover and
for the requirement itself: a message that is self-describing can be answered from any client, forwarded,
pasted, or typed on a phone, and still reach the right session with the right `reply_to`. It is also how a
human *initiates* a message, names a target, or runs a command, with no slash command available (email, or a
Slack thread where slash commands are not allowed).

**Grammar.** A block is a run of `key: value` lines between a line that is exactly `[brigade]` and a line that
is exactly `[/brigade]`. Keys are lowercase ASCII with hyphens; values run to the end of the line; unknown keys
are ignored (JSON convention 2, carried over); a duplicate key is an error the bridge reports back to the human.
`[brigade]` was chosen over the common `-- ` footer separator because mail clients hide or strip quoted text
below `-- ` on reply, which is exactly where the block must survive.

**Outbound (bridge → human): the envelope, one field per `MessageEnvelope` member, plus how to reply.**

```
Frank's review session says:                      ← human-readable header, bridge text
<sender's summary, marked as the sender's>
<body>
[brigade]
team: brigade
message: 0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f
from-session: 6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2
from-name: frank-reviewer
from-label: sam@example.com (unverified)
from-principal: 9f3c1a20-…
in-reply-to: <message id>                         ← only when the envelope's reply_to is set
hops: 1
sent: 2026-10-03T14:02:11Z
to: 6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2         ← prefilled: a reply goes here …
reply-to: 0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f   ← … answering this message
[/brigade]
```

The last two lines are the reply pre-addressed: a human who replies in the thread writes nothing special, and a
human whose client lost the thread can leave the quoted block in place and the bridge still has both ids. Ids
are printed in full, never cut (the frame's rule: a silently truncated id sends the reply to nobody). The
sender's summary and body are placed above the block and **escaped**: any line in the sender's text that is
exactly `[brigade]` or `[/brigade]` is prefixed with a visible backslash, so a body cannot forge a block (the
same reason the frame sanitises its body, U-03/U-04). On Slack the block is inside a code fence, so ids are
not linkified and the text is not reflowed; by email the message is `text/plain`.

**Inbound (human → bridge): directives at the top of the human's own text.**

```
[brigade]
to: 3f9a2
summary: Login page broken on Safari 18
[/brigade]
Steps: open /login on Safari 18, submit, the page reloads blank. Console shows …
```

| Key | Meaning | Rule |
| --- | --- | --- |
| `to` | the recipient session | a full `session_id` or its tail of 5+ characters, resolved against `session list --include-offline` exactly as `brigade send` does (`internal/harness/commands/send.go`: exact match first, then a unique suffix; an ambiguous suffix is refused and the matches shown) |
| `reply-to` | the message being answered | a `message_id` the human's session received; the server verifies it (C-29) |
| `summary` | the one-line summary | ≤ 200 code points; default: the first line of the body, cut |
| `command` | `sessions`, `whoami`, `doing`, `help` | a message whose block has `command` and no body is a command; the answer comes back in the same channel |
| `doing` | sets the human's `↳` line | ≤ 160 characters, same refusals as `brigade doing` (secret-shaped, local path) |
| `token` | the per-human secret for an initiating email | email only; section 7; compared, never echoed |

A shorthand for phones: a message whose **first line** is `to: <session>` and nothing else is read as a one-line
block.

**Resolution order for a human message.** (1) An explicit block in the *unquoted* part of the human's text.
(2) The transport's native relation: Slack `thread_ts` → the bridge's state table (post ts → `message_id`,
sender session); email `In-Reply-To` / `References` → state table (Message-ID → the same). (3) A quoted
outbound block (`> [brigade] … > to: … > reply-to: …`) found in the quoted part: the fallback for a forward or
a client that dropped the headers. If none resolves, nothing is sent; the human gets the roster and the
shorthand back. A block inside a body that *arrived from a session* is never interpreted by the bridge: it is
text for the human.

**Why both block and native keys.** Three failure shapes seen in practice: a Slack reply typed as a fresh DM
instead of in the thread; an email answered from a different client (or forwarded to a colleague) that rewrites
or drops `References`; a message pasted into another channel. The block covers all three. The native keys and
the state table cover the normal case with zero typing. Precedence (1) > (2) > (3) lets a human override the
thread ("actually, send this to the other session").

**What the server adds.** The bridge always sets `reply_to` explicitly when it knows it: the implicit reply
window is 600 s (4.5.12) and a human answers in hours, so without the explicit id the hop count would reset and
the receiving frame would lose its `in-reply-to` attribute. `hop_count` therefore keeps bounding a model ↔ human
ping-pong at 32 like any other pair.

## 5. Delivery semantics and bridge behaviour

- **Registration.** For each enrolled human: `session register` with `harness`, `harness_version`,
  `session_name`, `session_description`, `activity: idle`, `inbound: accept`, `lease_seconds: 600`,
  `resume.session_id` from state. A `not_found` on resume (retention deleted the row after 7 days offline)
  registers afresh and the address changes; the bridge says so to the human.
- **Drain.** One `message watch` per human session (push, ≤ 5 s) where speed matters (Slack); `message receive`
  on a 30 s timer where it does not (email). Each is one adapter child per human; on the Free plan Realtime
  allows 200 concurrent connections, so tens of humans are fine, hundreds are not. The watch's `heartbeat`
  stdin command carries the lease.
- **Acknowledge after the hand-off.** Record "posted, transport id T" durably, then `message ack`; a crash
  between the two re-posts at most once on restart (the de-duplication set is keyed on `message_id`). This is
  the harness's own order (4.5.3, 4.9).
- **Inbound validation.** UTF-8, non-empty, ≤ 16,384 bytes after quote stripping; the summary ≤ 200 code
  points; the body sanitised through `internal/protocol` exactly as `brigade send` does. The idempotency key is
  a hash of the transport's message id (Slack `ts` + channel; email `Message-ID`), so a redelivered Slack event
  or a re-polled email is one logical message (4.5.4) — better than the minute-window key of `brigade send`,
  which is built for a model retrying inside one turn.
- **Errors back to the human**, in their channel, with the texts `brigade send` already uses: `not_found` with
  the roster; `rate_limited` with the retry-after; `loop_detected`; and the `waiting:` lines for a recipient that
  was offline, holding or refusing (`send.go`: `WaitingOffline`, `WaitingHold`, `WaitingRefuse`).
- **Pacing.** At most one post per two seconds per human, queued, never dropped (it is a mailbox). Slack's own
  limit is one message per second per channel.
- **Auto-reply guard (email).** Drop, do not send, an inbound mail with `Auto-Submitted` other than `no`,
  `X-Auto-Response-Suppress`, `Precedence: bulk | junk | auto_reply`, or a subject beginning `Automatic reply` /
  `Out of Office`; mark every bridge-sent mail `Auto-Submitted: auto-generated` (RFC 3834) so responders stay
  quiet. Without this a vacation responder and a helpful model would talk until hop 32.
- **Inbox caps.** A human whose bridge is down for days fills at 60 unacknowledged (4.5.12); senders then get
  `rate_limited: recipient_inbox_full` and `brigade send` tells the model. Document it; it is the correct
  behaviour.
- **Hold / refuse.** Not applicable on the human side; `accept` always. The receiving Claude Code side keeps
  its own policy untouched.

## 6. Slack

- **App.** Socket Mode: an app-level token (`xapp-…`, scope `connections:write`) opens the websocket via
  `apps.connections.open`; events and slash commands arrive as envelopes that must be acknowledged within 3 s;
  Slack closes the socket periodically with a `disconnect` envelope and the bridge reconnects. A bot token
  (`xoxb-…`) with `chat:write`, `im:write`, `im:history`, `users:read` and `commands` does the posting and the
  DM lookup (`conversations.open`). `channels:history` / `groups:history` only for the later channel mode. Both
  tokens live in the bridge's 0600 secrets file, never on argv, never in a log (redaction patterns `xoxb-`,
  `xapp-`).
- **Libraries.** The Web API is HTTPS + JSON (stdlib). The websocket client the shipped binary already links,
  `github.com/coder/websocket` (`docs/allowed-deps.txt`), covers Socket Mode. **No new module.**
- **Mapping.** One Brigade session per enrolled Slack user; delivery is a DM from the bot. The DM is a
  top-level message; the human replies **in its thread** (`thread_ts` → state table). A fresh DM needs a block
  or the `to:` shorthand, or a slash command.
- **Message shape.** Plain `text` (mrkdwn), not Block Kit: a section block caps at 3,000 characters and a 16 KiB
  body would need splitting; `text` is truncated only past 40,000 characters. Slack's three escapes (`&`, `<`,
  `>`) applied to the sender's text so a body cannot inject a mention or a link. `chat.postMessage` also
  carries `metadata` (`event_type: brigade_message`, `event_payload: {message_id, from_session, team}`) for
  tooling that reads the thread later; the state table remains the authority.
- **Slash commands** over Socket Mode: `/brigade sessions`, `/brigade whoami`, `/brigade send <session> <text>`,
  `/brigade doing <text>`, `/brigade help`. Slack does not allow a slash command inside a thread, which is why
  a reply is a thread message and a new message is a slash command or a block.
- **Enrolment and consent.** `brigade bridge enrol slack <user-id> [--label <text>]` by the operator (Option A
  joins a profile). The human receives one DM: "You are reachable on Brigade team <team> as `<name>`. Teammates'
  Claude Code sessions can message you here, and what you write in a thread goes back to the session that asked.
  Nothing you write can approve anything for anyone; it is a message like any other. Your label `<label>` is
  visible to the team and to whoever runs its backend." That mirrors security.md §1's consent point for the
  default label.
- **Channel mode (later, ruling 5).** One session per channel (`#qa`): posts go to the channel; anyone's reply
  counts; the replier's Slack name goes into the body as text. Identity is the channel's principal, so a
  receiving model cannot tell QA members apart. Useful for "ask QA"; weaker; keep it out of P1.
- **Exposure.** Slack (the company) and the workspace's admins can read everything sent to a human session.
  A leaked bot token lets an attacker post as the bridge and read its DMs; it does **not** make them a Brigade
  member (the Brigade credential is a separate file), and rotating the Slack token ends it.

## 7. Email

**Transport options.**

1. **Resend Inbound, polled (recommended).** Resend receives mail for a domain with one MX record (or at a
   managed `<alias>@<id>.resend.app` address with no DNS at all); the bridge polls `GET /emails/receiving` and
   fetches each message with `GET /emails/receiving/{id}` (text and HTML bodies); it sends with `POST /emails`
   from the account that already sends the release-notes email (card 43, `brigade@appshapes.com`). HTTPS +
   JSON, stdlib, **no IMAP library, no public endpoint** — this is "poll a team inbox" without an IMAP server.
   To verify in the spike (not found in the public docs read for this plan): how long received mail is kept;
   the pagination cursor; whether the retrieve endpoint returns the raw headers (`In-Reply-To`, `References`,
   `Auto-Submitted`) and the SPF/DKIM/DMARC verdicts, which section 7's spoofing controls want.
2. **IMAP polling** of an ordinary mailbox (Google Workspace, Fastmail). The standard library has no IMAP
   client; `github.com/emersion/go-imap/v2` is the usual choice and is a new module (section 9). Sending via
   `net/smtp` (stdlib) or Resend.
3. **A provider's REST API** (Gmail API, Microsoft Graph): HTTPS + JSON and `net/mail` for parsing, stdlib
   only, but an OAuth refresh-token flow written by hand and a Google Cloud project to keep.
4. **Resend's `email.received` webhook**: needs a public HTTPS endpoint, and the webhook carries no body
   anyway (a second call fetches it). Only if the bridge is hosted; otherwise polling wins.

**Mapping.** One Brigade session per enrolled address. The team inbox is the transport; the sessions are the
humans. Delivery: a `text/plain` mail from the team inbox to the human, `Subject: [brigade/<team>] <summary or
excerpt>` (header-injection safe: CR/LF stripped, length capped), `Message-ID: <msg.<message_id>@<bridge
domain>>`, `In-Reply-To`/`References` set when the Brigade message itself answers one the bridge sent (so the
human's client threads the conversation), `Reply-To` the team inbox, `Auto-Submitted: auto-generated`. The
body: the human-readable header, the sender's text, the envelope block.

**Inbound parsing.** Prefer the `text/plain` part; an HTML-only reply is reduced to text (stdlib tag stripping)
or bounced with a courteous note; quoted lines (`>` and the `On … wrote:` tail) and a `-- ` signature are
stripped by default, so a long thread does not blow the 16 KiB cap; attachments are ignored and the sender is
told (the protocol has none, 4.8).

**Spoofing: the one real security question.** An email `From:` is text. Without a control, a non-member who
knows an enrolled address could inject messages into the team through that human's session, which breaks the
"only members send" boundary of security.md §1. Three controls, at least one of the last two mandatory before an
email session may initiate:

- (a) **Allow-list**: only enrolled addresses are read at all (necessary, not sufficient).
- (b) **DMARC alignment**: accept only when the receiving service's authentication results say `dmarc=pass`
  (or aligned SPF/DKIM pass) for the `From:` domain. Needs the verdicts from the transport; verify Resend
  exposes them (spike). Most corporate domains publish DMARC today; a personal address at a provider with
  DKIM also passes.
- (c) **Correlation and token**: a *reply* is accepted only when its `In-Reply-To` / `References` names a
  Message-ID the bridge sent **to that address** (an attacker cannot guess a UUID-based id they never
  received); an *initiating* message must carry `token: <value>` in its block, a per-human secret issued at
  enrolment and held in the bridge's secrets file, compared and never echoed. This works with any transport
  and no header verdicts.

Recommendation: (a) + (c) always; (b) as well when the transport provides it, which then lets the token be
dropped for aligned domains.

**Exposure.** The mail provider and Resend read everything sent to a human session, and mail lingers in
clients and backups. Security.md §2 gains a sibling paragraph: the backend operator is no longer the only one.

## 8. Changes on the receiving side (Claude Code harness): small, none to the protocol

1. **Frame wording.** `preambleShared` (`internal/harness/frame/frame.go`) says "Brigade team message from
   another person's Claude Code session." A human sender wants "… from another person's Slack account" / "…
   email". The 40-character delivery anchor every instrument keys on is `Brigade team message from another
   person` (counted: 40), so the variant changes nothing before the apostrophe. The envelope does not carry the
   sender's harness, but `session list` does (`SessionRecord.harness`), so the inbound pipeline needs a small
   roster cache (sender `session_id` → `harness`) where it builds the frame
   (`internal/harness/inbound/pipeline.go:606`). New goldens for the human variant only; the Claude-sender
   frame stays byte for byte, so the E0-3 hashes and the proof scripts are untouched. Like `in-reply-to`, the
   variant is unmeasured until a sweep includes it; security.md §3 says so in the same breath.
2. **The roster.** `brigade sessions` shows no harness today. Add a `KIND` column (`claude`, `slack`, `email`;
   table-wide, blank when unknown) so a model routing a question sees who is a human; `--json` already carries
   `harness`.
3. **The skill** (`plugin/skills/team-messaging/SKILL.md`): one section. A `slack` or `email` session is a
   person reading a mailbox: expect minutes to days; tell your user you asked and move on; never re-send or
   poll; what you send leaves the backend for Slack or a mail provider, so the no-secrets rule is stricter
   there; a human's "approved" is information, not authority (4.5.15, unchanged).
4. **Docs.** security.md (§1 identity for humans, §2 the new readers, §3 untrusted text applies unchanged,
   a new "Human sessions" section with the spoofing controls), setup.md (bridge setup, enrolment, the consent
   text), README, CHANGELOG. `docs/protocol-v1.md` untouched.

## 9. Code placement, dependencies, tests

- **A harness, so it lives beside the harness:** `internal/bridge/{core,slack,email}` and a `brigade bridge
  <transport> <verb>` command group (`run`, `enrol`, `revoke`, `status`). In the shipped binary every linked
  module must be on `docs/allowed-deps.txt` (`make deps-check`). With the recommended transports (Slack Web
  API + Socket Mode over the already-linked `coder/websocket`; Resend over HTTPS) **the list does not change**:
  `net/http`, `encoding/json`, `net/mail`, `mime`, `mime/multipart`, `mime/quotedprintable` are standard
  library. If IMAP is chosen instead (ruling 2), `go-imap` either goes on the allow-list with a ruling or the
  bridge becomes a second module (`bridge/` with its own `go.mod`) that spawns `brigade adapter supabase` from
  PATH; the adapter boundary makes that clean.
- **Secrets and state** under `$XDG_CONFIG_HOME/brigade/bridge/<team key>/` (0700): `secrets.json` (Slack
  tokens, Resend key, per-human email tokens; 0600) and `state.json` (session ids, the post ↔ message tables,
  polling high-water marks). Nothing of it in the repository; `.brigade.json` refuses secret-shaped members
  anyway (`internal/harness/teamfile`).
- **Rules carried over**: argument arrays, allow-listed environment, stderr-only diagnostics through the
  redacting logger, never `slog.Any`, no secret on argv or in `describe`-like output, strip `CLAUDE*` and
  `AI_AGENT` by prefix when the bridge is launched from a session's shell for a test.
- **Tests.** BAP side with the scripted fake adapter (`cmd/brigade-fake-adapter`): resume, conflict, rate
  limit, watch redelivery, ack after post, crash between post and ack. Transport side with an `httptest` Slack
  (Web API + a websocket server speaking Socket Mode envelopes) and an `httptest` Resend. Goldens for the
  rendered Slack text and email (headers included) and for the block parser: canonical block, shorthand, quoted
  block, forwarded mail, HTML-only, auto-reply, a forged `[brigade]` inside a sender body, an ambiguous `to:`.
  Live tests opt-in behind a `BRIGADE_TEST_LIVE`-style flag against a sandbox workspace and a test inbox, never
  in `make test`.

## 10. Rulings needed

1. **Principal model**: A (one principal per human; recommended) or B (one bridge principal).
2. **Email transport**: Resend Inbound polled (recommended; verify headers and verdicts in the spike), IMAP
   (new module), or a provider API.
3. **Where the first bridge runs**: a member's machine as a daemon, or a small always-on host.
4. **Enrolment**: operator-only (`brigade bridge enrol`), or self-service from Slack (`/brigade join`) once a
   workspace is trusted.
5. **Channel-mode sessions** (`#qa` as a session): in scope now, or after per-person sessions ship.
6. **Frame variant for human senders** (goldens change; unmeasured until a sweep covers it) now, or ship the
   bridge first with the existing wording and the `KIND` column.

## 11. Phases and sizes

| Phase | Work | Size |
| --- | --- | --- |
| P0 spike | Socket Mode + one DM'd human + Option B + the fake adapter, then the real team: one message from a Claude session reaches the DM, one thread reply comes back with `in-reply-to` on the frame. Same spike: one real mail through Resend Inbound to read what the retrieve endpoint returns (headers, verdicts, retention). | 2–3 days |
| P1 bridge core | enrolment and profiles (ruling 1), resume, drain and ack, state, the block renderer and parser with goldens, redaction, `bridge status` | 1 week |
| P2 Slack | DMs, threads, metadata, slash commands, reconnect, pacing, consent DM | 3–4 days |
| P3 Email | Resend polling, threading headers, quote stripping, auto-reply guard, spoofing controls (a)+(c), optional (b) | 1 week |
| P4 Receiving side | frame variant (ruling 6), `KIND` column, skill section, security.md and setup.md, CHANGELOG; one minor release | 2–3 days |
| P5 Operations | launchd and systemd examples, rotate-token runbook, `make` targets for the live tests | 1–2 days |

Slack is shippable after P0, P1, P2 and P4; email adds P3. About three to four weeks of one agent's work in
all, in the usual sequence of small pushes.

## 12. Out of scope, on purpose

Attachments and file transfer, broadcast or fan-out (one recipient per message stays), read receipts ("the
product never says delivered", 4.5.1), rich text, any approval flow (a message cannot approve anything,
4.5.15), verified identity (labels stay unverified; the principal is the identity), and hosting the bridge as a
service. Each of these is on the protocol's own "deliberately does not say" list (4.8) or on the project's.

## Sources read for this plan

Repository: `docs/protocol-v1.md` (4.1, 4.2, 4.4.2–4.4.9, 4.5, 4.7, 4.8, 4.9), `docs/security.md` (§1, §3, §7),
`supabase/migrations/20260830120000_brigade_schema.sql`, `internal/harness/frame/frame.go`,
`internal/harness/commands/send.go`, `internal/harness/inbound/pipeline.go`, `internal/harness/policy/policy.go`,
`internal/harness/teamfile`, `plugin/skills/team-messaging/SKILL.md`, `docs/allowed-deps.txt`, `go.mod`,
`.context/plans/codex-team-participation.md`.

External, 2026-10-03: Slack, [Using Socket Mode](https://docs.slack.dev/apis/events-api/using-socket-mode/) and
[Implementing slash commands](https://api.slack.com/interactivity/slash-commands) (slash commands cannot be
invoked in threads); Slack, [chat.postMessage](https://docs.slack.dev/reference/methods/chat.postMessage/) and
[Truncating really long messages](https://api.slack.com/changelog/2018-04-truncating-really-long-messages)
(40,000-character truncation; 3,000 per section block); Resend,
[Inbound](https://resend.com/features/inbound), [List Received Emails](https://resend.com/docs/api-reference/emails/list-received-emails),
[Retrieve Received Email](https://resend.com/docs/api-reference/emails/retrieve-received-email) and
[email.received](https://resend.com/docs/webhooks/emails/received) (the webhook carries no body; the API does).
