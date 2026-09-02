-- Brigade housekeeping migration: retention and cleanup (plan 5.8, D13).
-- Applied after 20260830120000_brigade_schema.sql (tables) — this file only adds
-- brigade.gc_expired() and, when pg_cron is available, its hourly schedule.
-- Drafted for E0-1 (check (h) measured the pg_cron path below), finished in P2-3.
--
-- Retention constants (D13, published by `describe`):
--   * acknowledged (injected) messages: deleted 24 h after injected_at
--   * unacknowledged messages:          deleted 7 days after created_at
--   * sessions:                         deleted 7 days after closed_at or lease expiry
--                                       (so a `--resume` within a week still finds its inbox)
--   * join attempts:                    deleted after 24 h
--   * abandoned teams:                  see the comment inside gc_expired()
--
-- Scheduling: gc_expired() is meant to run hourly under pg_cron, but correctness never
-- depends on cron — session_heartbeat also calls it with probability 0.02 (plan 5.4),
-- so a stack without pg_cron degrades to opportunistic gc only.

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
  -- Phase 5 (P5-3): anonymous auth.users with no membership row (of any status) older than 7 days; never principals that
  -- hold a membership row, and never the creator of a live team: teams.created_by is on delete restrict (5.3), so such a
  -- delete would abort this whole function; the cleanup excludes them explicitly and retention.sql asserts it.
$$;
-- Server-only: no grant to anyone. PostgreSQL's built-in EXECUTE ... TO PUBLIC must be revoked
-- explicitly (the per-schema default-privileges statement is a documented no-op, plan 5.3);
-- with proacl non-null and no grants, service_role and authenticated both get 42501, which
-- functions.sql (9.3) asserts (no empty-grantee ACL entry on any brigade function).
revoke execute on function brigade.gc_expired() from public, anon, authenticated;

-- pg_cron scheduling — guarded so a stack WITHOUT pg_cron still applies this migration cleanly.
--
-- The plan's 5.8 text runs `create extension if not exists pg_cron` as a bare statement and wraps
-- only the cron.schedule() calls in an exception handler. On the minimal local stack pg_cron is
-- preloaded but the extension object does not exist until this migration creates it [plan 5.8,
-- verified live]; on a stack where the pg_cron shared library or extension packaging is absent
-- entirely, the bare `create extension` (and the `grant usage on schema cron`) would abort the
-- whole migration. To make its absence degrade to opportunistic gc (heartbeat-driven, D13)
-- instead of a failed migration, the extension creation, the schema grant and the schedule calls
-- are ALL inside one plpgsql block whose `exception when others` turns any failure into a NOTICE.
-- gc_expired() above is created unconditionally either way.
-- Measured (E0-1 check (h), pg_cron 1.6.4 on the `-x` stack of 5.9): the extension is created and
-- both jobs are scheduled, so `cron.job` lists brigade_gc there; the hosted project enables the
-- Cron integration (plan 5.9 step 3) or accepts opportunistic gc only.
--
-- cron.schedule(name, ...) upserts by job name, so re-running this block is idempotent.
do $$
begin
  create extension if not exists pg_cron with schema pg_catalog;
  grant usage on schema cron to postgres;
  perform cron.schedule('brigade_gc', '17 * * * *', $job$select brigade.gc_expired()$job$);
  perform cron.schedule('brigade_cron_log_gc', '23 3 * * *', $job$delete from cron.job_run_details where end_time < now() - interval '7 days'$job$);
exception when others then
  raise notice 'pg_cron not available, gc_expired() will run opportunistically only: %', sqlerrm;    -- correctness never depends on cron (D13)
end $$;
