-- Brigade schema migration (plan 5.3 schema, RLS, read policies and grants; 5.4 RPCs; 5.5 stamping triggers).
-- This is the whole data model of the Supabase backend: five tables in the `brigade` schema, read-only RLS for
-- authenticated members, and the RPCs that are the only write path (D22: there are NO insert/update/delete
-- policies on any table). Drafted for E0-1 (verified live 2026-08-30, 72/72), finished in P2-1/P2-2 against the
-- frozen protocol (docs/protocol-v1.md 4.4.10, 4.5, 4.6) and the conformance suite (internal/conformance/cases).
-- Applied first; 20260830120100 (realtime) and 20260830120200 (housekeeping) build on it.

-- ---------------------------------------------------------------------------
-- 5.3 Schema
-- ---------------------------------------------------------------------------

create schema if not exists brigade;
create extension if not exists pgcrypto with schema extensions;      -- pre-installed on Supabase; no-op there [verified live]
revoke all on schema brigade from public;
grant usage on schema brigade to authenticated, service_role;       -- deliberately NOT anon (T4)
-- service_role gets schema usage only: BYPASSRLS skips row policies but not GRANTs, and this schema never
-- grants it table or function privileges (the Supabase custom-schema recipe grants ALL to service_role;
-- Brigade deliberately does not). Consequence: the Data API with the secret/service-role key cannot read or
-- write brigade.* (42501), and test fixtures are written as `postgres` with simulated JWT claims (9.3), never
-- through PostgREST with an admin key.
-- Every function created in this schema gets PostgreSQL's built-in EXECUTE ... TO PUBLIC. A per-schema
-- `alter default privileges in schema brigade revoke execute on functions from public` does NOT remove it:
-- per-schema defaults only add to the global ones, and the manual's own example says the statement "has no
-- effect" [verified live on 2026-08-30 (pg_default_acl stays empty, new functions keep proacl NULL) and in
-- https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html]. So every function in this schema
-- carries an explicit `revoke execute on function ... from public` right after its `create function` (5.4
-- lists them), and functions.sql (9.3) asserts that no brigade function has an ACL entry with an empty grantee.

create table brigade.teams (
  id                uuid primary key default gen_random_uuid(),
  name              text not null check (char_length(name) between 1 and 64),
  created_by        uuid references auth.users(id) on delete restrict,   -- the only administrative authority (5.10); never orphaned silently; P5-3 skips live-team creators
  secret_hash       text not null,                      -- bcrypt of the random part only
  secret_version    integer not null default 1,
  secret_rotated_at timestamptz,
  created_at        timestamptz not null default now()
);

create table brigade.memberships (
  team_id               uuid not null references brigade.teams(id) on delete cascade,
  user_id               uuid not null references auth.users(id) on delete cascade,
  human_label           text check (char_length(human_label) <= 128),
  status                text not null default 'active' check (status in ('active','revoked','banned')),
  joined_secret_version integer not null,
  joined_at             timestamptz not null default now(),
  revoked_at            timestamptz,
  primary key (team_id, user_id)
);
create index memberships_user_idx on brigade.memberships (user_id);   -- the PK only indexes team_id (RLS guide)

create table brigade.sessions (
  id               uuid primary key default gen_random_uuid(),
  team_id          uuid not null references brigade.teams(id) on delete cascade,
  owner_id         uuid not null references auth.users(id) on delete cascade,
  name             text not null check (char_length(name) between 1 and 64),
  description      text check (char_length(description) <= 256),
  activity         text not null default 'idle' check (activity in ('busy','idle')),
  inbound          text check (inbound in ('accept','hold','refuse')),
  harness          text check (char_length(harness) <= 32),
  harness_version  text check (char_length(harness_version) <= 32),
  workspace_label  text check (char_length(workspace_label) <= 128),
  lease_seconds    integer not null default 90 check (lease_seconds between 30 and 600),
  last_seen_at     timestamptz not null default now(),
  closed_at        timestamptz,
  created_at       timestamptz not null default now()
);
create index sessions_team_seen_idx on brigade.sessions (team_id, last_seen_at desc);
create index sessions_owner_idx     on brigade.sessions (owner_id, created_at desc);

create table brigade.messages (
  id                   uuid primary key default gen_random_uuid(),
  seq                  bigint generated always as identity,         -- ordering hint for one recipient's drain, never a cursor
  team_id              uuid not null references brigade.teams(id) on delete cascade,
  sender_user_id       uuid not null references auth.users(id) on delete cascade,
  sender_session_id    uuid not null references brigade.sessions(id) on delete cascade,
  recipient_session_id uuid not null references brigade.sessions(id) on delete cascade,
  kind                 text not null default 'text' check (kind = 'text'),
  summary              text check (char_length(summary) <= 200),
  body                 text not null check (octet_length(body) between 1 and 16384),
  body_hash            bytea not null,                               -- for the idempotency conflict check only
  reply_to             uuid references brigade.messages(id) on delete set null,
  hop_count            integer not null default 0 check (hop_count between 0 and 32),
  idempotency_key      text not null check (char_length(idempotency_key) between 1 and 128),
  delivery_state       text not null default 'accepted' check (delivery_state in ('accepted','injected')),
  created_at           timestamptz not null default now(),
  injected_at          timestamptz,
  unique (sender_session_id, idempotency_key)
);
create index messages_pending_idx       on brigade.messages (recipient_session_id, seq) where delivery_state = 'accepted';
create index messages_sender_recent_idx on brigade.messages (sender_session_id, created_at desc);
create index messages_created_idx       on brigade.messages (created_at);
create index messages_injected_idx      on brigade.messages (injected_at) where injected_at is not null;

create table brigade.join_attempts (
  id           bigint generated always as identity primary key,
  user_id      uuid not null,
  team_id      uuid,
  succeeded    boolean not null default false,
  attempted_at timestamptz not null default now()
);
create index join_attempts_user_time_idx on brigade.join_attempts (user_id, attempted_at desc);
create index join_attempts_team_time_idx on brigade.join_attempts (team_id, attempted_at desc);

alter table brigade.teams         enable row level security;
alter table brigade.memberships   enable row level security;
alter table brigade.sessions      enable row level security;
alter table brigade.messages      enable row level security;
alter table brigade.join_attempts enable row level security;   -- no policies, no grants: server-only (documented for the advisor lint)

-- Membership helper: the single source of team scoping (T4).
create or replace function brigade.my_team_ids()
returns setof uuid language sql stable security definer set search_path = ''
as $$ select m.team_id from brigade.memberships m where m.user_id = (select auth.uid()) and m.status = 'active' $$;
revoke execute on function brigade.my_team_ids() from public, anon;
grant  execute on function brigade.my_team_ids() to authenticated;

-- Read policies only. There are NO insert/update/delete policies on any table: writes are RPC-only (D22).
create policy teams_select on brigade.teams for select to authenticated
  using ( id in (select brigade.my_team_ids()) );
create policy memberships_select on brigade.memberships for select to authenticated
  using ( team_id in (select brigade.my_team_ids()) );            -- the roster is visible to every active member (D22)
-- sessions: caller must be an active member, and the row's owner must still be one (a revoked member's
-- sessions disappear from direct selects too, not only inside list_sessions).
create policy sessions_select on brigade.sessions for select to authenticated
  using ( team_id in (select brigade.my_team_ids())
          and exists (select 1 from brigade.memberships m
                      where m.team_id = sessions.team_id and m.user_id = sessions.owner_id and m.status = 'active') );
-- messages: ownership AND active membership. Without the membership predicate a revoked or banned principal
-- would keep reading every message addressed to its sessions.
create policy messages_select on brigade.messages for select to authenticated
  using ( team_id in (select brigade.my_team_ids())
          and ( sender_user_id = (select auth.uid())
                or exists (select 1 from brigade.sessions s
                           where s.id = messages.recipient_session_id and s.owner_id = (select auth.uid())) ) );

revoke all on all tables in schema brigade from anon, authenticated, service_role;
grant select (id, name, secret_version, created_at) on brigade.teams to authenticated;   -- never secret_hash
grant select on brigade.memberships, brigade.sessions, brigade.messages to authenticated;
-- join_attempts: nothing. service_role: nothing (see the schema comment). No views in v1 (if one is added: with (security_invoker = true)).

-- ---------------------------------------------------------------------------
-- 5.4 RPCs (all security definer, set search_path = '', revoke execute from
-- public, anon; grant execute to authenticated unless stated).
-- Error convention (D15): join_team returns a status instead of raising for
-- expected failures so the attempt row commits. Every other failure raises
-- 'brigade:<code>[:<detail>]'; the idempotency conflict is never raised with
-- 23505, so the unique_violation handler around the insert cannot swallow it.
-- not_found is raised with SQLSTATE PT404 (P2-2 decision, measured live on
-- 2026-09-02 through PostgREST with an anonymous principal): PostgREST maps a
-- PTnnn SQLSTATE to HTTP nnn, so the routine not-found answer is HTTP 404
-- with the same {code, details, hint, message} body it gave for P0002, where it
-- was HTTP 500 (E0-1 gateway behaviour 1). The adapter (P2-6) keys on the
-- 'brigade:' message prefix first, SQLSTATE second, and never on HTTP status;
-- the SQLSTATE column of 4.6's informative mapping is PT404, not P0002.
-- ---------------------------------------------------------------------------

-- Helpers (execute granted to nobody; called only from the RPCs below).
create or replace function brigade.session_record(s brigade.sessions, p_label text)
returns jsonb language sql stable security definer set search_path = ''
as $$ select jsonb_build_object(
  'session_id', s.id, 'session_name', s.name, 'session_description', s.description,
  'principal_ref', s.owner_id, 'human_label', p_label,
  'state', case when s.closed_at is not null or s.last_seen_at < now() - make_interval(secs => s.lease_seconds) then 'offline'
                when s.activity = 'busy' then 'active' else 'idle' end,
  'activity', s.activity, 'inbound', s.inbound,
  'last_seen_at', s.last_seen_at, 'lease_until', s.last_seen_at + make_interval(secs => s.lease_seconds),
  'harness', s.harness, 'harness_version', s.harness_version, 'workspace_label', s.workspace_label,
  'created_at', s.created_at) $$;
revoke execute on function brigade.session_record(brigade.sessions, text) from public, anon, authenticated;

create or replace function brigade.envelope(m brigade.messages)
returns jsonb language sql stable security definer set search_path = ''
as $$ select jsonb_build_object('protocol_version', '1', 'kind', m.kind, 'message_id', m.id, 'team_ref', m.team_id,
  'sender', jsonb_build_object('principal_ref', m.sender_user_id,
     'human_label', (select mm.human_label from brigade.memberships mm where mm.team_id = m.team_id and mm.user_id = m.sender_user_id),
     'session_id', m.sender_session_id,
     'session_name', (select s.name from brigade.sessions s where s.id = m.sender_session_id)),
  'recipient_session_id', m.recipient_session_id, 'summary', m.summary, 'body', m.body, 'reply_to', m.reply_to,
  'hop_count', m.hop_count, 'created_at', m.created_at, 'delivery_state', m.delivery_state) $$;
revoke execute on function brigade.envelope(brigade.messages) from public, anon, authenticated;

create or replace function brigade.message_result(m brigade.messages, p_dup boolean)
returns jsonb language sql stable security definer set search_path = ''
as $$ select jsonb_build_object('message_id', m.id, 'recipient_session_id', m.recipient_session_id,
             'created_at', m.created_at, 'duplicate', p_dup, 'hop_count', m.hop_count) $$;
revoke execute on function brigade.message_result(brigade.messages, boolean) from public, anon, authenticated;

-- create_team: the caller becomes the first member; the secret is generated server-side and returned once.
create or replace function brigade.create_team(p_name text, p_human_label text default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_id uuid; v_random text;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if p_name is null or char_length(p_name) not between 1 and 64 then
    raise exception 'brigade:invalid_input:team_name' using errcode = '22023'; end if;
  if p_human_label is not null and char_length(p_human_label) > 128 then
    raise exception 'brigade:invalid_input:human_label' using errcode = '22023'; end if;
  if (select count(*) from brigade.teams where created_by = v_uid and created_at > now() - interval '1 hour') >= 5 then
    raise exception 'brigade:rate_limited:team_create:3600' using errcode = 'P0001'; end if;
  v_random := encode(extensions.gen_random_bytes(16), 'hex');              -- 128 bits
  insert into brigade.teams (name, created_by, secret_hash)
  values (p_name, v_uid, extensions.crypt(v_random, extensions.gen_salt('bf', 10)))
  returning id into v_id;
  insert into brigade.memberships (team_id, user_id, human_label, joined_secret_version)
  values (v_id, v_uid, p_human_label, 1);
  return jsonb_build_object('status', 'created', 'team_id', v_id, 'team_name', p_name,
                            'join_secret', 'brg1.' || v_id::text || '.' || v_random);
end $$;
revoke execute on function brigade.create_team(text, text) from public, anon;
grant  execute on function brigade.create_team(text, text) to authenticated;

-- join_team: uniform failure, persisted attempts, per-principal hard limiter (the per-team count is advisory),
-- one bcrypt of the same cost even for unknown teams.
create or replace function brigade.join_team(p_join_secret text, p_human_label text default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare
  v_uid uuid := auth.uid(); v_team brigade.teams%rowtype; v_team_id uuid; v_random text;
  v_window constant interval := interval '15 minutes';
  v_user_fail integer; v_team_fail integer; v_oldest timestamptz;
  v_existing brigade.memberships%rowtype; v_rejoined boolean := false;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if p_human_label is not null and char_length(p_human_label) > 128 then
    raise exception 'brigade:invalid_input:human_label' using errcode = '22023'; end if;
  if p_join_secret is null or octet_length(p_join_secret) > 120
     or p_join_secret !~ '^brg1\.[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.[0-9a-f]{32}$' then
    return jsonb_build_object('status', 'invalid_input');
  end if;
  v_team_id := split_part(p_join_secret, '.', 2)::uuid;
  v_random  := split_part(p_join_secret, '.', 3);

  select count(*), min(attempted_at) into v_user_fail, v_oldest from brigade.join_attempts
   where user_id = v_uid and not succeeded and attempted_at > now() - v_window;
  if v_user_fail >= 5 then                                                   -- the only hard limit (D6)
    return jsonb_build_object('status', 'rate_limited',
      'retry_after_seconds', greatest(60, coalesce(extract(epoch from (v_oldest + v_window - now()))::integer, 900)));
  end if;
  select count(*) into v_team_fail from brigade.join_attempts                -- advisory only: the team id is not secret,
   where team_id = v_team_id and not succeeded and attempted_at > now() - v_window;   -- so it must never refuse a join
  if v_team_fail >= 20 then raise notice 'brigade:join_team:team_failures:%', v_team_fail; end if;

  select * into v_team from brigade.teams where id = v_team_id;
  if not found then
    -- Equalise latency (I-18): one bcrypt at the real cost against a fresh salt. No constant to paste, no
    -- placeholder that crypt() would reject as an invalid salt.
    perform extensions.crypt(v_random, extensions.gen_salt('bf', 10));
    insert into brigade.join_attempts (user_id, team_id) values (v_uid, v_team_id);
    return jsonb_build_object('status', 'invalid_secret');
  end if;
  if v_team.secret_hash <> extensions.crypt(v_random, v_team.secret_hash) then
    insert into brigade.join_attempts (user_id, team_id) values (v_uid, v_team_id);
    return jsonb_build_object('status', 'invalid_secret');
  end if;

  select * into v_existing from brigade.memberships where team_id = v_team.id and user_id = v_uid;
  v_rejoined := found;                                                       -- captured before later statements reset FOUND
  if v_rejoined and v_existing.status = 'banned' then
    insert into brigade.join_attempts (user_id, team_id) values (v_uid, v_team_id);
    return jsonb_build_object('status', 'invalid_secret');                 -- banned looks like a wrong secret
  end if;
  insert into brigade.memberships (team_id, user_id, human_label, joined_secret_version)
  values (v_team.id, v_uid, p_human_label, v_team.secret_version)
  on conflict (team_id, user_id) do update
    set status = 'active', revoked_at = null,
        joined_secret_version = excluded.joined_secret_version,
        human_label = coalesce(excluded.human_label, brigade.memberships.human_label);
  insert into brigade.join_attempts (user_id, team_id, succeeded) values (v_uid, v_team.id, true);
  delete from brigade.join_attempts where user_id = v_uid and not succeeded;
  return jsonb_build_object('status', 'joined', 'team_id', v_team.id, 'team_name', v_team.name, 'rejoined', v_rejoined,
                            'team_failures', v_team_fail);                  -- advisory; the adapter logs it at warn when > 0
end $$;
revoke execute on function brigade.join_team(text, text) from public, anon;
grant  execute on function brigade.join_team(text, text) to authenticated;

-- leave_team: the caller revokes its own membership and closes its open sessions in the team. Idempotent and uniform:
-- an unknown team, a non-member and an already-revoked member all answer left = true (the team id is not secret, and
-- there is nothing to disclose). A banned row stays banned. created_by is untouched, so a creator who leaves regains
-- administration by rejoining with the secret (5.10). join_team's upsert re-activates the row later (rejoined = true).
-- Before the sessions close it writes a membership_revoked broadcast, ids only, on each open session's private topic
-- (plan 5.6, C-08): a running watch treats any broadcast on its topic as a hint and drains at once, and the drain's
-- fetch_inbox answers the uniform unauthorized — without the hint the watch learns of its revocation only from its
-- periodic drain timer (30 s while joined), and a timer fast enough for C-08's 2 s is one RPC per second per watcher.
-- The hint is written from this definer function exactly as notify_message_inserted writes message_accepted (as
-- postgres, BYPASSRLS; a client has no insert policy), and realtime.send swallows its own failure into a WARNING,
-- so a realtime outage can never fail the leave. Realtime authorizes a private topic at join and on a token push,
-- not per broadcast, so the already-joined channel of the leaver still receives it (E0-2 (h)).
create or replace function brigade.leave_team(p_team_id uuid)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_status text;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  select status into v_status from brigade.memberships where team_id = p_team_id and user_id = v_uid;
  if found and v_status = 'active' then
    update brigade.memberships set status = 'revoked', revoked_at = now() where team_id = p_team_id and user_id = v_uid;
    perform realtime.send(jsonb_build_object('session_id', s.id),                -- ids only; never a body or a label (T4)
                          'membership_revoked', 'brigade:session:' || s.id::text, true)
       from brigade.sessions s where s.team_id = p_team_id and s.owner_id = v_uid and s.closed_at is null;
    update brigade.sessions set closed_at = coalesce(closed_at, now()), activity = 'idle'
     where team_id = p_team_id and owner_id = v_uid and closed_at is null;    -- ownership untouched: the UPDATE trigger allows it
  end if;
  return jsonb_build_object('team_id', p_team_id, 'left', true);
end $$;
revoke execute on function brigade.leave_team(uuid) from public, anon;
grant  execute on function brigade.leave_team(uuid) to authenticated;

-- register_session: new session, or re-open an owned one by Brigade id that is closed or expired (uniform not_found for
-- a foreign or unknown id; conflict:session_live while the session's lease is still valid, 4.5.8).
create or replace function brigade.register_session(
  p_team_id uuid, p_name text, p_description text default null, p_activity text default 'idle',
  p_inbound text default null, p_harness text default null, p_harness_version text default null,
  p_workspace_label text default null, p_lease_seconds integer default 90, p_resume_session_id uuid default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_row brigade.sessions%rowtype; v_resumed boolean := false; v_label text;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  select human_label into v_label from brigade.memberships where team_id = p_team_id and user_id = v_uid and status = 'active';
  if not found then raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  if p_name is null or char_length(p_name) not between 1 and 64 then raise exception 'brigade:invalid_input:session_name' using errcode = '22023'; end if;
  if p_description is not null and char_length(p_description) > 256 then raise exception 'brigade:invalid_input:session_description' using errcode = '22023'; end if;
  if p_activity is null or p_activity not in ('busy','idle') then raise exception 'brigade:invalid_input:activity' using errcode = '22023'; end if;
  if p_inbound is not null and p_inbound not in ('accept','hold','refuse') then raise exception 'brigade:invalid_input:inbound' using errcode = '22023'; end if;
  if p_harness is not null and char_length(p_harness) > 32 then raise exception 'brigade:invalid_input:harness' using errcode = '22023'; end if;
  if p_harness_version is not null and char_length(p_harness_version) > 32 then raise exception 'brigade:invalid_input:harness_version' using errcode = '22023'; end if;
  if p_workspace_label is not null and char_length(p_workspace_label) > 128 then raise exception 'brigade:invalid_input:workspace_label' using errcode = '22023'; end if;
  if p_lease_seconds is null or p_lease_seconds not between 30 and 600 then raise exception 'brigade:invalid_input:lease_seconds' using errcode = '22023'; end if;

  if p_resume_session_id is not null then
    -- 4.5.8 / C-19b: an owned session that is open with a valid lease is live; refuse before touching the row.
    if exists (select 1 from brigade.sessions
                where id = p_resume_session_id and owner_id = v_uid and team_id = p_team_id and closed_at is null
                  and last_seen_at + make_interval(secs => lease_seconds) > now()) then
      raise exception 'brigade:conflict:session_live' using errcode = 'P0001';   -- 4.5.8: two processes never drain one inbox
    end if;
    update brigade.sessions
       set closed_at = null, last_seen_at = now(), name = p_name, description = p_description, activity = p_activity,
           inbound = p_inbound, harness = p_harness, harness_version = p_harness_version,
           workspace_label = p_workspace_label, lease_seconds = p_lease_seconds
     where id = p_resume_session_id and owner_id = v_uid and team_id = p_team_id
    returning * into v_row;                                                 -- single row variable: valid INTO
    if not found then raise exception 'brigade:not_found' using errcode = 'PT404'; end if;
    v_resumed := true;
  else
    -- 120 new sessions per principal per hour. The cap bounds a registration flood (every row is a session the whole
    -- team lists and can be messaged), and its floor is the conformance suite's own budget: one --slow run registers
    -- about 55 sessions on its busiest principal, which the earlier cap of 30 refused with
    -- rate_limited:register_session on eleven cases (C-29, C-29b, C-30, C-31, C-32, C-34, C-35, C-36, C-39, C-41,
    -- C-42). Resumes do not count (they re-open a row). The window and the retry trailer are unchanged.
    if (select count(*) from brigade.sessions where owner_id = v_uid and created_at > now() - interval '1 hour') >= 120 then
      raise exception 'brigade:rate_limited:register_session:3600' using errcode = 'P0001'; end if;
    insert into brigade.sessions (team_id, owner_id, name, description, activity, inbound, harness, harness_version, workspace_label, lease_seconds)
    values (p_team_id, v_uid, p_name, p_description, p_activity, p_inbound, p_harness, p_harness_version, p_workspace_label, p_lease_seconds)
    returning * into v_row;
  end if;
  return brigade.session_record(v_row, v_label)
      || jsonb_build_object('resumed', v_resumed, 'lease_seconds', v_row.lease_seconds, 'server_time', now());
end $$;
revoke execute on function brigade.register_session(uuid, text, text, text, text, text, text, text, integer, uuid) from public, anon;
grant  execute on function brigade.register_session(uuid, text, text, text, text, text, text, text, integer, uuid) to authenticated;

-- session_heartbeat keeps its inline ownership-then-membership checks, which are the same two checks
-- owned_active_session performs in the same order (5.4 grants note); the closed-session conflict comes AFTER
-- them, so the answer to a revoked principal is unauthorized whatever the session's state (4.5.7).
create or replace function brigade.session_heartbeat(
  p_session_id uuid, p_activity text default null, p_name text default null, p_description text default null,
  p_inbound text default null, p_lease_seconds integer default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_row brigade.sessions%rowtype;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if p_lease_seconds is not null and p_lease_seconds not between 30 and 600 then raise exception 'brigade:invalid_input:lease_seconds' using errcode = '22023'; end if;
  if p_activity is not null and p_activity not in ('busy','idle') then raise exception 'brigade:invalid_input:activity' using errcode = '22023'; end if;
  if p_name is not null and char_length(p_name) not between 1 and 64 then raise exception 'brigade:invalid_input:session_name' using errcode = '22023'; end if;
  if p_description is not null and char_length(p_description) > 256 then raise exception 'brigade:invalid_input:session_description' using errcode = '22023'; end if;
  if p_inbound is not null and p_inbound not in ('accept','hold','refuse') then raise exception 'brigade:invalid_input:inbound' using errcode = '22023'; end if;
  select * into v_row from brigade.sessions where id = p_session_id and owner_id = v_uid;
  if not found then raise exception 'brigade:not_found' using errcode = 'PT404'; end if;
  if not exists (select 1 from brigade.memberships m where m.team_id = v_row.team_id and m.user_id = v_uid and m.status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  if v_row.closed_at is not null then raise exception 'brigade:conflict:session_closed' using errcode = 'P0001'; end if;
  update brigade.sessions
     set last_seen_at = now(), activity = coalesce(p_activity, activity), name = coalesce(p_name, name),
         description = coalesce(p_description, description), inbound = coalesce(p_inbound, inbound),
         lease_seconds = coalesce(p_lease_seconds, lease_seconds)
   where id = p_session_id
  returning * into v_row;
  if random() < 0.02 then perform brigade.gc_expired(); end if;             -- works without pg_cron
  return jsonb_build_object('session_id', v_row.id,
    'state', case when v_row.activity = 'busy' then 'active' else 'idle' end,
    'lease_until', v_row.last_seen_at + make_interval(secs => v_row.lease_seconds), 'server_time', now());
end $$;
revoke execute on function brigade.session_heartbeat(uuid, text, text, text, text, integer) from public, anon;
grant  execute on function brigade.session_heartbeat(uuid, text, text, text, text, integer) to authenticated;

-- owned_active_session: the shared ownership + active-membership check for the per-session RPCs.
-- not_found for an unknown, foreign or not-owned id (uniform); unauthorized for an owned session whose
-- owner's membership is no longer active (revocation is immediate for inbox access, D22).
create or replace function brigade.owned_active_session(p_session_id uuid, p_uid uuid)
returns brigade.sessions language plpgsql stable security definer set search_path = ''
as $$
declare v_row brigade.sessions%rowtype;
begin
  select * into v_row from brigade.sessions where id = p_session_id and owner_id = p_uid;
  if not found then raise exception 'brigade:not_found' using errcode = 'PT404'; end if;
  if not exists (select 1 from brigade.memberships m where m.team_id = v_row.team_id and m.user_id = p_uid and m.status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  return v_row;
end $$;
revoke execute on function brigade.owned_active_session(uuid, uuid) from public, anon, authenticated;

create or replace function brigade.close_session(p_session_id uuid)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid();
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.owned_active_session(p_session_id, v_uid);
  update brigade.sessions set closed_at = coalesce(closed_at, now()), activity = 'idle' where id = p_session_id;
  return jsonb_build_object('session_id', p_session_id, 'state', 'offline');
end $$;
revoke execute on function brigade.close_session(uuid) from public, anon;
grant  execute on function brigade.close_session(uuid) to authenticated;

-- list_sessions: computed state, owner label from the membership row, capped; caller must be an active member.
create or replace function brigade.list_sessions(p_team_id uuid, p_include_offline boolean default false, p_limit integer default 200)
returns jsonb language plpgsql stable security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_rows jsonb; v_cap integer := least(greatest(coalesce(p_limit, 200), 1), 500);
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if not exists (select 1 from brigade.memberships where team_id = p_team_id and user_id = v_uid and status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  select coalesce(jsonb_agg(r.rec order by r.offline, r.last_seen_at desc), '[]'::jsonb) into v_rows
    from (select brigade.session_record(s, m.human_label) as rec, s.last_seen_at,
                 (s.closed_at is not null or s.last_seen_at < now() - make_interval(secs => s.lease_seconds)) as offline
            from brigade.sessions s
            join brigade.memberships m on m.team_id = s.team_id and m.user_id = s.owner_id and m.status = 'active'
           where s.team_id = p_team_id
             and (p_include_offline or (s.closed_at is null and s.last_seen_at >= now() - make_interval(secs => s.lease_seconds)))
           order by (s.closed_at is not null or s.last_seen_at < now() - make_interval(secs => s.lease_seconds)), s.last_seen_at desc
           limit v_cap + 1) r;
  return jsonb_build_object('team_ref', p_team_id, 'team_name', (select t.name from brigade.teams t where t.id = p_team_id),
                            'server_time', now(), 'truncated', jsonb_array_length(v_rows) > v_cap,
                            'sessions', (select coalesce(jsonb_agg(e), '[]'::jsonb) from (select e from jsonb_array_elements(v_rows) e limit v_cap) x));
end $$;
revoke execute on function brigade.list_sessions(uuid, boolean, integer) from public, anon;
grant  execute on function brigade.list_sessions(uuid, boolean, integer) to authenticated;

-- list_members: the roster for every active member (D22). unauthorized with byte-identical text for a team the
-- caller does not belong to and for a random uuid (no team-existence oracle). v1 lists active members only.
-- Per member (4.4.10, C-43): last_seen_at is the max over that principal's sessions in the team in ANY state
-- (null with none); session_count counts only sessions that are not offline (open, lease still valid).
create or replace function brigade.list_members(p_team_id uuid)
returns jsonb language plpgsql stable security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid();
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if not exists (select 1 from brigade.memberships where team_id = p_team_id and user_id = v_uid and status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  return jsonb_build_object('team_ref', p_team_id, 'team_name', (select t.name from brigade.teams t where t.id = p_team_id),
    'server_time', now(),
    'members', coalesce((select jsonb_agg(jsonb_build_object(
        'principal_ref', m.user_id, 'human_label', m.human_label, 'status', m.status, 'joined_at', m.joined_at,
        'joined_secret_version', m.joined_secret_version,
        'last_seen_at', (select max(s.last_seen_at) from brigade.sessions s where s.team_id = m.team_id and s.owner_id = m.user_id),
        'session_count', (select count(*) from brigade.sessions s where s.team_id = m.team_id and s.owner_id = m.user_id
                             and s.closed_at is null and s.last_seen_at + make_interval(secs => s.lease_seconds) >= now()))
      order by m.joined_at)
      from brigade.memberships m where m.team_id = p_team_id and m.status = 'active'), '[]'::jsonb));
end $$;
revoke execute on function brigade.list_members(uuid) from public, anon;
grant  execute on function brigade.list_members(uuid) to authenticated;

-- send_message: stamps identity; uniform not_found; deterministic idempotency; layered limits; hop_count.
create or replace function brigade.send_message(
  p_sender_session_id uuid, p_recipient_session_id uuid, p_body text, p_idempotency_key text,
  p_summary text default null, p_reply_to uuid default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare
  v_uid uuid := auth.uid(); v_sender brigade.sessions%rowtype; v_recipient brigade.sessions%rowtype;
  v_row brigade.messages%rowtype; v_reply brigade.messages%rowtype;
  v_hash bytea; v_hops integer := 0; v_min integer; v_hour integer; v_pmin integer; v_phour integer;
  v_unacked integer; v_pair_unacked integer;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if p_body is null or octet_length(p_body) = 0 or octet_length(p_body) > 16384 then
    raise exception 'brigade:invalid_input:body' using errcode = '22023'; end if;
  if p_summary is not null and char_length(p_summary) > 200 then
    raise exception 'brigade:invalid_input:summary' using errcode = '22023'; end if;
  if p_idempotency_key is null or char_length(p_idempotency_key) not between 1 and 128 then
    raise exception 'brigade:invalid_input:idempotency_key' using errcode = '22023'; end if;

  -- sender: owned by the caller (uniform not_found otherwise), an active member (unauthorized, 4.5.7), and only
  -- then not closed (conflict, 4.5.8 / C-31); NO lease check (D12)
  select * into v_sender from brigade.sessions where id = p_sender_session_id and owner_id = v_uid;
  if not found then raise exception 'brigade:not_found' using errcode = 'PT404'; end if;
  if not exists (select 1 from brigade.memberships where team_id = v_sender.team_id and user_id = v_uid and status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  if v_sender.closed_at is not null then raise exception 'brigade:conflict:sender_closed' using errcode = 'P0001'; end if;

  -- recipient: same team and an active member's session (C-08: a revoked member's session is outside the team);
  -- a closed recipient is accepted, the message waits (C-31); the same not_found as for a random id (C-25)
  select s.* into v_recipient from brigade.sessions s
   where s.id = p_recipient_session_id and s.team_id = v_sender.team_id
     and exists (select 1 from brigade.memberships m where m.team_id = s.team_id and m.user_id = s.owner_id and m.status = 'active');
  if not found then raise exception 'brigade:not_found' using errcode = 'PT404'; end if;
  if v_recipient.id = v_sender.id then raise exception 'brigade:invalid_input:recipient_is_self' using errcode = '22023'; end if;

  v_hash := extensions.digest(p_body, 'sha256');

  -- idempotency (fast path; the unique index is the authority under races)
  select * into v_row from brigade.messages where sender_session_id = v_sender.id and idempotency_key = p_idempotency_key;
  if found then
    if v_row.body_hash <> v_hash or v_row.recipient_session_id <> p_recipient_session_id then
      raise exception 'brigade:conflict:idempotency_key' using errcode = 'P0001';   -- never 23505 (see 5.4 intro)
    end if;
    return brigade.message_result(v_row, true);
  end if;

  -- rate limits (D17, 4.5.12, C-28), IN THIS ORDER: per sender session (minute, hour), then per principal (summed
  -- over all of the principal's sessions, so registering more sessions does not multiply the budget), then the
  -- per-(sender, recipient) unacknowledged cap, then the recipient-wide one. The last two must stay in this order:
  -- one sender must never exhaust a recipient's inbox for everyone else, and a test that fills both caps at once
  -- must be told sender_quota_for_recipient (the mutant_caporder lesson of P1-5/P1-6). Each message carries
  -- retry_after_seconds as its last component; the adapter turns it into retry_after_ms > 0.
  select count(*) filter (where created_at > now() - interval '1 minute'), count(*) into v_min, v_hour
    from brigade.messages where sender_session_id = v_sender.id and created_at > now() - interval '1 hour';
  if v_min >= 20 then raise exception 'brigade:rate_limited:send_per_minute:60' using errcode = 'P0001'; end if;
  if v_hour >= 200 then raise exception 'brigade:rate_limited:send_per_hour:3600' using errcode = 'P0001'; end if;
  select count(*) filter (where created_at > now() - interval '1 minute'), count(*) into v_pmin, v_phour
    from brigade.messages where sender_user_id = v_uid and created_at > now() - interval '1 hour';
  if v_pmin >= 60 then raise exception 'brigade:rate_limited:principal_per_minute:60' using errcode = 'P0001'; end if;
  if v_phour >= 600 then raise exception 'brigade:rate_limited:principal_per_hour:3600' using errcode = 'P0001'; end if;
  select count(*) filter (where sender_session_id = v_sender.id), count(*) into v_pair_unacked, v_unacked
    from brigade.messages where recipient_session_id = v_recipient.id and delivery_state = 'accepted';
  if v_pair_unacked >= 15 then raise exception 'brigade:rate_limited:sender_quota_for_recipient:60' using errcode = 'P0001'; end if;
  if v_unacked >= 60 then raise exception 'brigade:rate_limited:recipient_inbox_full:60' using errcode = 'P0001'; end if;

  -- hop_count is server-computed (4.5.12, C-29, C-29b). Explicit: reply_to must be a message this sender session
  -- RECEIVED (its recipient is the sender), else the uniform not_found (no existence oracle); hop = its hop + 1.
  -- Implicit: with no reply_to, the most recent message the recipient sent to this sender within the 600 s
  -- implicit_reply_window is the message being answered (hop + 1; none → 0), so a pair of models that never label
  -- replies is still bounded. Exactly one more per step; above max_hop_count (32) the send is loop_detected.
  if p_reply_to is not null then
    select * into v_reply from brigade.messages where id = p_reply_to and recipient_session_id = v_sender.id;
    if not found then raise exception 'brigade:not_found' using errcode = 'PT404'; end if;
    v_hops := v_reply.hop_count + 1;
  else
    select * into v_reply from brigade.messages
     where sender_session_id = v_recipient.id and recipient_session_id = v_sender.id
       and created_at > now() - interval '10 minutes'
     order by created_at desc, seq desc limit 1;      -- seq breaks created_at ties (P2-4: pgTAP's one-transaction chain found the tie nondeterministic)
    if found then v_hops := v_reply.hop_count + 1; end if;
  end if;
  if v_hops > 32 then raise exception 'brigade:loop_detected:max_hops' using errcode = 'P0001'; end if;

  update brigade.sessions set last_seen_at = now() where id = v_sender.id;   -- a send proves liveness (D12)

  begin
    insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id,
                                  summary, body, body_hash, reply_to, hop_count, idempotency_key)
    values (v_sender.team_id, v_uid, v_sender.id, v_recipient.id, p_summary, p_body, v_hash, p_reply_to, v_hops, p_idempotency_key)
    returning * into v_row;
  exception when unique_violation then                                         -- a concurrent retry won the race
    select * into v_row from brigade.messages where sender_session_id = v_sender.id and idempotency_key = p_idempotency_key;
    if v_row.body_hash <> v_hash or v_row.recipient_session_id <> p_recipient_session_id then
      raise exception 'brigade:conflict:idempotency_key' using errcode = 'P0001';   -- propagates out of the handler
    end if;
    return brigade.message_result(v_row, true);
  end;
  return brigade.message_result(v_row, false);
end $$;
revoke execute on function brigade.send_message(uuid, uuid, text, text, text, uuid) from public, anon;
grant  execute on function brigade.send_message(uuid, uuid, text, text, text, uuid) to authenticated;

-- fetch_inbox: the unacknowledged set for an owned session, oldest first (the cursor, 5.7).
create or replace function brigade.fetch_inbox(p_session_id uuid, p_limit integer default 100)
returns jsonb language plpgsql stable security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid();
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.owned_active_session(p_session_id, v_uid);               -- not_found / unauthorized (revoked)
  return coalesce((
    select jsonb_agg(brigade.envelope(x.m) order by (x.m).seq)
      from (select m from brigade.messages m                                   -- whole-row reference: type brigade.messages
             where m.recipient_session_id = p_session_id and m.delivery_state = 'accepted'
             order by m.seq limit least(greatest(coalesce(p_limit, 100), 1), 200)) x), '[]'::jsonb);
end $$;
revoke execute on function brigade.fetch_inbox(uuid, integer) from public, anon;
grant  execute on function brigade.fetch_inbox(uuid, integer) to authenticated;

-- ack_messages: {acked, unknown}; already-injected owned ids count as acked; foreign ids are unknown, never errors.
create or replace function brigade.ack_messages(p_session_id uuid, p_message_ids uuid[])
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_mine uuid[]; v_unknown uuid[];
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.owned_active_session(p_session_id, v_uid);               -- not_found / unauthorized (revoked)
  update brigade.messages set delivery_state = 'injected', injected_at = now()
   where id = any(p_message_ids) and recipient_session_id = p_session_id and delivery_state = 'accepted';
  select coalesce(array_agg(m.id), '{}') into v_mine from brigade.messages m
   where m.id = any(p_message_ids) and m.recipient_session_id = p_session_id;
  select coalesce(array_agg(u.id), '{}') into v_unknown from unnest(p_message_ids) as u(id) where u.id <> all(v_mine);
  return jsonb_build_object('acked', to_jsonb(v_mine), 'unknown', to_jsonb(v_unknown));
end $$;
revoke execute on function brigade.ack_messages(uuid, uuid[]) from public, anon;
grant  execute on function brigade.ack_messages(uuid, uuid[]) to authenticated;

-- ---------------------------------------------------------------------------
-- 5.5 Stamping triggers (second layer; the RPCs already do this)
-- ---------------------------------------------------------------------------

create or replace function brigade.stamp_message()
returns trigger language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_team uuid;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  new.sender_user_id := v_uid; new.created_at := now(); new.delivery_state := 'accepted'; new.injected_at := null;
  new.body_hash := extensions.digest(new.body, 'sha256');
  select team_id into v_team from brigade.sessions where id = new.sender_session_id and owner_id = v_uid;
  if v_team is null then raise exception 'brigade:not_found' using errcode = 'PT404'; end if;
  new.team_id := v_team;
  if not exists (select 1 from brigade.sessions where id = new.recipient_session_id and team_id = v_team) then
    raise exception 'brigade:not_found' using errcode = 'PT404'; end if;
  return new;
end $$;
revoke execute on function brigade.stamp_message() from public, anon;
create trigger messages_stamp before insert on brigade.messages for each row execute function brigade.stamp_message();

-- INSERT: stamp owner and created_at from the caller's claims and require an active membership.
create or replace function brigade.stamp_session()
returns trigger language plpgsql security definer set search_path = ''
as $$
begin
  if auth.uid() is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  new.owner_id := auth.uid(); new.created_at := now();
  if not exists (select 1 from brigade.memberships where team_id = new.team_id and user_id = new.owner_id and status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  return new;
end $$;
revoke execute on function brigade.stamp_session() from public, anon;
create trigger sessions_stamp before insert on brigade.sessions for each row execute function brigade.stamp_session();

-- UPDATE: never rewrite ownership; only assert that the identity columns are unchanged. An earlier draft ran the
-- INSERT stamp on UPDATE as well, which would have re-assigned any row updated on another user's behalf (a Phase 5
-- revoke_membership closing a revoked member's sessions, a support fix, a loosened policy) to the caller, i.e. a
-- mailbox-theft path in the layer meant to prevent one. No claims are required here: the RPCs already check the
-- caller, and administrative or fixture updates (backdating last_seen_at/closed_at as postgres) must stay possible.
create or replace function brigade.assert_session_identity()
returns trigger language plpgsql security definer set search_path = ''
as $$
begin
  if new.owner_id <> old.owner_id or new.team_id <> old.team_id or new.created_at <> old.created_at or new.id <> old.id then
    raise exception 'brigade:conflict:session_identity_immutable' using errcode = 'P0001'; end if;
  return new;
end $$;
revoke execute on function brigade.assert_session_identity() from public, anon;
create trigger sessions_identity before update on brigade.sessions for each row execute function brigade.assert_session_identity();
