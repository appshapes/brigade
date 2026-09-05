-- Brigade anonymous-user gc migration (plan 5.8, D13; P5-3, I-24).
-- Applied after 20260830120200_brigade_housekeeping.sql. This file adds brigade.gc_anonymous_users() and
-- re-creates brigade.gc_expired() so that it calls the new function as its LAST statement. Migrations are
-- append-only once applied (`supabase db push` tracks them by file name), so the housekeeping file is NOT
-- edited: the four statements of gc_expired() below are repeated byte for byte from
-- 20260830120200_brigade_housekeeping.sql:21-37, and only the P5-3 placeholder comment at its lines 38-40 is
-- replaced by the call. The housekeeping file's header stays the prose source for the four D13 windows;
-- internal/adapters/supabase/describe_test.go joins the intervals in BOTH files to what `describe` advertises,
-- so the two copies cannot drift apart silently.
--
-- Why a separate function rather than one more delete inside gc_expired(): gc_expired() is `language sql`, which
-- cannot trap an exception, and session_heartbeat calls it with probability 0.02 on the live path
-- (20260830120000_brigade_schema.sql:391). teams.created_by is `on delete restrict` (5.3), so a delete that
-- reached a creator would raise 23503 and, uncaught, abort the heartbeat's transaction and lapse the member's
-- lease [measured 2026-09-05 on the local stack]. The callee traps its own errors, so the message, session,
-- join-attempt and team gc can never be aborted by the user delete; and the call goes last, so the abandoned-team
-- delete has already orphaned any creator it is going to orphan: one gc_expired() call deletes an 8-day-old
-- single-member team, cascades its membership, and then reaps its now membership-less creator [measured].
-- Scheduling is unchanged: the hourly pg_cron job and the heartbeat's opportunistic call both reach this rule
-- through gc_expired(), so no second cron job exists to race the first for the same rows.

-- brigade.gc_anonymous_users(): the P5-3 rule of plan 5.8 / D13. Anonymous principals with no membership row of ANY
-- status and no team of their own, older than 7 days, are deleted; auth.sessions, auth.refresh_tokens (through
-- auth.sessions) and every brigade row cascade. Supabase does not clean anonymous users up
-- (https://supabase.com/docs/guides/auth/auth-anonymous, read 2026-09-05) and the keep-alive of P5-0 adds one such
-- row per day, so this function is what bounds auth.users on the hosted project.
--
-- The creator predicate excludes the creator of ANY team, not only of a "live" one: teams.created_by is
-- `on delete restrict` (5.3) and a violation raises 23503, which, uncaught, would abort session_heartbeat's
-- opportunistic gc call (5.4) and lapse a member's lease. Belt: the guard. Braces: the exception block, whose
-- subtransaction means this function can never abort its caller. A creator with a membership row is excluded twice
-- over, which is the point of 5.10's creator-loss paragraph.
--
-- created_at is NULL-able in auth.users and GoTrue always sets it; a row with a NULL created_at (only a hand-written
-- fixture) fails the comparison and is never reaped. Deliberate: this function deletes rows it can date.
--
-- Returns the number of users deleted, or -1 when the handler fired (its WARNING carries the cause), so a caller or
-- a test can tell "nothing was deletable" from "the statement rolled back". `is_anonymous`, not `email is null`:
-- users_is_anonymous_idx exists and a named human user must never be reaped by this rule.
create or replace function brigade.gc_anonymous_users()
returns integer language plpgsql security definer set search_path = ''
as $$
declare v_deleted integer := 0;
begin
  with victims as (
    select u.id from auth.users u
     where u.is_anonymous
       and u.created_at < now() - interval '7 days'
       and not exists (select 1 from brigade.memberships m where m.user_id = u.id)
       and not exists (select 1 from brigade.teams t where t.created_by = u.id)
     order by u.created_at
     limit 1000                     -- bounded work per call, like the messages delete: ~5 rows cascade per user
  ), gone as (
    delete from auth.users u using victims v where u.id = v.id returning 1
  )
  select count(*) into v_deleted from gone;
  return v_deleted;
exception when others then
  raise warning 'brigade.gc_anonymous_users: %', sqlerrm;   -- never aborts gc_expired or a heartbeat (D13)
  return -1;
end $$;
-- Server-only: no grant to anyone. PostgreSQL's built-in EXECUTE ... TO PUBLIC must be revoked explicitly (the
-- per-schema default-privileges statement is a documented no-op, plan 5.3). functions.sql asserts that the only
-- ACL grantee is the owner, retention.sql that authenticated, service_role and anon are all refused, and
-- scripts/ci/advisor-lints.sql that the function is not on the authenticated-executable list.
revoke execute on function brigade.gc_anonymous_users() from public, anon, authenticated;

-- gc_expired(): the four statements of 20260830120200_brigade_housekeeping.sql:21-37, unchanged, plus the call.
create or replace function brigade.gc_expired()
returns void language sql security definer set search_path = ''
as $$
  delete from brigade.messages where ctid in (
    select ctid from brigade.messages
     where (injected_at is not null and injected_at < now() - interval '24 hours')
        or created_at < now() - interval '7 days'
     order by created_at limit 5000);                                      -- bounded work per call
  delete from brigade.sessions
   where (closed_at is not null and closed_at < now() - interval '7 days')
      or last_seen_at + make_interval(secs => lease_seconds) < now() - interval '7 days';
  delete from brigade.join_attempts where attempted_at < now() - interval '24 hours';
  -- Abandoned teams: anyone with the publishable key can mint principals (30/h/IP hosted) and create 5 teams each,
  -- so teams would otherwise accumulate forever. A team older than 7 days with at most one membership and no session
  -- activity (no session row at all, or none seen within 7 days) is deleted; memberships cascade, and the Phase 5
  -- anonymous-user rule then reclaims the creator (a principal with no membership).
  delete from brigade.teams t
   where t.created_at < now() - interval '7 days'
     and (select count(*) from brigade.memberships m where m.team_id = t.id) <= 1
     and not exists (select 1 from brigade.sessions s where s.team_id = t.id and s.last_seen_at > now() - interval '7 days');
  -- P5-3: anonymous principals with no membership row of any status, older than 7 days, that own no team. Last,
  -- so the abandoned-team delete above has already orphaned any creator it is going to orphan (5.8). The callee
  -- traps its own errors, so a restricted delete can never abort this function or the heartbeat that calls it.
  select brigade.gc_anonymous_users();
$$;
-- `create or replace` keeps an existing function's ACL, so the housekeeping migration's revoke is still in force;
-- it is repeated here so this file states the rule it depends on and stays correct if ever applied on its own.
revoke execute on function brigade.gc_expired() from public, anon, authenticated;
