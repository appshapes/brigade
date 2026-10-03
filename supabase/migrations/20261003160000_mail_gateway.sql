-- Brigade mail gateway migration (Trello card 48; .context/plans/human-sessions-slack-email.md). Applied after
-- 20260923022323_session_sync_peer.sql. It adds the durable state of the hosted mail gateway: which member and
-- session ARE the gateway for a team, the thread table that ties a Brigade message to the mail it came from or
-- went out as, and the set of provider mail ids already handled (a webhook is retried; a mail is read once).
--
-- Nothing in the brigade schema changes. The gateway is an ordinary anonymous member (joined by the installer,
-- scripts/gateway-install.sh) and the Edge Functions under supabase/functions/ act as it by setting the JWT
-- claims PostgREST would set, inside one transaction, exactly as supabase/tests/helpers/auth.sql does: every
-- RPC, limit, stamp and check of docs/protocol-v1.md applies to the gateway as to any member, and service_role
-- still holds nothing in brigade.* (hygiene.sql §4, functions.sql §7 stand). This schema is NOT exposed on the
-- Data API (config.toml [api] schemas names public, graphql_public and brigade only), has row level security
-- on every table and no policy and no grant: only postgres — the functions' connection — reads or writes it.
-- The hosted Security Advisor therefore never lists it, and scripts/ci/advisor-lints.sql's exposed-schema lints
-- do not scan it; the one lint that scans every schema (policy_exists_rls_disabled) finds RLS on and no policy.
--
-- Retention: a thread row is useful for as long as a person might answer the mail, which is longer than a
-- message lives (7 days unacknowledged, 24 h acknowledged, 4.5.9): 90 days, swept by the hourly gc below and,
-- where pg_cron is absent, never — the tables are small. received rows follow the same sweep.

create schema if not exists brigade_gateway;
revoke all on schema brigade_gateway from public;
-- No usage grant to anon, authenticated or service_role: postgres only, and nothing for PostgREST to expose.

create table brigade_gateway.gateways (
  team_id    uuid primary key references brigade.teams(id) on delete cascade,
  user_id    uuid not null references auth.users(id) on delete cascade,   -- the gateway's principal (a member)
  session_id uuid not null,                                               -- its current session; re-registered by the tick when the row is gone
  kind       text not null default 'email' check (kind in ('email')),
  created_at timestamptz not null default now()
);

create table brigade_gateway.threads (
  brigade_message_id uuid primary key,                       -- the Brigade message this row describes
  team_id            uuid not null references brigade.teams(id) on delete cascade,
  direction          text not null check (direction in ('in', 'out')),   -- in: mail -> session; out: session -> mail
  session_id         uuid not null,                          -- in: the recipient session; out: the sender session
  address            text not null check (char_length(address) <= 320),  -- the person's address (unverified text)
  mail_message_id    text check (char_length(mail_message_id) <= 998),   -- RFC 5322 Message-ID: theirs (in) or ours (out)
  provider_mail_id   text check (char_length(provider_mail_id) <= 128),
  subject            text check (char_length(subject) <= 998),
  created_at         timestamptz not null default now()
);
create index threads_mail_message_id_idx on brigade_gateway.threads (mail_message_id) where mail_message_id is not null;
create index threads_created_idx on brigade_gateway.threads (created_at);

create table brigade_gateway.received (
  provider_mail_id text primary key check (char_length(provider_mail_id) <= 128),
  received_at      timestamptz not null default now()
);
create index received_at_idx on brigade_gateway.received (received_at);

alter table brigade_gateway.gateways enable row level security;
alter table brigade_gateway.threads  enable row level security;
alter table brigade_gateway.received enable row level security;
revoke all on all tables in schema brigade_gateway from public, anon, authenticated, service_role;

-- gc: bounded, hourly where pg_cron runs (the housekeeping migration's guarded shape); server-only like gc_expired.
create or replace function brigade_gateway.gc()
returns void language sql security definer set search_path = ''
as $$
  delete from brigade_gateway.threads  where created_at  < now() - interval '90 days';
  delete from brigade_gateway.received where received_at < now() - interval '90 days';
$$;
revoke execute on function brigade_gateway.gc() from public, anon, authenticated, service_role;

do $$
begin
  perform cron.schedule('brigade_gateway_gc', '41 * * * *', $job$select brigade_gateway.gc()$job$);
exception when others then
  raise notice 'pg_cron not available, brigade_gateway.gc() is not scheduled: %', sqlerrm;
end $$;
