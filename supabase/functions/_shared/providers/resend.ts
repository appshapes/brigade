// The Resend provider: the inbound webhook (Svix-signed, carrying only ids), the fetch of a received mail's
// body and headers, and the send. Everything provider-specific lives here; the gateway core sees only the
// shapes in ../mail.ts. A second provider is a second file exporting the same Provider.

import {
  headerValue,
  htmlToText,
  type InboundMail,
  type OutboundMail,
  parseAddress,
  parseMessageIds,
} from "../mail.ts";
import { verifySvix } from "../svix.ts";

export interface Provider {
  name: string;
  /** webhook reads a provider's webhook request. It answers null when the event is not a received mail, and
   * throws WebhookRejected when the request is not authentic. */
  webhook(req: Request, rawBody: string): Promise<{ providerId: string } | null>;
  fetch(providerId: string): Promise<InboundMail>;
  /** send delivers one mail; a SendError says whether a retry can succeed. */
  send(mail: OutboundMail): Promise<{ providerId: string }>;
}

export class WebhookRejected extends Error {}

export class SendError extends Error {
  retryable: boolean;
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.retryable = status === 429 || status >= 500;
  }
}

export interface ResendOptions {
  apiKey: string;
  /** The webhook signing secret (`whsec_…`). Without it every webhook is rejected: an unsigned inbound path
   * would let anyone on the internet put text in front of the team's sessions. */
  webhookSecret: string | null;
  fetchFn?: typeof fetch;
  baseUrl?: string;
}

const API = "https://api.resend.com";

export function resendProvider(opts: ResendOptions): Provider {
  const f = opts.fetchFn ?? fetch;
  const base = opts.baseUrl ?? API;
  const auth = { Authorization: `Bearer ${opts.apiKey}` };

  return {
    name: "resend",

    async webhook(req, rawBody) {
      if (!opts.webhookSecret) throw new WebhookRejected("the webhook signing secret is not configured");
      if (!(await verifySvix(opts.webhookSecret, req.headers, rawBody))) {
        throw new WebhookRejected("the webhook signature does not verify");
      }
      let event: { type?: string; data?: { email_id?: string } };
      try {
        event = JSON.parse(rawBody);
      } catch {
        throw new WebhookRejected("the webhook body is not JSON");
      }
      if (event.type !== "email.received" || !event.data?.email_id) return null;
      return { providerId: event.data.email_id };
    },

    async fetch(providerId) {
      const r = await f(`${base}/emails/receiving/${encodeURIComponent(providerId)}`, { headers: auth });
      if (!r.ok) throw new Error(`resend: retrieve received email answered ${r.status}`);
      const d = await r.json();
      const from = parseAddress(String(d.from ?? ""));
      const headers = (d.headers ?? {}) as Record<string, unknown>;
      const to = ([] as string[])
        .concat(d.to ?? [], d.received_for ?? [])
        .map((a: string) => parseAddress(String(a)).address);
      const text = typeof d.text === "string" && d.text.trim() !== ""
        ? d.text
        : htmlToText(String(d.html ?? ""));
      const messageId = parseMessageIds(d.message_id ?? headerValue(headers, "message-id"))[0] ?? null;
      return {
        providerId,
        from: from.address,
        fromName: from.name,
        to: [...new Set(to)],
        subject: String(d.subject ?? ""),
        messageId,
        inReplyTo: parseMessageIds(headerValue(headers, "in-reply-to"))[0] ?? null,
        references: parseMessageIds(headerValue(headers, "references")),
        text,
        receivedAt: String(d.created_at ?? new Date().toISOString()),
      };
    },

    async send(mail) {
      const body: Record<string, unknown> = {
        from: mail.from,
        to: [mail.to],
        subject: mail.subject,
        text: mail.text,
        headers: mail.headers,
      };
      if (mail.replyTo) body.reply_to = [mail.replyTo];
      const r = await f(`${base}/emails`, {
        method: "POST",
        headers: { ...auth, "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        let detail = "";
        try {
          const e = await r.json();
          detail = String(e.message ?? e.name ?? "");
        } catch { /* no body */ }
        throw new SendError(r.status, `resend: send answered ${r.status}${detail ? ": " + detail : ""}`);
      }
      const d = await r.json();
      return { providerId: String(d.id) };
    },
  };
}
