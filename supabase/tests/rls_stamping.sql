-- rls_stamping.sql (plan 9.3, I-01, I-03, I-04, I-05): the tables are not writable by members or by service_role
-- (42501 on insert/update/delete of messages, sessions, memberships), and the second layer of plan 5.5 holds for a
-- writer that CAN reach the tables (postgres with simulated claims, via pg_temp.as_user): sender_user_id, team_id,
-- created_at, delivery_state and injected_at of a message and owner_id/created_at of a session come from the
-- claims and the sender session, never from the caller; no claims means brigade:unauthenticated; the UPDATE
-- trigger refuses any change to owner_id, team_id, created_at or id and never re-stamps a row updated on another
-- user's behalf.
begin;
\ir helpers/auth.sql
select plan(55);

-- Fixtures through the RPCs: team A (X creator, Y joiner) and team Z (elsewhere). Sessions sx (X) and sy (Y) in A.
select pg_temp.new_user() as x \gset
select pg_temp.new_user() as y \gset
select pg_temp.new_user() as z \gset
select pg_temp.login(:'x', true, 'Xavier');
select r->>'team_id' as team_a, r->>'join_secret' as secret_a from brigade.create_team('Team A', 'Xavier') as r \gset
select (brigade.register_session(:'team_a'::uuid, 'sx'))->>'session_id' as sx \gset
select pg_temp.login(:'y', true, 'Yara');
select is((brigade.join_team(:'secret_a', 'Yara'))->>'status', 'joined', 'fixture: Y joins team A');
select (brigade.register_session(:'team_a'::uuid, 'sy'))->>'session_id' as sy \gset
select pg_temp.login(:'z', true, 'Zed');
select r->>'team_id' as team_z from brigade.create_team('Team Z', 'Zed') as r \gset
select (brigade.register_session(:'team_z'::uuid, 'sz'))->>'session_id' as sz \gset
select pg_temp.logout();

-- 1. Direct writes are 42501 for a member (the grants are SELECT only; there is no write policy either, D22).
select pg_temp.login(:'x', true, 'Xavier');
select throws_ok($$insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id, body, body_hash, idempotency_key)
                   values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', '$$ || :'sx' || $$', '$$ || :'sy' || $$', 'b', '\x00', 'k')$$,
                 '42501', 'permission denied for table messages', 'member: insert into messages is 42501');
select throws_ok($$update brigade.messages set body = 'x'$$, '42501', 'permission denied for table messages', 'member: update messages is 42501');
select throws_ok($$delete from brigade.messages$$, '42501', 'permission denied for table messages', 'member: delete from messages is 42501');
select throws_ok($$insert into brigade.sessions (team_id, owner_id, name) values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', 'forged')$$,
                 '42501', 'permission denied for table sessions', 'member: insert into sessions is 42501');
select throws_ok($$update brigade.sessions set name = 'x'$$, '42501', 'permission denied for table sessions', 'member: update sessions is 42501');
select throws_ok($$delete from brigade.sessions$$, '42501', 'permission denied for table sessions', 'member: delete from sessions is 42501');
select throws_ok($$insert into brigade.memberships (team_id, user_id, joined_secret_version) values ('$$ || :'team_a' || $$', '$$ || :'z' || $$', 1)$$,
                 '42501', 'permission denied for table memberships', 'member: insert into memberships is 42501');
select throws_ok($$update brigade.memberships set status = 'banned'$$, '42501', 'permission denied for table memberships', 'member: update memberships is 42501');
select throws_ok($$delete from brigade.memberships$$, '42501', 'permission denied for table memberships', 'member: delete from memberships is 42501');
select throws_ok($$update brigade.teams set secret_hash = 'x'$$, '42501', 'permission denied for table teams', 'member: update teams is 42501');
select pg_temp.logout();

-- 2. ... and for service_role (schema usage only).
set local role service_role;
select throws_ok($$insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id, body, body_hash, idempotency_key)
                   values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', '$$ || :'sx' || $$', '$$ || :'sy' || $$', 'b', '\x00', 'k')$$,
                 '42501', 'permission denied for table messages', 'service_role: insert into messages is 42501');
select throws_ok($$update brigade.messages set body = 'x'$$, '42501', 'permission denied for table messages', 'service_role: update messages is 42501');
select throws_ok($$delete from brigade.messages$$, '42501', 'permission denied for table messages', 'service_role: delete from messages is 42501');
select throws_ok($$insert into brigade.sessions (team_id, owner_id, name) values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', 'forged')$$,
                 '42501', 'permission denied for table sessions', 'service_role: insert into sessions is 42501');
select throws_ok($$update brigade.sessions set name = 'x'$$, '42501', 'permission denied for table sessions', 'service_role: update sessions is 42501');
select throws_ok($$delete from brigade.sessions$$, '42501', 'permission denied for table sessions', 'service_role: delete from sessions is 42501');
select throws_ok($$insert into brigade.memberships (team_id, user_id, joined_secret_version) values ('$$ || :'team_a' || $$', '$$ || :'z' || $$', 1)$$,
                 '42501', 'permission denied for table memberships', 'service_role: insert into memberships is 42501');
select throws_ok($$update brigade.memberships set status = 'banned'$$, '42501', 'permission denied for table memberships', 'service_role: update memberships is 42501');
select throws_ok($$delete from brigade.memberships$$, '42501', 'permission denied for table memberships', 'service_role: delete from memberships is 42501');
reset role;

-- 3. Message stamping: a postgres insert with X's claims that forges sender_user_id = Y, team_id = Z, an old
--    created_at, delivery_state = injected and an injected_at is stored with X, the sender session's team, now(),
--    accepted and null.
select pg_temp.as_user(:'x');
select is(current_user::text, 'postgres', 'as_user keeps the postgres role (fixture writer)');
select is(auth.uid(), :'x'::uuid, 'as_user sets auth.uid() to X');
insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id, body, body_hash, idempotency_key,
                              created_at, delivery_state, injected_at)
  values (:'team_z'::uuid, :'y'::uuid, :'sx'::uuid, :'sy'::uuid, 'forged stamps', '\x00', 'stamp-1', '2000-01-01', 'injected', '2000-01-02')
  returning id as m_forged \gset
select is((select sender_user_id from brigade.messages where id = :'m_forged'::uuid), :'x'::uuid, 'stamp_message: sender_user_id is the claims subject X, not the forged Y');
select is((select team_id from brigade.messages where id = :'m_forged'::uuid), :'team_a'::uuid, 'stamp_message: team_id is the sender session''s team, not the forged Z');
select is((select created_at from brigade.messages where id = :'m_forged'::uuid), now(), 'stamp_message: created_at is now(), not the forged 2000-01-01');
select is((select delivery_state from brigade.messages where id = :'m_forged'::uuid), 'accepted', 'stamp_message: delivery_state is forced to accepted');
select is((select injected_at from brigade.messages where id = :'m_forged'::uuid), null::timestamptz, 'stamp_message: injected_at is forced to null');
select is((select body_hash from brigade.messages where id = :'m_forged'::uuid), extensions.digest('forged stamps', 'sha256'), 'stamp_message: body_hash is recomputed from the body, not taken from the caller');
select throws_ok($$insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id, body, body_hash, idempotency_key)
                   values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', '$$ || :'sy' || $$', '$$ || :'sx' || $$', 'not my session', '\x00', 'stamp-2')$$,
                 'PT404', 'brigade:not_found', 'stamp_message: a sender session the claims subject does not own is not_found');
select throws_ok($$insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id, body, body_hash, idempotency_key)
                   values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', '$$ || :'sx' || $$', '$$ || :'sz' || $$', 'other team', '\x00', 'stamp-3')$$,
                 'PT404', 'brigade:not_found', 'stamp_message: a recipient outside the sender''s team is not_found');
select throws_ok($$insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id, body, body_hash, idempotency_key)
                   values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', '$$ || :'sx' || $$', '$$ || :'sy' || $$', repeat('x', 16385), '\x00', 'stamp-4')$$,
                 '23514', null, 'messages: the 16 KiB body check holds even for a postgres insert');
select lives_ok($$insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id, body, body_hash, idempotency_key)
                   values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', '$$ || :'sx' || $$', '$$ || :'sy' || $$', repeat('x', 16384), '\x00', 'stamp-5')$$,
                 'messages: a 16384-byte body is accepted (the boundary)');

-- 4. Session stamping: owner_id and created_at come from the claims and now(); an active membership is required.
insert into brigade.sessions (team_id, owner_id, name, created_at) values (:'team_a'::uuid, :'y'::uuid, 'forged owner', '2000-01-01')
  returning id as s_forged \gset
select is((select owner_id from brigade.sessions where id = :'s_forged'::uuid), :'x'::uuid, 'stamp_session: owner_id is the claims subject X, not the forged Y');
select is((select created_at from brigade.sessions where id = :'s_forged'::uuid), now(), 'stamp_session: created_at is now(), not the forged 2000-01-01');
select throws_ok($$insert into brigade.sessions (team_id, owner_id, name) values ('$$ || :'team_z' || $$', '$$ || :'x' || $$', 'not a member')$$,
                 '42501', 'brigade:unauthorized', 'stamp_session: a team the claims subject is not an active member of is unauthorized');
update brigade.memberships set status = 'revoked' where team_id = :'team_a'::uuid and user_id = :'x'::uuid;
select throws_ok($$insert into brigade.sessions (team_id, owner_id, name) values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', 'revoked')$$,
                 '42501', 'brigade:unauthorized', 'stamp_session: a revoked membership is unauthorized (active is required)');
update brigade.memberships set status = 'active' where team_id = :'team_a'::uuid and user_id = :'x'::uuid;

-- 5. No claims at all: both stamping triggers raise brigade:unauthenticated.
select pg_temp.logout();
select is(auth.uid(), null::uuid, 'logout clears the claims');
select throws_ok($$insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id, body, body_hash, idempotency_key)
                   values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', '$$ || :'sx' || $$', '$$ || :'sy' || $$', 'no claims', '\x00', 'stamp-6')$$,
                 '28000', 'brigade:unauthenticated', 'stamp_message: a postgres insert with no claims is unauthenticated');
select throws_ok($$insert into brigade.sessions (team_id, owner_id, name) values ('$$ || :'team_a' || $$', '$$ || :'x' || $$', 'no claims')$$,
                 '28000', 'brigade:unauthenticated', 'stamp_session: a postgres insert with no claims is unauthenticated');

-- 6. The UPDATE trigger: identity columns are immutable, with or without claims; everything else may change and
--    an update made on another user's behalf never re-stamps owner_id.
select throws_ok($$update brigade.sessions set owner_id = '$$ || :'y' || $$' where id = '$$ || :'sx' || $$'$$,
                 'P0001', 'brigade:conflict:session_identity_immutable', 'sessions UPDATE (no claims): owner_id cannot change');
select throws_ok($$update brigade.sessions set team_id = '$$ || :'team_z' || $$' where id = '$$ || :'sx' || $$'$$,
                 'P0001', 'brigade:conflict:session_identity_immutable', 'sessions UPDATE (no claims): team_id cannot change');
select throws_ok($$update brigade.sessions set created_at = '2000-01-01' where id = '$$ || :'sx' || $$'$$,
                 'P0001', 'brigade:conflict:session_identity_immutable', 'sessions UPDATE (no claims): created_at cannot change');
select throws_ok($$update brigade.sessions set id = gen_random_uuid() where id = '$$ || :'sx' || $$'$$,
                 'P0001', 'brigade:conflict:session_identity_immutable', 'sessions UPDATE (no claims): id cannot change');
select pg_temp.as_user(:'y');
select throws_ok($$update brigade.sessions set owner_id = '$$ || :'y' || $$' where id = '$$ || :'sx' || $$'$$,
                 'P0001', 'brigade:conflict:session_identity_immutable', 'sessions UPDATE (with Y''s claims): owner_id cannot be taken over');
select throws_ok($$update brigade.sessions set owner_id = '$$ || :'x' || $$', created_at = created_at + interval '1 second' where id = '$$ || :'sx' || $$'$$,
                 'P0001', 'brigade:conflict:session_identity_immutable', 'sessions UPDATE (with claims): same owner but a shifted created_at is refused');
-- Update of X's session with Y's claims that changes only closed_at: allowed, and owner_id stays X (the
-- mailbox-theft path the trigger comment describes: an INSERT-style re-stamp would have handed sx to Y).
select lives_ok($$update brigade.sessions set closed_at = now() where id = '$$ || :'sx' || $$'$$, 'sessions UPDATE (with Y''s claims): closing X''s session is allowed');
select is((select owner_id from brigade.sessions where id = :'sx'::uuid), :'x'::uuid, 'sessions UPDATE on another user''s behalf leaves owner_id unchanged (never re-stamped from the claims)');
select is((select team_id from brigade.sessions where id = :'sx'::uuid), :'team_a'::uuid, '... and team_id unchanged');
select ok((select closed_at is not null from brigade.sessions where id = :'sx'::uuid), '... while closed_at did change');
select pg_temp.logout();
select lives_ok($$update brigade.sessions set last_seen_at = now() - interval '5 minutes', closed_at = null, name = 'renamed' where id = '$$ || :'sx' || $$'$$,
                'sessions UPDATE (no claims, as postgres): backdating last_seen_at, reopening and renaming are allowed (the 9.3 fixture pattern)');
select is((select owner_id from brigade.sessions where id = :'sx'::uuid), :'x'::uuid, '... and owner_id is still X');
-- The definer path: leave_team closes the caller's sessions through the same trigger and keeps ownership.
select pg_temp.login(:'x', true, 'Xavier');
select is((brigade.leave_team(:'team_a'::uuid))->>'left', 'true', 'definer function: leave_team closes X''s sessions through the UPDATE trigger');
select pg_temp.logout();
select ok((select closed_at is not null from brigade.sessions where id = :'sx'::uuid), '... the session is closed');
select is((select owner_id from brigade.sessions where id = :'sx'::uuid), :'x'::uuid, '... and owner_id is still X after a definer-function update');
-- messages has no UPDATE trigger: backdating created_at/injected_at as postgres works (the 9.3 fixture pattern).
select lives_ok($$update brigade.messages set created_at = now() - interval '1 day', injected_at = now() where id = '$$ || :'m_forged' || $$'$$,
                'messages UPDATE as postgres: created_at and injected_at can be time-shifted (no UPDATE trigger, by design)');

select * from finish();
rollback;
