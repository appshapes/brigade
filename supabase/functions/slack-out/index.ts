// slack-out: the tick. Called every minute by pg_cron through pg_net with the tick token. Keeps the Slack gateway
// session's lease alive, drains its inbox, posts each message to the person or channel it resolves to (a reply
// goes into the thread it answers), records the thread, and acknowledges only after Slack accepted the post. A
// message with no address is answered to its sender and acknowledged.

import { slackApi, SlackError } from "../_shared/providers/slack.ts";
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
} from "../_shared/db.ts";
import { type Envelope, excerpt } from "../_shared/gateway.ts";
import {
  renderSlackText,
  resolveSlackOutbound,
  SLACK_GATEWAY_VERSION,
  SLACK_HARNESS,
  SLACK_SESSION_NAME,
  slackDescription,
  type SlackTarget,
} from "../_shared/slack.ts";

const env = (k: string) => Deno.env.get(k) ?? "";

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

Deno.serve(async (req) => {
  if (req.method !== "POST") return json(405, { error: "method not allowed" });
  const token = env("BRIGADE_SLACK_TICK_TOKEN");
  if (!token || req.headers.get("x-brigade-tick") !== token) return json(401, { error: "bad tick token" });

  const teamRef = env("BRIGADE_TEAM_REF");
  const botHandle = env("BRIGADE_SLACK_BOT_HANDLE") || "brigade";
  const workspace = env("BRIGADE_SLACK_WORKSPACE") || "slack";
  const api = slackApi(env("BRIGADE_SLACK_BOT_TOKEN"));
  const sql = connect();
  const report = { heartbeat: "", delivered: 0, undeliverable: 0, retry: 0, errors: [] as string[] };
  try {
    const gw = await loadGateway(sql, teamRef, "slack");
    const desc = slackDescription(botHandle, workspace);
    try {
      report.heartbeat = (await asMember(sql, gw.userId, (tx) => heartbeat(tx, gw.sessionId, desc))).state;
    } catch (e) {
      if (e instanceof RpcError && (e.code === "not_found" || e.code === "conflict")) {
        const resume = e.code === "conflict" ? gw.sessionId : null;
        const r = await asMember(
          sql,
          gw.userId,
          (tx) =>
            registerSession(
              tx,
              gw.teamId,
              SLACK_SESSION_NAME,
              desc,
              SLACK_HARNESS,
              SLACK_GATEWAY_VERSION,
              resume,
            ),
        );
        await setGatewaySession(sql, gw.teamId, "slack", r.session_id);
        gw.sessionId = r.session_id;
        report.heartbeat = r.resumed ? "resumed" : "registered";
      } else {
        throw e;
      }
    }

    const inbox = (await asMember(sql, gw.userId, (tx) => fetchInbox(tx, gw.sessionId, 50))) as Envelope[];
    const threads = { byBrigadeId: (id: string) => threadByBrigadeId(sql, id) };
    for (const m of inbox) {
      const ack = () => asMember(sql, gw.userId, (tx) => ackMessages(tx, gw.sessionId, [m.message_id]));
      const already = await threadByBrigadeId(sql, m.message_id);
      if (already && already.direction === "out") {
        await ack();
        continue;
      }
      const out = await resolveSlackOutbound(m, threads);
      if (!out.ok) {
        await tellSender(
          sql,
          gw,
          m,
          `The Slack gateway could not deliver your message ${m.message_id}: ${out.reason}`,
        );
        await ack();
        report.undeliverable++;
        continue;
      }
      let channel: string;
      try {
        const c = await resolveChannel(api, out.target);
        if (!c) {
          await tellSender(
            sql,
            gw,
            m,
            `The Slack gateway could not deliver your message ${m.message_id}: no Slack user or channel matches "${
              describeTarget(out.target)
            }"`,
          );
          await ack();
          report.undeliverable++;
          continue;
        }
        channel = c;
      } catch (e) {
        if (e instanceof SlackError && e.retryable) {
          report.retry++;
          report.errors.push(e.message);
          continue;
        }
        await tellSender(
          sql,
          gw,
          m,
          `The Slack gateway could not deliver your message ${m.message_id}: ${
            e instanceof Error ? e.message : String(e)
          }`,
        );
        await ack();
        report.undeliverable++;
        continue;
      }
      const text = renderSlackText(m, out, { teamName: gw.teamName, botHandle });
      const metadata = {
        event_type: "brigade_message",
        event_payload: { message_id: m.message_id, from_session: m.sender.session_id, team: gw.teamName },
      };
      let posted: { ts: string; channel: string };
      try {
        posted = await postWithJoin(api, channel, text, out.threadTs, metadata);
      } catch (e) {
        if (e instanceof SlackError && e.retryable) {
          report.retry++;
          report.errors.push(e.message);
          continue;
        }
        await tellSender(
          sql,
          gw,
          m,
          `The Slack gateway could not post your message ${m.message_id} to ${channel}: ${
            e instanceof Error ? e.message : String(e)
          }`,
        );
        await ack();
        report.undeliverable++;
        continue;
      }
      await recordThread(sql, gw.teamId, {
        brigade_message_id: m.message_id,
        direction: "out",
        session_id: m.sender.session_id,
        address: posted.channel,
        mail_message_id: out.threadTs ?? posted.ts,
        provider_mail_id: posted.ts,
        subject: excerpt(m.summary || out.text, 120),
        kind: "slack",
        actor: out.mention,
      });
      await ack();
      report.delivered++;
    }
    return json(200, report);
  } catch (e) {
    console.error("slack-out failed", e instanceof Error ? e.message : String(e));
    return json(500, { error: e instanceof Error ? e.message : String(e), ...report });
  } finally {
    await sql.end({ timeout: 2 });
  }
});

/** resolveChannel turns a target into the channel to post in: a person's DM, or a channel by id or name. */
async function resolveChannel(api: ReturnType<typeof slackApi>, t: SlackTarget): Promise<string | null> {
  switch (t.kind) {
    case "user":
      return await api.openDM(t.id);
    case "user-name": {
      const u = await api.findUserByName(t.name);
      return u ? await api.openDM(u.id) : null;
    }
    case "user-email": {
      const u = await api.lookupByEmail(t.email);
      return u ? await api.openDM(u.id) : null;
    }
    case "channel":
      return t.id;
    case "channel-name": {
      const c = await api.findChannelByName(t.name);
      return c ? c.id : null;
    }
  }
}

/** postWithJoin posts, and on not_in_channel joins the public channel once and posts again. */
async function postWithJoin(
  api: ReturnType<typeof slackApi>,
  channel: string,
  text: string,
  threadTs: string | null,
  metadata: Record<string, unknown>,
) {
  try {
    return await api.postMessage({ channel, text, threadTs, metadata });
  } catch (e) {
    if (e instanceof SlackError && e.code === "not_in_channel") {
      await api.joinChannel(channel);
      return await api.postMessage({ channel, text, threadTs, metadata });
    }
    throw e;
  }
}

function describeTarget(t: SlackTarget): string {
  switch (t.kind) {
    case "user":
    case "channel":
      return t.id;
    case "user-name":
      return "@" + t.name;
    case "user-email":
      return t.email;
    case "channel-name":
      return "#" + t.name;
  }
}

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
        summary: "slack gateway: not delivered",
        replyTo: m.message_id,
      }));
  } catch (e) {
    console.error("could not tell the sender", e instanceof Error ? e.message : String(e));
  }
}
