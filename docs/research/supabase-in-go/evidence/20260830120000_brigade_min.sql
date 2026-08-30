-- Minimal Brigade schema for the Go client experiment (modelled on plan section 5).
create schema if not exists brigade;
revoke all on schema brigade from public;
grant usage on schema brigade to authenticated, service_role;
alter default privileges in schema brigade revoke execute on functions from public, anon, authenticated;

create table brigade.sessions (
  id          uuid primary key default gen_random_uuid(),
  owner_id    uuid not null references auth.users(id) on delete cascade,
  name        text not null check (char_length(name) between 1 and 64),
  created_at  timestamptz not null default now()
);
create table brigade.messages (
  id                   uuid primary key default gen_random_uuid(),
  seq                  bigint generated always as identity,
  sender_user_id       uuid not null references auth.users(id) on delete cascade,
  sender_session_id    uuid not null references brigade.sessions(id) on delete cascade,
  recipient_session_id uuid not null references brigade.sessions(id) on delete cascade,
  body                 text not null check (octet_length(body) between 1 and 16384),
  delivery_state       text not null default 'accepted' check (delivery_state in ('accepted','injected')),
  created_at           timestamptz not null default now()
);
alter table brigade.sessions enable row level security;
alter table brigade.messages enable row level security;
create policy sessions_select on brigade.sessions for select to authenticated
  using ( owner_id = (select auth.uid()) );
create policy messages_select on brigade.messages for select to authenticated
  using ( exists (select 1 from brigade.sessions s
                  where s.id = messages.recipient_session_id and s.owner_id = (select auth.uid())) );
revoke all on all tables in schema brigade from anon, authenticated, service_role;
grant select on brigade.sessions, brigade.messages to authenticated;

create or replace function brigade.whoami()
returns jsonb language sql stable set search_path = ''
as $$ select jsonb_build_object('uid', auth.uid(), 'role', auth.role(), 'claims', auth.jwt(), 'server_time', now()) $$;
grant execute on function brigade.whoami() to anon, authenticated;

create or replace function brigade.register_session(p_name text)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_row brigade.sessions%rowtype;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if p_name is null or char_length(p_name) not between 1 and 64 then
    raise exception 'brigade:invalid_input:session_name' using errcode = '22023'; end if;
  insert into brigade.sessions (owner_id, name) values (v_uid, p_name) returning * into v_row;
  return jsonb_build_object('session_id', v_row.id, 'session_name', v_row.name, 'principal_ref', v_row.owner_id,
                            'created_at', v_row.created_at, 'server_time', now());
end $$;
grant execute on function brigade.register_session(text) to authenticated;

create or replace function brigade.send_message(p_sender_session_id uuid, p_recipient_session_id uuid, p_body text)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_sender brigade.sessions%rowtype; v_row brigade.messages%rowtype;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if p_body is null or octet_length(p_body) = 0 or octet_length(p_body) > 16384 then
    raise exception 'brigade:invalid_input:body' using errcode = '22023'; end if;
  select * into v_sender from brigade.sessions where id = p_sender_session_id and owner_id = v_uid;
  if not found then raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
  if not exists (select 1 from brigade.sessions where id = p_recipient_session_id) then
    raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
  insert into brigade.messages (sender_user_id, sender_session_id, recipient_session_id, body)
  values (v_uid, v_sender.id, p_recipient_session_id, p_body) returning * into v_row;
  return jsonb_build_object('message_id', v_row.id, 'seq', v_row.seq, 'recipient_session_id', v_row.recipient_session_id,
                            'created_at', v_row.created_at);
end $$;
grant execute on function brigade.send_message(uuid, uuid, text) to authenticated;

create or replace function brigade.fetch_inbox(p_session_id uuid, p_limit integer default 100)
returns jsonb language plpgsql stable security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid();
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if not exists (select 1 from brigade.sessions where id = p_session_id and owner_id = v_uid) then
    raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
  return coalesce((select jsonb_agg(jsonb_build_object('message_id', m.id, 'seq', m.seq, 'body', m.body,
                                                        'sender_session_id', m.sender_session_id, 'created_at', m.created_at,
                                                        'delivery_state', m.delivery_state) order by m.seq)
                     from brigade.messages m
                    where m.recipient_session_id = p_session_id and m.delivery_state = 'accepted'), '[]'::jsonb);
end $$;
grant execute on function brigade.fetch_inbox(uuid, integer) to authenticated;

-- Error-shape probes: one function per SQLSTATE the plan's 5.4 table uses.
create or replace function brigade.raise_unauthorized() returns jsonb language plpgsql security definer set search_path = ''
as $$ begin raise exception 'brigade:unauthorized' using errcode = '42501'; end $$;
create or replace function brigade.raise_conflict() returns jsonb language plpgsql security definer set search_path = ''
as $$ begin raise exception 'brigade:conflict:session_live' using errcode = 'P0001'; end $$;
create or replace function brigade.raise_rate_limited() returns jsonb language plpgsql security definer set search_path = ''
as $$ begin raise exception 'brigade:rate_limited:send_per_minute:60' using errcode = 'P0001'; end $$;
create or replace function brigade.raise_invalid() returns jsonb language plpgsql security definer set search_path = ''
as $$ begin raise exception 'brigade:invalid_input:body' using errcode = '22023'; end $$;
create or replace function brigade.raise_unauthenticated() returns jsonb language plpgsql security definer set search_path = ''
as $$ begin raise exception 'brigade:unauthenticated' using errcode = '28000'; end $$;
create or replace function brigade.raise_with_detail() returns jsonb language plpgsql security definer set search_path = ''
as $$ begin raise exception 'brigade:not_found' using errcode = 'P0002', detail = 'detail text here', hint = 'hint text here'; end $$;
grant execute on function brigade.raise_unauthorized(), brigade.raise_conflict(), brigade.raise_rate_limited(),
  brigade.raise_invalid(), brigade.raise_unauthenticated(), brigade.raise_with_detail() to authenticated;
-- brigade.authenticated_only(): granted to authenticated but NOT anon, so an apikey-only call is a real 42501.
create or replace function brigade.authenticated_only() returns jsonb language sql stable set search_path = ''
as $$ select jsonb_build_object('ok', true) $$;
grant execute on function brigade.authenticated_only() to authenticated;

-- Realtime: Broadcast from Database (plan 5.6).
create or replace function brigade.notify_message_inserted()
returns trigger language plpgsql security definer set search_path = ''
as $$
begin
  perform realtime.send(jsonb_build_object('message_id', new.id, 'seq', new.seq),
                        'message_accepted', 'brigade:session:' || new.recipient_session_id::text, true);
  return null;
end $$;
create trigger messages_notify after insert on brigade.messages for each row execute function brigade.notify_message_inserted();

create or replace function brigade.owns_session_topic(p_topic text) returns boolean
language sql stable security definer set search_path = ''
as $$ select exists (select 1 from brigade.sessions s
                     where s.owner_id = (select auth.uid()) and p_topic = 'brigade:session:' || s.id::text) $$;
revoke execute on function brigade.owns_session_topic(text) from public, anon;
grant  execute on function brigade.owns_session_topic(text) to authenticated;

-- The realtime.messages table is created by the Realtime service's own migrations; guard so the
-- experiment records whether it exists at migration time rather than failing the stack start.
do $$ begin
  if to_regclass('realtime.messages') is null then
    raise notice 'brigade: realtime.messages does not exist yet; apply realtime policy after start';
  else
    execute $p$create policy brigade_session_topic_read on realtime.messages for select to authenticated
      using ( realtime.messages.extension = 'broadcast' and brigade.owns_session_topic((select realtime.topic())) )$p$;
  end if;
end $$;
