-- rls_isolation.sql (plan 9.3, I-08, I-09, I-10, I-16, I-22): a member of team A sees zero rows of team B in every
-- table (with the positive control that it sees its own); anon sees and calls nothing; service_role gets 42501 on
-- every table; the roster (memberships, list_members) is team-scoped (D22) and list_members answers a non-member
-- with brigade:unauthorized byte-identical for a foreign team and a random uuid; a revoked membership (set directly,
-- as E0-2's h-sql variant does) hides every row and every verb after a pre-revocation baseline; leave_team revokes,
-- closes, is idempotent, and join_team re-activates with rejoined = true.
begin;
\ir helpers/auth.sql
select plan(106);

-- Local helper: run a statement as the CURRENT role and return '<sqlstate> <message>' (null when it succeeds), so
-- two errors can be compared byte for byte.
create function pg_temp.err(q text) returns text language plpgsql as $$
begin
  execute q;
  return null;
exception when others then
  return sqlstate || ' ' || sqlerrm;
end $$;

-- Fixtures through the RPCs: team A (A1 creator, A2 joiner), team B (B1 creator), N a member of nothing.
select pg_temp.new_user() as a1 \gset
select pg_temp.new_user() as a2 \gset
select pg_temp.new_user() as b1 \gset
select pg_temp.new_user() as n1 \gset
select pg_temp.login(:'a1', true, 'Alice');
select r->>'team_id' as team_a, r->>'join_secret' as secret_a from brigade.create_team('Team A', 'Alice') as r \gset
select (brigade.register_session(:'team_a'::uuid, 'a1-main'))->>'session_id' as s_a1 \gset
select pg_temp.login(:'a2', true, 'Bob');
select is((brigade.join_team(:'secret_a', 'Bob'))->>'status', 'joined', 'fixture: A2 joins team A');
select (brigade.register_session(:'team_a'::uuid, 'a2-main'))->>'session_id' as s_a2 \gset
select pg_temp.login(:'b1', true, 'Bea');
select r->>'team_id' as team_b, r->>'join_secret' as secret_b from brigade.create_team('Team B', 'Bea') as r \gset
select (brigade.register_session(:'team_b'::uuid, 'b1-main'))->>'session_id' as s_b1 \gset
select (brigade.register_session(:'team_b'::uuid, 'b1-second'))->>'session_id' as s_b2 \gset
select (brigade.send_message(:'s_b1'::uuid, :'s_b2'::uuid, 'b internal', 'kb1'))->>'message_id' as m_b \gset
select pg_temp.login(:'a1', true, 'Alice');
select (brigade.send_message(:'s_a1'::uuid, :'s_a2'::uuid, 'a1 to a2', 'ka1'))->>'message_id' as m_a1_a2 \gset
select pg_temp.login(:'a2', true, 'Bob');
select (brigade.send_message(:'s_a2'::uuid, :'s_a1'::uuid, 'a2 to a1', 'ka2'))->>'message_id' as m_a2_a1 \gset
select pg_temp.logout();
select isnt(:'team_a'::uuid, :'team_b'::uuid, 'fixture: two distinct teams');
select is((select count(*) from brigade.messages where team_id = :'team_b'::uuid), 1::bigint, 'fixture (as postgres): team B holds one message');
select is((select count(*) from brigade.messages where team_id = :'team_a'::uuid), 2::bigint, 'fixture (as postgres): team A holds two messages');

-- 1. A member of A sees its own team in every table (positive controls) and zero rows of B in every table.
select pg_temp.login(:'a1', true, 'Alice');
select is((select count(*) from brigade.teams where id = :'team_a'::uuid), 1::bigint, 'A1 sees team A (control)');
select is((select count(*) from brigade.memberships where team_id = :'team_a'::uuid), 2::bigint, 'A1 sees both memberships of A (control)');
select is((select count(*) from brigade.sessions where team_id = :'team_a'::uuid), 2::bigint, 'A1 sees both sessions of A (control)');
select is((select count(*) from brigade.messages where team_id = :'team_a'::uuid), 2::bigint, 'A1 sees both messages of A: sent and received (control)');
select is((select count(*) from brigade.teams where id = :'team_b'::uuid), 0::bigint, 'isolation: A1 sees zero rows of team B in teams');
select is((select count(*) from brigade.memberships where team_id = :'team_b'::uuid), 0::bigint, 'isolation: A1 sees zero rows of team B in memberships');
select is((select count(*) from brigade.sessions where team_id = :'team_b'::uuid), 0::bigint, 'isolation: A1 sees zero rows of team B in sessions');
select is((select count(*) from brigade.messages where team_id = :'team_b'::uuid), 0::bigint, 'isolation: A1 sees zero rows of team B in messages');
select is((select count(*) from brigade.sessions where id in (:'s_b1'::uuid, :'s_b2'::uuid)), 0::bigint, 'isolation: B''s sessions are invisible to A1 by id');
select is((select count(*) from brigade.messages where id = :'m_b'::uuid), 0::bigint, 'isolation: B''s message is invisible to A1 by id');
select is((select count(*) from brigade.teams), 1::bigint, 'A1''s whole teams view is exactly one row (no other team leaks)');
select is((select count(*) from brigade.memberships), 2::bigint, 'A1''s whole memberships view is exactly team A''s two rows (D22)');
select throws_ok($$select * from brigade.join_attempts$$, '42501', 'permission denied for table join_attempts', 'a member cannot read join_attempts at all');
-- The messages policy is ownership AND membership: a member sees only messages it sent or that address its sessions.
select pg_temp.login(:'b1', true, 'Bea');
select is((select count(*) from brigade.messages where team_id = :'team_a'::uuid), 0::bigint, 'isolation: B1 sees zero rows of team A in messages');
select is((select count(*) from brigade.sessions where team_id = :'team_a'::uuid), 0::bigint, 'isolation: B1 sees zero rows of team A in sessions');
select pg_temp.logout();

-- 2. anon sees nothing and can call nothing (schema usage is not granted, T4).
set local role anon;
select throws_ok($$select id from brigade.teams$$, '42501', 'permission denied for schema brigade', 'anon: teams unreachable');
select throws_ok($$select * from brigade.messages$$, '42501', 'permission denied for schema brigade', 'anon: messages unreachable');
select throws_ok($$select brigade.my_team_ids()$$, '42501', 'permission denied for schema brigade', 'anon: my_team_ids unreachable');
select throws_ok($$select brigade.list_sessions(gen_random_uuid())$$, '42501', 'permission denied for schema brigade', 'anon: list_sessions unreachable');
reset role;

-- 3. service_role gets 42501 on every table (BYPASSRLS skips policies, not grants).
set local role service_role;
select throws_ok($$select * from brigade.teams$$, '42501', 'permission denied for table teams', 'service_role: 42501 on teams');
select throws_ok($$select * from brigade.memberships$$, '42501', 'permission denied for table memberships', 'service_role: 42501 on memberships');
select throws_ok($$select * from brigade.sessions$$, '42501', 'permission denied for table sessions', 'service_role: 42501 on sessions');
select throws_ok($$select * from brigade.messages$$, '42501', 'permission denied for table messages', 'service_role: 42501 on messages');
select throws_ok($$select * from brigade.join_attempts$$, '42501', 'permission denied for table join_attempts', 'service_role: 42501 on join_attempts');
reset role;

-- 4. Every brigade table has RLS and every policy is `to authenticated`; no grant to anon (hygiene.sql pins the detail).
select is((select count(*) from pg_class c where c.relnamespace = 'brigade'::regnamespace and c.relkind = 'r' and not c.relrowsecurity), 0::bigint, 'every brigade table has RLS');
select is((select count(*) from pg_policy p join pg_class c on c.oid = p.polrelid where c.relnamespace = 'brigade'::regnamespace and p.polroles <> array['authenticated'::regrole]::oid[]), 0::bigint, 'every brigade policy is to authenticated');
select is((select count(*) from information_schema.role_table_grants where table_schema = 'brigade' and grantee = 'anon'), 0::bigint, 'no table grant to anon');

-- 5. list_members: every active member with last_seen_at and session_count, to ANY active member (A2 is the joiner,
--    not the creator); unauthorized byte-identical for a non-member against team A and against a random uuid.
--    The roster rule (4.4.10, C-43): last_seen_at is the max over the member's sessions in ANY state; session_count
--    counts only sessions that are not offline. A1 gets a CLOSED second session seen most recently, A2 an EXPIRED one
--    seen more recently than its live session (backdated as postgres); both extras are removed after the block.
select pg_temp.login(:'a1', true, 'Alice');
select (brigade.register_session(:'team_a'::uuid, 'a1-closed'))->>'session_id' as s_a1_closed \gset
select lives_ok($$select brigade.close_session('$$ || :'s_a1_closed' || $$'::uuid)$$, 'fixture: A1''s second session is closed (its last_seen_at stays now(), the most recent of A1''s)');
select pg_temp.login(:'a2', true, 'Bob');
select (brigade.register_session(:'team_a'::uuid, 'a2-expired', null, 'idle', null, null, null, null, 30))->>'session_id' as s_a2_expired \gset
select pg_temp.logout();
update brigade.sessions set last_seen_at = now() - interval '10 seconds' where id = :'s_a1'::uuid;           -- lease 90: live
update brigade.sessions set last_seen_at = now() - interval '50 seconds' where id = :'s_a2'::uuid;           -- lease 90: live
update brigade.sessions set last_seen_at = now() - interval '40 seconds' where id = :'s_a2_expired'::uuid;   -- lease 30: expired 10 s ago, yet A2's most recently seen
select pg_temp.login(:'a2', true, 'Bob');
select r as roster_a from brigade.list_members(:'team_a'::uuid) as r \gset
select is(jsonb_array_length((:'roster_a'::jsonb)->'members'), 2, 'list_members: A2 (joiner) gets both active members of A');
select is((select array_agg((m->>'principal_ref')::uuid order by m->>'principal_ref') from jsonb_array_elements((:'roster_a'::jsonb)->'members') m),
          (select array_agg(u order by u) from unnest(array[:'a1'::uuid, :'a2'::uuid]) u), 'list_members: the members are exactly A1 and A2');
select is((select count(*) from jsonb_array_elements((:'roster_a'::jsonb)->'members') m
            where m->>'status' = 'active' and m->>'last_seen_at' is not null and (m->>'session_count')::int = 1 and m->>'joined_at' is not null),
          2::bigint, 'list_members: each member carries status active, a non-null last_seen_at, session_count 1 and joined_at');
select is((select (m->>'last_seen_at')::timestamptz from jsonb_array_elements((:'roster_a'::jsonb)->'members') m where m->>'principal_ref' = :'a1'), now(), 'list_members: A1''s last_seen_at is its CLOSED session''s (the max over sessions in ANY state, 4.4.10)');
select is((select (m->>'last_seen_at')::timestamptz from jsonb_array_elements((:'roster_a'::jsonb)->'members') m where m->>'principal_ref' = :'a2'), now() - interval '40 seconds', 'list_members: A2''s last_seen_at is its EXPIRED session''s (more recent than its live one)');
select is((select (m->>'session_count')::int from jsonb_array_elements((:'roster_a'::jsonb)->'members') m where m->>'principal_ref' = :'a1'), 1, 'list_members: A1''s session_count excludes the CLOSED session (1 live of 2)');
select is((select (m->>'session_count')::int from jsonb_array_elements((:'roster_a'::jsonb)->'members') m where m->>'principal_ref' = :'a2'), 1, 'list_members: A2''s session_count excludes the EXPIRED session (1 live of 2)');
select is((select m->>'human_label' from jsonb_array_elements((:'roster_a'::jsonb)->'members') m where m->>'principal_ref' = :'a1'), 'Alice', 'list_members: human_label comes from the membership row');
select is((:'roster_a'::jsonb)->>'team_name', 'Team A', 'list_members: team_name');
select is((:'roster_a'::jsonb)->>'team_ref', :'team_a', 'list_members: team_ref');
select is((select count(*) from jsonb_array_elements((:'roster_a'::jsonb)->'members') m where m->>'principal_ref' = :'b1'), 0::bigint, 'list_members: never a member of another team');
select pg_temp.logout();
delete from brigade.sessions where id in (:'s_a1_closed'::uuid, :'s_a2_expired'::uuid);   -- fixture cleanup as postgres: the counts below stay as before
update brigade.sessions set last_seen_at = now() where id in (:'s_a1'::uuid, :'s_a2'::uuid);
select pg_temp.login(:'b1', true, 'Bea');
select lives_ok($$select brigade.list_members('$$ || :'team_b' || $$'::uuid)$$, 'positive control: B1 may list its own team');
-- fetch_inbox / ack_messages: another team's session id and a random uuid answer byte-identical not_found (positive control first).
select lives_ok($$select brigade.fetch_inbox('$$ || :'s_b1' || $$'::uuid)$$, 'positive control: B1 drains its own session');
select is(pg_temp.err($$select brigade.fetch_inbox('$$ || :'s_a1' || $$'::uuid)$$), pg_temp.err($$select brigade.fetch_inbox(gen_random_uuid())$$), 'fetch_inbox: byte-identical text for another team''s session id and a random uuid');
select is(pg_temp.err($$select brigade.fetch_inbox(gen_random_uuid())$$), 'PT404 brigade:not_found', 'fetch_inbox: that text is brigade:not_found (PT404)');
select is(pg_temp.err($$select brigade.ack_messages('$$ || :'s_a1' || $$'::uuid, array['$$ || :'m_a2_a1' || $$'::uuid])$$), pg_temp.err($$select brigade.ack_messages(gen_random_uuid(), array['$$ || :'m_a2_a1' || $$'::uuid])$$), 'ack_messages: byte-identical text for another team''s session id and a random uuid');
select is(pg_temp.err($$select brigade.list_members('$$ || :'team_a' || $$'::uuid)$$), '42501 brigade:unauthorized', 'list_members: non-member against team A is brigade:unauthorized');
select is(pg_temp.err($$select brigade.list_members('$$ || :'team_a' || $$'::uuid)$$), pg_temp.err($$select brigade.list_members(gen_random_uuid())$$),
          'list_members: byte-identical unauthorized for a foreign team id and a random uuid (no team-existence oracle)');
select isnt(pg_temp.err($$select brigade.list_members(gen_random_uuid())$$), pg_temp.err($$select brigade.fetch_inbox(gen_random_uuid())$$),
            'control: unauthorized and not_found are different texts, so the identity above is not vacuous');
select pg_temp.login(:'n1');
select is(pg_temp.err($$select brigade.list_members('$$ || :'team_a' || $$'::uuid)$$), '42501 brigade:unauthorized', 'list_members: a member of nothing is unauthorized too');
select pg_temp.logout();

-- 6. Revocation by direct update (membership only; the session stays OPEN, so only the membership predicate changes).
--    Pre-revocation baseline first: A2 saw its rows and could use every verb seconds before.
select pg_temp.login(:'a2', true, 'Bob');
select is((select count(*) from brigade.messages where recipient_session_id = :'s_a2'::uuid), 1::bigint, 'baseline: A2 sees the message addressed to its session');
select is((select count(*) from brigade.messages), 2::bigint, 'baseline: A2 sees two messages (sent and received)');
select is((select count(*) from brigade.sessions where team_id = :'team_a'::uuid), 2::bigint, 'baseline: A2 sees both sessions of A');
select is(jsonb_array_length(brigade.fetch_inbox(:'s_a2'::uuid)), 1, 'baseline: fetch_inbox works for A2 and holds one message');
select ok(brigade.owns_session_topic('brigade:session:' || :'s_a2'), 'baseline: owns_session_topic is true for A2''s own open session');
select lives_ok($$select brigade.list_sessions('$$ || :'team_a' || $$'::uuid)$$, 'baseline: list_sessions works for A2');
select lives_ok($$select brigade.session_heartbeat('$$ || :'s_a2' || $$'::uuid)$$, 'baseline: heartbeat works for A2');
select pg_temp.login(:'a1', true, 'Alice');
select is((select count(*) from brigade.sessions where id = :'s_a2'::uuid), 1::bigint, 'baseline: A1 sees A2''s session');
select pg_temp.logout();
update brigade.memberships set status = 'revoked', revoked_at = now() where team_id = :'team_a'::uuid and user_id = :'a2'::uuid;
select is((select count(*) from brigade.sessions where id = :'s_a2'::uuid and closed_at is null), 1::bigint, 'fixture: A2''s session is still open after the direct revoke');
select pg_temp.login(:'a2', true, 'Bob');
select is((select count(*) from brigade.messages), 0::bigint, 'revoked: zero rows from messages by direct select');
select is((select count(*) from brigade.messages where recipient_session_id = :'s_a2'::uuid), 0::bigint, 'revoked: even messages addressed to its own session are gone');
select is((select count(*) from brigade.sessions), 0::bigint, 'revoked: zero rows from sessions by direct select (its own included)');
select is((select count(*) from brigade.memberships), 0::bigint, 'revoked: zero rows from memberships');
select is((select count(*) from brigade.teams), 0::bigint, 'revoked: zero rows from teams');
select is(pg_temp.err($$select brigade.fetch_inbox('$$ || :'s_a2' || $$'::uuid)$$), '42501 brigade:unauthorized', 'revoked: fetch_inbox on its own open session is unauthorized (C-08: the watch is told at its next drain)');
select is(pg_temp.err($$select brigade.ack_messages('$$ || :'s_a2' || $$'::uuid, array['$$ || :'m_a1_a2' || $$'::uuid])$$), '42501 brigade:unauthorized', 'revoked: ack_messages is unauthorized');
select is(pg_temp.err($$select brigade.close_session('$$ || :'s_a2' || $$'::uuid)$$), '42501 brigade:unauthorized', 'revoked: close_session is unauthorized');
select is(pg_temp.err($$select brigade.register_session('$$ || :'team_a' || $$'::uuid, 'again')$$), '42501 brigade:unauthorized', 'revoked: register_session is unauthorized');
select is(pg_temp.err($$select brigade.list_sessions('$$ || :'team_a' || $$'::uuid)$$), '42501 brigade:unauthorized', 'revoked: list_sessions is unauthorized');
select is(pg_temp.err($$select brigade.list_members('$$ || :'team_a' || $$'::uuid)$$), '42501 brigade:unauthorized', 'revoked: list_members is unauthorized');
select is(pg_temp.err($$select brigade.session_heartbeat('$$ || :'s_a2' || $$'::uuid)$$), '42501 brigade:unauthorized', 'revoked: heartbeat on its OPEN session is unauthorized, not conflict (membership is checked before the closed-session state, 4.5.7)');
select is(pg_temp.err($$select brigade.send_message('$$ || :'s_a2' || $$'::uuid, '$$ || :'s_a1' || $$'::uuid, 'x', 'krev')$$), '42501 brigade:unauthorized', 'revoked: send from its OPEN session is unauthorized, not conflict');
select ok(not brigade.owns_session_topic('brigade:session:' || :'s_a2'), 'revoked: owns_session_topic is false for its own open session (E0-2 h-sql)');
select pg_temp.login(:'a1', true, 'Alice');
select is((select count(*) from brigade.sessions where id = :'s_a2'::uuid), 0::bigint, 'revoked: other members no longer see the revoked member''s session by direct select');
select is((select count(*) from jsonb_array_elements(brigade.list_sessions(:'team_a'::uuid, true)->'sessions') s where s->>'session_id' = :'s_a2'), 0::bigint, 'revoked: list_sessions hides the revoked member''s session even with include_offline');
select is(jsonb_array_length(brigade.list_members(:'team_a'::uuid)->'members'), 1, 'revoked: list_members lists only the remaining active member');
select is(pg_temp.err($$select brigade.send_message('$$ || :'s_a1' || $$'::uuid, '$$ || :'s_a2' || $$'::uuid, 'x', 'krev2')$$), pg_temp.err($$select brigade.send_message('$$ || :'s_a1' || $$'::uuid, gen_random_uuid(), 'x', 'krev3')$$),
          'revoked: a send to the revoked member''s session is byte-identical to a send to a random uuid (C-08)');
select is(pg_temp.err($$select brigade.send_message('$$ || :'s_a1' || $$'::uuid, gen_random_uuid(), 'x', 'krev3')$$), 'PT404 brigade:not_found', 'revoked: that shared text is brigade:not_found under PT404');
select pg_temp.logout();
-- Restore A2 (as postgres) so the leave_team flow below starts from an active membership, and prove the restore.
update brigade.memberships set status = 'active', revoked_at = null where team_id = :'team_a'::uuid and user_id = :'a2'::uuid;
select pg_temp.login(:'a2', true, 'Bob');
select is(jsonb_array_length(brigade.fetch_inbox(:'s_a2'::uuid)), 1, 'restored: fetch_inbox works again and the parked message survived the revocation');

-- 7. leave_team: revokes the caller's row, closes its open sessions, is idempotent; rejoin re-activates.
select is((brigade.leave_team(:'team_a'::uuid))->>'left', 'true', 'leave_team: left = true');
select is(pg_temp.err($$select brigade.fetch_inbox('$$ || :'s_a2' || $$'::uuid)$$), '42501 brigade:unauthorized', 'after leave_team: fetch_inbox is unauthorized');
select is(pg_temp.err($$select brigade.session_heartbeat('$$ || :'s_a2' || $$'::uuid)$$), '42501 brigade:unauthorized', 'after leave_team: heartbeat on the now CLOSED session is unauthorized, not conflict:session_closed (membership is checked before the closed state, 4.5.7)');
select is(pg_temp.err($$select brigade.send_message('$$ || :'s_a2' || $$'::uuid, '$$ || :'s_a1' || $$'::uuid, 'x', 'kleft')$$), '42501 brigade:unauthorized', 'after leave_team: send from the now CLOSED session is unauthorized, not conflict:sender_closed');
select is((brigade.leave_team(:'team_a'::uuid))->>'left', 'true', 'leave_team: a second call still answers left = true (idempotent)');
select is((brigade.leave_team(gen_random_uuid()))->>'left', 'true', 'leave_team: an unknown team answers left = true too (nothing to disclose)');
select pg_temp.logout();
select is((select status from brigade.memberships where team_id = :'team_a'::uuid and user_id = :'a2'::uuid), 'revoked', 'after leave_team (as postgres): the caller''s own row is revoked');
select ok((select revoked_at is not null from brigade.memberships where team_id = :'team_a'::uuid and user_id = :'a2'::uuid), 'after leave_team: revoked_at is set');
select ok((select closed_at is not null from brigade.sessions where id = :'s_a2'::uuid), 'after leave_team: the caller''s open session in the team is closed');
select ok((select closed_at is null from brigade.sessions where id = :'s_a1'::uuid), 'after leave_team: another member''s session is untouched');
select is((select owner_id from brigade.sessions where id = :'s_a2'::uuid), :'a2'::uuid, 'after leave_team: the closed session keeps its owner');
select pg_temp.login(:'a1', true, 'Alice');
select is(jsonb_array_length(brigade.list_members(:'team_a'::uuid)->'members'), 1, 'after leave_team: A1''s roster no longer lists A2');
select pg_temp.login(:'a2', true, 'Bob');
select r as rejoin from brigade.join_team(:'secret_a') as r \gset
select is((:'rejoin'::jsonb)->>'status', 'joined', 'rejoin: join_team with the secret succeeds');
select is((:'rejoin'::jsonb)->>'rejoined', 'true', 'rejoin: rejoined = true');
select is((:'rejoin'::jsonb)->>'team_id', :'team_a', 'rejoin: the same team');
select is(jsonb_array_length(brigade.fetch_inbox(:'s_a2'::uuid)), 1, 'rejoin: the same principal owns the same session again and its parked message is still there');
select is((select human_label from brigade.memberships where team_id = :'team_a'::uuid and user_id = :'a2'::uuid), 'Bob', 'rejoin without a label keeps the stored human_label');
select pg_temp.login(:'a1', true, 'Alice');
select is(jsonb_array_length(brigade.list_members(:'team_a'::uuid)->'members'), 2, 'rejoin: A2 reappears in every other member''s list_members');
select is((select m->>'status' from jsonb_array_elements(brigade.list_members(:'team_a'::uuid)->'members') m where m->>'principal_ref' = :'a2'), 'active', 'rejoin: A2 is listed active');
select pg_temp.logout();

-- 8. Within-team message privacy (I-08): the messages policy is ownership AND membership. A third active member C
--    sees the team's sessions (control) but neither of the two messages exchanged between A1 and A2; a message sent
--    to C's session is then visible to C (positive control) and still invisible to A2.
select pg_temp.new_user() as c1 \gset
select pg_temp.login(:'c1', true, 'Cy');
select is((brigade.join_team(:'secret_a', 'Cy'))->>'status', 'joined', 'fixture: C joins team A');
select (brigade.register_session(:'team_a'::uuid, 'c1-main'))->>'session_id' as s_c1 \gset
select is((select count(*) from brigade.sessions where team_id = :'team_a'::uuid), 3::bigint, 'control: C sees the three sessions of A (it is an active member)');
select is((select count(*) from brigade.messages), 0::bigint, 'privacy: C sees NONE of the messages exchanged between A1 and A2 (a member reads only what it sent or received)');
select pg_temp.login(:'a1', true, 'Alice');
select (brigade.send_message(:'s_a1'::uuid, :'s_c1'::uuid, 'a1 to c1', 'kac'))->>'message_id' as m_a1_c1 \gset
select pg_temp.login(:'c1', true, 'Cy');
select is((select array_agg(id) from brigade.messages), array[:'m_a1_c1'::uuid], 'privacy: C sees exactly the one message addressed to its session (positive control)');
select pg_temp.login(:'a2', true, 'Bob');
select is((select count(*) from brigade.messages where id = :'m_a1_c1'::uuid), 0::bigint, 'privacy: A2 does not see the message from A1 to C');
select is((select count(*) from brigade.messages), 2::bigint, 'privacy: A2 still sees exactly its own two (sent and received)');
select pg_temp.logout();

select * from finish();
rollback;
