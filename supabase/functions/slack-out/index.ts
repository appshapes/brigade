// slack-out: the tick. Called every minute by pg_cron through pg_net with the tick token. For every Slack gateway
// of the project (one Slack app serves them all, card 128) it keeps the gateway session's lease alive, drains its
// inbox, posts each message to the person or channel it resolves to (a reply goes into the thread it answers),
// turns the @handles in the text into mentions, records the thread, and acknowledges only after Slack accepted
// the post. A message with no address is answered to its sender and acknowledged.

import { type SlackApi, slackApi, SlackError } from "../_shared/providers/slack.ts";
import {
  ackMessages,
  asMember,
  connect,
  fetchInbox,
  type Gateway,
  heartbeat,
  listSlackGateways,
  recordThread,
  registerSession,
  RpcError,
  sendMessage,
  setGatewaySession,
  threadByBrigadeId,
} from "../_shared/db.ts";
import { type Envelope, excerpt } from "../_shared/gateway.ts";
import {
  isSlackUserId,
  mentionNames,
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

/** What one tick learns from Slack once and every gateway of the project shares: the @handle lookups, and whether
 * the token carries the channel-history scopes (asked only when a message is about to go out). */
interface TickShared {
  names: Map<string, string | null>;
  hearsChannelThreads: boolean | null;
}

interface TickReport {
  team: string;
  heartbeat: string;
  delivered: number;
  undeliverable: number;
  retry: number;
}

Deno.serve(async (req) => {
  if (req.method !== "POST") return json(405, { error: "method not allowed" });
  const token = env("BRIGADE_SLACK_TICK_TOKEN");
  if (!token || req.headers.get("x-brigade-tick") !== token) return json(401, { error: "bad tick token" });

  const botHandle = env("BRIGADE_SLACK_BOT_HANDLE") || "brigade";
  const workspace = env("BRIGADE_SLACK_WORKSPACE") || "slack";
  const api = slackApi(env("BRIGADE_SLACK_BOT_TOKEN"));
  const sql = connect();
  const report = {
    heartbeat: "",
    delivered: 0,
    undeliverable: 0,
    retry: 0,
    error: null as string | null,
    errors: [] as string[],
    gateways: [] as TickReport[],
  };
  try {
    // One project may carry several teams' Slack gateways, all served by this app: each is heartbeated and
    // drained in turn, and one team's failure does not stop the others.
    const gateways = await listSlackGateways(sql);
    const shared: TickShared = { names: new Map(), hearsChannelThreads: null };
    for (const gw of gateways) {
      const one: TickReport = { team: gw.teamName, heartbeat: "", delivered: 0, undeliverable: 0, retry: 0 };
      try {
        await tick(
          sql,
          api,
          gw,
          slackDescription(botHandle, workspace),
          botHandle,
          shared,
          one,
          report.errors,
        );
      } catch (e) {
        const msg = `${gw.teamName}: ${e instanceof Error ? e.message : String(e)}`;
        console.error("slack-out failed for a gateway", msg);
        report.errors.push(msg);
        report.error ??= msg;
      }
      report.gateways.push(one);
      report.delivered += one.delivered;
      report.undeliverable += one.undeliverable;
      report.retry += one.retry;
    }
    report.heartbeat = report.gateways.map((g) => g.heartbeat).join(",");
    return json(report.error ? 500 : 200, report);
  } catch (e) {
    console.error("slack-out failed", e instanceof Error ? e.message : String(e));
    return json(500, { ...report, error: e instanceof Error ? e.message : String(e) });
  } finally {
    await sql.end({ timeout: 2 });
  }
});

/** tick heartbeats one gateway's session, drains its inbox and posts each message to Slack. */
async function tick(
  sql: ReturnType<typeof connect>,
  api: SlackApi,
  gw: Gateway,
  desc: string,
  botHandle: string,
  shared: TickShared,
  one: TickReport,
  errors: string[],
) {
  try {
    one.heartbeat = (await asMember(sql, gw.userId, (tx) => heartbeat(tx, gw.sessionId, desc))).state;
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
      one.heartbeat = r.resumed ? "resumed" : "registered";
    } else {
      throw e;
    }
  }

  const inbox = (await asMember(sql, gw.userId, (tx) => fetchInbox(tx, gw.sessionId, 50))) as Envelope[];
  const threads = { byBrigadeId: (id: string) => threadByBrigadeId(sql, id) };
  // The footer promises that a reply in a channel thread is heard only once the installed app's token carries
  // the channel-history scopes, which the administrator grants by reinstalling the app; until then it asks for
  // the mention. Read from Slack's own header, so a redeploy before the reinstall tells nobody something false.
  if (inbox.length > 0 && shared.hearsChannelThreads === null) {
    shared.hearsChannelThreads = await hearsThreads(api);
  }
  const hearsChannelThreads = shared.hearsChannelThreads ?? false;
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
      one.undeliverable++;
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
        one.undeliverable++;
        continue;
      }
      channel = c;
    } catch (e) {
      if (e instanceof SlackError && e.retryable) {
        one.retry++;
        errors.push(e.message);
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
      one.undeliverable++;
      continue;
    }
    let mentions: Record<string, string>;
    try {
      mentions = await resolveMentions(api, out.text, shared.names);
    } catch (e) {
      if (e instanceof SlackError && e.retryable) {
        one.retry++;
        errors.push(e.message);
        continue;
      }
      mentions = {}; // a handle Slack would not look up stays text
    }
    const text = renderSlackText(m, out, {
      teamName: gw.teamName,
      botHandle,
      hearsChannelThreads,
      mentions,
    });
    const metadata = {
      event_type: "brigade_message",
      event_payload: { message_id: m.message_id, from_session: m.sender.session_id, team: gw.teamName },
    };
    let posted: { ts: string; channel: string };
    try {
      posted = await postWithJoin(api, channel, text, out.threadTs, metadata);
    } catch (e) {
      if (e instanceof SlackError && e.retryable) {
        one.retry++;
        errors.push(e.message);
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
      one.undeliverable++;
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
    one.delivered++;
  }
}

/** hearsThreads says whether the bot token carries channels:history, the scope under which Slack delivers a
 * channel thread's replies to slack-in. False when Slack cannot be asked: the footer then asks for the mention,
 * which always works. */
async function hearsThreads(api: SlackApi): Promise<boolean> {
  try {
    return (await api.scopes()).includes("channels:history");
  } catch {
    return false;
  }
}

/** resolveMentions finds the Slack user behind each @handle in a session's text: a user id written bare as it
 * is, a name through the user list (a bot, the gateway's own included, never matches); an unknown name stays
 * text. One lookup per name per tick. A retryable Slack error propagates, so the message waits for the next
 * tick instead of going out with the name unlinked. */
async function resolveMentions(
  api: SlackApi,
  text: string,
  cache: Map<string, string | null>,
): Promise<Record<string, string>> {
  const ids: Record<string, string> = {};
  for (const name of mentionNames(text)) {
    let id: string | null;
    if (isSlackUserId(name)) id = name;
    else {
      const key = name.toLowerCase();
      if (!cache.has(key)) cache.set(key, (await api.findUserByName(name))?.id ?? null);
      id = cache.get(key) ?? null;
    }
    if (id) ids[name] = id;
  }
  return ids;
}

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
        summary: "slack gateway: not delivered",
        replyTo: m.message_id,
      }));
  } catch (e) {
    console.error("could not tell the sender", e instanceof Error ? e.message : String(e));
  }
}
