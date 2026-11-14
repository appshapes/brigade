-- Brigade team-move migration (work item 273, part 2; ADR 0003; capability team.move of docs/protocol-v1.md 4.4.10).
-- Applied after 20261015120000_mail_gateway_allowed_senders.sql. A team that leaves this project for another backend
-- (a Brigade server) keeps its team id, and its creator records the move here: where the team went (the adapter, the
-- url and the certificate pin, as the team's file names them at the new backend) and the move ticket, a join secret
-- of that team which the new backend admits a member with for as long as its operator keeps the move open. A member's
-- session whose team file changed asks this project, which its member consented to and still holds a session on,
-- whether the team moved, and follows only when the record names exactly what the file names (docs/security.md).
-- This file adds one table and three RPCs: record_team_move and clear_team_move, behind `team move` and
-- `team move --clear`, and read_team_move, behind `team moved`. It changes no existing table, policy or function,
-- so an adapter that predates it loses nothing, and an adapter that has the commands on a project without this
-- file reads "no move" (PGRST202 on read_team_move is answered as moved false) and refuses to record one.
-- team_move.sql is the pgTAP proof; functions.sql pins the catalogue (31 functions, 20 RPCs, 11 server-only after
-- this file) and hygiene.sql the table.
--
-- What the table holds that nothing else here does: the ticket in the clear. A member's adapter has to present the
-- ticket to the new backend, so a hash would be no use; the join secret itself is still only a bcrypt (teams
-- .secret_hash). Whoever can read this table (the project's owner, a holder of its service key) can therefore join
-- the team at its new backend until the ticket ends, as a member and never as its owner, and every active member
-- reads it through read_team_move, each of whom holds the join secret already. The table is closed to every API
-- role: RLS on, no policy and no grant, exactly as join_attempts, and only the three functions below touch it.
--
-- The oracle rule of team_admin holds here unchanged. record_team_move and clear_team_move go through
-- team_as_creator, so every refusal of the creator gate is the ONE text '42501 brigade:unauthorized', for a team that
-- does not exist as for an active member that is not the creator, and it is raised before any argument is read, so a
-- caller that is not the creator learns nothing about what a record must look like. read_team_move raises the same
-- text for everyone who is not an active member, as list_members does.

create table brigade.team_moves (
  team_id     uuid primary key references brigade.teams(id) on delete cascade,   -- ending the team takes its record with it
  adapter     text not null check (char_length(adapter) between 1 and 64),
  url         text not null check (char_length(url) between 1 and 2048),
  certificate text check (char_length(certificate) between 1 and 128),
  ticket      text not null check (char_length(ticket) between 1 and 512),
  recorded_by uuid not null,            -- who recorded it; a note and never an authority, so no foreign key holds a user back from the gc
  recorded_at timestamptz not null default now()
);
alter table brigade.team_moves enable row level security;   -- no policies, no grants: server-only, as join_attempts
revoke all on brigade.team_moves from public, anon, authenticated, service_role;

-- record_team_move: creator only. The limits are the protocol's (4.4.10: adapter 64, url 2048, certificate 128 code
-- points), checked here as well because the table is the last place they are held. The ticket must be a join secret
-- of THIS team, 'brg1.<team id>.<secret>' with nothing in it that is a space or a control character: a ticket of
-- another team admits nobody at the new backend, and recording it would send every member's session to a refusal.
-- A second record replaces the first (one move per team), and the answer never carries the ticket.
create or replace function brigade.record_team_move(
  p_team_id uuid, p_adapter text, p_url text, p_ticket text, p_certificate text default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare
  v_uid uuid := auth.uid(); v_row brigade.team_moves%rowtype;
  v_prefix text := 'brg1.' || p_team_id::text || '.';
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.team_as_creator(p_team_id, v_uid);
  if p_adapter is null or char_length(p_adapter) not between 1 and 64 then
    raise exception 'brigade:invalid_input:adapter' using errcode = '22023'; end if;
  if p_url is null or char_length(p_url) not between 1 and 2048 then
    raise exception 'brigade:invalid_input:url' using errcode = '22023'; end if;
  if p_certificate is not null and char_length(p_certificate) not between 1 and 128 then
    raise exception 'brigade:invalid_input:certificate' using errcode = '22023'; end if;
  if p_ticket is null or char_length(p_ticket) > 512 or char_length(p_ticket) <= char_length(v_prefix)
     or left(p_ticket, char_length(v_prefix)) <> v_prefix or p_ticket ~ '[[:space:][:cntrl:]]' then
    raise exception 'brigade:invalid_input:ticket' using errcode = '22023'; end if;
  insert into brigade.team_moves (team_id, adapter, url, certificate, ticket, recorded_by, recorded_at)
  values (p_team_id, p_adapter, p_url, p_certificate, p_ticket, v_uid, now())
  on conflict (team_id) do update
     set adapter = excluded.adapter, url = excluded.url, certificate = excluded.certificate,
         ticket = excluded.ticket, recorded_by = excluded.recorded_by, recorded_at = excluded.recorded_at
  returning * into v_row;
  return jsonb_build_object('moved', true, 'team_id', v_row.team_id, 'adapter', v_row.adapter, 'url', v_row.url,
                            'certificate', v_row.certificate, 'recorded_at', v_row.recorded_at);
end $$;
revoke execute on function brigade.record_team_move(uuid, text, text, text, text) from public, anon;
grant  execute on function brigade.record_team_move(uuid, text, text, text, text) to authenticated;

-- clear_team_move: creator only; removes the record. With none it changes nothing and answers the same, so the
-- command behind it is idempotent. It does not end the move at the new backend: the ticket admits there until that
-- backend's operator closes the move or the ticket ends. It only stops this project from handing the ticket out.
create or replace function brigade.clear_team_move(p_team_id uuid)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_cleared integer := 0;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.team_as_creator(p_team_id, v_uid);
  delete from brigade.team_moves where team_id = p_team_id;
  get diagnostics v_cleared = row_count;
  return jsonb_build_object('moved', false, 'team_id', p_team_id, 'cleared', v_cleared > 0);
end $$;
revoke execute on function brigade.clear_team_move(uuid) from public, anon;
grant  execute on function brigade.clear_team_move(uuid) to authenticated;

-- read_team_move: every ACTIVE member of the team, and nobody else, with list_members' own gate and its one text.
-- A member that was revoked or banned reads no record, so a ticket never reaches someone the team's creator put
-- out. The answer is the one place the ticket leaves this project: the adapter writes it to a file of the member's
-- and to no output (4.4.10).
create or replace function brigade.read_team_move(p_team_id uuid)
returns jsonb language plpgsql stable security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_row brigade.team_moves%rowtype;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if not exists (select 1 from brigade.memberships where team_id = p_team_id and user_id = v_uid and status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  select * into v_row from brigade.team_moves where team_id = p_team_id;
  if not found then
    return jsonb_build_object('moved', false, 'team_id', p_team_id); end if;
  return jsonb_build_object('moved', true, 'team_id', v_row.team_id, 'adapter', v_row.adapter, 'url', v_row.url,
                            'certificate', v_row.certificate, 'ticket', v_row.ticket, 'recorded_at', v_row.recorded_at);
end $$;
revoke execute on function brigade.read_team_move(uuid) from public, anon;
grant  execute on function brigade.read_team_move(uuid) to authenticated;
