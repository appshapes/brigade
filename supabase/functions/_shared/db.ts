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

export interface Gateway {
  teamId: string;
  teamName: string;
  userId: string;
  sessionId: string;
}

export async function loadGateway(sql: Sql, teamRef: string): Promise<Gateway> {
  const rows = await sql`
    select g.team_id, t.name as team_name, g.user_id, g.session_id
      from brigade_gateway.gateways g join brigade.teams t on t.id = g.team_id
     where g.team_id = ${teamRef}::uuid`;
  if (rows.length === 0) {
    throw new Error("the gateway is not installed for this team: run make gateway-install");
  }
  const r = rows[0];
  return { teamId: r.team_id, teamName: r.team_name, userId: r.user_id, sessionId: r.session_id };
}

export async function setGatewaySession(sql: Sql, teamId: string, sessionId: string): Promise<void> {
  await sql`update brigade_gateway.gateways set session_id = ${sessionId}::uuid where team_id = ${teamId}::uuid`;
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
  address: string;
  mail_message_id: string | null;
  provider_mail_id: string | null;
  subject: string | null;
}

/** markReceived records a provider mail id and answers true the first time it is seen (webhooks retry). */
export async function markReceived(sql: Sql, providerId: string): Promise<boolean> {
  const rows = await sql`insert into brigade_gateway.received (provider_mail_id) values (${providerId})
                         on conflict do nothing returning provider_mail_id`;
  return rows.length > 0;
}

export async function recordThread(sql: Sql, teamId: string, row: ThreadRow): Promise<void> {
  await sql`insert into brigade_gateway.threads
              (brigade_message_id, team_id, direction, session_id, address, mail_message_id, provider_mail_id, subject)
            values (${row.brigade_message_id}::uuid, ${teamId}::uuid, ${row.direction}, ${row.session_id}::uuid,
                    ${row.address}, ${row.mail_message_id}, ${row.provider_mail_id}, ${row.subject})
            on conflict (brigade_message_id) do nothing`;
}

export async function threadByBrigadeId(sql: Sql, id: string): Promise<ThreadRow | null> {
  if (!/^[0-9a-f-]{36}$/i.test(id)) return null;
  const rows =
    await sql`select brigade_message_id, direction, session_id, address, mail_message_id, provider_mail_id, subject
                           from brigade_gateway.threads where brigade_message_id = ${id}::uuid`;
  return rows.length ? (rows[0] as ThreadRow) : null;
}

export async function threadByMailIds(sql: Sql, ids: string[]): Promise<ThreadRow | null> {
  if (ids.length === 0) return null;
  const rows =
    await sql`select brigade_message_id, direction, session_id, address, mail_message_id, provider_mail_id, subject
                           from brigade_gateway.threads where mail_message_id = any(${ids}::text[])
                          order by created_at desc limit 1`;
  return rows.length ? (rows[0] as ThreadRow) : null;
}
