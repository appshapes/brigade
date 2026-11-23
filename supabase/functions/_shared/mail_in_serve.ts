// The gateway core's inbound endpoint, mail-in. A connector (docs/mail-connectors.md) POSTs a received mail here
// in the contract's shape, authenticated by the connector secret; the core decides where the mail goes and sends it
// into the team as a Brigade message from the gateway session. A mail from a sender the team does not list is
// dropped unanswered (card 55). A mail that cannot be routed is answered by mail, through the connector's send
// endpoint, with the reason and the roster.
//
// What the connector is answered, and what the provider then does (work item 388). 200 once the gateway is done
// with the mail: carried, bounced, answered as a command, or not the gateway's to carry; a later delivery of it
// is answered `duplicate`. 500 when something failed before that, or while an earlier delivery of the mail is
// still being carried: the connector passes the failure on, the provider delivers again, and that delivery is
// carried, because the mail is written down as received only once its outcome is decided (the marks are in
// db.ts). It was written down first until then, and a failure after it lost the mail.
//
// The handler is here and not in mail-in/index.ts so that a test runs it against the local stack's database with
// a stand-in for the connector (../_live/gateway_in_test.ts): index.ts serves it and does nothing else.

import { authorized, fromInboundWire, sendClient, SendError } from "./connector.ts";
import {
  asMember,
  BEING_CARRIED,
  carriedBefore,
  carriedIn,
  connect,
  done,
  listMailGateways,
  listSessions,
  mailSettingsFromEnv,
  recordThread,
  release,
  RpcError,
  sendMessage,
  take,
  threadByBrigadeId,
  threadByMailIds,
} from "./db.ts";
import {
  addressedToInbox,
  brigadeBody,
  gatewayForMail,
  resolveInbound,
  resolveRecipient,
  rosterText,
  senderAllowed,
  type SessionRecord,
} from "./gateway.ts";
import { domainOf } from "./mail.ts";

const env = (k: string) => Deno.env.get(k) ?? "";

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

/** What a test puts in place of the world: the fetch the connector's send endpoint is reached with. */
export interface MailInDeps {
  fetchFn?: typeof fetch;
}

export function mailInHandler(deps: MailInDeps = {}): (req: Request) => Promise<Response> {
  return async (req: Request): Promise<Response> => {
    if (req.method !== "POST") return json(405, { error: "method not allowed" });
    const secret = env("BRIGADE_MAIL_CONNECTOR_SECRET");
    if (!authorized(req, "mail-in", secret)) {
      return json(401, { error: "the request does not carry this gateway's connector secret" });
    }
    const raw = await req.text();
    const parsed = fromInboundWire(raw);
    if (!parsed.ok) return json(400, { error: parsed.reason });
    const m = parsed.value;
    const sql = connect();
    // Whether this delivery holds the mail's claim: taken before the mail is carried, given back with the mark
    // once the gateway is done with it, and given back without the mark when the delivery fails.
    let carrying: string | null = null;
    const decided = async (body: unknown): Promise<Response> => {
      if (carrying !== null) await done(sql, m.providerId, carrying);
      carrying = null;
      return json(200, body);
    };
    try {
      // One project may carry several teams' gateways; the inbox the mail was delivered to says whose it is. A
      // mail that is nobody's, or from a sender the team does not list, is not written down: anyone can send one,
      // and a later delivery of it is dropped as this one is.
      const gw = gatewayForMail(await listMailGateways(sql, mailSettingsFromEnv()), m.to);
      if (!gw) return json(200, { ignored: "not addressed to a gateway inbox of this project" });
      if (!senderAllowed(m.from, gw.allowedSenders)) {
        // Card 55: the list is the one bar on strangers. No roster and no bounce (a reply to a forged From would make
        // the gateway a backscatter source and confirm the address is live); the domain alone goes to the log.
        const domain = domainOf(m.from) || "?";
        console.error(
          `mail-in: dropped a mail from @${domain}: not on team ${gw.teamName}'s allowed senders`,
        );
        return json(200, { ignored: "the sender is not on the team's allowed-senders list" });
      }
      const taken = await take(sql, m.providerId);
      if (taken === "done") return json(200, { duplicate: true });
      if (taken === "carried") return json(500, { error: BEING_CARRIED });
      carrying = taken.token;
      const { inbox, publicAddress, from } = gw;
      const mail = sendClient(gw.sendUrl, secret, deps.fetchFn);
      const threads = {
        byBrigadeId: (id: string) => threadByBrigadeId(sql, id),
        byMailIds: (ids: string[]) => threadByMailIds(sql, ids),
      };
      const resolution = await resolveInbound(m, inbox, threads);
      const roster = await asMember(sql, gw.userId, (tx) => listSessions(tx, gw.teamId, true));
      const sessions = roster.sessions as SessionRecord[];
      const rosterNote = rosterText(gw.teamName, sessions, gw.sessionId, publicAddress);

      // What a person is told by mail is their answer, and the gateway is done with the mail once it is decided,
      // whether or not the answer could be mailed.
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
          await bounce(`The lines at the top of your email could not be read: ${resolution.reason}.`);
          return await decided({ bounced: "invalid" });
        case "unaddressed":
          await bounce(
            'No session was named. Start your email with a line "to: <session>" (see the list below), or reply to an email a session sent you.',
          );
          return await decided({ bounced: "unaddressed" });
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
          return await decided({ command: resolution.command });
        }
        case "message": {
          const r = resolveRecipient(
            resolution.target,
            sessions.filter((s) => s.session_id !== gw.sessionId),
          );
          if ("error" in r) {
            await bounce(`The message was not sent: ${r.error}.`);
            return await decided({ bounced: "not_found" });
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
            try {
              sent = await asMember(sql, gw.userId, (tx) => sendMessage(tx, args));
            } catch (e) {
              if (!(e instanceof RpcError && e.code === "not_found" && args.replyTo)) throw e;
              // The message being answered is gone (retention); the text still reaches the session, unthreaded.
              sent = await asMember(sql, gw.userId, (tx) => sendMessage(tx, { ...args, replyTo: null }));
            }
          } catch (e) {
            if (!(e instanceof RpcError)) throw e;
            if (!carriedBefore(e)) {
              await bounce(
                `Brigade refused the message (${e.code}${e.detail ? ": " + e.detail : ""}). Try again later.`,
              );
              return await decided({ bounced: e.code });
            }
            // A mail that is delivered again after a failure is sent again, and Brigade may hold its message
            // already: sent with the same words it is answered as a duplicate, with other words the key is
            // refused, and the row the earlier delivery recorded says which message it is and where it went,
            // which need not be where this delivery would have sent it. The mail is carried, and nobody is
            // told that it was refused. With no such row Brigade holds the message all the same.
            const known = await carriedIn(sql, "email", m.providerId);
            if (known === null) return await decided({ duplicate: true });
            return await decided({ message_id: known.messageId, to: known.sessionId, duplicate: true });
          }
          // The thread row, and then the mark that the gateway is done with the mail: Brigade has the message,
          // and an answer to it finds the person. A failure before the mark leaves the mail to the provider's
          // next delivery, which records the row.
          await recordThread(sql, gw.teamId, {
            brigade_message_id: sent.message_id,
            direction: "in",
            session_id: r.session.session_id,
            address: m.from,
            mail_message_id: m.messageId,
            provider_mail_id: m.providerId,
            subject: m.subject || null,
          });
          return await decided({
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
      if (carrying !== null) {
        // Best effort: a claim that stays ends by itself, and until then a delivery is told to come again.
        await release(sql, m.providerId, carrying).catch((e: unknown) =>
          console.error("mail-in: the claim was not given back", e instanceof Error ? e.message : String(e))
        );
      }
      await sql.end({ timeout: 2 });
    }
  };
}
