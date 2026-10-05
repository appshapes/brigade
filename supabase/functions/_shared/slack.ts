// The Slack gateway core: pure functions over Slack events and Brigade envelopes, with the database and the
// Slack Web API behind small interfaces so every rule here is unit-tested without either. The design is the
// mail gateway's (gateway.ts) with Slack's own facts: Slack authenticates the author (a user id is a verified
// identity, a name is not), a reply lives in a thread whose root ts is the key, and there is no quoting.

import {
  BlockError,
  escapeBlockLines,
  type Fields,
  isBareCommand,
  parseLeading,
  renderBlock,
} from "./block.ts";
import {
  type Envelope,
  excerpt,
  isGatewaySession,
  MAX_BODY_BYTES,
  MAX_SUMMARY_CHARS,
  type SessionRecord,
  type ThreadRow,
} from "./gateway.ts";
import { isAddress } from "./mail.ts";

export const SLACK_GATEWAY_VERSION = "0.1.0";
export const SLACK_SESSION_NAME = "slack-gateway";
export const SLACK_HARNESS = "gateway-slack";

/** slackDescription is the gateway session's `↳` line. ≤ 256 code points. */
export function slackDescription(botHandle: string, workspace: string): string {
  return `slack gateway for ${workspace}: a person DMs @${botHandle} or mentions it with a first line "to: <session>"; a session writes to me with "to: @name", "to: #channel" or "to: <email>"`;
}

// ---- Signature -------------------------------------------------------------------------------------------

/** verifySlackSignature checks X-Slack-Signature (`v0=` + hex HMAC-SHA256 of `v0:<timestamp>:<body>` under the
 * app's signing secret) and the request timestamp's age. */
export async function verifySlackSignature(
  secret: string,
  headers: Headers,
  body: string,
  nowSeconds = Math.floor(Date.now() / 1000),
  toleranceSeconds = 300,
): Promise<boolean> {
  const ts = headers.get("x-slack-request-timestamp");
  const sig = headers.get("x-slack-signature");
  if (!ts || !sig || !secret) return false;
  const t = Number(ts);
  if (!Number.isFinite(t) || Math.abs(nowSeconds - t) > toleranceSeconds) return false;
  const expected = "v0=" + (await hmacHex(secret, `v0:${ts}:${body}`));
  return constantTimeEqual(sig, expected);
}

/** signSlack produces the header a Slack request would carry, for tests. */
export async function signSlack(secret: string, ts: string, body: string): Promise<string> {
  return "v0=" + (await hmacHex(secret, `v0:${ts}:${body}`));
}

async function hmacHex(secret: string, text: string): Promise<string> {
  const k = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = new Uint8Array(await crypto.subtle.sign("HMAC", k, new TextEncoder().encode(text)));
  return Array.from(mac, (b) => b.toString(16).padStart(2, "0")).join("");
}

function constantTimeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

// ---- Events ----------------------------------------------------------------------------------------------

/** InboundSlack is one thing a person did that the gateway may carry: a DM to the bot, a mention of it in a
 * channel, or a slash command. */
export interface InboundSlack {
  /** Slack's event id (dedupe key), or `cmd:<trigger_id>` for a slash command. */
  eventId: string;
  source: "message" | "app_mention" | "command";
  user: string;
  channel: string;
  /** im, channel, group (private channel), mpim, or null when Slack did not say. */
  channelType: string | null;
  /** The message's own ts, null for a slash command. */
  ts: string | null;
  /** The ts of the thread's root when the message is a reply inside a thread. */
  threadTs: string | null;
  text: string;
}

export type ParsedRequest =
  | { kind: "url_verification"; challenge: string }
  | { kind: "event"; message: InboundSlack }
  | { kind: "command"; message: InboundSlack; command: string; responseUrl: string | null }
  | { kind: "ignore"; reason: string };

/** parseEventsRequest reads an Events API body. botUserId is the gateway's own user, whose messages are ignored. */
export function parseEventsRequest(raw: string, botUserId: string | null): ParsedRequest {
  let doc: Record<string, unknown>;
  try {
    doc = JSON.parse(raw);
  } catch {
    return { kind: "ignore", reason: "not json" };
  }
  if (doc.type === "url_verification" && typeof doc.challenge === "string") {
    return { kind: "url_verification", challenge: doc.challenge };
  }
  if (doc.type !== "event_callback" || typeof doc.event !== "object" || doc.event === null) {
    return { kind: "ignore", reason: `type ${String(doc.type)}` };
  }
  const ev = doc.event as Record<string, unknown>;
  const eventId = typeof doc.event_id === "string" ? doc.event_id : "";
  if (!eventId) return { kind: "ignore", reason: "no event id" };
  if (ev.type !== "message" && ev.type !== "app_mention") {
    return { kind: "ignore", reason: `event ${String(ev.type)}` };
  }
  if (ev.subtype || ev.bot_id || typeof ev.user !== "string") {
    return { kind: "ignore", reason: "not a person's message" };
  }
  if (botUserId && ev.user === botUserId) return { kind: "ignore", reason: "our own message" };
  if (ev.type === "message" && ev.channel_type !== "im") {
    return { kind: "ignore", reason: "a channel message without a mention" };
  }
  const text = typeof ev.text === "string" ? ev.text : "";
  return {
    kind: "event",
    message: {
      eventId,
      source: ev.type as "message" | "app_mention",
      user: ev.user,
      channel: String(ev.channel ?? ""),
      channelType: typeof ev.channel_type === "string" ? ev.channel_type : null,
      ts: typeof ev.ts === "string" ? ev.ts : null,
      threadTs: typeof ev.thread_ts === "string" && ev.thread_ts !== ev.ts ? ev.thread_ts : null,
      text: stripMention(text, botUserId),
    },
  };
}

/** parseCommandRequest reads a slash command's form body. */
export function parseCommandRequest(raw: string): ParsedRequest {
  const f = new URLSearchParams(raw);
  const user = f.get("user_id") ?? "";
  const channel = f.get("channel_id") ?? "";
  if (!user || !channel) return { kind: "ignore", reason: "not a slash command" };
  const trigger = f.get("trigger_id") ?? crypto.randomUUID();
  return {
    kind: "command",
    command: (f.get("command") ?? "").replace(/^\//, ""),
    responseUrl: f.get("response_url"),
    message: {
      eventId: `cmd:${trigger}`,
      source: "command",
      user,
      channel,
      channelType: f.get("channel_name") === "directmessage" ? "im" : null,
      ts: null,
      threadTs: null,
      text: (f.get("text") ?? "").trim(),
    },
  };
}

/** stripMention removes the bot's own mention from a text and unescapes Slack's three entities. */
export function stripMention(text: string, botUserId: string | null): string {
  let t = text;
  if (botUserId) t = t.replace(new RegExp(`<@${botUserId}(?:\\|[^>]*)?>`, "g"), "");
  return t.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&").trim();
}

/** escapeMrkdwn makes untrusted text safe to post: Slack's three control characters. */
export function escapeMrkdwn(text: string): string {
  return text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

// ---- Targets ---------------------------------------------------------------------------------------------

export type SlackTarget =
  | { kind: "user"; id: string }
  | { kind: "user-name"; name: string }
  | { kind: "user-email"; email: string }
  | { kind: "channel"; id: string }
  | { kind: "channel-name"; name: string };

/** parseSlackTarget reads the `to:` value of a session's message: `@name`, `#channel`, an email address, a Slack
 * user or channel id, or the `<@U…|name>` / `<#C…|name>` forms a copied Slack message carries. */
export function parseSlackTarget(raw: string): SlackTarget | null {
  const v = raw.trim();
  let m: RegExpExecArray | null;
  if ((m = /^<@([UW][A-Z0-9]+)(?:\|[^>]*)?>$/.exec(v))) return { kind: "user", id: m[1] };
  if ((m = /^<#([CG][A-Z0-9]+)(?:\|[^>]*)?>$/.exec(v))) return { kind: "channel", id: m[1] };
  if (/^[UW][A-Z0-9]{6,}$/.test(v)) return { kind: "user", id: v };
  if (/^[CGD][A-Z0-9]{6,}$/.test(v)) return { kind: "channel", id: v };
  if ((m = /^@([^\s@#]+)$/.exec(v))) return { kind: "user-name", name: m[1] };
  if ((m = /^#([^\s@#]+)$/.exec(v))) return { kind: "channel-name", name: m[1] };
  if (isAddress(v)) return { kind: "user-email", email: v.toLowerCase() };
  return null;
}

// ---- Inbound ---------------------------------------------------------------------------------------------

export interface SlackThreads {
  byThread(channel: string, threadTs: string): Promise<ThreadRow | null>;
}

export type SlackInbound =
  | { kind: "command"; command: string; args: string; text: string }
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

/** resolveSlackInbound decides what a person's message is and where it goes: their explicit `to:` line or block
 * first, then the thread they replied in. A slash command's text is `sessions`, `help`, or `send <session> <text>`. */
export async function resolveSlackInbound(m: InboundSlack, threads: SlackThreads): Promise<SlackInbound> {
  if (m.source === "command") {
    const [verb, ...rest] = m.text.split(/\s+/);
    const v = (verb ?? "").toLowerCase();
    if (v === "send" && rest.length >= 2) {
      return {
        kind: "message",
        target: rest[0],
        replyTo: null,
        summary: null,
        text: rest.slice(1).join(" "),
        source: "command",
      };
    }
    return { kind: "command", command: v || "help", args: rest.join(" "), text: m.text };
  }
  let leading: { fields: Fields | null; rest: string };
  try {
    leading = parseLeading(m.text);
  } catch (e) {
    if (e instanceof BlockError) return { kind: "invalid", reason: e.message };
    throw e;
  }
  const f = leading.fields ?? {};
  const text = leading.rest.trim();
  if (f.command) return { kind: "command", command: f.command.trim().toLowerCase(), args: "", text };
  const bare = leading.fields ? null : isBareCommand(text);
  if (bare) return { kind: "command", command: bare, args: "", text };
  let target: string | null = f.to ?? null;
  let replyTo: string | null = f["reply-to"] ?? null;
  let source = target ? "block" : "";
  if (!target && m.threadTs) {
    // The newest row of the thread: a session's post (answer it) or the person's own earlier message (a second
    // message to the same session; it cannot be a reply_to, since the gateway sent that one).
    const row = await threads.byThread(m.channel, m.threadTs);
    if (row) {
      target = row.session_id;
      if (row.direction === "out") replyTo = replyTo ?? row.brigade_message_id;
      source = "thread";
    }
  }
  if (!target) return { kind: "unaddressed", text };
  return { kind: "message", target, replyTo, summary: f.summary ?? null, text, source };
}

export interface SlackContext {
  userName: string | null;
  channelName: string | null;
  permalink: string | null;
}

/** slackBody renders a person's Slack message as the body of a Brigade message. */
export function slackBody(
  m: InboundSlack,
  text: string,
  ctx: SlackContext,
): { body: string; summary: string } {
  const who = ctx.userName ? `@${ctx.userName} (${m.user}, unverified name)` : m.user;
  const where = m.channelType === "im"
    ? "in a direct message"
    : ctx.channelName
    ? `in #${ctx.channelName}`
    : `in ${m.channel}`;
  const head = `Slack message from ${who} ${where}:\n\n`;
  const block = "\n\n" + renderBlock([
    ["via", "slack"],
    ["from", m.user],
    ["from-name", ctx.userName],
    ["channel", m.channel],
    ["channel-name", ctx.channelName],
    ["ts", m.ts],
    ["thread", m.threadTs],
    ["permalink", ctx.permalink],
  ]);
  const budget = MAX_BODY_BYTES - utf8Len(head) - utf8Len(block);
  const body = head + truncateUTF8(escapeBlockLines(text), budget) + block;
  return { body, summary: cutChars(excerpt(text, MAX_SUMMARY_CHARS) || "slack message", MAX_SUMMARY_CHARS) };
}

// ---- Outbound --------------------------------------------------------------------------------------------

export type SlackOutbound =
  | { ok: true; target: SlackTarget; threadTs: null; mention: null; text: string }
  | {
    ok: true;
    target: { kind: "channel"; id: string };
    threadTs: string | null;
    mention: string | null;
    text: string;
  }
  | { ok: false; reason: string };

export interface OutboundThreads {
  byBrigadeId(id: string): Promise<ThreadRow | null>;
}

/** resolveSlackOutbound decides where a session's message goes: its `to:` directive, or the thread of the
 * Slack message it replies to. */
export async function resolveSlackOutbound(env: Envelope, threads: OutboundThreads): Promise<SlackOutbound> {
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
    const target = parseSlackTarget(f.to);
    if (!target) {
      return { ok: false, reason: `"to: ${f.to}" is not @name, #channel, an email address or a Slack id` };
    }
    return { ok: true, target, threadTs: null, mention: null, text };
  }
  if (env.reply_to) {
    const row = await threads.byBrigadeId(env.reply_to);
    if (row && row.kind === "slack" && row.direction === "in") {
      return {
        ok: true,
        target: { kind: "channel", id: row.address },
        threadTs: row.mail_message_id,
        mention: row.actor ?? null,
        text,
      };
    }
  }
  return {
    ok: false,
    reason:
      'no address: start the body with a line "to: @name", "to: #channel" or "to: <email>", or reply (--reply-to) to a message that came from Slack',
  };
}

export interface SlackRenderContext {
  teamName: string;
  botHandle: string;
}

/** renderSlackText writes the Slack message for a Brigade envelope: who wrote it, the text (escaped), the
 * envelope block in a code fence, and how to reply. A mention in front notifies the person in a channel thread. */
export function renderSlackText(
  env: Envelope,
  out: SlackOutbound & { ok: true },
  ctx: SlackRenderContext,
): string {
  const label = env.sender.human_label ? `${escapeMrkdwn(env.sender.human_label)}, unverified` : "no label";
  const head = `${out.mention ? `<@${out.mention}> ` : ""}*${
    escapeMrkdwn(env.sender.session_name)
  }* (${label}) on Brigade team ${escapeMrkdwn(ctx.teamName)}:\n\n`;
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
    `\n_Reply in this thread to answer. To write to another session, message @${ctx.botHandle} with a first line "to: <session>"._`;
  return head + escapeMrkdwn(escapeBlockLines(out.text.trim())) + "\n\n```\n" + block + "\n```" + tail;
}

/** slackRoster lists the sessions a person can write to: the ones online now, the gateways left out. */
export function slackRoster(
  teamName: string,
  sessions: SessionRecord[],
  gatewayIds: string[],
  botHandle: string,
): string {
  const rows = sessions.filter((s) =>
    !gatewayIds.includes(s.session_id) && !isGatewaySession(s) && s.state !== "offline"
  );
  const lines = [
    `Sessions on Brigade team *${
      escapeMrkdwn(teamName)
    }* right now (names and labels are their owners' own words):`,
    "",
  ];
  if (rows.length === 0) lines.push("  (none)");
  for (const s of rows) {
    const label = s.human_label ? ` · ${escapeMrkdwn(s.human_label)} (unverified)` : "";
    lines.push(`• \`${s.session_id.slice(-5)}\`  ${escapeMrkdwn(s.session_name)}  _${s.state}_${label}`);
    if (s.session_description) lines.push(`      ↳ ${escapeMrkdwn(oneLine(s.session_description))}`);
  }
  lines.push(
    "",
    `To write to one: message @${botHandle} with a first line \`to: <the five characters>\`, or \`/brigade send <five characters> <text>\`.`,
  );
  return lines.join("\n");
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
  const marker = "\n[… cut by the Slack gateway: the message was longer than a Brigade message may be]";
  const room = budget - utf8Len(marker);
  let out = "";
  for (const c of Array.from(s)) {
    if (utf8Len(out + c) > room) break;
    out += c;
  }
  return out + marker;
}
