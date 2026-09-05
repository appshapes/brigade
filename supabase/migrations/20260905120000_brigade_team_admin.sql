-- Brigade team-administration migration (plan 5.10, capability team.admin; P5-2, I-16 lag half, I-20, I-21).
-- Applied after 20260905041134_anonymous_user_gc.sql. This file adds the creator-only administration of a team:
-- brigade.team_as_creator, the shared gate (server-only), and four RPCs — rotate_join_secret, revoke_membership,
-- revoke_memberships_by_version and transfer_team — behind the adapter verbs `team rotate-secret`,
-- `team revoke-member` and `team transfer` of the frozen convention row docs/protocol-v1.md:156 (adapter-defined
-- shapes, "creator only", capability team.admin at 4.7 :799). It changes no table, no policy and no table grant:
-- revocation is already immediate on every read path because my_team_ids() and every reading RPC filter on
-- status = 'active' (5.3, 5.4; rls_isolation.sql), and join_team already answers a banned principal with the
-- wrong-secret body (20260830120000_brigade_schema.sql:259-262). team_admin.sql is the pgTAP proof; functions.sql
-- pins the catalogue (28 functions, 17 RPCs, 11 server-only after this file).
--
-- The oracle rule, once. Every refusal of the creator gate is ONE text, '42501 brigade:unauthorized', for a team that
-- does not exist, a team the caller is not a member of, a revoked or banned membership and an active member that is
-- not the creator, so no RPC here is a team-existence or a creator-identity oracle (docs/protocol-v1.md 4.5 rule 7;
-- the same text list_members raises, schema :463-464). And nothing here changes join_team: a banned principal
-- presenting the current, correct secret stays byte-identical to a stranger presenting garbage.
--
-- No rotation throttle, considered and rejected: a refusal inside a `secret_rotated_at > now() - 10 s` window would
-- save one bcrypt (~60 ms) at most every 10 s, but rotation is destructive exactly once, so an administrator whose
-- second attempt is refused has ALREADY rotated and no longer holds the secret — a worse failure than the load it
-- prevents. Only the creator can call it, and the same principal can already spend five create_team bcrypts an hour.

-- team_as_creator: the shared "creator only" gate of 5.10 — teams.created_by = the caller AND an active membership
-- (a creator who ran team leave rejoins first). ONE uniform brigade:unauthorized for: the team does not exist, the
-- caller is not a member, the caller is a revoked or banned member, and the caller is an active member but not the
-- creator. Byte-identical in all four (4.5 rule 7). `is distinct from` because created_by is nullable (schema :32);
-- `stable` as owned_active_session (schema :402), which is also called from RPCs that then update.
create or replace function brigade.team_as_creator(p_team_id uuid, p_uid uuid)
returns brigade.teams language plpgsql stable security definer set search_path = ''
as $$
declare v_team brigade.teams%rowtype;
begin
  select * into v_team from brigade.teams where id = p_team_id;
  if not found or v_team.created_by is distinct from p_uid then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  if not exists (select 1 from brigade.memberships m
                  where m.team_id = p_team_id and m.user_id = p_uid and m.status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  return v_team;
end $$;
-- Server-only: the three-way revoke of owned_active_session (schema :413), never a grant. functions.sql asserts that
-- the only ACL grantee is the owner and that authenticated is refused live.
revoke execute on function brigade.team_as_creator(uuid, uuid) from public, anon, authenticated;

-- rotate_join_secret: creator only. A fresh 128-bit random part exactly as create_team mints it (schema :201), bcrypt
-- of the random part only, secret_version + 1, secret_rotated_at = now(), and the whole secret returned ONCE in the
-- create_team shape ('brg1.<team_id>.<32 hex>', schema :208) so the adapter's ParseJoinSecret and its
-- TeamRef() == team_id check apply unchanged. Existing members are unaffected by construction and by omission:
-- nothing here touches brigade.memberships, and no read path anywhere reads joined_secret_version — it is a record
-- of how a member got in, never an authorization input. Rotation alone revokes nobody; the version becomes an
-- authorization input only inside revoke_memberships_by_version, which the creator calls explicitly.
create or replace function brigade.rotate_join_secret(p_team_id uuid)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_team brigade.teams%rowtype; v_random text;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.team_as_creator(p_team_id, v_uid);
  v_random := encode(extensions.gen_random_bytes(16), 'hex');              -- 128 bits, as create_team
  update brigade.teams
     set secret_hash = extensions.crypt(v_random, extensions.gen_salt('bf', 10)),
         secret_version = secret_version + 1,
         secret_rotated_at = now()
   where id = p_team_id
  returning * into v_team;
  return jsonb_build_object('status', 'rotated', 'team_id', v_team.id, 'team_name', v_team.name,
                            'secret_version', v_team.secret_version, 'secret_rotated_at', v_team.secret_rotated_at,
                            'join_secret', 'brg1.' || v_team.id::text || '.' || v_random);
end $$;
revoke execute on function brigade.rotate_join_secret(uuid) from public, anon;
grant  execute on function brigade.rotate_join_secret(uuid) to authenticated;

-- revoke_membership: creator only; sets the target's row to 'revoked' (rejoin allowed: join_team's upsert
-- re-activates it, schema :263-268) or, with p_ban, to 'banned' (terminal for join_team: the wrong-secret body,
-- schema :259-262; revoke_membership(…, false) on a banned row is the un-ban). The self-target is refused as
-- invalid_input:principal_ref — a self-ban would be unrecoverable (the creator could never un-ban itself, because
-- this gate needs an active membership) and `team leave` is the supported way out, uniform and reversible.
-- UPDATE ONLY, never an insert: a pre-emptive row for a principal that never joined would reference auth.users(id)
-- and raise 23503 for a random uuid while succeeding for a real one — an auth.users existence oracle reachable by
-- any creator. A target with no row is a silent no-op with changed = false; a creator sees the whole roster (D22),
-- so it loses no information it was entitled to. `changed` reports whether the status actually moved (a second
-- call on an already-revoked row answers false).
-- The broadcast comes BEFORE the close, exactly as leave_team does it (schema :281-288, :298-303): the select
-- that drives it filters on closed_at is null. A running watch treats any broadcast on its topic as a drain hint,
-- the drain's fetch_inbox raises the uniform unauthorized (owned_active_session, schema :409-411) and the watch
-- exits 5 — the adapter needs no change for this (I-16 arm A). The session close is leave_team's statement
-- verbatim; the sessions_identity trigger allows it because ownership is untouched (schema :653-657, which names
-- this function).
create or replace function brigade.revoke_membership(p_team_id uuid, p_user_id uuid, p_ban boolean default false)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_target text := case when coalesce(p_ban, false) then 'banned' else 'revoked' end;
  v_changed boolean; v_closed integer := 0;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.team_as_creator(p_team_id, v_uid);
  if p_user_id = v_uid then raise exception 'brigade:invalid_input:principal_ref' using errcode = '22023'; end if;
  update brigade.memberships
     set status = v_target,
         revoked_at = case when status = 'active' then now() else revoked_at end
   where team_id = p_team_id and user_id = p_user_id and status <> v_target
  returning true into v_changed;
  perform realtime.send(jsonb_build_object('session_id', s.id),                -- ids only; never a body or a label (T4)
                        'membership_revoked', 'brigade:session:' || s.id::text, true)
     from brigade.sessions s where s.team_id = p_team_id and s.owner_id = p_user_id and s.closed_at is null;
  update brigade.sessions set closed_at = coalesce(closed_at, now()), activity = 'idle'
   where team_id = p_team_id and owner_id = p_user_id and closed_at is null;    -- ownership untouched: the UPDATE trigger allows it
  get diagnostics v_closed = row_count;
  return jsonb_build_object('team_id', p_team_id, 'principal_ref', p_user_id, 'status', v_target,
                            'changed', coalesce(v_changed, false), 'sessions_closed', v_closed);
end $$;
revoke execute on function brigade.revoke_membership(uuid, uuid, boolean) from public, anon;
grant  execute on function brigade.revoke_membership(uuid, uuid, boolean) to authenticated;

-- revoke_memberships_by_version: creator only; the leaked-secret playbook (rotate, then evict everyone who entered
-- with the old secret so they come back through the new one). Revokes every ACTIVE membership of the team whose
-- joined_secret_version <= p_max_version, EXCLUDING the caller: the creator's own row carries version 1
-- (create_team, schema :205-206), so after one rotation the natural call `(team, 1)` would otherwise revoke the
-- creator, close its sessions and lock it out of this gate on the next call. Always 'revoked', never 'banned', and
-- no p_ban: banned would block exactly the rejoin the playbook depends on for the honest majority (a specific
-- principal is banned with revoke_membership, one at a time, deliberately). status = 'active' leaves banned rows
-- alone, so a version sweep never silently un-bans anyone. Then the same broadcast-then-close pass as
-- revoke_membership over the revoked principals' open sessions. No principal list in the answer: it stays small
-- and stable, and list_members afterwards is the record.
create or replace function brigade.revoke_memberships_by_version(p_team_id uuid, p_max_version integer)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_users uuid[]; v_closed integer := 0;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.team_as_creator(p_team_id, v_uid);
  if p_max_version is null then raise exception 'brigade:invalid_input:max_version' using errcode = '22023'; end if;
  with hit as (
    update brigade.memberships
       set status = 'revoked', revoked_at = now()
     where team_id = p_team_id and status = 'active'
       and user_id <> v_uid
       and joined_secret_version <= p_max_version
    returning user_id
  ) select coalesce(array_agg(user_id), '{}') into v_users from hit;
  perform realtime.send(jsonb_build_object('session_id', s.id),                -- ids only (T4)
                        'membership_revoked', 'brigade:session:' || s.id::text, true)
     from brigade.sessions s where s.team_id = p_team_id and s.owner_id = any(v_users) and s.closed_at is null;
  update brigade.sessions set closed_at = coalesce(closed_at, now()), activity = 'idle'
   where team_id = p_team_id and owner_id = any(v_users) and closed_at is null;
  get diagnostics v_closed = row_count;
  return jsonb_build_object('team_id', p_team_id, 'max_version', p_max_version,
                            'revoked', coalesce(array_length(v_users, 1), 0), 'sessions_closed', v_closed);
end $$;
revoke execute on function brigade.revoke_memberships_by_version(uuid, integer) from public, anon;
grant  execute on function brigade.revoke_memberships_by_version(uuid, integer) to authenticated;

-- transfer_team: creator only; sets teams.created_by to an ACTIVE member of the team. A delegation, not a
-- departure: the old creator keeps its active membership and its sessions and becomes an ordinary member (its next
-- admin call fails inside team_as_creator with the uniform unauthorized, byte-identical to a random team id); a
-- creator that also wants out runs team leave afterwards. The active-membership check comes BEFORE the update and
-- raises ONE text for "not an active member of this team" and "a uuid that names no auth.users row": otherwise the
-- created_by foreign key (schema :32) would raise 23503 for a random uuid and succeed for a real principal — the
-- same auth.users oracle revoke_membership closes. A self-transfer is an accepted no-op: idempotent, harmless, and
-- refusing it would need a distinct error text for no security gain.
create or replace function brigade.transfer_team(p_team_id uuid, p_new_creator uuid)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid();
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.team_as_creator(p_team_id, v_uid);
  if not exists (select 1 from brigade.memberships m
                  where m.team_id = p_team_id and m.user_id = p_new_creator and m.status = 'active') then
    raise exception 'brigade:invalid_input:principal_ref' using errcode = '22023'; end if;
  update brigade.teams set created_by = p_new_creator where id = p_team_id;
  return jsonb_build_object('team_id', p_team_id, 'principal_ref', p_new_creator, 'transferred', true);
end $$;
revoke execute on function brigade.transfer_team(uuid, uuid) from public, anon;
grant  execute on function brigade.transfer_team(uuid, uuid) to authenticated;
