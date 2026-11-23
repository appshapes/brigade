// The gateway's view of the database. Two kinds of call: Brigade's own RPCs, run AS THE GATEWAY MEMBER by
// setting the JWT claims PostgREST would set (the same trick supabase/tests/helpers/auth.sql uses), so every
// limit, check and stamp of docs/protocol-v1.md applies to the gateway exactly as to any member; and the
// gateway's own tables in the brigade_gateway schema (supabase/migrations/*_mail_gateway.sql), which only
// postgres reads. The connection string is Supabase's injected SUPABASE_DB_URL, or BRIGADE_DB_URL when set.

import postgres from "npm:postgres@3.4.9";

// deno-lint-ignore no-explicit-any
export type Sql = any;

export function connect(): Sql {
  const url = Deno.env.get("BRIGADE_DB_URL") ?? Deno.env.get("SUPABASE_DB_URL");
  if (!url) throw new Error("no database url: SUPABASE_DB_URL is not set");
  return postgres(url, { prepare: false, max: 1, idle_timeout: 5, connect_timeout: 15 });
}

/** RpcError is a Brigade RPC refusal: code is one of docs/protocol-v1.md 4.6's twelve. */
export class RpcError extends Error {
  code: string;
  detail: string;
  constructor(code: string, detail: string, message: string) {
    super(message);
    this.code = code;
    this.detail = detail;
  }
}

/** mapError turns a Postgres error from an RPC into an RpcError: a `brigade:<code>[:<detail>]` message names the
 * code; anything else is `internal`. */
export function mapError(e: unknown): RpcError {
  const msg = e instanceof Error ? e.message : String(e);
  const m = /^brigade:([a-z_]+)(?::(.*))?$/.exec(msg.trim());
  if (m) return new RpcError(m[1], m[2] ?? "", msg);
  return new RpcError("internal", "", msg);
}

/** asMember runs fn in one transaction with uid's JWT claims set, as PostgREST presents an authenticated member. */
export async function asMember<T>(sql: Sql, uid: string, fn: (tx: Sql) => Promise<T>): Promise<T> {
  const claims = JSON.stringify({
    sub: uid,
    role: "authenticated",
    aud: "authenticated",
    is_anonymous: true,
  });
  try {
    return (await sql.begin(async (tx: Sql) => {
      await tx`select set_config('request.jwt.claim.sub', ${uid}, true), set_config('request.jwt.claims', ${claims}, true)`;
      return await fn(tx);
    })) as T;
  } catch (e) {
    throw mapError(e);
  }
}

export type GatewayKind = "email" | "slack";

export interface Gateway {
  teamId: string;
  teamName: string;
  userId: string;
  sessionId: string;
  kind: GatewayKind;
}

/** listSlackGateways returns every Slack gateway of the project with its team, oldest first. One Slack app serves
 * them all (card 128): the bot token, signing secret, handle and workspace are the project's, and nothing per
 * team is read from the environment. */
export async function listSlackGateways(sql: Sql): Promise<Gateway[]> {
  const rows = await sql`
    select g.team_id, t.name as team_name, g.user_id, g.session_id
      from brigade_gateway.gateways g join brigade.teams t on t.id = g.team_id
     where g.kind = 'slack'
     order by g.created_at`;
  return rows.map((r: Record<string, string>) => ({
    teamId: r.team_id,
    teamName: r.team_name,
    userId: r.user_id,
    sessionId: r.session_id,
    kind: "slack" as const,
  }));
}

/** MailSettings is what differs between two teams' mail gateways in one project: public values kept in the
 * gateways row (migration 20261005090000). The secrets are project-wide. */
export interface MailSettings {
  inbox: string;
  publicAddress: string;
  from: string;
  provider: string;
  sendUrl: string;
  /** Who may write (card 55): lowercase addresses and `@domain` entries; empty keeps the gateway open. */
  allowedSenders: string[];
}

export type MailGateway = Gateway & MailSettings;

/** mailSettingsFromEnv reads the project's BRIGADE_MAIL_* secrets, which name one team's settings: the fallback
 * for a gateways row installed before the settings columns existed. */
export function mailSettingsFromEnv(): { teamRef: string } & MailSettings {
  const env = (k: string) => Deno.env.get(k) ?? "";
  const inbox = env("BRIGADE_MAIL_INBOX").toLowerCase();
  return {
    teamRef: env("BRIGADE_TEAM_REF"),
    inbox,
    publicAddress: (env("BRIGADE_MAIL_PUBLIC_ADDRESS") || inbox).toLowerCase(),
    from: env("BRIGADE_MAIL_FROM"),
    provider: env("BRIGADE_MAIL_PROVIDER"),
    sendUrl: env("BRIGADE_MAIL_SEND_URL"),
    allowedSenders: [],
  };
}

/** listMailGateways returns every mail gateway of the project with its settings, oldest first. A row without
 * settings takes the project's secrets when it is the team they name, and is left out otherwise. The
 * allowed-senders list is the row's in either case: it was never a secret. */
export async function listMailGateways(
  sql: Sql,
  fallback: { teamRef: string } & MailSettings,
): Promise<MailGateway[]> {
  const rows = await sql`
    select g.team_id, t.name as team_name, g.user_id, g.session_id,
           g.inbox, g.public_address, g.from_address, g.provider, g.send_url, g.allowed_senders
      from brigade_gateway.gateways g join brigade.teams t on t.id = g.team_id
     where g.kind = 'email'
     order by g.created_at`;
  const out: MailGateway[] = [];
  for (const r of rows) {
    const base: Gateway = {
      teamId: r.team_id,
      teamName: r.team_name,
      userId: r.user_id,
      sessionId: r.session_id,
      kind: "email",
    };
    const allowedSenders = Array.isArray(r.allowed_senders) ? (r.allowed_senders as string[]) : [];
    if (r.inbox) {
      const inbox = String(r.inbox).toLowerCase();
      out.push({
        ...base,
        inbox,
        publicAddress: String(r.public_address || inbox).toLowerCase(),
        from: String(r.from_address ?? fallback.from),
        provider: String(r.provider ?? fallback.provider),
        sendUrl: String(r.send_url ?? fallback.sendUrl),
        allowedSenders,
      });
    } else if (r.team_id === fallback.teamRef && fallback.inbox) {
      out.push({ ...base, ...fallback, allowedSenders });
    } else {
      console.error(
        `mail gateway of team ${r.team_name} has no settings yet: run make gateway-install for it`,
      );
    }
  }
  return out;
}

export async function setGatewaySession(
  sql: Sql,
  teamId: string,
  kind: GatewayKind,
  sessionId: string,
): Promise<void> {
  await sql`update brigade_gateway.gateways set session_id = ${sessionId}::uuid
             where team_id = ${teamId}::uuid and kind = ${kind}`;
}

// ---- Brigade RPCs, as the member -------------------------------------------------------------------------

export interface SendArgs {
  sender: string;
  recipient: string;
  body: string;
  key: string;
  summary: string | null;
  replyTo: string | null;
}

export async function sendMessage(tx: Sql, a: SendArgs) {
  const rows =
    await tx`select brigade.send_message(${a.sender}::uuid, ${a.recipient}::uuid, ${a.body}, ${a.key},
                                                   ${a.summary}, ${a.replyTo}::uuid) as r`;
  return rows[0].r as { message_id: string; duplicate: boolean; hop_count: number };
}

export async function fetchInbox(tx: Sql, session: string, limit = 50) {
  const rows = await tx`select brigade.fetch_inbox(${session}::uuid, ${limit}) as r`;
  return (rows[0].r ?? []) as unknown[];
}

export async function ackMessages(tx: Sql, session: string, ids: string[]) {
  const rows = await tx`select brigade.ack_messages(${session}::uuid, ${ids}::uuid[]) as r`;
  return rows[0].r as { acked: string[]; unknown: string[] };
}

export async function heartbeat(tx: Sql, session: string, description: string, lease = 600) {
  const rows =
    await tx`select brigade.session_heartbeat(${session}::uuid, 'idle', null, ${description}, 'accept', ${lease}) as r`;
  return rows[0].r as { state: string };
}

export async function registerSession(
  tx: Sql,
  teamId: string,
  name: string,
  description: string,
  harness: string,
  version: string,
  resume: string | null,
) {
  const rows =
    await tx`select brigade.register_session(${teamId}::uuid, ${name}, ${description}, 'idle', 'accept',
                                                       ${harness}, ${version}, null, 600, ${resume}::uuid) as r`;
  return rows[0].r as { session_id: string; resumed: boolean };
}

export async function listSessions(tx: Sql, teamId: string, includeOffline = true) {
  const rows = await tx`select brigade.list_sessions(${teamId}::uuid, ${includeOffline}, 500) as r`;
  return rows[0].r as { team_name: string; sessions: unknown[] };
}

// ---- The gateway's own tables ----------------------------------------------------------------------------

export interface ThreadRow {
  brigade_message_id: string;
  direction: "in" | "out";
  session_id: string;
  /** email: the person's address; slack: the channel the message lives in. */
  address: string;
  /** email: the mail's Message-ID; slack: the ts of the thread's root message. */
  mail_message_id: string | null;
  /** email: the provider's id for the mail; slack: the event id (in) or the posted ts (out). */
  provider_mail_id: string | null;
  subject: string | null;
  /** The gateway kind the row belongs to; the first migration's rows read as email. */
  kind?: GatewayKind;
  /** slack: the person's Slack user id. */
  actor?: string | null;
}

// What the gateway knows of a person's message, by the id its provider gave it (work item 388). A provider
// delivers a message again until it is answered 200, and the gateway takes each message once. It used to write
// the id down before it carried the message, and answered every later delivery `duplicate`: when anything failed
// after that, the message was gone, with no bounce and no session told. Now the id is written down once the
// gateway is DONE with the message, as the server's gateways do (`server/crates/brigade-server/src/gateway/`),
// and while a delivery carries the message it holds a claim on it, so that a second delivery which comes
// meanwhile (Slack sends one when three seconds pass without an answer) is not carried beside the first. Both
// are rows of brigade_gateway.received and need no migration: `<id>` once done, as every row before this was,
// and `carrying:<md5 of the id>` for the claim, which fits the column whatever the id's length. A claim is its
// delivery's own: the moment it was taken is its token, and a delivery gives back only the claim that carries its
// token, so one that outlived its claim does not take away the claim of the delivery that came after it. The
// token is the moment as text, and is compared as text: handed to the database as a time, the client would cut it
// to milliseconds, and it would match no claim.

/** How long a claim holds. A delivery that failed gives its claim back at once; one that was cut off (a
 * function killed at its time limit) cannot, and its claim ends by itself after this, well before a provider's
 * later deliveries. A delivery that is still carrying after this long is carried beside. */
export const CARRYING_SECONDS = 60;

/** isDone answers whether the gateway is done with the message: every later delivery of it is a duplicate. */
export async function isDone(sql: Sql, providerId: string): Promise<boolean> {
  const rows = await sql`select 1 from brigade_gateway.received where provider_mail_id = ${providerId}`;
  return rows.length > 0;
}

/** claim takes the message for this delivery and answers the claim's token, or null when another delivery
 * holds it now. */
export async function claim(
  sql: Sql,
  providerId: string,
  seconds = CARRYING_SECONDS,
): Promise<string | null> {
  const rows = await sql`insert into brigade_gateway.received (provider_mail_id, received_at)
                         values ('carrying:' || md5(${providerId}), clock_timestamp())
                         on conflict (provider_mail_id) do update set received_at = clock_timestamp()
                           where brigade_gateway.received.received_at < now() - make_interval(secs => ${seconds})
                         returning received_at::text as token`;
  return rows.length ? (rows[0].token as string) : null;
}

/** take is what a delivery does before it carries a message: it answers the claim's token, or `done` when the
 * gateway is done with the message, or `carried` when another delivery holds it now. The mark is looked for
 * AFTER the claim is taken: a delivery that finished between a look and a claim has left the mark and given its
 * claim back in one statement, and a look before the claim alone would carry the message a second time. */
export async function take(sql: Sql, providerId: string): Promise<{ token: string } | "done" | "carried"> {
  const token = await claim(sql, providerId);
  if (token === null) return (await isDone(sql, providerId)) ? "done" : "carried";
  if (await isDone(sql, providerId)) {
    await release(sql, providerId, token);
    return "done";
  }
  return { token };
}

/** done writes the message down as one the gateway is done with, and gives the delivery's claim back, in one
 * statement. */
export async function done(sql: Sql, providerId: string, token: string): Promise<void> {
  await sql`with marked as (insert into brigade_gateway.received (provider_mail_id) values (${providerId})
                            on conflict do nothing)
            delete from brigade_gateway.received
             where provider_mail_id = 'carrying:' || md5(${providerId}) and received_at::text = ${token}`;
}

/** release gives the delivery's claim back without the mark: it failed, and the next one carries the message. */
export async function release(sql: Sql, providerId: string, token: string): Promise<void> {
  await sql`delete from brigade_gateway.received
             where provider_mail_id = 'carrying:' || md5(${providerId}) and received_at::text = ${token}`;
}

/** carriedIn finds what an earlier delivery made of a person's message, by the row it recorded: the Brigade
 * message and the session it went to. */
export async function carriedIn(
  sql: Sql,
  kind: GatewayKind,
  providerId: string,
): Promise<{ messageId: string; sessionId: string } | null> {
  const rows = await sql`select brigade_message_id, session_id from brigade_gateway.threads
                          where kind = ${kind} and direction = 'in' and provider_mail_id = ${providerId}
                          order by created_at desc limit 1`;
  return rows.length
    ? { messageId: rows[0].brigade_message_id as string, sessionId: rows[0].session_id as string }
    : null;
}

/** carriedBefore says whether a send was refused because Brigade already holds a message under its key, sent
 * with other words. A key is one mail's or one Slack event's, so that message is this one, carried by an earlier
 * delivery that failed before the gateway was done: only what the provider said of it in between has changed. */
export function carriedBefore(e: unknown): boolean {
  return e instanceof RpcError && e.code === "conflict" && e.detail === "idempotency_key";
}

/** What a delivery is answered while another delivery of the same message holds its claim: a failure, so that
 * the provider delivers again, and by then the first has decided what the message became. */
export const BEING_CARRIED = "an earlier delivery of this message is being carried";

export async function recordThread(sql: Sql, teamId: string, row: ThreadRow): Promise<void> {
  await sql`insert into brigade_gateway.threads
              (brigade_message_id, team_id, direction, session_id, address, mail_message_id, provider_mail_id, subject, kind, actor)
            values (${row.brigade_message_id}::uuid, ${teamId}::uuid, ${row.direction}, ${row.session_id}::uuid,
                    ${row.address}, ${row.mail_message_id}, ${row.provider_mail_id}, ${row.subject},
                    ${row.kind ?? "email"}, ${row.actor ?? null})
            on conflict (brigade_message_id) do nothing`;
}

export async function threadByBrigadeId(sql: Sql, id: string): Promise<ThreadRow | null> {
  if (!/^[0-9a-f-]{36}$/i.test(id)) return null;
  const rows =
    await sql`select brigade_message_id, direction, session_id, address, mail_message_id, provider_mail_id,
                                subject, kind, actor
                           from brigade_gateway.threads where brigade_message_id = ${id}::uuid`;
  return rows.length ? (rows[0] as ThreadRow) : null;
}

export async function threadByMailIds(sql: Sql, ids: string[]): Promise<ThreadRow | null> {
  if (ids.length === 0) return null;
  const rows =
    await sql`select brigade_message_id, direction, session_id, address, mail_message_id, provider_mail_id,
                                subject, kind, actor
                           from brigade_gateway.threads
                          where kind = 'email' and mail_message_id = any(${ids}::text[])
                          order by created_at desc limit 1`;
  return rows.length ? (rows[0] as ThreadRow) : null;
}

/** threadBySlack finds the newest row of a Slack thread: the channel and the ts of its root message. */
export async function threadBySlack(sql: Sql, channel: string, threadTs: string): Promise<ThreadRow | null> {
  const rows =
    await sql`select brigade_message_id, direction, session_id, address, mail_message_id, provider_mail_id,
                                subject, kind, actor
                           from brigade_gateway.threads
                          where kind = 'slack' and address = ${channel} and mail_message_id = ${threadTs}
                          order by created_at desc limit 1`;
  return rows.length ? (rows[0] as ThreadRow) : null;
}
