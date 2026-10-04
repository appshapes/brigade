// The mail connector contract (docs/mail-connectors.md). A connector stands between one mail provider and the
// gateway core, and the two exchange exactly two JSON shapes over HTTPS: a received mail, connector → core, and
// a mail to send, core → connector. One secret authenticates both directions: a bearer header, a basic-auth
// password, or the path segment after the endpoint's name, for a caller that can only be given a URL. The core
// never sees a provider's own webhook or API; a shipped connector wraps a Provider (providers/resend.ts,
// providers/postmark.ts) behind connector_serve.ts.

import { type InboundMail, isAddress, type OutboundMail } from "./mail.ts";

export const CONTRACT_VERSION = 1;

/** SendError is a failed send. `retryable` says whether a later attempt can succeed: by default a 429, a 5xx or
 * no answer at all (status 0) can, and anything else is this mail's fault. */
export class SendError extends Error {
  status: number;
  retryable: boolean;
  constructor(status: number, message: string, retryable?: boolean) {
    super(message);
    this.status = status;
    this.retryable = retryable ?? (status === 0 || status === 429 || status >= 500);
  }
}

/** WebhookRejected is a provider webhook that is not authentic. `status` is what the connector answers the
 * provider: 401 by default, 403 for a provider that stops retrying on it (Postmark). */
export class WebhookRejected extends Error {
  status: number;
  constructor(message: string, status = 401) {
    super(message);
    this.status = status;
  }
}

/** Provider is what a shipped connector wraps: the provider's webhook, a fetch of a received mail by the
 * provider's id (for a webhook that carries only the id), and a send. */
export interface Provider {
  name: string;
  /** webhook reads a provider's webhook request. It answers null when the event is not a received mail, the id
   * and, when the webhook carried the mail itself, the mail; it throws WebhookRejected when the request is not
   * authentic. */
  webhook(req: Request, rawBody: string): Promise<{ providerId: string; mail?: InboundMail } | null>;
  fetch(providerId: string): Promise<InboundMail>;
  send(mail: OutboundMail): Promise<{ providerId: string }>;
}

// ---- Authentication ----------------------------------------------------------------------------------------

/** bearerOf reads `Authorization: Bearer <token>`. */
export function bearerOf(req: Request): string | null {
  const m = /^Bearer\s+(\S+)\s*$/i.exec(req.headers.get("authorization") ?? "");
  return m ? m[1] : null;
}

/** basicPasswordOf reads the password of `Authorization: Basic <base64 user:password>`; the user is not checked. */
export function basicPasswordOf(req: Request): string | null {
  const m = /^Basic\s+(\S+)\s*$/i.exec(req.headers.get("authorization") ?? "");
  if (!m) return null;
  let decoded: string;
  try {
    decoded = atob(m[1]);
  } catch {
    return null;
  }
  const colon = decoded.indexOf(":");
  return colon < 0 ? null : decoded.slice(colon + 1);
}

/** pathSecretOf reads the path segment after `anchor` (…/mail-in/<secret>). */
export function pathSecretOf(req: Request, anchor: string): string | null {
  const segs = new URL(req.url).pathname.split("/").filter(Boolean);
  const i = segs.lastIndexOf(anchor);
  if (i < 0 || i + 1 >= segs.length) return null;
  try {
    return decodeURIComponent(segs[i + 1]);
  } catch {
    return null;
  }
}

/** authorized reports whether a request carries `secret` as a bearer, a basic-auth password or a path segment
 * after `anchor`. An empty secret authorizes nothing. */
export function authorized(req: Request, anchor: string, secret: string): boolean {
  if (!secret) return false;
  for (const got of [bearerOf(req), basicPasswordOf(req), pathSecretOf(req, anchor)]) {
    if (got !== null && constantTimeEqual(got, secret)) return true;
  }
  return false;
}

export function constantTimeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

// ---- The wire shapes ---------------------------------------------------------------------------------------

export type Parsed<T> = { ok: true; value: T } | { ok: false; reason: string };

/** InboundWire is a received mail as a connector POSTs it to the core. */
export interface InboundWire {
  brigade_mail: 1;
  /** The provider's id for the mail: the core's idempotency key. */
  id: string;
  from: string;
  from_name?: string | null;
  /** Every address the mail was delivered to, plus tags; the core keeps only mail addressed to its inbox. */
  to: string[];
  subject?: string;
  message_id?: string | null;
  in_reply_to?: string | null;
  references?: string[];
  text?: string;
  received_at?: string;
}

export function toInboundWire(m: InboundMail): InboundWire {
  return {
    brigade_mail: 1,
    id: m.providerId,
    from: m.from,
    from_name: m.fromName,
    to: m.to,
    subject: m.subject,
    message_id: m.messageId,
    in_reply_to: m.inReplyTo,
    references: m.references,
    text: m.text,
    received_at: m.receivedAt,
  };
}

export function fromInboundWire(raw: string): Parsed<InboundMail> {
  const d = parseObject(raw);
  if (!d.ok) return d;
  const o = d.value;
  if (o.brigade_mail !== CONTRACT_VERSION) return bad(`brigade_mail must be ${CONTRACT_VERSION}`);
  const id = str(o.id);
  if (!id) return bad("id is required");
  const from = (str(o.from) ?? "").toLowerCase();
  if (!isAddress(from)) return bad("from must be an email address");
  if (!Array.isArray(o.to) || o.to.some((a) => typeof a !== "string")) {
    return bad("to must be a list of addresses");
  }
  const to = [...new Set((o.to as string[]).map((a) => a.trim().toLowerCase()).filter(Boolean))];
  if (o.references !== undefined && o.references !== null) {
    if (!Array.isArray(o.references) || o.references.some((a) => typeof a !== "string")) {
      return bad("references must be a list of strings");
    }
  }
  for (const k of ["from_name", "subject", "message_id", "in_reply_to", "text", "received_at"]) {
    if (o[k] !== undefined && o[k] !== null && typeof o[k] !== "string") return bad(`${k} must be a string`);
  }
  return {
    ok: true,
    value: {
      providerId: id,
      from,
      fromName: str(o.from_name),
      to,
      subject: str(o.subject) ?? "",
      messageId: str(o.message_id),
      inReplyTo: str(o.in_reply_to),
      references: (o.references as string[] | undefined) ?? [],
      text: typeof o.text === "string" ? o.text : "",
      receivedAt: str(o.received_at) ?? new Date().toISOString(),
    },
  };
}

/** OutboundWire is a mail to send as the core POSTs it to a connector's send endpoint. */
export interface OutboundWire {
  brigade_mail: 1;
  from: string;
  to: string;
  reply_to: string | null;
  subject: string;
  text: string;
  headers: Record<string, string>;
}

export function toOutboundWire(m: OutboundMail): OutboundWire {
  return {
    brigade_mail: 1,
    from: m.from,
    to: m.to,
    reply_to: m.replyTo,
    subject: m.subject,
    text: m.text,
    headers: m.headers,
  };
}

export function fromOutboundWire(raw: string): Parsed<OutboundMail> {
  const d = parseObject(raw);
  if (!d.ok) return d;
  const o = d.value;
  if (o.brigade_mail !== CONTRACT_VERSION) return bad(`brigade_mail must be ${CONTRACT_VERSION}`);
  const from = str(o.from);
  if (!from) return bad("from is required");
  const to = (str(o.to) ?? "").toLowerCase();
  if (!isAddress(to)) return bad("to must be one email address");
  if (typeof o.subject !== "string") return bad("subject must be a string");
  if (typeof o.text !== "string") return bad("text must be a string");
  if (o.reply_to !== undefined && o.reply_to !== null && typeof o.reply_to !== "string") {
    return bad("reply_to must be a string");
  }
  const headers: Record<string, string> = {};
  if (o.headers !== undefined && o.headers !== null) {
    if (typeof o.headers !== "object" || Array.isArray(o.headers)) return bad("headers must be an object");
    for (const [k, v] of Object.entries(o.headers as Record<string, unknown>)) {
      if (typeof v !== "string") return bad(`header ${k} must be a string`);
      headers[k] = v;
    }
  }
  return {
    ok: true,
    value: { from, to, replyTo: str(o.reply_to), subject: o.subject, text: o.text, headers },
  };
}

function parseObject(raw: string): Parsed<Record<string, unknown>> {
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return bad("the body is not JSON");
  }
  if (typeof v !== "object" || v === null || Array.isArray(v)) return bad("the body is not a JSON object");
  return { ok: true, value: v as Record<string, unknown> };
}

function bad(reason: string): { ok: false; reason: string } {
  return { ok: false, reason };
}

function str(v: unknown): string | null {
  return typeof v === "string" && v !== "" ? v : null;
}

// ---- The core's send client --------------------------------------------------------------------------------

export interface SendClient {
  send(mail: OutboundMail): Promise<{ providerId: string }>;
}

/** sendClient is how the core sends a mail: one POST to the connector's send URL with the secret as a bearer.
 * A 2xx with an `id` is success; anything else is a SendError whose `retryable` the connector may state in its
 * JSON answer and which otherwise follows the status. */
export function sendClient(sendUrl: string, secret: string, fetchFn: typeof fetch = fetch): SendClient {
  return {
    async send(mail) {
      if (!sendUrl) throw new SendError(0, "BRIGADE_MAIL_SEND_URL is not set: run the installer", false);
      let r: Response;
      try {
        r = await fetchFn(sendUrl, {
          method: "POST",
          headers: { Authorization: `Bearer ${secret}`, "Content-Type": "application/json" },
          body: JSON.stringify(toOutboundWire(mail)),
        });
      } catch (e) {
        throw new SendError(0, `connector: ${e instanceof Error ? e.message : String(e)}`, true);
      }
      let d: Record<string, unknown> = {};
      try {
        const v = await r.json();
        if (typeof v === "object" && v !== null) d = v as Record<string, unknown>;
      } catch { /* no body */ }
      if (r.ok) {
        const id = str(d.id);
        if (!id) throw new SendError(502, "connector: the send answer carries no id", true);
        return { providerId: id };
      }
      const message = str(d.error) ?? `connector: send answered ${r.status}`;
      throw new SendError(r.status, message, typeof d.retryable === "boolean" ? d.retryable : undefined);
    },
  };
}
