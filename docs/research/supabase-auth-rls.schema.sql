create extension if not exists pgcrypto with schema extensions;
create schema if not exists private;
revoke all on schema private from public;
grant usage on schema private to authenticated;

create table public.teams (
  id               uuid primary key default gen_random_uuid(),
  name             text not null check (length(name) between 1 and 80),
  join_secret_hash text not null,
  secret_version   integer not null default 1,
  created_by       uuid references auth.users(id) on delete set null,
  created_at       timestamptz not null default now()
);
create table public.team_members (
  team_id     uuid not null references public.teams(id) on delete cascade,
  user_id     uuid not null default auth.uid() references auth.users(id) on delete cascade,
  human_label text check (length(human_label) <= 120),
  joined_at   timestamptz not null default now(),
  primary key (team_id, user_id)
);
create index team_members_user_id_idx on public.team_members (user_id);
create table public.sessions (
  id             uuid primary key default gen_random_uuid(),
  team_id        uuid not null references public.teams(id) on delete cascade,
  owner_user_id  uuid not null default auth.uid() references auth.users(id) on delete cascade,
  name           text not null check (length(name) between 1 and 64),
  description    text check (length(description) <= 500),
  state          text not null default 'active' check (state in ('active','idle','offline')),
  harness        text, harness_version text,
  last_seen_at   timestamptz not null default now(),
  lease_until    timestamptz not null default now() + interval '5 minutes',
  created_at     timestamptz not null default now()
);
create index sessions_team_id_idx on public.sessions (team_id);
create index sessions_owner_idx   on public.sessions (owner_user_id);
create table public.messages (
  id                    uuid primary key default gen_random_uuid(),
  team_id               uuid not null references public.teams(id) on delete cascade,
  sender_user_id        uuid not null default auth.uid() references auth.users(id) on delete cascade,
  sender_session_id     uuid not null references public.sessions(id) on delete cascade,
  recipient_session_id  uuid not null references public.sessions(id) on delete cascade,
  kind                  text not null default 'text' check (kind = 'text'),
  summary               text check (length(summary) <= 200),
  body                  text not null check (length(body) <= 16384),
  reply_to              uuid references public.messages(id) on delete set null,
  idempotency_key       text not null,
  created_at            timestamptz not null default now(),
  acked_at              timestamptz,
  unique (sender_session_id, idempotency_key)
);
create index messages_recipient_created_idx on public.messages (recipient_session_id, created_at);
create index messages_team_idx on public.messages (team_id);

create or replace function private.my_team_ids()
returns setof uuid language sql security definer stable set search_path = ''
as $$ select team_id from public.team_members where user_id = (select auth.uid()) $$;
revoke execute on function private.my_team_ids() from public;
grant execute on function private.my_team_ids() to authenticated;

alter table public.teams        enable row level security;
alter table public.team_members enable row level security;
alter table public.sessions     enable row level security;
alter table public.messages     enable row level security;
revoke all on all tables in schema public from anon;

create policy teams_select on public.teams for select to authenticated
  using ( id in (select private.my_team_ids()) );
create policy members_select on public.team_members for select to authenticated
  using ( team_id in (select private.my_team_ids()) );
create policy members_delete_self on public.team_members for delete to authenticated
  using ( user_id = (select auth.uid()) );
create policy sessions_select on public.sessions for select to authenticated
  using ( team_id in (select private.my_team_ids()) );
create policy sessions_insert on public.sessions for insert to authenticated
  with check ( owner_user_id = (select auth.uid()) and team_id in (select private.my_team_ids()) );
create policy sessions_update on public.sessions for update to authenticated
  using ( owner_user_id = (select auth.uid()) )
  with check ( owner_user_id = (select auth.uid()) and team_id in (select private.my_team_ids()) );
create policy sessions_delete on public.sessions for delete to authenticated
  using ( owner_user_id = (select auth.uid()) );
create policy messages_select on public.messages for select to authenticated
  using (
    messages.sender_user_id = (select auth.uid())
    or exists (select 1 from public.sessions s
               where s.id = messages.recipient_session_id and s.owner_user_id = (select auth.uid()))
  );
create policy messages_insert on public.messages for insert to authenticated
  with check (
    messages.sender_user_id = (select auth.uid())
    and exists (select 1 from public.sessions ss
                where ss.id = messages.sender_session_id and ss.owner_user_id = (select auth.uid())
                  and ss.team_id = messages.team_id)
    and exists (select 1 from public.sessions rs
                where rs.id = messages.recipient_session_id and rs.team_id = messages.team_id)
  );
create policy messages_update_ack on public.messages for update to authenticated
  using ( exists (select 1 from public.sessions s
                  where s.id = messages.recipient_session_id and s.owner_user_id = (select auth.uid())) )
  with check ( true );

revoke all on public.teams, public.team_members, public.sessions, public.messages from authenticated;
grant select (id, name, secret_version, created_at) on public.teams to authenticated;
grant select, delete on public.team_members to authenticated;
grant select on public.sessions to authenticated;
grant insert (team_id, name, description, state, harness, harness_version) on public.sessions to authenticated;
grant update (name, description, state, harness, harness_version, last_seen_at, lease_until) on public.sessions to authenticated;
grant delete on public.sessions to authenticated;
grant select on public.messages to authenticated;
grant insert (sender_session_id, recipient_session_id, kind, summary, body, reply_to, idempotency_key) on public.messages to authenticated;
grant update (acked_at) on public.messages to authenticated;

-- join attempts + RPCs
create table public.team_join_attempts (
  id           bigint generated always as identity primary key,
  user_id      uuid not null,
  team_id      uuid,
  attempted_at timestamptz not null default now(),
  succeeded    boolean not null default false
);
create index team_join_attempts_user_time_idx on public.team_join_attempts (user_id, attempted_at desc);
alter table public.team_join_attempts enable row level security;
revoke all on public.team_join_attempts from anon, authenticated;

create or replace function public.create_team(p_name text, p_join_secret text, p_human_label text default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_id uuid;
begin
  if v_uid is null then raise exception 'unauthenticated' using errcode = '28000'; end if;
  if p_join_secret is null or octet_length(p_join_secret) < 16 or octet_length(p_join_secret) > 72 then
    raise exception 'join secret must be 16..72 bytes' using errcode = '22023';
  end if;
  insert into public.teams (name, join_secret_hash, created_by)
  values (p_name, extensions.crypt(p_join_secret, extensions.gen_salt('bf', 10)), v_uid)
  returning id into v_id;
  insert into public.team_members (team_id, user_id, human_label) values (v_id, v_uid, p_human_label);
  return jsonb_build_object('status', 'created', 'team_id', v_id, 'team_name', p_name);
end $$;

create or replace function public.join_team(p_team_id uuid, p_join_secret text, p_human_label text default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare
  v_uid uuid := auth.uid(); v_team public.teams%rowtype;
  v_window interval := interval '15 minutes'; v_max integer := 5;
  v_failures integer; v_oldest timestamptz;
begin
  if v_uid is null then raise exception 'unauthenticated' using errcode = '28000'; end if;
  if p_join_secret is null or octet_length(p_join_secret) > 72 then
    return jsonb_build_object('status', 'invalid_input');
  end if;
  select count(*), min(attempted_at) into v_failures, v_oldest
  from public.team_join_attempts
  where user_id = v_uid and not succeeded and attempted_at > now() - v_window;
  if v_failures >= v_max then
    return jsonb_build_object('status', 'rate_limited',
      'retry_after_seconds', greatest(1, extract(epoch from (v_oldest + v_window - now()))::integer));
  end if;
  select * into v_team from public.teams where id = p_team_id;
  if not found or v_team.join_secret_hash <> extensions.crypt(p_join_secret, v_team.join_secret_hash) then
    insert into public.team_join_attempts (user_id, team_id) values (v_uid, p_team_id);
    return jsonb_build_object('status', 'invalid_secret');
  end if;
  insert into public.team_members (team_id, user_id, human_label)
  values (v_team.id, v_uid, p_human_label)
  on conflict (team_id, user_id) do update set human_label = coalesce(excluded.human_label, public.team_members.human_label);
  insert into public.team_join_attempts (user_id, team_id, succeeded) values (v_uid, v_team.id, true);
  delete from public.team_join_attempts where user_id = v_uid and not succeeded;
  return jsonb_build_object('status', 'joined', 'team_id', v_team.id, 'team_name', v_team.name);
end $$;
revoke execute on function public.create_team(text, text, text) from public, anon;
revoke execute on function public.join_team(uuid, text, text) from public, anon;
grant execute on function public.create_team(text, text, text) to authenticated;
grant execute on function public.join_team(uuid, text, text) to authenticated;

-- stamping triggers
create or replace function private.stamp_message()
returns trigger language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_team uuid;
begin
  if v_uid is null then raise exception 'unauthenticated' using errcode = '28000'; end if;
  new.sender_user_id := v_uid;
  new.created_at := now();
  new.acked_at := null;
  select team_id into v_team from public.sessions where id = new.sender_session_id and owner_user_id = v_uid;
  if v_team is null then raise exception 'sender_session_id is not a session you own' using errcode = '42501'; end if;
  new.team_id := v_team;
  if not exists (select 1 from public.sessions where id = new.recipient_session_id and team_id = v_team) then
    raise exception 'recipient session not found in your team' using errcode = '42501';
  end if;
  return new;
end $$;
create trigger messages_stamp before insert on public.messages for each row execute function private.stamp_message();

create or replace function private.stamp_session()
returns trigger language plpgsql security definer set search_path = ''
as $$
begin
  if auth.uid() is null then raise exception 'unauthenticated' using errcode = '28000'; end if;
  new.owner_user_id := auth.uid();
  if tg_op = 'INSERT' then new.created_at := now(); end if;
  if not exists (select 1 from public.team_members where team_id = new.team_id and user_id = new.owner_user_id) then
    raise exception 'not a member of that team' using errcode = '42501';
  end if;
  return new;
end $$;
create trigger sessions_stamp before insert or update on public.sessions for each row execute function private.stamp_session();
alter publication supabase_realtime add table public.messages;
