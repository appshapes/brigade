// mail-out: the tick. Called every minute by pg_cron through pg_net with the tick token. Keeps the gateway
// session's lease alive (so the roster shows it online exactly while this runs), drains the gateway's inbox,
// mails each message to the address it resolves to, records the thread, and acknowledges only after the
// provider accepted the mail. A message with no address is answered to its sender and acknowledged.

import { resendProvider, SendError } from "../_shared/providers/resend.ts";
import {
  ackMessages,
  asMember,
  connect,
  fetchInbox,
  heartbeat,
  loadGateway,
  recordThread,
  registerSession,
  RpcError,
  sendMessage,
  setGatewaySession,
  threadByBrigadeId,
  threadByMailIds,
} from "../_shared/db.ts";
import {
  description,
  type Envelope,
  GATEWAY_VERSION,
  HARNESS,
  renderOutbound,
  resolveOutbound,
  SESSION_NAME,
} from "../_shared/gateway.ts";

const env = (k: string) => Deno.env.get(k) ?? "";

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

Deno.serve(async (req) => {
  if (req.method !== "POST") return json(405, { error: "method not allowed" });
  const token = env("BRIGADE_TICK_TOKEN");
  if (!token || req.headers.get("x-brigade-tick") !== token) return json(401, { error: "bad tick token" });

  const teamRef = env("BRIGADE_TEAM_REF");
  const inbox = env("BRIGADE_MAIL_INBOX").toLowerCase();
  const from = env("BRIGADE_MAIL_FROM");
  const mail = resendProvider({
    apiKey: env("RESEND_API_KEY"),
    webhookSecret: env("RESEND_WEBHOOK_SECRET") || null,
  });
  const sql = connect();
  const report = { heartbeat: "", delivered: 0, undeliverable: 0, retry: 0, errors: [] as string[] };
  try {
    const gw = await loadGateway(sql, teamRef);
    const desc = description(inbox);
    try {
      const h = await asMember(sql, gw.userId, (tx) => heartbeat(tx, gw.sessionId, desc));
      report.heartbeat = h.state;
    } catch (e) {
      if (e instanceof RpcError && (e.code === "not_found" || e.code === "conflict")) {
        // The session row is gone (retention after a long outage) or closed: register again, resuming when it
        // still exists, so the gateway's address stays the same whenever it can.
        const resume = e.code === "conflict" ? gw.sessionId : null;
        const r = await asMember(
          sql,
          gw.userId,
          (tx) => registerSession(tx, gw.teamId, SESSION_NAME, desc, HARNESS, GATEWAY_VERSION, resume),
        );
        await setGatewaySession(sql, gw.teamId, r.session_id);
        gw.sessionId = r.session_id;
        report.heartbeat = r.resumed ? "resumed" : "registered";
      } else {
        throw e;
      }
    }

    const inboxMessages = (await asMember(sql, gw.userId, (tx) =>
      fetchInbox(tx, gw.sessionId, 50))) as Envelope[];
    const threads = {
      byBrigadeId: (id: string) => threadByBrigadeId(sql, id),
      byMailIds: (ids: string[]) => threadByMailIds(sql, ids),
    };
    for (const m of inboxMessages) {
      const ack = () => asMember(sql, gw.userId, (tx) => ackMessages(tx, gw.sessionId, [m.message_id]));
      const already = await threadByBrigadeId(sql, m.message_id);
      if (already && already.direction === "out") {
        // Sent on an earlier tick that died before the acknowledgement: never send it twice.
        await ack();
        continue;
      }
      const out = await resolveOutbound(m, threads, gw.teamName);
      if (!out.ok) {
        await tellSender(
          sql,
          gw,
          m,
          `The mail gateway could not deliver your message ${m.message_id}: ${out.reason}`,
        );
        await ack();
        report.undeliverable++;
        continue;
      }
      const rendered = renderOutbound(m, out, { teamName: gw.teamName, from, inbox });
      let sent: { providerId: string };
      try {
        sent = await mail.send(rendered);
      } catch (e) {
        if (e instanceof SendError && !e.retryable) {
          await tellSender(
            sql,
            gw,
            m,
            `The mail gateway could not deliver your message ${m.message_id} to ${out.address}: ${e.message}`,
          );
          await ack();
          report.undeliverable++;
          continue;
        }
        report.retry++;
        report.errors.push(e instanceof Error ? e.message : String(e));
        continue; // left unacknowledged: the next tick tries again
      }
      await recordThread(sql, gw.teamId, {
        brigade_message_id: m.message_id,
        direction: "out",
        session_id: m.sender.session_id,
        address: out.address,
        mail_message_id: null, // the relay assigns the Message-ID; a reply is found by the Reply-To tag
        provider_mail_id: sent.providerId,
        subject: rendered.subject,
      });
      await ack();
      report.delivered++;
    }
    return json(200, report);
  } catch (e) {
    console.error("mail-out failed", e instanceof Error ? e.message : String(e));
    return json(500, { error: e instanceof Error ? e.message : String(e), ...report });
  } finally {
    await sql.end({ timeout: 2 });
  }
});

/** tellSender answers a session whose message the gateway could not deliver; a failure to tell is logged, never
 * fatal (the message is acknowledged either way, so the inbox does not fill with the same undeliverable). */
async function tellSender(
  sql: ReturnType<typeof connect>,
  gw: { userId: string; sessionId: string },
  m: Envelope,
  text: string,
) {
  try {
    await asMember(sql, gw.userId, (tx) =>
      sendMessage(tx, {
        sender: gw.sessionId,
        recipient: m.sender.session_id,
        body: text,
        key: `gw-err:${m.message_id}`,
        summary: "mail gateway: not delivered",
        replyTo: m.message_id,
      }));
  } catch (e) {
    console.error("could not tell the sender", e instanceof Error ? e.message : String(e));
  }
}
