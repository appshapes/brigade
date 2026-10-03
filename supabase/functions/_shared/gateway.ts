// The gateway core: pure functions over mail and Brigade envelopes, with the database behind a small lookup
// interface so every rule here is unit-tested without a connection. Inbound: resolve where a person's mail
// goes and build the Brigade body. Outbound: resolve where a session's message goes and render the mail.
// The precedence a person's mail is resolved by: an explicit block (or `to:` line) they typed; then the reply
// address tag and the mail headers, through the thread table; then a block quoted from the mail they answer.

import { BlockError, escapeBlockLines, type Fields, lastBlock, parseLeading, renderBlock } from "./block.ts";
import { domainOf, type InboundMail, isAddress, localOf, type OutboundMail, stripQuoted } from "./mail.ts";

export const GATEWAY_VERSION = "0.1.0";
export const SESSION_NAME = "mail-gateway";
export const HARNESS = "gateway-email";
export const MAX_BODY_BYTES = 16384;
export const MAX_SUMMARY_CHARS = 200;

export interface Envelope {
  message_id: string;
  team_ref: string;
  sender: { principal_ref: string; human_label?: string | null; session_id: string; session_name: string };
  recipient_session_id: string;
  summary?: string | null;
  body: string;
  reply_to?: string | null;
  hop_count: number;
  created_at: string;
}

export interface SessionRecord {
  session_id: string;
  session_name: string;
  session_description?: string | null;
  principal_ref: string;
  human_label?: string | null;
  state: string;
  inbound?: string | null;
  harness?: string | null;
}

export interface ThreadRow {
  brigade_message_id: string;
  direction: "in" | "out";
  session_id: string;
  address: string;
  mail_message_id: string | null;
  provider_mail_id: string | null;
  subject: string | null;
}

export interface ThreadLookup {
  byBrigadeId(id: string): Promise<ThreadRow | null>;
  byMailIds(ids: string[]): Promise<ThreadRow | null>;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** description is the gateway session's `↳` line: the one place the roster tells a model and a person where
 * mail goes. ≤ 256 code points (docs/protocol-v1.md 4.4.2). */
export function description(inbox: string): string {
  return `email gateway: a person writes to ${inbox} with a first line "to: <session>"; a session writes to me with the same line, "to: <address>"`;
}

/** replyTag is the Reply-To address of an outbound mail: the inbox with the Brigade message id as a plus tag,
 * so a reply names the message it answers in its own To header, whatever the mail client did to the rest. */
export function replyTag(inbox: string, messageId: string): string {
  return `${localOf(inbox)}+${messageId}@${domainOf(inbox)}`;
}

/** tagOf finds the message id a reply address carries, when one of the delivery addresses is the inbox with
 * a plus tag. */
export function tagOf(addresses: string[], inbox: string): string | null {
  const local = localOf(inbox).toLowerCase();
  const domain = domainOf(inbox);
  for (const a of addresses) {
    if (domainOf(a) !== domain) continue;
    const l = localOf(a).toLowerCase();
    if (!l.startsWith(local + "+")) continue;
    const tag = l.slice(local.length + 1);
    if (UUID.test(tag)) return tag;
  }
  return null;
}

/** resolveSession finds the session an argument names: the id in full, or a unique tail of at least five
 * characters, as `brigade send` does (internal/harness/commands/send.go). Names are never addresses. */
export function resolveSession(
  arg: string,
  sessions: SessionRecord[],
): { session: SessionRecord } | { error: string } {
  const a = arg.trim().toLowerCase();
  const exact = sessions.find((s) => s.session_id.toLowerCase() === a);
  if (exact) return { session: exact };
  if (a.length < 5) {
    return {
      error: `"${arg}" is too short to name a session: give at least the last five characters of its id`,
    };
  }
  const tails = sessions.filter((s) => s.session_id.toLowerCase().endsWith(a));
  if (tails.length === 1) return { session: tails[0] };
  if (tails.length > 1) {
    return {
      error: `these characters end ${tails.length} session ids; use one of them in full: ${
        tails.map((s) => s.session_id).join(", ")
      }`,
    };
  }
  return { error: `no session ends with "${arg}"` };
}

export type Inbound =
  | { kind: "command"; command: string; text: string }
  | {
    kind: "message";
    target: string;
    replyTo: string | null;
    summary: string | null;
    text: string;
    source: string;
  }
  | { kind: "unaddressed"; text: string }
  | { kind: "invalid"; reason: string };

/** resolveInbound decides what a received mail is and where it goes. */
export async function resolveInbound(
  mail: InboundMail,
  inbox: string,
  threads: ThreadLookup,
): Promise<Inbound> {
  let leading: { fields: Fields | null; rest: string };
  try {
    leading = parseLeading(mail.text);
  } catch (e) {
    if (e instanceof BlockError) return { kind: "invalid", reason: e.message };
    throw e;
  }
  const { body, quoted } = stripQuoted(leading.rest);
  const f = leading.fields ?? {};
  if (f.command) return { kind: "command", command: f.command.trim().toLowerCase(), text: body };

  let target: string | null = f.to ?? null;
  let replyTo: string | null = f["reply-to"] ?? null;
  const summary = f.summary ?? null;
  let source = target ? "block" : "";

  if (!target) {
    const tag = tagOf(mail.to, inbox);
    if (tag) {
      const row = await threads.byBrigadeId(tag);
      if (row && row.direction === "out") {
        target = row.session_id;
        replyTo = replyTo ?? row.brigade_message_id;
        source = "reply-address";
      }
    }
  }
  if (!target) {
    const ids = [mail.inReplyTo, ...mail.references].filter((x): x is string => !!x);
    const own = /brigade\.([0-9a-f-]{36})@/i.exec(ids.join(" "));
    const row = own ? await threads.byBrigadeId(own[1]) : ids.length ? await threads.byMailIds(ids) : null;
    if (row && row.direction === "out") {
      target = row.session_id;
      replyTo = replyTo ?? row.brigade_message_id;
      source = "headers";
    }
  }
  if (!target) {
    const q = lastBlock(quoted);
    if (q?.to) {
      target = q.to;
      replyTo = replyTo ?? q["reply-to"] ?? null;
      source = "quoted-block";
    }
  }
  if (!target) return { kind: "unaddressed", text: body };
  return { kind: "message", target, replyTo, summary, text: body, source };
}

/** brigadeBody renders a person's mail as the body of a Brigade message: a header line, the text (escaped so
 * it cannot forge a block), and the envelope block. The whole thing fits the protocol's 16 KiB cap. */
export function brigadeBody(mail: InboundMail, text: string): { body: string; summary: string } {
  const who = mail.fromName ? `${mail.fromName} <${mail.from}>` : mail.from;
  const head = `Email from ${who} (unverified), subject "${oneLine(mail.subject)}":\n\n`;
  const block = "\n\n" + renderBlock([
    ["via", "email"],
    ["from", mail.from],
    ["subject", oneLine(mail.subject)],
    ["mail-id", mail.messageId],
    ["received", mail.receivedAt],
  ]);
  const budget = MAX_BODY_BYTES - utf8Len(head) - utf8Len(block);
  const body = head + truncateUTF8(escapeBlockLines(text.trim()), budget) + block;
  return { body, summary: cutChars(mail.subject.trim() || "email", MAX_SUMMARY_CHARS) };
}

export type Outbound =
  | { ok: true; address: string; subject: string; inReplyTo: string | null; text: string }
  | { ok: false; reason: string };

/** resolveOutbound decides where a session's message to the gateway goes: a `to:` directive in its body, or
 * the thread of the mail it replies to. */
export async function resolveOutbound(
  env: Envelope,
  threads: ThreadLookup,
  teamName: string,
): Promise<Outbound> {
  let leading: { fields: Fields | null; rest: string };
  try {
    leading = parseLeading(env.body);
  } catch (e) {
    if (e instanceof BlockError) return { ok: false, reason: e.message };
    throw e;
  }
  const f = leading.fields ?? {};
  const text = leading.rest;
  if (f.to) {
    const address = f.to.trim().toLowerCase();
    if (!isAddress(address)) return { ok: false, reason: `"to: ${f.to}" is not an email address` };
    const subject = f.subject?.trim() || `[brigade/${teamName}] ${excerpt(env.summary || text)}`;
    return { ok: true, address, subject: oneLine(subject), inReplyTo: null, text };
  }
  if (env.reply_to) {
    const row = await threads.byBrigadeId(env.reply_to);
    if (row && row.direction === "in") {
      const base = row.subject?.trim() || `[brigade/${teamName}] ${excerpt(env.summary || text)}`;
      const subject = /^re:/i.test(base) ? base : `Re: ${base}`;
      return {
        ok: true,
        address: row.address,
        subject: oneLine(subject),
        inReplyTo: row.mail_message_id,
        text,
      };
    }
  }
  return {
    ok: false,
    reason:
      'no address: start the body with a line "to: <email address>", or reply (--reply-to) to a message that came from an email',
  };
}

export interface RenderContext {
  teamName: string;
  from: string;
  inbox: string;
}

/** renderOutbound writes the mail for a Brigade message: who wrote it, the text (escaped), the envelope block
 * with the reply pre-addressed, and how to reply. Threading rides on the Reply-To tag first (a reply names the
 * message it answers in its own To header, whatever the client did to the rest), then the mail headers, then
 * the quoted block. */
export function renderOutbound(
  env: Envelope,
  out: Outbound & { ok: true },
  ctx: RenderContext,
): OutboundMail {
  const label = env.sender.human_label ? `${env.sender.human_label}, unverified` : "no label";
  const head = `${env.sender.session_name} (${label}) on Brigade team ${ctx.teamName} wrote:\n\n`;
  const block = renderBlock([
    ["team", ctx.teamName],
    ["message", env.message_id],
    ["from-session", env.sender.session_id],
    ["from-name", env.sender.session_name],
    ["from-label", env.sender.human_label ?? null],
    ["from-principal", env.sender.principal_ref],
    ["in-reply-to", env.reply_to ?? null],
    ["hops", env.hop_count],
    ["sent", env.created_at],
    ["to", env.sender.session_id],
    ["reply-to", env.message_id],
  ]);
  const tail =
    `\n\nReply to this email to answer. To write to another session, send a new email to ${ctx.inbox} whose first line is "to: <session>".\n`;
  const text = head + escapeBlockLines(out.text.trim()) + "\n\n" + block + tail;
  // No Message-ID header: Resend's relay (Amazon SES, measured 2026-10-03) replaces it with its own, so the thread
  // table never knows the id a reply's In-Reply-To will carry. The Reply-To tag is the key that survives; the
  // two headers below thread the mail under the person's own message in their client.
  const headers: Record<string, string> = { "Auto-Submitted": "auto-generated" };
  if (out.inReplyTo) {
    headers["In-Reply-To"] = out.inReplyTo;
    headers["References"] = out.inReplyTo;
  }
  return {
    from: ctx.from,
    to: out.address,
    replyTo: replyTag(ctx.inbox, env.message_id),
    subject: out.subject,
    text,
    headers,
  };
}

/** rosterText lists the sessions a person can write to, the gateway itself left out. */
export function rosterText(
  teamName: string,
  sessions: SessionRecord[],
  gatewaySessionId: string,
  inbox: string,
): string {
  const rows = sessions.filter((s) => s.session_id !== gatewaySessionId);
  const lines = [
    `Sessions on Brigade team ${teamName} right now (names and labels are their owners' own words):`,
    "",
  ];
  if (rows.length === 0) lines.push("  (none)");
  for (const s of rows) {
    const tail = s.session_id.slice(-5);
    const label = s.human_label ? `${s.human_label} (unverified)` : "";
    lines.push(`  ${tail}  ${s.session_name}  ${s.state}  ${label}`.replace(/\s+$/, ""));
    if (s.session_description) lines.push(`         ↳ ${oneLine(s.session_description)}`);
  }
  lines.push(
    "",
    `To write to one, send an email to ${inbox} whose first line is:`,
    "",
    "  to: <the five characters in the first column>",
    "",
  );
  return lines.join("\n");
}

export function excerpt(s: string | null | undefined, n = 60): string {
  const first = (s ?? "").split("\n").find((l) => l.trim() !== "")?.trim() ?? "";
  return cutChars(first, n) || "message";
}

function oneLine(s: string): string {
  return s.replace(/[\r\n]+/g, " ").trim();
}

function cutChars(s: string, n: number): string {
  const cps = Array.from(s);
  return cps.length <= n ? s : cps.slice(0, n - 1).join("") + "…";
}

function utf8Len(s: string): number {
  return new TextEncoder().encode(s).length;
}

function truncateUTF8(s: string, budget: number): string {
  if (budget <= 0) return "";
  if (utf8Len(s) <= budget) return s;
  const marker = "\n[… cut by the mail gateway: the email was longer than a Brigade message may be]";
  const room = budget - utf8Len(marker);
  const cps = Array.from(s);
  let out = "";
  for (const c of cps) {
    if (utf8Len(out + c) > room) break;
    out += c;
  }
  return out + marker;
}
