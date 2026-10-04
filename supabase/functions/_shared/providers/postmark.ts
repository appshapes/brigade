// The Postmark provider, wrapped by the mail-connector-postmark function. Postmark's inbound webhook carries the
// whole mail, so no fetch follows it; it carries no signature, so the connector authenticates it by the basic-auth
// password the installer put in the webhook URL (or the same secret as a path segment), and answers 403 to anything
// else, which is the status that stops Postmark's retries. Sending is one POST to /email with the server token.

import {
  headerValue,
  htmlToText,
  type InboundMail,
  type OutboundMail,
  parseAddress,
  parseMessageIds,
} from "../mail.ts";
import { authorized, type Provider, SendError, WebhookRejected } from "../connector.ts";

export interface PostmarkOptions {
  serverToken: string;
  /** The webhook password (`https://brigade:<password>@…` in the server's InboundHookUrl). Without it every
   * webhook is rejected: an unauthenticated inbound path would let anyone put text in front of the team. */
  webhookSecret: string | null;
  fetchFn?: typeof fetch;
  baseUrl?: string;
}

const API = "https://api.postmarkapp.com";

/** postmarkInbound maps an inbound webhook payload (or an inbound message's details) to an InboundMail. A
 * recipient with a MailboxHash whose Email lost the `+tag` gets it back, so the gateway's Reply-To tag resolves. */
export function postmarkInbound(doc: Record<string, unknown>): InboundMail {
  const fromFull = (doc.FromFull ?? {}) as Record<string, unknown>;
  const from = parseAddress(String(fromFull.Email ?? doc.From ?? ""));
  const fromName = String(fromFull.Name ?? doc.FromName ?? "").trim() || from.name;
  const to = new Set<string>();
  for (const key of ["ToFull", "CcFull", "BccFull"]) {
    for (const r of (doc[key] as unknown[] | undefined) ?? []) {
      const o = (r ?? {}) as Record<string, unknown>;
      const a = withHash(String(o.Email ?? ""), String(o.MailboxHash ?? ""));
      if (a) to.add(a);
    }
  }
  if (typeof doc.OriginalRecipient === "string" && doc.OriginalRecipient) {
    to.add(withHash(doc.OriginalRecipient, String(doc.MailboxHash ?? "")));
  }
  const headers: Record<string, string> = {};
  for (const h of (doc.Headers as unknown[] | undefined) ?? []) {
    const o = (h ?? {}) as Record<string, unknown>;
    if (typeof o.Name === "string" && !(o.Name in headers)) headers[o.Name] = String(o.Value ?? "");
  }
  const text = typeof doc.TextBody === "string" && doc.TextBody.trim() !== ""
    ? doc.TextBody
    : htmlToText(String(doc.HtmlBody ?? ""));
  const date = new Date(String(doc.Date ?? ""));
  return {
    providerId: String(doc.MessageID ?? ""),
    from: from.address,
    fromName,
    to: [...to],
    subject: String(doc.Subject ?? ""),
    messageId: parseMessageIds(headerValue(headers, "message-id"))[0] ?? null,
    inReplyTo: parseMessageIds(headerValue(headers, "in-reply-to"))[0] ?? null,
    references: parseMessageIds(headerValue(headers, "references")),
    text,
    receivedAt: Number.isNaN(date.getTime()) ? new Date().toISOString() : date.toISOString(),
  };
}

/** withHash lowercases an address and puts a MailboxHash back as its `+tag` when the address lacks one. */
export function withHash(email: string, hash: string): string {
  const a = email.trim().toLowerCase();
  if (!a) return "";
  const at = a.lastIndexOf("@");
  if (!hash || at < 0 || a.slice(0, at).includes("+")) return a;
  return `${a.slice(0, at)}+${hash.trim().toLowerCase()}@${a.slice(at + 1)}`;
}

export function postmarkProvider(opts: PostmarkOptions): Provider {
  const f = opts.fetchFn ?? fetch;
  const base = opts.baseUrl ?? API;
  const auth = { "X-Postmark-Server-Token": opts.serverToken, Accept: "application/json" };

  return {
    name: "postmark",

    webhook(req, rawBody) {
      if (!opts.webhookSecret) {
        return Promise.reject(new WebhookRejected("the webhook secret is not configured", 403));
      }
      if (!authorized(req, "webhook", opts.webhookSecret)) {
        return Promise.reject(
          new WebhookRejected("the request does not carry this connector's webhook secret", 403),
        );
      }
      let doc: unknown;
      try {
        doc = JSON.parse(rawBody);
      } catch {
        return Promise.resolve(null);
      }
      if (typeof doc !== "object" || doc === null || Array.isArray(doc)) return Promise.resolve(null);
      const d = doc as Record<string, unknown>;
      if (typeof d.MessageID !== "string" || !d.MessageID || !(d.FromFull || d.From)) {
        return Promise.resolve(null);
      }
      if (typeof d.MessageStream === "string" && d.MessageStream !== "inbound") return Promise.resolve(null);
      return Promise.resolve({ providerId: d.MessageID, mail: postmarkInbound(d) });
    },

    async fetch(providerId) {
      const r = await f(`${base}/messages/inbound/${encodeURIComponent(providerId)}/details`, {
        headers: auth,
      });
      if (!r.ok) throw new Error(`postmark: inbound message details answered ${r.status}`);
      const d = (await r.json()) as Record<string, unknown>;
      return postmarkInbound({ ...d, MessageID: d.MessageID ?? providerId });
    },

    async send(mail) {
      const body: Record<string, unknown> = {
        From: mail.from,
        To: mail.to,
        Subject: mail.subject,
        TextBody: mail.text,
        MessageStream: "outbound",
        Headers: Object.entries(mail.headers).map(([Name, Value]) => ({ Name, Value })),
      };
      if (mail.replyTo) body.ReplyTo = mail.replyTo;
      const r = await f(`${base}/email`, {
        method: "POST",
        headers: { ...auth, "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      let d: Record<string, unknown> = {};
      try {
        const v = await r.json();
        if (typeof v === "object" && v !== null) d = v as Record<string, unknown>;
      } catch { /* no body */ }
      const code = typeof d.ErrorCode === "number" ? d.ErrorCode : 0;
      if (!r.ok || code !== 0) {
        const detail = `${code ? ` (error ${code})` : ""}${d.Message ? ": " + String(d.Message) : ""}`;
        throw new SendError(
          r.status,
          `postmark: send answered ${r.status}${detail}`,
          r.status === 429 || r.status >= 500,
        );
      }
      return { providerId: String(d.MessageID ?? "") };
    },
  };
}
