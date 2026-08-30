# Supabase Realtime and durable delivery for the Brigade default adapter

Date: 2026-08-30. Scope: how the default Supabase adapter's `message watch` process should receive new messages for one session, including catch-up after being offline, and how session leases, acknowledgment, deduplication, and retention should work.

Verification sources used, in order of authority:

1. Live local Supabase stack found running on this machine (`supabase_db_sbtest`: `public.ecr.aws/supabase/postgres:17.6.1.165`, PostgreSQL 17.6; `supabase_realtime_sbtest`: `realtime:v2.129.3`; `supabase_auth_sbtest`: `gotrue:v2.196.0`). Only read-only queries and one rolled-back transaction were run against it. Function bodies, DDL, grants and role flags below are copied from it.
2. Library source at the current monorepo location (`supabase-js` 2.112.4, published 2026-08-24; `@supabase/realtime-js` 2.112.4; `@supabase/phoenix` 0.4.5; `@supabase/auth-js` 2.112.4), downloaded and grepped today.
3. Official docs pages, fetched today (URLs inline).

Confidence labels: **verified** (docs + source or live DB), **likely** (one source, or inferred from source without a doc statement), **uncertain**.

---

## 1. Summary of the recommendation

Use the hybrid the logical plan already assumes, with these concrete choices:

- **Durable rows are the only source of truth.** `public.messages` rows are inserted by a `send_message` RPC that stamps sender identity and server time. A send is "accepted" when the insert commits.
- **Realtime is a wake-up signal, not a transport.** An `AFTER INSERT` trigger on `public.messages` calls `realtime.send(...)` with a small payload (`message_id`, `seq`, `recipient_session_id`) on the private topic `session:<recipient_session_id>`. The watcher never injects from the realtime payload; it runs one `drain()` routine that queries unacknowledged rows.
- **`drain()` runs on every `SUBSCRIBED` callback (initial join and every automatic rejoin after a reconnect), on every broadcast event, and on a slow safety timer.** This gives at-least-once delivery on top of Realtime's at-most-once fan-out, and covers messages that arrived while offline.
- **Acknowledge by updating `delivery_state` on the row** (`accepted -> injected`) through an `ack_messages(uuid[])` RPC, after the local injection succeeded. The "cursor" for catch-up is the set of rows still in `accepted`, not a numeric watermark (see 6.2 for why a `seq > last` cursor can skip rows).
- **Deduplicate by `message_id`** in the watcher with a bounded in-memory set; duplicates are expected and harmless.
- **Session state comes from heartbeat rows, not Presence.** `sessions.last_seen_at` plus `activity` and a lease length are enough for a `session_states` view computing `active | idle | offline`.
- **Retention:** a `gc_expired()` function called from a pg_cron job (hosted and local both have pg_cron), and also opportunistically from the heartbeat RPC so the schema works without pg_cron.
- **Node 24 needs no `ws` package.** supabase-js >= 2.55.0 uses the global `WebSocket`; `engines.node >= 22`.

Broadcast-from-database is preferred over `postgres_changes`. At Brigade scale either would work; the reasons are operational (section 3.5), not throughput.

---

## 2. postgres_changes: what it is and why it is the fallback, not the default

### 2.1 Enabling

Tables must be added to the `supabase_realtime` publication: `alter publication supabase_realtime add table public.messages;` (dashboard toggle or SQL). **Verified.** https://supabase.com/docs/guides/realtime/postgres-changes

On the local stack the publication exists but is empty by default (`puballtables = f`, zero rows in `pg_publication_tables`). **Verified live.**

### 2.2 Row Level Security is evaluated per subscriber, per event

Docs: "Realtime authorizes every event against each subscriber. When you make a single change to a table with 100 subscribed users, Realtime performs 100 authorization checks — one per user." Throughput therefore "scales with subscriber count, not write volume." Changes "are processed on a single thread to preserve their order, which means larger compute add-ons don't meaningfully increase Postgres Changes throughput." **Verified.** https://supabase.com/docs/guides/realtime/postgres-changes

Two RLS caveats:

- "RLS policies are not applied to `DELETE` statements, because there is no way for Postgres to verify that a user has access to a deleted record." Deletes are broadcast to every subscriber of the table (subject to the filter). Do not put secrets in rows you delete while subscribers exist. **Verified.**
- Tables outside `public` need `grant select on "schema"."table" to authenticated;`. **Verified.**

Documented throughput on a Micro instance with RLS enabled: 500 clients about 30 DB changes/sec; 3,000 clients about 5 DB changes/sec; p95 latency 228-616 ms. The docs' rule: "For more than approximately 3,000 concurrent subscribers on identical changes, use Broadcast instead." **Verified** (numbers from the postgres-changes page; the separate benchmarks page reports a different run, https://supabase.com/docs/guides/realtime/benchmarks).

### 2.3 REPLICA IDENTITY

`alter table public.messages replica identity full;` is required to receive `old` on UPDATE/DELETE and "You can only filter Delete events when the table has replica identity set to full." Not required for INSERT-only subscriptions like an inbox. **Verified.**

### 2.4 Filter syntax

`filter: 'recipient_session_id=eq.<uuid>'`. Operators: `eq, neq, lt, lte, gt, gte, in` (max 100 values), `like, ilike, match, imatch, is, isdistinct`; `not.` prefix negates; commas combine as AND (`a=eq.1,b=gt.2`); OR is not supported; values containing `, ( ) " \` must be double-quoted PostgREST-style. The realtime-js 2.112 source confirms the comma-AND and `not.` forms and adds a typed `postgresChangesFilter()` builder. **Verified** (docs + `RealtimeChannel.ts` lines 186-206).

### 2.5 Payload and size cap

Client receives `{ schema, table, commit_timestamp, eventType, new, old, errors }`. The WAL reader trims records larger than `max_record_bytes` (default 1 MiB) to fields <= 64 bytes and sets `errors = ["Error 413: Payload Too Large"]`. Plan limit table says "Postgres change payload: 1,024 KB" on every plan. **Verified.** https://github.com/supabase/walrus/blob/master/README.md, https://supabase.com/docs/guides/realtime/limits

### 2.6 Operational costs specific to postgres_changes

- A dedicated "Postgres Changes connection pool" (default 2, max 20) plus a replication slot are only started when postgres_changes is used; `DatabaseLackOfConnections` is a documented error when the tenant database has no spare connections. On Free-tier Nano compute this is a real risk. **Verified.** https://supabase.com/docs/guides/realtime/settings, https://supabase.com/docs/guides/realtime/error_codes
- The Supabase Realtime AI-prompt guidance now says "`postgres_changes` should be avoided due to scalability limitations" and "Use `broadcast` for all realtime events (database changes via triggers, ...)". **Verified.** https://supabase.com/docs/guides/ai-tools/ai-prompts/use-realtime

### 2.7 Sketch (only if you want the zero-trigger spike first)

```ts
// supabase-js 2.112.x, Node 24. Requires: alter publication supabase_realtime add table public.messages;
const ch = supabase
  .channel(`inbox:${sessionId}`, { config: { private: true } })   // private join still needs a realtime.messages SELECT policy (section 3.3)
  .on('postgres_changes',
      { event: 'INSERT', schema: 'public', table: 'messages', filter: `recipient_session_id=eq.${sessionId}` },
      (p) => { if (p.errors) log(p.errors); void drain() })     // treat as a hint; drain() is the real path
  .subscribe((status, err) => { if (status === 'SUBSCRIBED') void drain(); else log(status, err) })
```

The `SUBSCRIBED` callback is only invoked after the server echoes back matching postgres_changes bindings; a mismatch produces `CHANNEL_ERROR("mismatch between server and client bindings for postgres_changes")`. **Verified** (`RealtimeChannel.ts` `_updatePostgresBindings`).

---

## 3. Broadcast from the database (recommended)

### 3.1 Mechanism

"Realtime Broadcast from Database sets up a replication slot against a publication created for the `realtime.messages` table. This lets Realtime listen for Write Ahead Log (WAL) changes whenever new rows are inserted. When Realtime spots a new insert in the WAL, it broadcasts that message to the target channel right away." **Verified.** https://supabase.com/blog/realtime-broadcast-from-database, https://supabase.com/docs/guides/realtime/architecture

The connection pool for broadcast is "a single always-active connection" (unlike postgres_changes pools, which are "only started if you use Postgres Changes"). **Verified.** https://supabase.com/docs/guides/realtime/concepts

### 3.2 The functions, as they exist in realtime v2.129.3 (copied from the live DB)

```sql
-- realtime.send: inserts one row into realtime.messages. private defaults to TRUE.
CREATE OR REPLACE FUNCTION realtime.send(payload jsonb, event text, topic text, private boolean DEFAULT true)
 RETURNS void LANGUAGE plpgsql AS $function$
DECLARE generated_id uuid; final_payload jsonb;
BEGIN
  BEGIN
    generated_id := gen_random_uuid();
    -- Check if payload has an 'id' key, if not, add the generated UUID
    IF payload ? 'id' THEN final_payload := payload;
    ELSE final_payload := jsonb_set(payload, '{id}', to_jsonb(generated_id)); END IF;
    EXECUTE format('SET LOCAL realtime.topic TO %L', topic);
    INSERT INTO realtime.messages (id, payload, event, topic, private, extension)
    VALUES (generated_id, final_payload, event, topic, private, 'broadcast');
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'WarnSendingBroadcastMessage: %', SQLERRM;
  END;
END; $function$;

-- realtime.broadcast_changes: wraps the row in {old_record, record, operation, table, schema} and calls realtime.send (private = true).
CREATE OR REPLACE FUNCTION realtime.broadcast_changes(topic_name text, event_name text, operation text, table_name text,
  table_schema text, new record, old record, level text DEFAULT 'ROW'::text) RETURNS void LANGUAGE plpgsql AS $function$
DECLARE row_data jsonb := '{}'::jsonb;
BEGIN
  IF level = 'STATEMENT' THEN RAISE EXCEPTION 'function can only be triggered for each row, not for each statement'; END IF;
  IF operation = 'INSERT' OR operation = 'UPDATE' OR operation = 'DELETE' THEN
    row_data := jsonb_build_object('old_record', OLD, 'record', NEW, 'operation', operation, 'table', table_name, 'schema', table_schema);
    PERFORM realtime.send (row_data, event_name, topic_name);
  ELSE RAISE EXCEPTION 'Unexpected operation type: %', operation; END IF;
EXCEPTION WHEN OTHERS THEN RAISE EXCEPTION 'Failed to process the row: %', SQLERRM;
END; $function$;

-- realtime.topic(): the topic being joined or written, for use in RLS policies. No 'realtime:' prefix.
CREATE OR REPLACE FUNCTION realtime.topic() RETURNS text LANGUAGE sql STABLE AS $function$
select nullif(current_setting('realtime.topic', true), '')::text; $function$;
```

Facts that follow from the bodies (**verified live**):

- `realtime.send` swallows every error into a `WARNING`. A failed notification never rolls back the caller's transaction. Good for durability; bad for observability: a misconfigured trigger silently broadcasts nothing.
- `realtime.send` adds an `id` key to your payload (the `realtime.messages` row id) unless you supplied one.
- `broadcast_changes` sends `private = true` always. Clients must join with `{ config: { private: true } }`; "A public broadcast only reaches public channels and a private broadcast only reaches private channels." https://supabase.com/docs/guides/realtime/broadcast
- These functions are `SECURITY INVOKER`, owned by `supabase_realtime_admin`, executable by PUBLIC. They run as whatever role fired the trigger.

### 3.3 Authorization: RLS on `realtime.messages`

Live DDL of `realtime.messages` (realtime v2.129.3):

```text
topic text NOT NULL, extension text NOT NULL, payload jsonb, event text, private boolean DEFAULT false,
updated_at timestamp NOT NULL DEFAULT now(), inserted_at timestamp NOT NULL DEFAULT now(),
id uuid NOT NULL DEFAULT gen_random_uuid(), binary_payload bytea
Partitioned by RANGE (inserted_at), daily partitions (five existed: 2026-08-29 .. 2026-09-02)
PK (id, inserted_at); index (inserted_at DESC, topic) WHERE extension='broadcast' AND private IS TRUE
RLS enabled; NO policies by default
Grants: anon/authenticated/service_role: INSERT, SELECT, UPDATE; postgres: all; owner supabase_realtime_admin
```

How join authorization works (docs, **verified**): when a client joins a private topic, Realtime, using the client's JWT, "performs a query on the `realtime.messages` table and then rolls it back" — a SELECT policy grants the right to receive broadcasts on that topic, an INSERT policy grants the right to send. Policies are "cached for the connection duration and not queried for every message"; they are re-evaluated "when a client connects and subscribes to a Channel" and when "a new JWT arrives via the `access_token` message". "If no new JWT is received before expiration, the client disconnects." https://supabase.com/docs/guides/realtime/authorization

Realtime Settings has an "Allow public access" toggle; when disabled, only private channels can be joined and "every join is checked against the Row Level Security policies on `realtime.messages`". Disable it for the Brigade project. **Verified.** https://supabase.com/docs/guides/realtime/settings

Role facts from the live DB (**verified**): `postgres` has `rolbypassrls = true` (and `service_role`), `authenticated` and `anon` do not.

Consequence, and the single most important gotcha in this digest: the trigger function on `public.messages` **must be `SECURITY DEFINER` and owned by `postgres`** (which it is when created by a migration or the SQL editor). Otherwise the trigger runs as `authenticated`, `realtime.send`'s INSERT into `realtime.messages` is blocked by the policy-less RLS table, the error is downgraded to a WARNING, the user's `public.messages` insert commits, and no notification is ever sent. The official trigger template is `security definer` for this reason. With no INSERT policy for `authenticated`, clients also cannot publish arbitrary broadcasts on session topics over the socket, which is what we want: only the database emits.

Policy for Brigade (topic `session:<session_id>`; only the principal that registered that session may receive):

```sql
-- Receive-only: no INSERT policy, no presence policy.
create policy "session owner receives inbox notifications"
on realtime.messages for select to authenticated
using (
  realtime.messages.extension = 'broadcast'
  and exists (
    select 1 from public.sessions s
    where s.principal_id = (select auth.uid())
      and s.closed_at is null
      and (select realtime.topic()) = 'session:' || s.id::text
  )
);
```

Anonymous users get the `authenticated` role with an `is_anonymous` JWT claim, so `to authenticated` covers them. **Verified.** https://supabase.com/docs/guides/auth/auth-anonymous

Topic naming: docs recommend `scope:entity`; `realtime.topic()` returns the bare topic (the client prepends `realtime:` on the wire; `realtime.send` sets the setting to the raw `topic` argument). **Verified for send (function body); likely for join (docs examples match a bare `room_topic` column).**

### 3.4 Delivery semantics: at-most-once, retention 3 days, replay of up to 25

- Persistence: "All the messages sent using Broadcast from the Database are stored in `realtime.messages` table and will be deleted after 3 days." "Messages are stored in daily partitions, and partitions older than 72 hours are dropped. Because whole days are removed at once, a message stays available for at least 72 hours and at most 4 days." **Verified.** https://supabase.com/docs/guides/realtime/broadcast
- Delivery guarantee: no docs page states one. The architecture is fan-out without per-subscriber tracking; a disconnected subscriber's messages are simply not delivered to it. A community answer in the Supabase discussions puts it as "fire and forget... on reconnect always fetch current state from the database." **Likely** (no official statement; consistent with the docs' own "Broadcast replay" feature existing at all). https://github.com/orgs/supabase/discussions/21093
- Broadcast replay (private channels only; supabase-js >= 2.74.0): `config.broadcast.replay = { since: <unix ms>, limit: <= 25 }` replays stored messages on join, marked `payload.meta.replayed = true`. Limited to 25 messages and 72 hours, so it is not a substitute for a database catch-up query. Not recommended for Brigade; `drain()` covers it. **Verified.**
- Ordering: no ordering guarantee is documented for Broadcast. The WAL reader is sequential, so per-topic order is preserved in practice, but the watcher must not rely on it. `postgres_changes` docs do state ordering is preserved (single thread). **Likely** for Broadcast; **verified** for postgres_changes.
- `ack: true` on a channel only means "the server has received the message before resolving `send`'s promise"; it is a sender-side ack, not delivery. Irrelevant when the database is the sender. **Verified.**
- Payload size: Broadcast payload limit is 256 KB on Free, 3,000 KB on Pro/Team. The Brigade notification payload is under 200 bytes. **Verified.** https://supabase.com/docs/guides/realtime/limits

### 3.5 Why Broadcast-from-DB over postgres_changes for Brigade

1. Authorization is per topic, checked at join and cached, instead of per event per subscriber. It is also expressed once, in one policy, keyed on the session id.
2. No publication changes, no `REPLICA IDENTITY FULL`, no 1 MiB record trimming, no unfiltered DELETE fan-out.
3. No postgres_changes connection pool or second replication slot on a Free-tier database.
4. Supabase's own current guidance.
5. The trigger is the same shape whether we later want a lean notification or a full-record broadcast.

Cost: one migration for the trigger and one RLS policy on `realtime.messages`, plus the SECURITY DEFINER gotcha above.

### 3.6 Trigger for Brigade (lean notification)

```sql
create or replace function public.notify_message_inserted()
returns trigger
language plpgsql
security definer            -- REQUIRED: runs as postgres (BYPASSRLS) so the insert into realtime.messages succeeds
set search_path = ''
as $$
begin
  perform realtime.send(
    jsonb_build_object('message_id', new.id, 'seq', new.seq, 'recipient_session_id', new.recipient_session_id),
    'message_accepted',                          -- event name the watcher filters on
    'session:' || new.recipient_session_id::text, -- topic; must match the SELECT policy above
    true);                                       -- private
  return null;
end $$;

create trigger messages_notify
after insert on public.messages
for each row execute function public.notify_message_inserted();
```

If you prefer zero-fetch injection, replace the body with `perform realtime.broadcast_changes('session:' || new.recipient_session_id::text, 'message_accepted', TG_OP, TG_TABLE_NAME, TG_TABLE_SCHEMA, new, old);` and the watcher receives `payload.record` (the full row). You then have two injection paths to deduplicate. Not recommended for v1.

---

## 4. Presence: not for session state

Presence is CRDT-style shared state held in the Realtime cluster, synced through the server, and "notifies all subscribers on every change"; it "is best suited for slow-changing state such as online/offline status". Plan limits: 10 presence keys per object, 5 presence calls per client per 30 seconds, presence messages/sec 20 (Free) / 50 (Pro). In realtime-js 2.112 a client receives presence only if `config.presence.enabled = true` or it registers a `presence` listener; `track()` on private channels needs an INSERT policy for `extension = 'presence'`. **Verified.** https://supabase.com/docs/guides/realtime/presence, https://supabase.com/docs/guides/realtime/limits, `RealtimeChannel.ts` lines 53-68.

Why heartbeat rows are better for Brigade:

- `session list` is a one-shot CLI command. It needs a queryable, durable list including `last_seen_at` for idle and offline sessions; Presence is in-memory, lost on disconnect, and only visible to a connected socket after a `sync`.
- The lease model in the protocol ("clean closure is an optimization; lease expiry is authoritative") maps directly onto `last_seen_at + lease_seconds`.
- Presence would add a second authorization surface (`presence` policies) and per-client rate limits for no protocol benefit.

Presence remains a possible later optimization for instant online/offline flips in a UI. Not needed for v1.

---

## 5. Session leases

```sql
create table public.sessions (
  id            uuid primary key default gen_random_uuid(),
  team_id       uuid not null references public.teams(id) on delete cascade,
  principal_id  uuid not null references auth.users(id) on delete cascade,
  name          text not null,
  description   text,
  harness       text, harness_version text,
  activity      text not null default 'idle' check (activity in ('busy','idle')),  -- from the registry file's status
  lease_seconds int  not null default 90 check (lease_seconds between 30 and 3600),
  last_seen_at  timestamptz not null default now(),
  closed_at     timestamptz,
  created_at    timestamptz not null default now()
);
create index sessions_team_seen_idx on public.sessions (team_id, last_seen_at desc);

-- security_invoker so the caller's RLS on sessions applies (Postgres >= 15; hosted and local are 15/17).
create or replace view public.session_states with (security_invoker = true) as
select s.*,
       case
         when s.closed_at is not null                                              then 'offline'
         when s.last_seen_at < now() - make_interval(secs => s.lease_seconds)      then 'offline'
         when s.activity = 'busy'                                                  then 'active'
         else                                                                           'idle'
       end as state
from public.sessions s;

create or replace function public.session_heartbeat(p_session_id uuid, p_activity text default null, p_name text default null)
returns timestamptz language plpgsql security definer set search_path = '' as $$
declare v_now timestamptz := now();
begin
  update public.sessions
     set last_seen_at = v_now,
         activity     = coalesce(p_activity, activity),
         name         = coalesce(p_name, name)
   where id = p_session_id and principal_id = (select auth.uid()) and closed_at is null;
  if not found then raise exception 'session not owned by caller or closed' using errcode = '42501'; end if;
  if random() < 0.02 then perform public.gc_expired(); end if;   -- lazy cleanup fallback (section 8)
  return v_now;
end $$;
```

Heartbeat cadence: the watcher calls `session_heartbeat` every 30 s (lease 90 s = three missed beats), and immediately whenever the registry file's `status` flips busy/idle. The WebSocket heartbeat (25 s) is a separate, transport-level thing and does not touch the row.

---

## 6. Durable rows, acknowledgment, deduplication, cursor

### 6.1 Schema

```sql
create table public.messages (
  id                    uuid primary key default gen_random_uuid(),   -- message_id in the envelope
  seq                   bigint generated always as identity,           -- stable ordering hint, not a watermark
  team_id               uuid not null references public.teams(id) on delete cascade,
  kind                  text not null default 'text',
  sender_principal_id   uuid not null,
  sender_session_id     uuid not null references public.sessions(id) on delete cascade,
  recipient_session_id  uuid not null references public.sessions(id) on delete cascade,
  idempotency_key       text,
  summary               text check (octet_length(summary) <= 512),
  body                  text not null check (octet_length(body) <= 65536),
  reply_to              uuid references public.messages(id) on delete set null,
  delivery_state        text not null default 'accepted' check (delivery_state in ('accepted','injected')),
  created_at            timestamptz not null default now(),
  injected_at           timestamptz,
  unique (sender_session_id, idempotency_key)
);
-- The drain query touches only this partial index.
create index messages_pending_idx on public.messages (recipient_session_id, seq) where delivery_state = 'accepted';
create index messages_created_idx on public.messages (created_at);

alter table public.messages enable row level security;
create policy "recipient or sender may read" on public.messages for select to authenticated
using (exists (select 1 from public.sessions s
               where s.principal_id = (select auth.uid())
                 and s.id in (messages.recipient_session_id, messages.sender_session_id)));
-- No INSERT/UPDATE policies: writes go through the RPCs below.
```

### 6.2 Cursor: "unacknowledged set", not `seq > last_seen`

`seq` values come from a sequence assigned at insert time; a transaction holding seq 41 can commit after seq 42 is already visible. A watcher that remembers "last seen 42" and asks for `seq > 42` skips 41 forever. Selecting `where delivery_state = 'accepted'` instead is idempotent and cannot skip; `seq` is used only to order the batch. The same reasoning applies to `created_at`. **Verified** (standard Postgres sequence/visibility behavior).

### 6.3 Acknowledgment: column vs ack table

For v1 (exactly one recipient per message) a `delivery_state` column is enough, cheaper, and keeps the drain query on one partial index. An ack table (`message_receipts(message_id, session_id, state, at)`) is the shape you need once a message can have several recipients (team broadcast, which the plan explicitly defers). It can be introduced later without changing the CLI protocol, because the CLI only exposes `message ack <ids>`.

```sql
create or replace function public.ack_messages(p_message_ids uuid[])
returns setof uuid language sql security definer set search_path = '' as $$
  update public.messages m
     set delivery_state = 'injected', injected_at = now()
    from public.sessions s
   where m.id = any(p_message_ids)
     and m.recipient_session_id = s.id
     and s.principal_id = (select auth.uid())
     and m.delivery_state = 'accepted'
  returning m.id;
$$;
```

The watcher acks only after local injection succeeded (socket write flushed or NDJSON line written). A crash between injection and ack yields one redelivery on the next drain. That is the protocol's at-least-once.

### 6.4 Deduplication by `message_id`

The watcher keeps a bounded `Set<string>` of injected ids (say 2,000, FIFO eviction). On drain, an id already in the set is not re-injected but is re-acked (its earlier ack may have failed). Across watcher restarts the durable `delivery_state` is the dedupe; a redelivery after a crash-before-ack is accepted as a duplicate the harness must tolerate (the protocol already says so).

### 6.5 Send RPC (stamps identity; idempotent)

```sql
create or replace function public.send_message(
  p_sender_session_id uuid, p_recipient_session_id uuid, p_body text,
  p_summary text default null, p_reply_to uuid default null, p_idempotency_key text default null)
returns public.messages language plpgsql security definer set search_path = '' as $$
declare v_sender public.sessions; v_recipient_id uuid; v_row public.messages;
begin
  select * into v_sender from public.sessions
   where id = p_sender_session_id and principal_id = (select auth.uid()) and closed_at is null;
  if not found then raise exception 'sender session is not owned by the caller' using errcode = '42501'; end if;

  select id into v_recipient_id from public.sessions
   where id = p_recipient_session_id and team_id = v_sender.team_id;            -- team isolation
  if not found then raise exception 'recipient session not found' using errcode = 'P0002'; end if;

  if p_idempotency_key is not null then
    select * into v_row from public.messages
     where sender_session_id = v_sender.id and idempotency_key = p_idempotency_key;
    if found then return v_row; end if;                                          -- retry returns the same logical message
  end if;

  if (select count(*) from public.messages
       where sender_session_id = v_sender.id and created_at > now() - interval '1 minute') >= 60 then
    raise exception 'rate limited' using errcode = '53400';
  end if;

  insert into public.messages (team_id, sender_principal_id, sender_session_id, recipient_session_id,
                               idempotency_key, summary, body, reply_to)
  values (v_sender.team_id, v_sender.principal_id, v_sender.id, v_recipient_id,
          p_idempotency_key, p_summary, p_body, p_reply_to)
  returning * into v_row;
  return v_row;                                                                  -- 'accepted'
exception when unique_violation then
  select * into v_row from public.messages
   where sender_session_id = v_sender.id and idempotency_key = p_idempotency_key;
  return v_row;
end $$;
```

The trigger in 3.6 fires inside this insert; because `realtime.send` never raises, a Realtime outage cannot make `send_message` fail.

---

## 7. The watcher: supabase-js on Node 24, reconnection, heartbeat, token refresh

### 7.1 WebSocket on Node: no `ws` needed

- realtime-js 2.15.1 / supabase-js 2.55.0 (2025-08-12) stopped importing `ws`: "For most users (Browser, Node.js 22+): No changes required"; Node < 22 must pass `realtime: { transport: ws }`. **Verified.** https://github.com/orgs/supabase/discussions/37869, https://supabase.com/docs/guides/troubleshooting/realtime-connections-timed_out-status
- `@supabase/supabase-js@2.112.4` has `engines.node >= 22.0.0`; `@supabase/realtime-js@2.112.4` depends only on `tslib` and `@supabase/phoenix`. **Verified** (`npm view`, 2026-08-30).
- `WebSocketFactory.detectEnvironment()` uses the global `WebSocket` first; if Node is detected without it: "Node.js detected but native WebSocket not found." / "Ensure you are running Node.js 22+ or provide a WebSocket implementation via the transport option." **Verified** (`lib/websocket-factory.ts`).
- Node's global `WebSocket`: added v21.0.0/v20.10.0, unflagged v22.0.0, "No longer experimental" v22.4.0. On this machine, Node v24.16.0: `typeof WebSocket === 'function'`, `typeof fetch === 'function'`. **Verified.** https://nodejs.org/api/globals.html
- Shipping note: because the plugin's deps are installed with `npm ci --ignore-scripts` (verified-facts.md), pure-JS supabase-js is fine; there is no native module.

### 7.2 Heartbeat, reconnect, rejoin (from source)

| Behavior | Value | Source |
| --- | --- | --- |
| Client heartbeat interval | 25,000 ms (`CONNECTION_TIMEOUTS.HEARTBEAT_INTERVAL`) | `RealtimeClient.ts` |
| Server expectation | "The heartbeat message should be sent at least every 25 seconds to avoid a connection timeout." | https://supabase.com/docs/guides/realtime/protocol |
| Heartbeat timeout | If no reply before the next interval fires: log "heartbeat timeout. Attempting to re-establish connection", `heartbeatCallback('timeout')`, channels errored, socket closed (1000, "heartbeat timeout"), reconnect scheduled | `@supabase/phoenix` `socket.js` `heartbeatTimeout()` |
| Socket close (not manual) | channels errored, heartbeats cleared, `reconnectTimer.scheduleTimeout()` | `socket.js` `onConnClose()` |
| Socket reconnect backoff | `[1000, 2000, 5000, 10000][tries-1] || 10000` (realtime-js overrides Phoenix's default) | `RealtimeClient.ts` `RECONNECT_INTERVALS` |
| On reopen | `reconnectTimer.reset()`, heartbeat restarted, every errored channel calls `rejoin()` (re-sends the join with the current access token) | `socket.js` `onConnOpen()`, `channel.js` |
| Channel rejoin backoff (join rejected/timed out while socket open) | `[1000, 2000, 5000][tries-1] || 10000` | `socket.js` `rejoinAfterMs` |
| Join timeout | 10,000 ms (`DEFAULT_TIMEOUT`) then `TIMED_OUT` and rejoin | `lib/constants.ts`, `RealtimeChannel.ts` |
| `subscribe(cb)` statuses | `SUBSCRIBED` on join ok (after postgres_changes bindings confirmed, if any); `CHANNEL_ERROR` on join error or socket error; `TIMED_OUT`; `CLOSED` | `RealtimeChannel.ts` lines 440-495 |
| `SUBSCRIBED` re-fires after a rejoin | Yes: Phoenix `Push.resend()` calls `reset()` which keeps `recHooks`, so the `receive('ok')` hook runs again | `phoenix/push.js` |
| Manual `disconnect()` | sets `closeWasClean`, no auto-reconnect | `socket.js` |
| Empty-channel auto-disconnect | socket disconnects `2 * heartbeatIntervalMs` after the last channel is removed | `RealtimeClient.ts` `_disconnectOnEmptyChannelsAfterMs` |
| Push buffer | 100 pending pushes max per channel (older discarded) | `channelAdapter.ts` |

All **verified** from source downloaded today.

### 7.3 Token refresh in a long-running Node process

- supabase-js constructs the RealtimeClient with `accessToken: this._getAccessToken` (session-based) and, on `onAuthStateChange` (`SIGNED_IN`, `TOKEN_REFRESHED`, `INITIAL_SESSION`), calls `this.realtime.setAuth(token)`. **Verified** (`SupabaseClient.ts`).
- realtime-js calls the `accessToken` callback on every heartbeat `sent` and after every successful join; if the token changed, it pushes an `access_token` message to each joined channel and merges it into the join payload for future rejoins. **Verified** (`_wrapHeartbeatCallback`, `_performAuth`).
- auth-js in Node: `_handleVisibilityChange()` has "in non-browser environments the refresh token ticker runs always" when `autoRefreshToken` is true; the ticker runs every 30 s (`AUTO_REFRESH_TICK_DURATION_MS`) and refreshes when 3 or fewer ticks (90 s) remain before `expires_at`; the timers are `unref()`'d so they do not keep the process alive by themselves (the open WebSocket does). **Verified** (`GoTrueClient.ts`, `auth-js/src/lib/constants.ts`).
- Default JWT lifetime is 3,600 s (`GOTRUE_JWT_EXP=3600` locally; `jwt_expiry = 3600` in the generated `config.toml`). The Realtime server disconnects a client whose JWT expires without a refresh. With the ticker and heartbeat hooks above, a healthy watcher never hits that. **Verified.**
- In Node, `persistSession: true` without a custom `storage` uses an in-memory adapter. The adapter must supply a `storage` object (`getItem/setItem/removeItem`, async allowed per the docs' React Native example) that writes the session to a 0600 file (or keychain) under the profile directory, otherwise every process start creates a new anonymous principal. **Verified** (`GoTrueClient.ts` constructor; https://supabase.com/docs/reference/javascript/initializing).
- Explicitly `await supabase.realtime.setAuth()` after sign-in and before `subscribe()`; the docs' broadcast-from-database snippet does exactly this to avoid joining a private channel before the token is set. **Verified.**

### 7.4 Watcher sketch (TypeScript, supabase-js 2.112.x, Node 24)

```ts
// brigade-supabase: message watch  (stdout = NDJSON events only; everything else on stderr)
import { createClient, type SupabaseClient } from '@supabase/supabase-js'
import { promises as fs } from 'node:fs'

type MessageRow = {
  id: string; seq: number; kind: string; sender_principal_id: string; sender_session_id: string
  recipient_session_id: string; summary: string | null; body: string; reply_to: string | null
  created_at: string; delivery_state: 'accepted' | 'injected'
}

function fileStorage(path: string) {          // 0600 session file; supabase-js allows async adapters
  return {
    getItem: async (k: string) => { try { return JSON.parse(await fs.readFile(path, 'utf8'))[k] ?? null } catch { return null } },
    setItem: async (k: string, v: string) => {
      let all: Record<string, string> = {}
      try { all = JSON.parse(await fs.readFile(path, 'utf8')) } catch {}
      all[k] = v
      await fs.writeFile(path, JSON.stringify(all), { mode: 0o600 })
    },
    removeItem: async (k: string) => {
      let all: Record<string, string> = {}
      try { all = JSON.parse(await fs.readFile(path, 'utf8')) } catch {}
      delete all[k]
      await fs.writeFile(path, JSON.stringify(all), { mode: 0o600 })
    },
  }
}

export function makeClient(url: string, publishableKey: string, sessionFile: string): SupabaseClient {
  return createClient(url, publishableKey, {
    auth: { persistSession: true, autoRefreshToken: true, detectSessionInUrl: false, storage: fileStorage(sessionFile) },
    realtime: { heartbeatIntervalMs: 25_000, timeout: 10_000, logger: (k: string, m: string) => process.stderr.write(`[rt:${k}] ${m}\n`) },
    // No `transport`: Node >= 22 provides global WebSocket. For Node < 22 you would pass `transport: ws`.
  })
}

export async function watch(supabase: SupabaseClient, sessionId: string, inject: (m: MessageRow) => Promise<void>) {
  const seen = new Set<string>()                 // dedupe by message_id, bounded
  const remember = (id: string) => { seen.add(id); if (seen.size > 2000) seen.delete(seen.values().next().value!) }
  let draining: Promise<void> | null = null
  let again = false

  async function drainOnce() {
    for (;;) {
      const { data, error } = await supabase
        .from('messages').select('*')
        .eq('recipient_session_id', sessionId)
        .eq('delivery_state', 'accepted')           // the cursor is "still unacknowledged", not seq > last
        .order('seq', { ascending: true })
        .limit(100)
      if (error) { process.stderr.write(`drain error: ${error.message}\n`); return }
      const rows = (data ?? []) as MessageRow[]
      const toAck: string[] = []
      for (const row of rows) {
        if (!seen.has(row.id)) {
          await inject(row)                          // socket post or NDJSON line; throws on failure -> not acked
          remember(row.id)
        }
        toAck.push(row.id)                           // re-ack duplicates whose earlier ack may have failed
      }
      if (toAck.length) {
        const { error: ackErr } = await supabase.rpc('ack_messages', { p_message_ids: toAck })
        if (ackErr) { process.stderr.write(`ack error: ${ackErr.message}\n`); return }   // will retry on next drain
      }
      if (rows.length < 100) return
    }
  }
  function drain() {                                 // coalesce concurrent triggers into at most one follow-up run
    if (draining) { again = true; return draining }
    draining = (async () => { do { again = false; await drainOnce() } while (again) })().finally(() => { draining = null })
    return draining
  }

  await supabase.realtime.setAuth()                  // token must be set before joining a private channel
  const channel = supabase
    .channel(`session:${sessionId}`, { config: { private: true } })
    .on('broadcast', { event: 'message_accepted' }, () => { void drain() })   // payload is only a hint
    .on('system', {}, (p) => process.stderr.write(`[rt:system] ${p.status} ${p.message}\n`))
    .subscribe((status, err) => {
      process.stderr.write(`[rt:channel] ${status}${err ? ' ' + err.message : ''}\n`)
      if (status === 'SUBSCRIBED') void drain()      // initial join AND every automatic rejoin after a reconnect
    })

  const safety = setInterval(() => { void drain() }, 60_000)   // covers at-most-once loss while connected
  const beat = setInterval(async () => {
    const { error } = await supabase.rpc('session_heartbeat', { p_session_id: sessionId, p_activity: readRegistryStatus() })
    if (error) process.stderr.write(`heartbeat error: ${error.message}\n`)
  }, 30_000)
  beat.unref(); safety.unref()

  return async () => { clearInterval(safety); clearInterval(beat); await supabase.removeChannel(channel) }
}
```

Notes on the sketch:

- `inject` for the Claude Code inbox socket is a short-lived Unix-socket write of two NDJSON frames (auth, user). The socket sends nothing back, so "injected" means the write completed and the connection closed without error.
- Every stdout line becomes a Claude notification under a plugin Monitor, so the watcher writes only deliberate NDJSON events to stdout.
- `system` events with `status: 'error'` include `Unauthorized` (RLS refused the join) and `Token has expired`; realtime-js moves the channel to `errored` and schedules a rejoin. Map `Unauthorized`/`RlsPolicyError` to the protocol's `unauthorized` exit code, `TooManyConnections`/`ConnectionRateLimitReached`/`ClientJoinRateLimitReached` to `rate limited`, `RealtimeDisabledForTenant`/`DatabaseLackOfConnections` to `unavailable`. https://supabase.com/docs/guides/realtime/error_codes

---

## 8. Retention and cleanup

### 8.1 pg_cron: available on hosted and local

- Hosted: enable via Dashboard -> Integrations -> Cron, or `create extension pg_cron with schema pg_catalog; grant usage on schema cron to postgres; grant all privileges on all tables in schema cron to postgres;`. Jobs "can run SQL snippets or database functions"; guidance: "no more than 8 Jobs run concurrently. Each Job should run no more than 10 minutes." Sub-minute schedules: "You can use [1-59] seconds (e.g. `30 seconds`)", requires Postgres 15.1.1.61 or later. `cron.job_run_details` "are not cleaned up automatically". **Verified.** https://supabase.com/docs/guides/cron, https://supabase.com/docs/guides/cron/install, https://supabase.com/docs/guides/cron/quickstart
- Local: the `supabase/postgres` image preloads pg_cron. On the live local stack: `shared_preload_libraries` includes `pg_cron` and `pg_net`; `cron.database_name = postgres`; `pg_available_extensions` lists `pg_cron 1.6.4`; inside a transaction that was rolled back, `create extension pg_cron with schema pg_catalog` and `cron.schedule('probe_job','30 seconds','select 1')` both succeeded (job database `postgres`, username `postgres`). **Verified live.** The image's `postgresql.conf.j2` in `supabase/postgres` carries the same preload list. https://github.com/supabase/postgres

```sql
create extension if not exists pg_cron with schema pg_catalog;
grant usage on schema cron to postgres;

create or replace function public.gc_expired()
returns void language sql security definer set search_path = '' as $$
  delete from public.messages
   where ctid in (select ctid from public.messages
                   where created_at < now() - interval '7 days'
                   order by created_at limit 5000);                 -- bounded work per call
  update public.sessions set closed_at = now()
   where closed_at is null and last_seen_at < now() - interval '30 days';
$$;
revoke execute on function public.gc_expired() from public, anon, authenticated;

select cron.schedule('brigade_gc', '17 * * * *', $$select public.gc_expired()$$);          -- hourly
select cron.schedule('brigade_cron_log_gc', '23 3 * * *',
  $$delete from cron.job_run_details where end_time < now() - interval '7 days'$$);
```

Note: `gc_expired()` is also called probabilistically from `session_heartbeat` (section 5) so a project without pg_cron (self-hosted, or a team that never enabled the integration) still converges. Revoking EXECUTE from `authenticated` is fine because `session_heartbeat` is SECURITY DEFINER and owns the call.

### 8.2 Things you do not clean

- `realtime.messages` is Supabase-managed: daily partitions, dropped after 3 days by the Realtime janitor. **Verified.**
- Do not run the docs' generic anonymous-user cleanup (`delete from auth.users where is_anonymous is true and created_at < now() - interval '30 days'`): Brigade principals are anonymous users; deleting them cascades to memberships and sessions. If ever needed, delete only principals with no membership or with all sessions closed for a long time. **Verified** that the docs recommend such a query for ordinary apps. https://supabase.com/docs/guides/auth/auth-anonymous

---

## 9. Plan limits relevant to a team of Claude Code sessions (2026-08-30)

| Item | Free | Pro | Notes |
| --- | --- | --- | --- |
| Concurrent Realtime connections | 200 | 500 included, then $10 per 1,000 peak | one watcher = one connection |
| Messages per month | 2,000,000 | 5,000,000, then $2.50 per 1,000,000 | broadcast = 1 sent + 1 per subscriber; DB change = 1 per listening client |
| Messages per second (rolling minute) | 100 | 500 (2,500 with spend cap off) | an "event" is one message sent by or delivered to one client |
| Channel joins per second | 100 | 500 | rejoin storms after an outage count here |
| Channels per connection | 100 | 100 | we use 1 |
| Broadcast payload | 256 KB | 3,000 KB | notification payload < 200 B |
| Postgres change payload | 1,024 KB | 1,024 KB | only if using postgres_changes |
| Presence keys / calls | 10 per object; 5 calls per client per 30 s | same | unused |
| Broadcast replay | 72 h, 25 per request | same | unused |
| Database size | 500 MB | 8 GB then $0.125/GB | messages are small |
| Free project pause | after 1 week of inactivity | n/a | see gotchas |
| Anonymous sign-in rate limit | 30 per hour per IP (adjustable) | same | one sign-in per profile, so negligible |

Sources: https://supabase.com/docs/guides/realtime/limits, https://supabase.com/pricing, https://supabase.com/docs/guides/platform/manage-your-usage/realtime-messages, https://supabase.com/docs/guides/platform/manage-your-usage/realtime-peak-connections, https://supabase.com/docs/guides/realtime/settings, https://supabase.com/docs/guides/auth/auth-anonymous. Whether WebSocket heartbeats count as messages is not documented (**uncertain**; assume no). Back-of-envelope: 10 sessions exchanging 1,000 messages/day produce about 2,000 Realtime messages/day, roughly 60,000/month, 3% of the Free quota.

---

## 10. Local development notes

- `npx supabase init` (CLI, 2026-08-30) generates `config.toml` with `[db] major_version = 17`, `[realtime] enabled = true`, `[auth] jwt_expiry = 3600`, `enable_anonymous_sign_ins = false` (must be set to `true`). **Verified** from the generated file.
- The local Realtime image (v2.129.3) already has `realtime.send`, `realtime.send_binary`, `realtime.broadcast_changes`, `realtime.topic`. **Verified live.**
- Only one local stack can bind 54321/54322 at a time; a second project needs different ports in `config.toml`. (My own `supabase start` for a scratch project failed on the already-bound 54322 and was cleaned up; the pre-existing `sbtest` stack was left untouched.)
- `npx supabase` works without a global install (the CLI is not installed on this machine).

---

## 11. Open questions

1. Whether `realtime.topic()` at join time is the bare topic or carries the `realtime:` prefix is inferred from docs examples and the `send` body, not verified end to end. The first integration experiment should assert the policy with a deliberately wrong topic and confirm `Unauthorized`.
2. Whether heartbeats or `access_token` refreshes count toward the messages quota is undocumented.
3. Ordering across a Realtime cluster for Broadcast is undocumented; the design does not depend on it, but the drain's `order by seq` should be tested under concurrent senders.
4. Free-tier pause after one week of inactivity: define the adapter's `unavailable` error for a paused project and decide whether Brigade documents "use Pro or a self-hosted stack for a team that goes quiet."
5. Whether the inbox-socket write should be considered "injected" without any acknowledgment from Claude Code (the socket returns nothing), or whether a follow-up `UserPromptSubmit`/`Stop` hook should promote `injected -> processed`.
