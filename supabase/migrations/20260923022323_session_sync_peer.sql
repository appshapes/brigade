-- Brigade session sync-peer migration (capability session.sync_peer; C-47). Applied after
-- 20260920180000_session_brigade_version.sql. It carries ONE fact onto brigade.sessions: the opaque
-- `<adapter>:<descriptor>` a session's folder-sync adapter publishes so a teammate's adapter can introduce it — for
-- Syncthing, `syncthing:` and the device id (docs/protocol-v1.md 4.4.2 `sync_peer`, at most 256 code points,
-- unverified text no consumer but the sync adapter its prefix names interprets). It goes into brigade.session_record
-- (so `session register` and `session list` return it, 4.4.3) and through register_session and session_heartbeat
-- (4.4.4: absent means unchanged, the harness never clears it). A heartbeat carries it because a session's sync
-- adapter usually attaches after the registration, so the watcher's first heartbeat after that is the first call
-- that knows it (plan folder-sync.md 4.2).
--
-- Why it exists: folder sync (plan folder-sync.md) introduces the peers of a team's sessions to one another, and the
-- roster is the one thing Brigade has that a sync engine lacks. A session whose harness predates this member, or
-- that has no sync adapter attached, never sends it and reads as null — a teammate's adapter simply has no peer to
-- introduce for it.
--
-- Migrations are append-only once applied (`supabase db push` tracks them by file name), so no earlier file is
-- edited: register_session, session_heartbeat and session_record below repeat 20260920180000's bodies, byte for byte
-- apart from the appended parameter, its validation and the column in the insert, the resume update, the heartbeat's
-- coalesce() list and the record.
--
-- DROP FUNCTION rather than CREATE OR REPLACE for the two RPCs, for the reason 20260910193200 gives at length: a
-- replace with a different parameter list OVERLOADS (a second pg_proc row), PostgREST answers a named-argument RPC
-- matching two candidates with 300 Multiple Choices, and supabase/tests/functions.sql pins the pg_proc row count. The
-- drop takes the old ACL with it, so revoke and grant are restated with the FULL new argument-type list, and
-- scripts/ci/advisor-lints.sql's expected-signature rows are updated in the same commit. Every existing caller keeps
-- working because the parameter is APPENDED with default null; an adapter whose backend lacks this migration gets
-- PGRST202 and retries without the parameter (internal/adapters/supabase/compat.go) — the value is dropped, never
-- the registration or the heartbeat.
--
-- The cap is a wire-format bound with no `limits` member (protocol.MaxSyncPeerChars: `limits` members are required,
-- so a new one would fail `describe` for every adapter written before it). It is checked twice, as brigade_version's
-- is: the RPC raises the D15 text 'brigade:invalid_input:sync_peer' (errcode 22023, so the adapter answers
-- invalid_input with details.field = sync_peer, C-47), and the column constraint is the belt beneath.

alter table brigade.sessions
  add column sync_peer text check (char_length(sync_peer) <= 256);

-- session_record: the same object as before plus the member, always present (null when the harness has not reported
-- it — JSON convention 4: the adapter decodes null as absent and omits it). CREATE OR REPLACE keeps the function's
-- ACL; the revoke is restated so this file reads complete on its own.
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
  'model', s.model, 'context_used_tokens', s.context_used_tokens,
  'brigade_version', s.brigade_version,
  'sync_peer', s.sync_peer,
  'created_at', s.created_at) $$;
revoke execute on function brigade.session_record(brigade.sessions, text) from public, anon, authenticated;

drop function brigade.register_session(uuid, text, text, text, text, text, text, text, integer, uuid, text, bigint, text, text);
create function brigade.register_session(
  p_team_id uuid, p_name text, p_description text default null, p_activity text default 'idle',
  p_inbound text default null, p_harness text default null, p_harness_version text default null,
  p_workspace_label text default null, p_lease_seconds integer default 90, p_resume_session_id uuid default null,
  p_model text default null, p_context_used_tokens bigint default null, p_human_label text default null,
  p_brigade_version text default null, p_sync_peer text default null)
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
  if p_human_label is not null and char_length(p_human_label) > 128 then raise exception 'brigade:invalid_input:human_label' using errcode = '22023'; end if;
  if p_lease_seconds is null or p_lease_seconds not between 30 and 600 then raise exception 'brigade:invalid_input:lease_seconds' using errcode = '22023'; end if;
  if p_model is not null and char_length(p_model) > 128 then raise exception 'brigade:invalid_input:model' using errcode = '22023'; end if;
  if p_context_used_tokens is not null and p_context_used_tokens < 0 then raise exception 'brigade:invalid_input:context_used_tokens' using errcode = '22023'; end if;
  if p_brigade_version is not null and char_length(p_brigade_version) > 64 then raise exception 'brigade:invalid_input:brigade_version' using errcode = '22023'; end if;
  if p_sync_peer is not null and char_length(p_sync_peer) > 256 then raise exception 'brigade:invalid_input:sync_peer' using errcode = '22023'; end if;

  -- C-45: fill an EMPTY membership label, never replace one. It runs after every validation, so a registration this
  -- call is about to refuse writes nothing, and before the record is built, so the returned record already carries
  -- the adopted label. The update repeats `human_label is null` in its predicate as well as in the branch, so two
  -- concurrent registrations cannot both write it; v_label comes from `returning`, and when the predicate matched
  -- no row — the losing half of that race, under READ COMMITTED — the label the winner wrote is re-read, so this
  -- call can never return a label the membership does not hold.
  if coalesce(v_label, '') = '' and p_human_label is not null then
    update brigade.memberships set human_label = p_human_label
     where team_id = p_team_id and user_id = v_uid and status = 'active' and coalesce(human_label, '') = ''
    returning human_label into v_label;
    if not found then
      select human_label into v_label from brigade.memberships where team_id = p_team_id and user_id = v_uid and status = 'active';
    end if;
  end if;

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
           workspace_label = p_workspace_label, lease_seconds = p_lease_seconds,
           model = p_model, context_used_tokens = p_context_used_tokens, brigade_version = p_brigade_version,
           sync_peer = p_sync_peer
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
    insert into brigade.sessions (team_id, owner_id, name, description, activity, inbound, harness, harness_version, workspace_label, lease_seconds,
                                  model, context_used_tokens, brigade_version, sync_peer)
    values (p_team_id, v_uid, p_name, p_description, p_activity, p_inbound, p_harness, p_harness_version, p_workspace_label, p_lease_seconds,
            p_model, p_context_used_tokens, p_brigade_version, p_sync_peer)
    returning * into v_row;
  end if;
  return brigade.session_record(v_row, v_label)
      || jsonb_build_object('resumed', v_resumed, 'lease_seconds', v_row.lease_seconds, 'server_time', now());
end $$;
revoke execute on function brigade.register_session(uuid, text, text, text, text, text, text, text, integer, uuid, text, bigint, text, text, text) from public, anon;
grant  execute on function brigade.register_session(uuid, text, text, text, text, text, text, text, integer, uuid, text, bigint, text, text, text) to authenticated;

drop function brigade.session_heartbeat(uuid, text, text, text, text, integer, text, bigint, text);
create function brigade.session_heartbeat(
  p_session_id uuid, p_activity text default null, p_name text default null, p_description text default null,
  p_inbound text default null, p_lease_seconds integer default null,
  p_model text default null, p_context_used_tokens bigint default null, p_brigade_version text default null,
  p_sync_peer text default null)
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
  if p_model is not null and char_length(p_model) > 128 then raise exception 'brigade:invalid_input:model' using errcode = '22023'; end if;
  if p_context_used_tokens is not null and p_context_used_tokens < 0 then raise exception 'brigade:invalid_input:context_used_tokens' using errcode = '22023'; end if;
  if p_brigade_version is not null and char_length(p_brigade_version) > 64 then raise exception 'brigade:invalid_input:brigade_version' using errcode = '22023'; end if;
  if p_sync_peer is not null and char_length(p_sync_peer) > 256 then raise exception 'brigade:invalid_input:sync_peer' using errcode = '22023'; end if;
  select * into v_row from brigade.sessions where id = p_session_id and owner_id = v_uid;
  if not found then raise exception 'brigade:not_found' using errcode = 'PT404'; end if;
  if not exists (select 1 from brigade.memberships m where m.team_id = v_row.team_id and m.user_id = v_uid and m.status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  if v_row.closed_at is not null then raise exception 'brigade:conflict:session_closed' using errcode = 'P0001'; end if;
  update brigade.sessions
     set last_seen_at = now(), activity = coalesce(p_activity, activity), name = coalesce(p_name, name),
         description = coalesce(p_description, description), inbound = coalesce(p_inbound, inbound),
         lease_seconds = coalesce(p_lease_seconds, lease_seconds),
         model = coalesce(p_model, model), context_used_tokens = coalesce(p_context_used_tokens, context_used_tokens),
         brigade_version = coalesce(p_brigade_version, brigade_version), sync_peer = coalesce(p_sync_peer, sync_peer)
   where id = p_session_id
  returning * into v_row;
  if random() < 0.02 then perform brigade.gc_expired(); end if;             -- works without pg_cron
  return jsonb_build_object('session_id', v_row.id,
    'state', case when v_row.activity = 'busy' then 'active' else 'idle' end,
    'lease_until', v_row.last_seen_at + make_interval(secs => v_row.lease_seconds), 'server_time', now());
end $$;
revoke execute on function brigade.session_heartbeat(uuid, text, text, text, text, integer, text, bigint, text, text) from public, anon;
grant  execute on function brigade.session_heartbeat(uuid, text, text, text, text, integer, text, bigint, text, text) to authenticated;
