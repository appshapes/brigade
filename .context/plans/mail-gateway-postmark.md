# The mail gateway's second provider: Postmark

Status: **plan only, not started** (Rjae, 2026-10-04: "The only follow-up I want to do at this time is 'a second mail
provider' … make a pass at an implementation plan … I don't want to start the implementation yet"). Follows card 48,
whose docs list "one provider" as a limit. Open a Trello card when the work starts; its number goes on the commits.

**Credentials.** `CLAUDE.user.md` now carries a Postmark **account login** (a user name and password). The gateway does
not use that: it needs a **Server API token**, which the Postmark UI shows under *Servers → the server → API Tokens*.
The installer reads it from the environment (`read -rs POSTMARK_SERVER_TOKEN`), never from argv; it is set as a
function secret and appears nowhere else. Neither the login nor the token goes in a test fixture, a doc, a card or a
plan (memory: fixtures never from credentials in context).

## 1. Facts about Postmark (read 2026-10-04; the ones marked *measure* are unconfirmed)

- **Inbound is a webhook that carries the whole mail.** The JSON has `From`, `FromName`, `FromFull {Email, Name,
  MailboxHash}`, `To`, `ToFull [{Email, Name, MailboxHash}]`, `Cc`/`CcFull`, `OriginalRecipient`, `Subject`,
  `MessageID` (Postmark's UUID), `Date`, `TextBody`, `HtmlBody`, `StrippedTextReply`, `Headers [{Name, Value}]`
  (`Message-ID`, `In-Reply-To`, `References` are in there), `Attachments [{Name, Content, ContentType,
  ContentLength}]`, `MessageStream: "inbound"`. No second API call is needed, unlike Resend's webhook, which carries
  only the id.
- **Plus addressing is native.** `user+tag@…` arrives with `MailboxHash: "tag"`. Our Reply-To tag
  (`<inbox local>+<message id>@<inbox domain>`) therefore works unchanged. *Measure:* whether `ToFull[].Email` keeps
  the `+tag` or strips it to the bare address (if stripped, the provider rebuilds it from `MailboxHash`).
- **The receiving address** is `<InboundHash>@inbound.postmarkapp.com`, read-only, per server: `GET /server` with the
  server token returns `InboundAddress` and `InboundHash`. A team that wants `brigade@in.their-domain` sets
  `InboundDomain` and adds one MX record, host `inbound.postmarkapp.com`, priority 10, on a subdomain (Postmark's
  recommendation), and enables SMTP on the server. Inbound works before account approval.
- **The webhook URL is set with the server token alone**: `PUT /server` with `InboundHookUrl`. One token covers
  everything the installer does. No account token.
- **No webhook signature.** Postmark says it does not support HMAC signatures, and inbound webhooks are not verified
  the way outbound ones can be. The documented control is **HTTP basic auth in the URL**
  (`https://user:password@host/path`), plus an IP allowlist from a support page whose addresses "can change for each
  attempt". Retries: 10, from 1 minute to 6 hours, on any non-200; a **403 stops the retries**.
- **Sending** is `POST https://api.postmarkapp.com/email` with `X-Postmark-Server-Token`: `From`, `To`, `Subject`,
  `TextBody`, `ReplyTo`, `Headers [{Name, Value}]`, `MessageStream` (default `outbound`). Response `MessageID`,
  `ErrorCode` (0 is success), `Message`. `406` is an inactive recipient (hard bounce or spam complaint). *Measure:*
  whether a custom `Message-ID` survives (our design does not depend on it: the tag is the key, as with Resend's
  relay) and that `In-Reply-To`/`References` custom headers are kept.
- **Before account approval** a server sends only to addresses on domains added and confirmed in the account. The
  `from` address needs a confirmed sender signature or domain. A live test that mails the owner's own domain works
  while approval is pending.
- Inbound attachments are capped at 35 MB cumulative; we drop attachments anyway.

## 2. Design

The provider seam already exists in name (`supabase/functions/_shared/providers/resend.ts` exports `Provider`), but
not in fact: `mail-in` and `mail-out` construct `resendProvider` directly and nothing reads `BRIGADE_MAIL_PROVIDER`
(the installer sets it to `resend`; the doc claims the functions honour it). The work is to make the seam real and
add one file behind it.

### 2.1 The seam

- `providers/provider.ts`: the `Provider` interface, `SendError` and `WebhookRejected` move here from `resend.ts`.
  The interface gains one optional member on the webhook result: `webhook(req, raw) → { providerId; mail?:
  InboundMail } | null`. A provider whose webhook carries the mail returns it; `mail-in` uses `hook.mail ?? await
  provider.fetch(hook.providerId)`. Resend is unchanged.
- `providers/index.ts`: `providerFromEnv(): Provider` reads `BRIGADE_MAIL_PROVIDER` (`resend` default for installed
  gateways whose secret predates the switch) and builds the one provider from its own secrets. An unknown name
  throws at startup with the name in the message. `mail-in` and `mail-out` call it; they stop importing `resend.ts`.
- `gateway.ts`, `mail.ts`, `block.ts`, the thread table, the migration and the pgTAP tests do not change. The
  `received` dedupe key becomes `mail:<provider>:<id>`? No: `providerId` values do not collide across providers
  within one team because a team has one provider; keep `mail:<id>`.

### 2.2 `providers/postmark.ts`

- **`webhook`**: authenticate, then parse. Authentication is the basic-auth password the installer put in the
  webhook URL: `Authorization: Basic base64(brigade:<secret>)`, compared in constant time against
  `BRIGADE_MAIL_WEBHOOK_SECRET`; a missing or wrong header throws `WebhookRejected`. Parse the JSON; a body that is
  not an inbound message (no `MessageID` or `MessageStream !== "inbound"`) returns `null`. Build the `InboundMail`:
  `providerId` = `MessageID`; `from` = `FromFull.Email` lowercased, `fromName` = `FromFull.Name` or null;
  `to` = every `ToFull[].Email` and `CcFull[].Email` plus `OriginalRecipient`, lowercased, deduped, each rebuilt as
  `local+MailboxHash@domain` when a `MailboxHash` is present and the `Email` lacks it; `subject`; `messageId`,
  `inReplyTo`, `references` from `Headers` through the existing `headerValue`/`parseMessageIds` (the array is
  folded into a record first); `text` = `TextBody`, else `htmlToText(HtmlBody)`; `receivedAt` = `Date` parsed, else
  now. `StrippedTextReply` is ignored: our `stripQuoted` keeps the rules provider-blind.
- **`fetch`**: `GET /messages/inbound/<id>/details` with the server token, mapped the same way. Used only when a
  webhook arrived without a body (it will not; this keeps the interface honest and is unit-tested).
- **`send`**: `POST /email` with `From`, `To`, `Subject`, `TextBody`, `ReplyTo` (the tag), `Headers` from
  `mail.headers` as `[{Name, Value}]`, `MessageStream: "outbound"`. Return `{ providerId: MessageID }`. Errors:
  HTTP 429 and 5xx are `SendError(retryable)`; 422 with `ErrorCode` 406 (inactive recipient) and every other 4xx are
  final, so `mail-out` tells the sender and acknowledges. Postmark answers 200 with a non-zero `ErrorCode` in some
  cases: treat `ErrorCode !== 0` as a final error with `Message` in the text.
- **`mail-in`'s answer codes.** 200 once authentic (as today). For `WebhookRejected`: keep 401 for consistency with
  Resend, or answer 403 so Postmark stops retrying an unauthenticated POST? *Ruling 2 below.*

### 2.3 The installer

One script, `scripts/gateway-install.sh`, gains `--provider resend|postmark` (`make gateway-install
provider=postmark …`; default `resend`, so today's command is unchanged). The Resend-only step 3 and the secrets
block move under a `case "$provider"`; everything else (migration, functions, member, tick, team file) is shared.

Postmark's step 3:

1. `GET https://api.postmarkapp.com/server` with `X-Postmark-Server-Token` from a 0600 header file: read
   `InboundAddress`. The inbox is, in order: `--inbox` (a custom inbound domain the admin set up), the
   `BRIGADE_MAIL_INBOX` secret an installed gateway already has (the re-run rule from 93ea26b), else
   `InboundAddress`.
2. Generate the webhook secret (`od … /dev/urandom`, as the tick token is). `PUT /server` with
   `InboundHookUrl: https://brigade:<secret>@<ref>.supabase.co/functions/v1/mail-in`. The secret reaches Postmark
   once, in that URL; Postmark stores it. The response echoes the URL, so the script must not print it.
3. Secrets: `BRIGADE_MAIL_PROVIDER=postmark`, `POSTMARK_SERVER_TOKEN`, `BRIGADE_MAIL_WEBHOOK_SECRET`, plus the shared
   five (team ref, from, inbox, public address, tick token). No `RESEND_*`.
4. The `from` check (`*@*.*`) stays; the error text names "a sender signature or domain confirmed in Postmark".
5. The summary's "receiving address (Resend)" line names the provider.

Idempotent as today: a re-run keeps the member and the inbox, re-sets the webhook URL with a fresh secret, rotates the
tick token. `--dry-run` prints the plan. Shellcheck 0.11 locally and 0.9.0 through Docker, as CLAUDE.md asks.

### 2.4 Security

- The control on inbound is a password over TLS, not a signature. Written in `docs/security.md` §14 as a fourth
  mail bullet: anyone holding the webhook URL can put text in front of the team's sessions, the same exposure the
  public address already has, so the webhook secret is a secret like the tick token. Rotated by a re-run.
- The IP allowlist is not implemented: the addresses are on a support page, change per attempt, and would need a
  fetch at request time or a hard-coded list. Named as a later hardening.
- "More people read the mail" (security.md §14) names Resend; it becomes "the mail provider (Resend or Postmark)".
- *Measure before relying on it:* the Supabase functions gateway forwards an `Authorization: Basic …` header to a
  function deployed with `--no-verify-jwt`. If it strips or rejects it, the fallback is the secret as a path
  segment, `…/functions/v1/mail-in/<secret>` (Supabase routes any sub-path to the function), compared the same way.

## 3. Work items, in order of pushes

| # | Work | Size |
| --- | --- | --- |
| 1 | `providers/provider.ts` (types moved), `providers/index.ts` (`providerFromEnv`), `mail-in`/`mail-out` on the seam; Resend behaviour unchanged; `make functions-check` green | small |
| 2 | `providers/postmark.ts` with `postmark_test.ts`: a fixture webhook built from Postmark's documented example (not from any real mail), the auth accept/reject, the `MailboxHash` rebuild, `TextBody` vs `HtmlBody`, header folding, `send`'s request shape and error mapping | medium |
| 3 | The installer's `--provider` switch and the Makefile's `provider=`; the usage lines; shellcheck both versions | medium |
| 4 | Docs: `docs/mail-gateway.md` (TL;DR step 1 "a Resend or Postmark account"; "What you need" per provider; "What the command does" step 3 per provider; "An address of the team's own" gains the Postmark MX path; "Writing your own gateway" last paragraph names `providers/index.ts`; Limits drops "One provider"); `docs/security.md` §14; `README.md` one clause; `CHANGELOG.md` under Unreleased; `docs/development.md` layout row | small |
| 5 | Live measurement (section 5), the fixes it finds, the plan's "As built" note | half a day |
| 6 | Release (a minor): name the changelog section, `make release`, verify, update configurations, comment and move the card | small |

Items 1 to 4 need no network beyond `npx` for Deno and the Supabase CLI already cached; item 5 does.

## 4. Tests

- Unit (Deno, `make functions-check`, in CI's `fast` job): `postmark_test.ts` as above; `index_test.ts` for the
  provider switch (each name, the default, an unknown name); `mail-in` keeps working with a provider whose webhook
  returns the mail (a fake provider in `gateway_test.ts` or a new `mail_in_test.ts` over the pure parts).
- No pgTAP change: the schema is provider-blind.
- The Go side does not change (`teamfile` knows only the address).
- The fixture addresses stay placeholders (`a1b2c3d4@example.resend.app` style; for Postmark,
  `0123456789abcdef0123456789abcdef01234567@inbound.postmarkapp.com`).

## 5. Live measurement

On **thinktech-brigade** (`hsopqzvznxajeyswzztv`), which has no gateway yet, so nothing live is disturbed and the
appshapes team keeps Resend. Prerequisites the owner provides: a Postmark server, its Server API token, a sender
signature or confirmed domain for the `from` address, and a recipient on a confirmed domain (approval pending is
fine for that). Steps:

1. `make gateway-install project=hsopqzvznxajeyswzztv provider=postmark from='Brigade <…>' dry=1`, then for real.
2. Mail the printed address with `to: <session>` from a confirmed domain's mailbox → the session receives it;
   record latency and the `[brigade]` block.
3. The session answers with `--reply-to` → the mail arrives threaded; check the Reply-To tag and whether the
   Message-ID was replaced.
4. Reply to that mail → the session receives it with `in-reply-to` and `hops=2` (the `MailboxHash` path).
5. A mail with no `to:` → the roster bounce; a mail to the slack-gateway's tail → not in the roster (P27-4's fix).
6. A POST to `mail-in` without the basic-auth header → 401 (or 403 per ruling 2) and nothing stored; one with it and
   a non-inbound body → 200 `ignored`.
7. Uninstall is not scripted (card 48 left it out for Resend too): deleting the thinktech gateway afterwards means
   `cron.unschedule('brigade_gateway_tick')`, deleting the `brigade_gateway.gateways` row, closing the session,
   revoking the member, unsetting the secrets, removing the webhook URL, and taking `gateway.email` out of
   `.brigade.json`. Write that down as a runbook in the doc while doing it, or leave the gateway installed.

Record the measurements in this file under "As built" and in the card's comments, as card 48 did.

## 6. Rulings needed

1. **One installer with `--provider`, or a second script** (`scripts/gateway-install-postmark.sh`, as Slack has)?
   Recommended: one script; the shared steps are five of seven and the two must not drift.
2. **Unauthenticated webhook POSTs answer 401 or 403?** 403 stops Postmark's ten retries, which is right for an
   impostor and wrong for a misconfigured secret (the admin would see "Inbound Error" once and re-run the installer,
   which is also fine). Recommended: 403 for Postmark, 401 kept for Resend.
3. **Does the appshapes team move to Postmark**, or stay on Resend with Postmark measured on thinktech only?
   Recommended: stay; the public address's Workspace routing points at the Resend address and works.
4. **A card for this work**: open it when starting (`/trello-create`), the number goes on the commits.

## 7. Out of scope, on purpose

Attachments, auto-reply detection, sender verification and allow-lists, the IP allowlist, more than one provider per
team, SMTP instead of the API, Postmark's bounce and delivery webhooks (we learn of a final failure from the send
call's error, which is enough for "tell the sender"), and `StrippedTextReply` in place of our own quote stripping.

## Sources

Postmark developer docs, read 2026-10-04: Inbound webhook (payload fields, retries), Webhooks overview (basic auth
in the URL, no HMAC signatures, inbound webhooks not verified like outbound), Server API (`GET`/`PUT /server`,
`InboundHookUrl`, `InboundAddress`, `InboundHash`, `InboundDomain`), Inbound domain forwarding (MX
`inbound.postmarkapp.com` priority 10, subdomain recommended, SMTP enabled), Parse an email (the hash address,
`MailboxHash`, 35 MB attachments), Email API (`POST /email` fields, `406`), the account approval article (own
domains only until approved; inbound works before approval). The custom Message-ID article returned 404; measure it.
Repository: `supabase/functions/_shared/providers/resend.ts`, `mail-in/index.ts`, `mail-out/index.ts`,
`scripts/gateway-install.sh`, `docs/mail-gateway.md`, `docs/security.md` §14.
