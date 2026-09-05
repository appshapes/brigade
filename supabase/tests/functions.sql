-- functions.sql (plan 9.3, I-12): every function in the brigade schema is security definer with an empty
-- search_path, has execute revoked from public/anon (no empty-grantee `=X` ACL entry, non-null proacl), the
-- helpers, trigger functions, gc_expired and gc_anonymous_users are granted to nobody, the RPCs are granted to authenticated and
-- nothing else, and service_role can execute no RPC. Mechanical over pg_catalog plus three live calls.
begin;
\ir helpers/auth.sql
select plan(185);

-- The catalogue this file pins: 23 functions, 13 of them RPCs callable by authenticated, 10 server-only.
create temp table fn_expected (name text primary key, rpc boolean not null);
insert into fn_expected values
  ('my_team_ids', true), ('create_team', true), ('join_team', true), ('leave_team', true),
  ('register_session', true), ('session_heartbeat', true), ('close_session', true), ('list_sessions', true),
  ('list_members', true), ('send_message', true), ('fetch_inbox', true), ('ack_messages', true),
  ('owns_session_topic', true),
  ('session_record', false), ('envelope', false), ('message_result', false), ('owned_active_session', false),
  ('stamp_message', false), ('stamp_session', false), ('assert_session_identity', false),
  ('notify_message_inserted', false), ('gc_expired', false), ('gc_anonymous_users', false);

create temp view fn as
  select p.oid, p.proname, p.prosecdef, p.proconfig, p.proacl, p.proowner,
         pg_get_function_identity_arguments(p.oid) as args
    from pg_proc p where p.pronamespace = 'brigade'::regnamespace;

-- 1. The function set is exactly the expected one (no stray helper, nothing missing).
select functions_are('brigade', array(select name from fn_expected), 'brigade holds exactly the 23 expected functions');
select is((select count(*) from fn), 23::bigint, 'no overloads: 23 pg_proc rows in brigade');

-- 2. Every function is security definer (23 assertions).
select ok(prosecdef, 'security definer: ' || proname) from fn order by proname;

-- 3. Every function pins an EMPTY search_path in proconfig (23 assertions).
select ok(exists (select 1 from unnest(coalesce(proconfig, '{}')) c where c ~ '^search_path=("")?$'),
          'search_path='''' in proconfig: ' || proname)
  from fn order by proname;

-- 4. Explicit revoke happened: proacl is non-null on every function (23 assertions). A null proacl means the
--    built-in EXECUTE TO PUBLIC is still in force (plan 5.3: the per-schema default-privileges statement is a no-op).
select ok(proacl is not null, 'non-null proacl (explicit revoke ran): ' || proname) from fn order by proname;

-- 5. No empty-grantee (=X, i.e. PUBLIC) ACL entry on any function (23 assertions).
select ok(not exists (select 1 from aclexplode(proacl) a where a.grantee = 0),
          'no PUBLIC (=X) execute entry: ' || proname)
  from fn order by proname;

-- 6. anon can execute nothing in brigade (23 assertions).
select ok(not has_function_privilege('anon', oid, 'execute'), 'anon cannot execute: ' || proname) from fn order by proname;

-- 7. service_role can execute nothing in brigade (23 assertions; E0-1 (i)).
select ok(not has_function_privilege('service_role', oid, 'execute'), 'service_role cannot execute: ' || proname)
  from fn order by proname;

-- 8. authenticated can execute exactly the RPCs and none of the helpers/triggers/gc (23 assertions).
select is(has_function_privilege('authenticated', f.oid, 'execute'), e.rpc,
          case when e.rpc then 'authenticated may execute RPC: ' else 'authenticated may NOT execute server-only: ' end || f.proname)
  from fn f join fn_expected e on e.name = f.proname order by f.proname;

-- 9. The server-only ten are granted to nobody at all: the only ACL grantee is the owner (10 assertions).
select ok(not exists (select 1 from aclexplode(f.proacl) a where a.grantee <> f.proowner),
          'granted to nobody but the owner: ' || f.proname)
  from fn f join fn_expected e on e.name = f.proname where not e.rpc order by f.proname;

-- 10. Live denials, with the positive control that the same call works for a member.
select pg_temp.logout();
select pg_temp.new_user() as u1 \gset
select pg_temp.login(:'u1');
select throws_ok($$select brigade.list_members(gen_random_uuid())$$, '42501', 'brigade:unauthorized',
                 'positive control: authenticated reaches list_members (its own unauthorized, not a grant denial)');
select pg_temp.logout();
set local role service_role;
select throws_ok($$select brigade.list_members(gen_random_uuid())$$, '42501', 'permission denied for function list_members',
                 'service_role: execute denied on list_members (E0-1 (i))');
select throws_ok($$select brigade.gc_expired()$$, '42501', 'permission denied for function gc_expired',
                 'service_role: execute denied on gc_expired');
reset role;
set local role anon;
select throws_ok($$select brigade.list_members(gen_random_uuid())$$, '42501', 'permission denied for schema brigade',
                 'anon: no usage on schema brigade, so no function is reachable');
reset role;
select pg_temp.login(:'u1');
select throws_ok($$select brigade.gc_expired()$$, '42501', 'permission denied for function gc_expired',
                 'authenticated: gc_expired is server-only');
select throws_ok($$select brigade.owned_active_session(gen_random_uuid(), gen_random_uuid())$$, '42501',
                 'permission denied for function owned_active_session', 'authenticated: owned_active_session is server-only');
select pg_temp.logout();

-- 11. The triggers that wire the server-only functions are present and bound to them.
select triggers_are('brigade', 'messages', array['messages_stamp', 'messages_notify'], 'messages has exactly the stamp and notify triggers');
select triggers_are('brigade', 'sessions', array['sessions_stamp', 'sessions_identity'], 'sessions has exactly the stamp and identity triggers');
select trigger_is('brigade', 'messages', 'messages_stamp', 'brigade', 'stamp_message', 'messages_stamp calls brigade.stamp_message');
select trigger_is('brigade', 'messages', 'messages_notify', 'brigade', 'notify_message_inserted', 'messages_notify calls brigade.notify_message_inserted');
select trigger_is('brigade', 'sessions', 'sessions_stamp', 'brigade', 'stamp_session', 'sessions_stamp calls brigade.stamp_session');
select trigger_is('brigade', 'sessions', 'sessions_identity', 'brigade', 'assert_session_identity', 'sessions_identity calls brigade.assert_session_identity');

select * from finish();
rollback;
