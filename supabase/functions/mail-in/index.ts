// mail-in: the gateway core's inbound endpoint. A connector (docs/mail-connectors.md) POSTs a received mail here
// in the contract's shape, authenticated by the connector secret; the core decides where the mail goes and sends it
// into the team as a Brigade message from the gateway session. A mail that cannot be routed is answered by mail,
// through the connector's send endpoint, with the reason and the roster. Always 200 to the connector once the
// request is authentic and well-formed: a retry would not change the outcome.

import { authorized, fromInboundWire, sendClient, SendError } from "../_shared/connector.ts";
import {
  asMember,
  connect,
  listSessions,
  loadGateway,
  markReceived,
  recordThread,
  RpcError,
  sendMessage,
  threadByBrigadeId,
  threadByMailIds,
} from "../_shared/db.ts";
import {
  addressedToInbox,
  brigadeBody,
  resolveInbound,
  resolveSession,
  rosterText,
  type SessionRecord,
} from "../_shared/gateway.ts";

const env = (k: string) => Deno.env.get(k) ?? "";

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

Deno.serve(async (req) => {
  if (req.method !== "POST") return json(405, { error: "method not allowed" });
  const secret = env("BRIGADE_MAIL_CONNECTOR_SECRET");
  if (!authorized(req, "mail-in", secret)) {
    return json(401, { error: "the request does not carry this gateway's connector secret" });
  }
  const raw = await req.text();
  const parsed = fromInboundWire(raw);
  if (!parsed.ok) return json(400, { error: parsed.reason });
  const m = parsed.value;
  const mail = sendClient(env("BRIGADE_MAIL_SEND_URL"), secret);

  const teamRef = env("BRIGADE_TEAM_REF");
  const inbox = env("BRIGADE_MAIL_INBOX").toLowerCase();
  const publicAddress = (env("BRIGADE_MAIL_PUBLIC_ADDRESS") || inbox).toLowerCase();
  const from = env("BRIGADE_MAIL_FROM");
  const sql = connect();
  try {
    if (!(await markReceived(sql, m.providerId))) return json(200, { duplicate: true });
    const gw = await loadGateway(sql, teamRef, "email");
    if (!addressedToInbox(m.to, inbox)) {
      return json(200, { ignored: "not addressed to this gateway's inbox" });
    }
    const threads = {
      byBrigadeId: (id: string) => threadByBrigadeId(sql, id),
      byMailIds: (ids: string[]) => threadByMailIds(sql, ids),
    };
    const resolution = await resolveInbound(m, inbox, threads);
    const roster = await asMember(sql, gw.userId, (tx) => listSessions(tx, gw.teamId, true));
    const sessions = roster.sessions as SessionRecord[];
    const rosterNote = rosterText(gw.teamName, sessions, gw.sessionId, publicAddress);

    const bounce = async (reason: string) => {
      // Never answer the gateway's own inbox or a tagged reply address of it: that would be a loop.
      if (!m.from || addressedToInbox([m.from], inbox)) return;
      const text = `Your email was not delivered to a Brigade session.\n\n${reason}\n\n${rosterNote}`;
      const headers: Record<string, string> = { "Auto-Submitted": "auto-replied" };
      if (m.messageId) {
        headers["In-Reply-To"] = m.messageId;
        headers["References"] = m.messageId;
      }
      try {
        await mail.send({
          from,
          to: m.from,
          replyTo: inbox,
          subject: m.subject ? `Re: ${m.subject}` : "[brigade] not delivered",
          text,
          headers,
        });
      } catch (e) {
        console.error("bounce failed", e instanceof Error ? e.message : String(e));
      }
    };

    switch (resolution.kind) {
      case "invalid":
        await bounce(`The [brigade] block at the top of your email could not be read: ${resolution.reason}.`);
        return json(200, { bounced: "invalid" });
      case "unaddressed":
        await bounce(
          'No session was named. Start your email with a line "to: <session>" (see the list below), or reply to an email a session sent you.',
        );
        return json(200, { bounced: "unaddressed" });
      case "command": {
        const text = resolution.command === "sessions" || resolution.command === "help"
          ? rosterNote
          : `Unknown command "${resolution.command}". Commands: sessions, help.\n\n${rosterNote}`;
        try {
          await mail.send({
            from,
            to: m.from,
            replyTo: inbox,
            subject: `[brigade/${gw.teamName}] ${resolution.command}`,
            text,
            headers: { "Auto-Submitted": "auto-replied" },
          });
        } catch (e) {
          console.error("command reply failed", e instanceof Error ? e.message : String(e));
        }
        return json(200, { command: resolution.command });
      }
      case "message": {
        const r = resolveSession(resolution.target, sessions.filter((s) => s.session_id !== gw.sessionId));
        if ("error" in r) {
          await bounce(`The session could not be found: ${r.error}.`);
          return json(200, { bounced: "not_found" });
        }
        const { body, summary } = brigadeBody(m, resolution.text);
        const args = {
          sender: gw.sessionId,
          recipient: r.session.session_id,
          body,
          key: `mail:${m.providerId}`,
          summary: resolution.summary ?? summary,
          replyTo: resolution.replyTo,
        };
        let sent: { message_id: string; duplicate: boolean };
        try {
          sent = await asMember(sql, gw.userId, (tx) => sendMessage(tx, args));
        } catch (e) {
          if (e instanceof RpcError && e.code === "not_found" && args.replyTo) {
            // The message being answered is gone (retention); the text still reaches the session, unthreaded.
            sent = await asMember(sql, gw.userId, (tx) => sendMessage(tx, { ...args, replyTo: null }));
          } else if (e instanceof RpcError) {
            await bounce(
              `Brigade refused the message (${e.code}${e.detail ? ": " + e.detail : ""}). Try again later.`,
            );
            return json(200, { bounced: e.code });
          } else {
            throw e;
          }
        }
        await recordThread(sql, gw.teamId, {
          brigade_message_id: sent.message_id,
          direction: "in",
          session_id: r.session.session_id,
          address: m.from,
          mail_message_id: m.messageId,
          provider_mail_id: m.providerId,
          subject: m.subject || null,
        });
        return json(200, {
          message_id: sent.message_id,
          to: r.session.session_id,
          source: resolution.source,
          duplicate: sent.duplicate,
        });
      }
    }
  } catch (e) {
    if (e instanceof SendError) return json(200, { error: e.message });
    console.error("mail-in failed", e instanceof Error ? e.message : String(e));
    return json(500, { error: e instanceof Error ? e.message : String(e) });
  } finally {
    await sql.end({ timeout: 2 });
  }
});
