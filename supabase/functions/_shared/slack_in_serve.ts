// The Slack gateway's inbound endpoint, slack-in: Slack's Events API and slash-command endpoint. Answers the URL verification challenge, verifies every
// other request's signature, reads a person's DM, mention, reply in a Brigade thread or /brigade command, decides
// where it goes and sends it into the team as a Brigade message from that team's gateway session. One Slack app
// may serve several teams of the project (card 128): the target is resolved across every served team's roster,
// and an ambiguous one is answered with the candidates by team. What cannot be routed is answered where the
// person is: in the DM, ephemerally in the channel, or in the slash command's own reply. A channel reply that did
// not mention the bot and is not in a Brigade thread is not the gateway's, and is dropped without a word.
//
// What Slack is answered, and what it then does (work item 388). 200 once the gateway is done with the event:
// carried, or the person told why not; a later delivery of it is answered `duplicate`. 500 when something failed
// before that, or while an earlier delivery of the event is still being carried: Slack delivers an event again
// when it is answered a failure or not answered within three seconds, and that delivery is carried, because the
// event is written down as received only once its outcome is decided (the marks are in db.ts). It was written
// down first until then, and a failure after it lost the person's message.
//
// A team whose roster the gateway may not read is left out, and the others are served: its gateway member was
// revoked, which an eviction after a rotated join secret does, and until the team's installer has run again it
// would otherwise have failed every event of every team the app serves.
//
// The handler is here and not in slack-in/index.ts so that a test runs it against the local stack's database
// with a stand-in for Slack's API (../_live/gateway_in_test.ts): index.ts serves it and does nothing else.

import { slackApi } from "./providers/slack.ts";
import {
  asMember,
  BEING_CARRIED,
  carriedBefore,
  carriedIn,
  connect,
  done,
  isDone,
  listSessions,
  listSlackGateways,
  recordThread,
  release,
  RpcError,
  sendMessage,
  take,
  threadBySlack,
} from "./db.ts";
import {
  resolveRecipientAcross,
  type SessionRecord,
  type TeamRecipient,
  type TeamSessions,
} from "./gateway.ts";
import {
  escapeMrkdwn,
  parseCommandRequest,
  parseEventsRequest,
  resolveSlackInbound,
  slackBody,
  slackRoster,
  verifySlackSignature,
} from "./slack.ts";

const env = (k: string) => Deno.env.get(k) ?? "";

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

/** sentNote says where a person's message went: the session (and its team, when the gateway serves several),
 * and, when they named a member, why that one and which other sessions of theirs are online. */
function sentNote(r: Exclude<TeamRecipient, { error: string }>, teamName: string | null): string {
  const name = (s: SessionRecord) => `*${escapeMrkdwn(s.session_name)}* (\`…${s.session_id.slice(-5)}\`)`;
  let text = `Sent to ${name(r.session)}`;
  if (teamName !== null) text += ` on team *${escapeMrkdwn(teamName)}*`;
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

/** What a test puts in place of the world: the fetch Slack's API is reached with. */
export interface SlackInDeps {
  fetchFn?: typeof fetch;
}

/** The bot's own user id, learned once per instance from auth.test. */
let botUserId: string | null = null;

export function slackInHandler(deps: SlackInDeps = {}): (req: Request) => Promise<Response> {
  return (req: Request) => answer(req, deps);
}

async function answer(req: Request, deps: SlackInDeps): Promise<Response> {
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

  const api = slackApi(env("BRIGADE_SLACK_BOT_TOKEN"), deps.fetchFn);
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

  const botHandle = env("BRIGADE_SLACK_BOT_HANDLE") || "brigade";
  const sql = connect();
  const key = `slack:${m.eventId}`;
  // Whether this delivery holds the event's claim: taken before the event is carried, given back with the mark
  // once the gateway is done with it, and given back without the mark when the delivery fails.
  let carrying: string | null = null;
  const decided = async (response: Response): Promise<Response> => {
    if (carrying !== null) await done(sql, key, carrying);
    carrying = null;
    return response;
  };
  try {
    // Where the message goes is decided first: a channel reply outside a Brigade thread is dropped before
    // anything is recorded or read.
    const resolution = await resolveSlackInbound(m, { byThread: (c, t) => threadBySlack(sql, c, t) });
    if (resolution.kind === "ignore") return json(200, { ignored: resolution.reason });
    if (await isDone(sql, key)) return json(200, { duplicate: true });
    const gateways = await listSlackGateways(sql);
    if (gateways.length === 0) {
      throw new Error(
        "the slack gateway is not installed for any team of this project: run its install target",
      );
    }
    const taken = await take(sql, key);
    if (taken === "done") return json(200, { duplicate: true });
    if (taken === "carried") return json(500, { error: BEING_CARRIED });
    carrying = taken.token;
    // Every served team's roster, read as that team's gateway member, its own gateway session left out. A team
    // that refuses its gateway member the read is left out (the header); any other failure is the event's.
    const teams: TeamSessions[] = [];
    for (const g of gateways) {
      let roster: { sessions: unknown[] };
      try {
        roster = await asMember(sql, g.userId, (tx) => listSessions(tx, g.teamId, true));
      } catch (e) {
        if (!(e instanceof RpcError && e.code === "unauthorized")) throw e;
        console.error(
          `slack-in: team ${g.teamName} is left out: its gateway member may not read its roster (run the team's slack gateway install target again)`,
        );
        continue;
      }
      teams.push({
        teamId: g.teamId,
        teamName: g.teamName,
        sessions: (roster.sessions as SessionRecord[]).filter((s) => s.session_id !== g.sessionId),
      });
    }
    if (teams.length === 0) {
      throw new Error(
        "no team of this project lets its slack gateway read its roster: run each team's install target again",
      );
    }
    const rosterNote = slackRoster(teams, gateways.map((g) => g.sessionId), botHandle);

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

    // What a person is told is their answer, and the gateway is done with the event once it is decided, whether
    // or not the answer could be posted.
    const told = async (text: string): Promise<Response> => await decided(await tell(text));

    switch (resolution.kind) {
      case "invalid":
        return await told(`The lines at the top of your message could not be read: ${resolution.reason}.`);
      case "unaddressed":
        return await told(
          `No session was named. Start your message with a line \`to: <session or member>\`, or reply in the thread under a session's message.\n\n${rosterNote}`,
        );
      case "command": {
        const text = resolution.command === "sessions" || resolution.command === "help"
          ? rosterNote
          : `Unknown command \`${resolution.command}\`. Try \`sessions\`, \`help\`, or \`send <session or member> <text>\`.\n\n${rosterNote}`;
        return await told(text);
      }
      case "message": {
        // A thread reply's target is the full id of the session the thread belongs to, so it resolves like any
        // other id; the refusal may name sessions and teams, so it is escaped like every other untrusted text.
        const r = resolveRecipientAcross(resolution.target, teams);
        if ("error" in r) {
          return await told(`The message was not sent: ${escapeMrkdwn(r.error)}.\n\n${rosterNote}`);
        }
        const gw = gateways.find((g) => g.teamId === r.teamId);
        if (!gw) throw new Error(`team ${r.teamName} has no slack gateway row`);
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
          try {
            sent = await asMember(sql, gw.userId, (tx) => sendMessage(tx, args));
          } catch (e) {
            if (!(e instanceof RpcError && e.code === "not_found" && args.replyTo)) throw e;
            sent = await asMember(sql, gw.userId, (tx) => sendMessage(tx, { ...args, replyTo: null }));
          }
        } catch (e) {
          if (!(e instanceof RpcError)) throw e;
          if (!carriedBefore(e)) {
            return await told(
              `Brigade refused the message (${e.code}${e.detail ? ": " + e.detail : ""}). Try again later.`,
            );
          }
          // An event that is delivered again after a failure is sent again, and Brigade may hold its message
          // already. Its words can differ, because the person's and the channel's names are asked of Slack each
          // time: the key is then refused, and the row the earlier delivery recorded says which message it is
          // and where it went, which need not be where this delivery would have sent it. The event is carried,
          // and nobody is told that it was refused, nor told a second time where it went. With no such row
          // Brigade holds the message all the same.
          const known = await carriedIn(sql, "slack", m.eventId);
          if (known === null) return await decided(json(200, { duplicate: true }));
          return await decided(
            json(200, { message_id: known.messageId, to: known.sessionId, duplicate: true }),
          );
        }
        // The thread row, and after what the person is shown the mark that the gateway is done with the event.
        // A failure before the mark leaves the event to Slack's next delivery, which records the row.
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
        const note = sentNote(r, teams.length > 1 ? r.teamName : null);
        if (isCommand) return await decided(json(200, { response_type: "ephemeral", text: note }));
        if (r.who !== null) await tell(note);
        return await decided(json(200, {
          message_id: sent.message_id,
          to: r.session.session_id,
          team: gw.teamName,
          source: resolution.source,
          duplicate: sent.duplicate,
        }));
      }
    }
    // Every kind is answered above; the type checker does not see that the switch is whole.
    throw new Error("a message of no known kind");
  } catch (e) {
    console.error("slack-in failed", e instanceof Error ? e.message : String(e));
    return json(500, { error: e instanceof Error ? e.message : String(e) });
  } finally {
    if (carrying !== null) {
      // Best effort: a claim that stays ends by itself, and until then a delivery is told to come again.
      await release(sql, key, carrying).catch((e: unknown) =>
        console.error("slack-in: the claim was not given back", e instanceof Error ? e.message : String(e))
      );
    }
    await sql.end({ timeout: 2 });
  }
}
