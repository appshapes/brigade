// The Resend provider, wrapped by the mail-connector-resend function: the inbound webhook (Svix-signed, carrying
// only ids), the fetch of a received mail's body and headers, and the send. Everything Resend-specific lives
// here; the connector and the core see only the shapes in ../mail.ts and ../connector.ts.

import {
  headerValue,
  htmlToText,
  type InboundMail,
  type OutboundMail,
  parseAddress,
  parseMessageIds,
} from "../mail.ts";
import { type Provider, SendError, WebhookRejected } from "../connector.ts";
import { verifySvix } from "../svix.ts";

export type { InboundMail, OutboundMail };

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
