-- pgTAP identity helpers for the Brigade database tests (plan 9.3; E0-1 check (d)).
-- Included from every test file with `\ir helpers/auth.sql` INSIDE its transaction, so the four
-- functions live in pg_temp and vanish with the rollback. pg_prove connects as `postgres`, which
-- may `set role` to the API roles and may write auth.users directly.
--
--   pg_temp.new_user(anon, created) insert an auth.users row and return its id. E0-1 (d): only `id`
--                                   is NOT NULL without a default, but an id-only insert leaves
--                                   `aud` and `role` as EMPTY STRINGS and `is_anonymous` false, so
--                                   `aud`, `role` and `is_anonymous` are set explicitly here.
--                                   `created_at` is nullable with NO default either (P5-3, measured
--                                   2026-09-05), and a NULL never satisfies gc_anonymous_users()'s
--                                   7-day comparison, so it is set too: `created` defaults to now()
--                                   and a retention fixture backdates it (GoTrue always dates a real
--                                   sign-up; only a hand-written row could be undated).
--   pg_temp.login(uid, anon, label) become that principal as PostgREST would present it: both JWT
--                                   GUCs (auth.uid() prefers the scalar `request.jwt.claim.sub`;
--                                   auth.jwt() reads the JSON `request.jwt.claims`) and
--                                   `set local role authenticated`, so RLS and the grants apply.
--   pg_temp.logout()                clear both GUCs (empty string, which auth.uid() reads as null)
--                                   and `reset role` back to `postgres`.
--   pg_temp.as_user(uid)            set ONLY the two JWT GUCs and keep the current role: a fixture
--                                   write to brigade.sessions/messages as `postgres` then passes
--                                   the stamping triggers (which raise brigade:unauthenticated
--                                   when auth.uid() is null and stamp owner_id/sender_user_id
--                                   from the claims), while RLS and grants still do not apply.
--
-- `set_config(..., true)` and `set local role` are transaction-local, so every switch is undone by
-- the test file's rollback. The `label` lands in user_metadata.human_label of the simulated claims
-- only; Brigade never reads a label from the JWT (memberships.human_label is the source).

create or replace function pg_temp.new_user(anon boolean default true, created timestamptz default now())
returns uuid language plpgsql as $$
declare uid uuid := gen_random_uuid();
begin
  insert into auth.users (id, aud, role, is_anonymous, created_at) values (uid, 'authenticated', 'authenticated', anon, created);
  return uid;
end $$;

create or replace function pg_temp.login(uid uuid, anon boolean default true, label text default null)
returns void language plpgsql as $$
begin
  perform set_config('request.jwt.claim.sub', uid::text, true);
  perform set_config('request.jwt.claims', jsonb_build_object(
    'sub', uid, 'role', 'authenticated', 'aud', 'authenticated', 'is_anonymous', anon,
    'user_metadata', jsonb_build_object('human_label', label))::text, true);
  execute 'set local role authenticated';
end $$;

create or replace function pg_temp.logout()
returns void language plpgsql as $$
begin
  perform set_config('request.jwt.claim.sub', '', true);
  perform set_config('request.jwt.claims', '', true);
  execute 'reset role';
end $$;

create or replace function pg_temp.as_user(uid uuid)
returns void language plpgsql as $$
begin
  perform set_config('request.jwt.claim.sub', uid::text, true);
  perform set_config('request.jwt.claims', jsonb_build_object(
    'sub', uid, 'role', 'authenticated', 'aud', 'authenticated', 'is_anonymous', true)::text, true);
end $$;
