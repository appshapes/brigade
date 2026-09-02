-- realtime_policy.sql (plan 9.3, I-13 policy half, I-14): the messages_notify trigger writes an ids-only broadcast
-- row on brigade:session:<recipient> (never the body); the realtime.messages select policy grants rows only to
-- the owner of the OPEN session named by the joined topic (simulated with set_config('realtime.topic', ...)),
-- not to another member, a non-member, a closed session, a revoked member or anon; no insert policy exists, so a
-- client cannot broadcast (a direct insert is refused and realtime.send from a client writes nothing); and
-- leave_team writes an ids-only membership_revoked broadcast on the topic of each of the leaver's open sessions
-- (the hint that ends a running watch at once, C-08).
begin;
\ir helpers/auth.sql
select plan(53);

-- Fixtures: team A (A1 creator with s1, A2 joiner with s2 and s3), N a member of nothing.
select pg_temp.new_user() as a1 \gset
select pg_temp.new_user() as a2 \gset
select pg_temp.new_user() as n1 \gset
select pg_temp.login(:'a1', true, 'Alice');
select r->>'team_id' as team_a, r->>'join_secret' as secret_a from brigade.create_team('Team A', 'Alice') as r \gset
select (brigade.register_session(:'team_a'::uuid, 's1'))->>'session_id' as s1 \gset
select pg_temp.login(:'a2', true, 'Bob');
select is((brigade.join_team(:'secret_a', 'Bob'))->>'status', 'joined', 'fixture: A2 joins team A');
select (brigade.register_session(:'team_a'::uuid, 's2'))->>'session_id' as s2 \gset
select (brigade.register_session(:'team_a'::uuid, 's3'))->>'session_id' as s3 \gset
select pg_temp.login(:'a1', true, 'Alice');
select (brigade.send_message(:'s1'::uuid, :'s2'::uuid, 'the body must never travel on the wire', 'k1'))->>'message_id' as m1 \gset
select (brigade.send_message(:'s1'::uuid, :'s3'::uuid, 'second recipient', 'k2'))->>'message_id' as m2 \gset
select pg_temp.logout();
select 'brigade:session:' || :'s1' as topic1 \gset
select 'brigade:session:' || :'s2' as topic2 \gset
select 'brigade:session:' || :'s3' as topic3 \gset

-- 1. The trigger's broadcast row (read as postgres, BYPASSRLS): one row per accepted message, ids only, private.
select is((select count(*) from realtime.messages where topic = :'topic2' and payload->>'message_id' = :'m1'), 1::bigint, 'notify: one broadcast row on the recipient''s topic for the message');
select is((select event from realtime.messages where payload->>'message_id' = :'m1'), 'message_accepted', 'notify: event message_accepted');
select is((select extension from realtime.messages where payload->>'message_id' = :'m1'), 'broadcast', 'notify: extension broadcast');
select is((select private from realtime.messages where payload->>'message_id' = :'m1'), true, 'notify: the broadcast is private');
select is((select array_agg(k order by k) from realtime.messages r, jsonb_object_keys(r.payload) k where r.payload->>'message_id' = :'m1'), array['id', 'message_id', 'seq'],
          'notify: the payload carries exactly message_id and seq (plus the id realtime.send adds) and never the body (T4)');
select is((select (r.payload->>'seq')::bigint from realtime.messages r where r.payload->>'message_id' = :'m1'), (select seq from brigade.messages where id = :'m1'::uuid), 'notify: seq matches the message row');
select is((select count(*) from realtime.messages where topic = :'topic1'), 0::bigint, 'notify: nothing is broadcast on the SENDER''s topic');
select is((select count(*) from realtime.messages where payload::text like '%the body must never%'), 0::bigint, 'notify: the body appears in no broadcast row');

-- 2. The policy set on realtime.messages: exactly one policy, for select, to authenticated; no insert/update/delete policy.
select policies_are('realtime', 'messages', array['brigade_session_topic_read'], 'realtime.messages carries exactly the brigade read policy');
select policy_roles_are('realtime', 'messages', 'brigade_session_topic_read', array['authenticated'], 'the read policy is to authenticated');
select policy_cmd_is('realtime', 'messages', 'brigade_session_topic_read', 'SELECT', 'the read policy is for select');
select is((select count(*) from pg_policy where polrelid = 'realtime.messages'::regclass and polcmd in ('a', 'w', 'd', '*')), 0::bigint, 'no insert, update, delete or ALL policy exists on realtime.messages (clients cannot broadcast)');
select ok((select relrowsecurity from pg_class where oid = 'realtime.messages'::regclass), 'RLS is enabled on realtime.messages');
select function_privs_are('brigade', 'owns_session_topic', array['text'], 'authenticated', array['EXECUTE'], 'owns_session_topic: authenticated may execute (Realtime evaluates the policy as the joining user)');
select function_privs_are('brigade', 'owns_session_topic', array['text'], 'anon', array[]::text[], 'owns_session_topic: anon may not');
select function_privs_are('brigade', 'owns_session_topic', array['text'], 'service_role', array[]::text[], 'owns_session_topic: service_role may not');

-- 3. Topic authorization through the policy (the join query Realtime runs), simulated with realtime.topic().
select pg_temp.login(:'a2', true, 'Bob');
select set_config('realtime.topic', :'topic2', true);
select is(realtime.topic(), :'topic2'::text, 'realtime.topic() returns the bare topic set on the connection');
select ok(brigade.owns_session_topic(:'topic2'), 'owns_session_topic: the owner of the open session s2 owns its topic');
select is((select count(*) from realtime.messages where topic = :'topic2'), 1::bigint, 'owner + own open topic: the broadcast row is visible (positive control)');
select is((select count(*) from realtime.messages where topic = :'topic3'), 1::bigint, 'the policy authorizes the JOINED topic, not rows: with s2''s topic pinned, rows of s3 (also A2''s) are visible too; Realtime scopes the query by topic itself (documented, not a defect)');
select set_config('realtime.topic', :'topic1', true);
select ok(not brigade.owns_session_topic(:'topic1'), 'owns_session_topic: A2 does not own s1''s topic (another member''s session)');
select is((select count(*) from realtime.messages), 0::bigint, 'a member joining ANOTHER member''s session topic sees no row');
select ok(not brigade.owns_session_topic('brigade:session:' || gen_random_uuid()::text), 'owns_session_topic: an unknown session id is false');
select ok(not brigade.owns_session_topic(null), 'owns_session_topic: a null topic is false');
select ok(not brigade.owns_session_topic(:'s2'), 'owns_session_topic: the bare session id without the brigade:session: prefix is false');
select pg_temp.login(:'a1', true, 'Alice');
select set_config('realtime.topic', :'topic2', true);
select is((select count(*) from realtime.messages where topic = :'topic2'), 0::bigint, 'the SENDER (a member, not the owner of s2) joining s2''s topic sees no row');
select pg_temp.login(:'n1');
select set_config('realtime.topic', :'topic2', true);
select is((select count(*) from realtime.messages where topic = :'topic2'), 0::bigint, 'a member of nothing joining s2''s topic sees no row');
select pg_temp.logout();
set local role anon;
select set_config('realtime.topic', :'topic2', true);
select is((select count(*) from realtime.messages where topic = :'topic2'), 0::bigint, 'anon joining s2''s topic sees no row (the policy is to authenticated only)');
reset role;
-- Closed session: the owner loses the topic (s2 closed through the RPC; the baseline above saw the row).
select pg_temp.login(:'a2', true, 'Bob');
select lives_ok($$select brigade.close_session('$$ || :'s2' || $$')$$, 'fixture: A2 closes s2');
select set_config('realtime.topic', :'topic2', true);
select ok(not brigade.owns_session_topic(:'topic2'), 'owns_session_topic: false for the owner''s CLOSED session');
select is((select count(*) from realtime.messages where topic = :'topic2'), 0::bigint, 'the owner of a closed session no longer sees its topic''s rows');
-- Revocation: s3 stays OPEN, only the membership row changes (E0-2 h-sql); baseline first.
select set_config('realtime.topic', :'topic3', true);
select is((select count(*) from realtime.messages where topic = :'topic3'), 1::bigint, 'baseline: A2 sees the row on its open session s3''s topic');
select ok(brigade.owns_session_topic(:'topic3'), 'baseline: owns_session_topic true for s3');
select pg_temp.logout();
update brigade.memberships set status = 'revoked', revoked_at = now() where team_id = :'team_a'::uuid and user_id = :'a2'::uuid;
select pg_temp.login(:'a2', true, 'Bob');
select set_config('realtime.topic', :'topic3', true);
select ok(not brigade.owns_session_topic(:'topic3'), 'revoked: owns_session_topic is false for the revoked member''s own OPEN session (membership join)');
select is((select count(*) from realtime.messages where topic = :'topic3'), 0::bigint, 'revoked: the row on its open session''s topic is no longer visible');

-- 4. No client send: a direct insert as authenticated is refused by RLS, and realtime.send from a client writes nothing.
select throws_ok($$insert into realtime.messages (topic, extension, payload, event, private) values ('$$ || :'topic3' || $$', 'broadcast', '{}', 'forged', true)$$,
                 '42501', 'new row violates row-level security policy for table "messages"', 'a client insert into realtime.messages is refused (no insert policy)');
set local client_min_messages = error;   -- realtime.send downgrades the refused insert to a WARNING; keep the TAP stream clean
select lives_ok($$select realtime.send('{"forged": true}'::jsonb, 'forged_event', '$$ || :'topic3' || $$', true)$$, 'realtime.send is callable by a client (it swallows the refusal into a WARNING) ...');
set local client_min_messages = notice;
select pg_temp.logout();
select is((select count(*) from realtime.messages where event = 'forged_event'), 0::bigint, '... but writes nothing: only the database emits broadcasts');

-- 5. leave_team wakes the leaver's running watches (plan 5.6, C-08): before it closes the caller's open sessions the
--    definer function writes a membership_revoked broadcast, ids only, on each open session's private topic, so a
--    watch drains at once instead of waiting for its timer and fetch_inbox answers the uniform unauthorized. A1
--    (Alice) still holds s1 open; a second open session s4 and a closed one s5 pin "each OPEN session"; a second
--    leave (idempotent, the row is already revoked) writes nothing more. Run as the leaving member, inside the
--    test transaction, exactly as the adapter calls it.
select pg_temp.login(:'a1', true, 'Alice');
select (brigade.register_session(:'team_a'::uuid, 's4'))->>'session_id' as s4 \gset
select (brigade.register_session(:'team_a'::uuid, 's5'))->>'session_id' as s5 \gset
select lives_ok($$select brigade.close_session('$$ || :'s5' || $$')$$, 'fixture: A1 closes s5 before the leave');
select 'brigade:session:' || :'s4' as topic4 \gset
select 'brigade:session:' || :'s5' as topic5 \gset
select is((select count(*) from realtime.messages where event = 'membership_revoked'), 0::bigint, 'leave_team: baseline, no membership_revoked row before the leave');
select is((brigade.leave_team(:'team_a'::uuid))->>'left', 'true', 'leave_team: A1 leaves team A');
select pg_temp.logout();
select is((select count(*) from realtime.messages where topic = :'topic1' and event = 'membership_revoked'), 1::bigint, 'leave_team: one membership_revoked row on the topic of the open session s1');
select is((select count(*) from realtime.messages where topic = :'topic4' and event = 'membership_revoked'), 1::bigint, 'leave_team: one membership_revoked row on the topic of the open session s4');
select is((select count(*) from realtime.messages where topic = :'topic5' and event = 'membership_revoked'), 0::bigint, 'leave_team: none on the topic of s5, closed before the leave');
select is((select count(*) from realtime.messages where event = 'membership_revoked'), 2::bigint, 'leave_team: exactly one row per open session, nowhere else');
select is((select array_agg(k order by k) from realtime.messages r, jsonb_object_keys(r.payload) k where r.topic = :'topic1' and r.event = 'membership_revoked'), array['id', 'session_id'],
          'leave_team: the payload carries exactly session_id (plus the id realtime.send adds), ids only (T4)');
select is((select payload->>'session_id' from realtime.messages where topic = :'topic1' and event = 'membership_revoked'), :'s1', 'leave_team: session_id names the session whose topic carries the row');
select is((select extension from realtime.messages where topic = :'topic4' and event = 'membership_revoked'), 'broadcast', 'leave_team: extension broadcast');
select is((select private from realtime.messages where topic = :'topic4' and event = 'membership_revoked'), true, 'leave_team: the broadcast is private');
select is((select count(*) from brigade.sessions where id in (:'s1'::uuid, :'s4'::uuid) and closed_at is not null), 2::bigint, 'leave_team: both open sessions are closed once the hint is written');
select pg_temp.login(:'a1', true, 'Alice');
select is((brigade.leave_team(:'team_a'::uuid))->>'left', 'true', 'leave_team again: idempotent, left = true');
select pg_temp.logout();
select is((select count(*) from realtime.messages where event = 'membership_revoked'), 2::bigint, 'leave_team again: an already-revoked member''s leave writes no further row');

select * from finish();
rollback;
