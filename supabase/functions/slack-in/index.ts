// slack-in: Slack's Events API and slash-command endpoint. Answers the URL verification challenge, verifies every
// other request's signature, reads a person's DM, mention, reply in a Brigade thread or /brigade command, decides
// where it goes and sends it into the team as a Brigade message from the gateway session. What cannot be routed
// is answered where the person is: in the DM, ephemerally in the channel, or in the slash command's own reply. A
// channel reply that did not mention the bot and is not in a Brigade thread is not the gateway's, and is dropped
// without a word.

import { slackApi } from "../_shared/providers/slack.ts";
import {
  asMember,
  connect,
  listSessions,
  loadGateway,
  markReceived,
  recordThread,
  RpcError,
  sendMessage,
  threadBySlack,
} from "../_shared/db.ts";
import { resolveRecipient, type SessionRecord } from "../_shared/gateway.ts";
import {
  escapeMrkdwn,
  parseCommandRequest,
  parseEventsRequest,
  resolveSlackInbound,
  slackBody,
  slackRoster,
  verifySlackSignature,
} from "../_shared/slack.ts";

const env = (k: string) => Deno.env.get(k) ?? "";

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

/** sentNote says where a person's message went: the session, and, when they named a member, why that one and
 * which other sessions of theirs are online. */
function sentNote(r: Exclude<ReturnType<typeof resolveRecipient>, { error: string }>): string {
  const name = (s: SessionRecord) => `*${escapeMrkdwn(s.session_name)}* (\`…${s.session_id.slice(-5)}\`)`;
  let text = `Sent to ${name(r.session)}`;
  if (r.who !== null) {
    const seen = r.session.state === "offline"
      ? "offline; the message waits for it"
      : `the ${r.session.state} session of theirs seen most recently`;
    text += `: ${escapeMrkdwn(r.who)}'s ${seen}`;
    const online = r.others.filter((s) => s.state !== "offline");
    if (online.length > 0) {
      text += `. ${online.length} other session${online.length === 1 ? "" : "s"} of theirs ${
        online.length === 1 ? "is" : "are"
      } online: ${online.map(name).join(", ")}; send to one of those to choose it`;
    }
  }
  return `${text}. Accepted means stored for that session, not read; its reply will come to you here.`;
}

/** The bot's own user id, learned once per instance from auth.test. */
let botUserId: string | null = null;

Deno.serve(async (req) => {
  if (req.method !== "POST") return json(405, { error: "method not allowed" });
  const raw = await req.text();
  const contentType = req.headers.get("content-type") ?? "";

  // The URL verification challenge carries nothing but a nonce to echo; Slack sends it when the request URL is
  // saved, which happens when the app is created from the manifest, before this function has any secret.
  if (contentType.includes("application/json")) {
    try {
      const d = JSON.parse(raw);
      if (d?.type === "url_verification" && typeof d.challenge === "string") {
        return json(200, { challenge: d.challenge });
      }
    } catch { /* not json: the signature check below answers */ }
  }
  if (!(await verifySlackSignature(env("BRIGADE_SLACK_SIGNING_SECRET"), req.headers, raw))) {
    return json(401, { error: "the request is not signed by this gateway's Slack app" });
  }

  const api = slackApi(env("BRIGADE_SLACK_BOT_TOKEN"));
  if (!botUserId) {
    try {
      botUserId = (await api.authTest()).userId;
    } catch {
      botUserId = null;
    }
  }
  const parsed = contentType.includes("application/x-www-form-urlencoded")
    ? parseCommandRequest(raw)
    : parseEventsRequest(raw, botUserId);
  if (parsed.kind === "url_verification") return json(200, { challenge: parsed.challenge });
  if (parsed.kind === "ignore") return json(200, { ignored: parsed.reason });
  const m = parsed.message;
  const isCommand = parsed.kind === "command";

  const teamRef = env("BRIGADE_TEAM_REF");
  const botHandle = env("BRIGADE_SLACK_BOT_HANDLE") || "brigade";
  const sql = connect();
  try {
    // Where the message goes is decided first: a channel reply outside a Brigade thread is dropped before
    // anything is recorded or read.
    const resolution = await resolveSlackInbound(m, { byThread: (c, t) => threadBySlack(sql, c, t) });
    if (resolution.kind === "ignore") return json(200, { ignored: resolution.reason });
    if (!(await markReceived(sql, `slack:${m.eventId}`))) return json(200, { duplicate: true });
    const gw = await loadGateway(sql, teamRef, "slack");
    const roster = await asMember(sql, gw.userId, (tx) => listSessions(tx, gw.teamId, true));
    const sessions = roster.sessions as SessionRecord[];
    const rosterNote = slackRoster(gw.teamName, sessions, [gw.sessionId], botHandle);

    // tell answers the person where they are. A slash command is answered in its own HTTP response (visible to
    // them only); a DM in the DM; a channel mention ephemerally, so the channel is not told twice.
    const tell = async (text: string): Promise<Response> => {
      if (isCommand) return json(200, { response_type: "ephemeral", text });
      try {
        if (m.channelType === "im") {
          await api.postMessage({ channel: m.channel, text, threadTs: m.threadTs ?? m.ts });
        } else await api.postEphemeral({ channel: m.channel, user: m.user, text, threadTs: m.threadTs });
      } catch (e) {
        console.error("could not answer the person", e instanceof Error ? e.message : String(e));
      }
      return json(200, { told: true });
    };

    switch (resolution.kind) {
      case "invalid":
        return await tell(`The lines at the top of your message could not be read: ${resolution.reason}.`);
      case "unaddressed":
        return await tell(
          `No session was named. Start your message with a line \`to: <session or member>\`, or reply in the thread under a session's message.\n\n${rosterNote}`,
        );
      case "command": {
        const text = resolution.command === "sessions" || resolution.command === "help"
          ? rosterNote
          : `Unknown command \`${resolution.command}\`. Try \`sessions\`, \`help\`, or \`send <session or member> <text>\`.\n\n${rosterNote}`;
        return await tell(text);
      }
      case "message": {
        const r = resolveRecipient(resolution.target, sessions.filter((s) => s.session_id !== gw.sessionId));
        if ("error" in r) return await tell(`The message was not sent: ${r.error}.\n\n${rosterNote}`);
        let userName: string | null = null;
        let channelName: string | null = null;
        let permalink: string | null = null;
        try {
          userName = (await api.userInfo(m.user)).name;
        } catch { /* the id alone is still the identity */ }
        if (m.channelType !== "im") {
          try {
            channelName = (await api.channelInfo(m.channel))?.name ?? null;
          } catch { /* the id alone */ }
        }
        if (m.ts) permalink = await api.permalink(m.channel, m.ts);
        const { body, summary } = slackBody(m, resolution.text, { userName, channelName, permalink });
        const args = {
          sender: gw.sessionId,
          recipient: r.session.session_id,
          body,
          key: `slack:${m.eventId}`,
          summary: resolution.summary ?? summary,
          replyTo: resolution.replyTo,
        };
        let sent: { message_id: string; duplicate: boolean };
        try {
          sent = await asMember(sql, gw.userId, (tx) => sendMessage(tx, args));
        } catch (e) {
          if (e instanceof RpcError && e.code === "not_found" && args.replyTo) {
            sent = await asMember(sql, gw.userId, (tx) => sendMessage(tx, { ...args, replyTo: null }));
          } else if (e instanceof RpcError) {
            return await tell(
              `Brigade refused the message (${e.code}${e.detail ? ": " + e.detail : ""}). Try again later.`,
            );
          } else {
            throw e;
          }
        }
        await recordThread(sql, gw.teamId, {
          brigade_message_id: sent.message_id,
          direction: "in",
          session_id: r.session.session_id,
          address: m.channel,
          mail_message_id: m.threadTs ?? m.ts,
          provider_mail_id: m.eventId,
          subject: summary,
          kind: "slack",
          actor: m.user,
        });
        if (m.ts) await api.addReaction(m.channel, m.ts, "eyes").catch(() => {});
        // A person who named a member is told which of their sessions got the message, and which others could
        // have, so a wrong pick is corrected by resending to one of those; a person who named a session is not.
        const note = sentNote(r);
        if (isCommand) return json(200, { response_type: "ephemeral", text: note });
        if (r.who !== null) await tell(note);
        return json(200, {
          message_id: sent.message_id,
          to: r.session.session_id,
          source: resolution.source,
          duplicate: sent.duplicate,
        });
      }
    }
  } catch (e) {
    console.error("slack-in failed", e instanceof Error ? e.message : String(e));
    return json(500, { error: e instanceof Error ? e.message : String(e) });
  } finally {
    await sql.end({ timeout: 2 });
  }
});
