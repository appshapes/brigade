# Mail connectors: how a mail provider plugs into the gateway

The [mail gateway](mail-gateway.md) has two halves. The **core** is Brigade's: it knows the team, the sessions, the
threads and the rules, and never a mail provider. A **connector** stands between the core and one provider. Resend
and Postmark ship as connectors and the installer deploys them for you. A team on any other provider writes a
connector of its own, in any language, anywhere with HTTPS, and nothing in Brigade changes. This page is the
contract between the two halves.

```
 person ──mail──▶ provider ──webhook──▶ connector ──POST inbound mail, bearer secret──▶ core (mail-in) ──▶ session
 session ──▶ core (mail-out, every minute) ──POST mail to send, bearer secret──▶ connector ──provider API──▶ person
```

## The shape of a connector

A connector does two things:

1. **Receives mail from the provider** (a webhook, a poll, anything), turns each mail into the inbound shape
   below, and POSTs it to the core's inbound URL.
2. **Serves a send endpoint** that accepts the outbound shape below, sends the mail through the provider, and
   answers with the provider's id.

The shipped connectors are each one file of about a hundred lines over a shared handler:
`supabase/functions/_shared/providers/resend.ts`, `supabase/functions/_shared/providers/postmark.ts`,
`supabase/functions/_shared/connector_serve.ts`.

## One secret, both directions

The installer mints one **connector secret**. The core sends it with every mail to send; the connector sends it
with every mail it forwards. Either side accepts it in any of three places:

- `Authorization: Bearer <secret>`
- `Authorization: Basic <base64 of anything:secret>` (the password; the user name is not checked)
- the path segment after the endpoint's name: `…/mail-in/<secret>`, `…/send/<secret>`, for a tool that can only
  be given a URL

For a shipped connector you never see the secret: it lives in the project's function secrets and the installer
mints a fresh one, for the core and the connector together, on every run. For your own, `make gateway-install
provider=external … secret_file=<path>` writes it, with the core's inbound URL, to a file you name outside the
repository. Re-running the installer reads it back from that file, so your connector keeps working; `rotate=1`
mints a new one, which you then configure again.

Anyone holding the secret can put text in front of the team's sessions, which the public address already allows,
and can send mail from the team's address through the connector, which the provider's own key already allows.
Treat it like the provider's key.

## Inbound: connector to core

`POST https://<ref>.supabase.co/functions/v1/mail-in`, `Content-Type: application/json`:

```json
{
  "brigade_mail": 1,
  "id": "the provider's id for this mail",
  "from": "alice@example.com",
  "from_name": "Alice",
  "to": ["a1b2c3d4+1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e@example.resend.app"],
  "subject": "Re: [brigade/team] Please retest",
  "message_id": "<CA+abc@mail.example.com>",
  "in_reply_to": "<0f0f0f0f@relay.example>",
  "references": ["<0f0f0f0f@relay.example>"],
  "text": "Retested, works.\n\nOn Sat, Oct 4 Brigade wrote:\n> please retest",
  "received_at": "2026-10-04T14:00:00Z"
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `brigade_mail` | yes | the contract version, `1` |
| `id` | yes | the provider's id for the mail; the core's idempotency key, so a retried webhook does not deliver twice |
| `from` | yes | the sender's address |
| `from_name` | no | the sender's display name |
| `to` | yes | every address the mail was delivered to, **with its `+tag` kept**: the core keeps only mail addressed to its inbox, and a reply names the message it answers in the tag |
| `subject` | no | the subject |
| `message_id`, `in_reply_to`, `references` | no | the mail headers, with their angle brackets; the core threads by them when the tag is missing |
| `text` | no | the plain-text body; when the mail has none, the HTML reduced to text. The core strips what the client quoted |
| `received_at` | no | when the provider received it, RFC 3339 |

The core answers `200` once the request is authentic and well-formed, whatever it did with the mail (delivered,
duplicate, ignored because not addressed to the inbox, bounced to the sender); `401` when the secret is missing
or wrong; `400` when the body is not this shape, with the reason.

What the connector answers its provider: `200` when the core answered `200` or `400`, so the provider does not
retry a mail that will not go through; a `5xx` when the core answered `401`, a `5xx`, or nothing, so the provider
retries and the failure shows in its dashboard. Verify the provider's webhook the provider's way before
forwarding anything; a webhook that fails that check gets the answer that stops the provider retrying (Postmark:
`403`).

## Outbound: core to connector

The core POSTs to the connector's **send URL** (`BRIGADE_MAIL_SEND_URL`), `Content-Type: application/json`:

```json
{
  "brigade_mail": 1,
  "from": "Brigade <brigade@example.com>",
  "to": "alice@example.com",
  "reply_to": "a1b2c3d4+1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e@example.resend.app",
  "subject": "[brigade/team] Please retest",
  "text": "frank-reviewer (sam@example.com, unverified) on Brigade team team wrote:\n\n…",
  "headers": { "Auto-Submitted": "auto-generated", "In-Reply-To": "<CA+abc@mail.example.com>" }
}
```

The connector sends it as `text/plain`, with `Reply-To` set to `reply_to` **exactly as given** (threading rides on
it: a reply from any client comes back to the tagged address), the `headers` passed through, and no HTML. It
answers:

- `200` with `{"id": "<the provider's id>"}` when the provider accepted the mail;
- otherwise a non-2xx status with `{"error": "<why>", "retryable": true|false}`. The core retries a `429`, a
  `5xx` or no answer on its next tick, a minute later, and treats any other status as final: it tells the sending
  session and acknowledges the message. `retryable` in the body overrides the status.

Anything other than a well-formed body with the secret is refused: `401` without the secret, `400` for a body that
is not this shape.

## Checking a connector

```sh
make mail-connector-check send_url=<your send URL> secret_file=<the file the installer wrote>
```

checks the send endpoint from the outside, as the core calls it: no secret is refused with `401`, a malformed body
with `400`. Add `send_to=<address> from='Name <address>'` to send one real mail and check the answer carries an id.
The inbound half is checked end to end: mail the connector's address with a first line `to: <session>` and watch
the session receive it, as [docs/mail-gateway.md](mail-gateway.md) describes.

## The shipped connectors

- **Resend** (`mail-connector-resend`). Resend's webhook carries only an id and is signed the Standard Webhooks
  way; the connector verifies the signature, fetches the mail from Resend, and forwards it. Sends go through
  Resend's send API.
- **Postmark** (`mail-connector-postmark`). Postmark's inbound webhook carries the whole mail and no signature;
  the installer puts a password in the webhook URL (`https://brigade:<password>@…`) and the connector refuses
  anything else with `403`, which stops Postmark's retries. Plus addressing arrives as `MailboxHash`; the
  connector puts the tag back on the address. Sends go through Postmark's email API with the server token.

Both are deployed into the team's own Supabase project beside the core by `make gateway-install`, and read their
provider's key from the project's function secrets. A third shipped connector is one provider file beside these
two and a case in the installer.

## Writing your own

Any HTTPS endpoint will do: a Cloudflare Worker, a function in the team's Supabase project deployed with the
Supabase CLI, a small server, a no-code automation that can POST JSON. The whole job is the two shapes above and
the secret. Then:

```sh
make gateway-install project=<ref> from='Brigade <brigade@your-domain.com>' \
  provider=external send_url=https://your-connector.example/send \
  inbox=<the address your connector receives at> secret_file=/absolute/path/outside/the/repository
```

Configure the two lines the secret file holds in your connector, run `make mail-connector-check`, and send the
first mail.
