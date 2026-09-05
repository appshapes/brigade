-- team_admin.sql (plan 5.10, P5-2; I-16 table/RPC halves re-run through revoke_membership, I-20, I-21, the pgTAP
-- `transfer_team` row of 08-phases.md:126): the creator gate answers ONE text, '42501 brigade:unauthorized', for a
-- non-creator member, a member of nothing and a creator of another team, byte-identical against a random team id
-- (no team-existence and no creator-identity oracle), with a non-vacuity control; rotate_join_secret bumps the
-- version, changes the hash, answers a create_team-shaped secret once, the old secret fails and the new works,
-- existing members are untouched and a refusal changes nothing; revoke_membership closes the target's open sessions
-- after writing one ids-only membership_revoked broadcast per open session, hides every row and refuses every verb
-- to the revoked member, is a no-op for a stranger (never an insert), is idempotent, refuses the self-target, and
-- the member rejoins with the current secret; `banned` is the oracle three ways — the banned member's join_team
-- answer is byte-identical to a wrong secret and to an unknown team, the attempt is recorded, and the un-ban is
-- revoke_membership(…, false); revoke_memberships_by_version revokes every active member at or below the version
-- EXCEPT the caller, never a banned row, closes and broadcasts, and the evicted rejoin at the new version;
-- transfer_team moves created_by to an active member only (one text for a non-member and a random uuid), the old
-- creator gets the uniform unauthorized from all four RPCs and stays an ordinary active member, the new creator
-- succeeds at all four, and a self-transfer is an accepted no-op. Every assertion is named after the rule it checks
-- so a mutation of 20260905120000_brigade_team_admin.sql maps to a line. Broadcast counts are scoped to this file's
-- own topics, never global: on a shared stack a live adapter may write realtime.messages rows of its own.
begin;
\ir helpers/auth.sql
select plan(257);

-- Local helper: run a statement as the CURRENT role and return '<sqlstate> <message>' (null when it succeeds), so
-- two error texts can be compared byte for byte (the rls_isolation.sql helper).
create function pg_temp.err(q text) returns text language plpgsql as $$
begin
  execute q;
  return null;
exception when others then
  return sqlstate || ' ' || sqlerrm;
end $$;

-- Local helper: the four admin RPCs against a team id, each with a harmless argument (a random target, version 0),
-- so the gate is what decides and a call that passes changes nothing observable.
create function pg_temp.admin_calls(team text) returns text[] language sql as $$
  select array[
    'select brigade.rotate_join_secret(''' || team || '''::uuid)',
    'select brigade.revoke_membership(''' || team || '''::uuid, gen_random_uuid())',
    'select brigade.revoke_memberships_by_version(''' || team || '''::uuid, 0)',
    'select brigade.transfer_team(''' || team || '''::uuid, gen_random_uuid())'
  ] $$;

-- Fixtures through the RPCs: team T (C1 creator; M1, M2, V1, V2, V3 join with the v1 secret), M3 a member of nothing,
-- X1 the creator of team U. C1's session and the messages are registered after section 1's leave/rejoin arm, which
-- closes the caller's sessions; every revocation section below therefore has an open-session, message-flowing
-- baseline of its own.
select pg_temp.new_user() as c1 \gset
select pg_temp.new_user() as m1 \gset
select pg_temp.new_user() as m2 \gset
select pg_temp.new_user() as m3 \gset
select pg_temp.new_user() as x1 \gset
select pg_temp.new_user() as v1 \gset
select pg_temp.new_user() as v2 \gset
select pg_temp.new_user() as v3 \gset
select pg_temp.login(:'c1', true, 'Cleo');
select r->>'team_id' as team_t, r->>'join_secret' as secret_v1 from brigade.create_team('Team T', 'Cleo') as r \gset
select pg_temp.login(:'m1', true, 'Mia');
select is((brigade.join_team(:'secret_v1', 'Mia'))->>'status', 'joined', 'fixture: M1 joins team T with the v1 secret');
select (brigade.register_session(:'team_t'::uuid, 'm1-a'))->>'session_id' as s_m1a \gset
select (brigade.register_session(:'team_t'::uuid, 'm1-b'))->>'session_id' as s_m1b \gset
select (brigade.register_session(:'team_t'::uuid, 'm1-closed'))->>'session_id' as s_m1c \gset
select lives_ok($$select brigade.close_session('$$ || :'s_m1c' || $$'::uuid)$$, 'fixture: M1 closes its third session before anything else happens');
select pg_temp.login(:'m2', true, 'Max');
select is((brigade.join_team(:'secret_v1', 'Max'))->>'status', 'joined', 'fixture: M2 joins team T with the v1 secret');
select (brigade.register_session(:'team_t'::uuid, 'm2-main'))->>'session_id' as s_m2 \gset
select pg_temp.login(:'v1', true, 'Vic');
select is((brigade.join_team(:'secret_v1', 'Vic'))->>'status', 'joined', 'fixture: V1 joins team T with the v1 secret');
select (brigade.register_session(:'team_t'::uuid, 'v1-main'))->>'session_id' as s_v1 \gset
select pg_temp.login(:'v2', true, 'Val');
select is((brigade.join_team(:'secret_v1', 'Val'))->>'status', 'joined', 'fixture: V2 joins team T with the v1 secret');
select pg_temp.login(:'v3', true, 'Vin');
select is((brigade.join_team(:'secret_v1', 'Vin'))->>'status', 'joined', 'fixture: V3 joins team T with the v1 secret');
select (brigade.register_session(:'team_t'::uuid, 'v3-a'))->>'session_id' as s_v3a \gset
select (brigade.register_session(:'team_t'::uuid, 'v3-closed'))->>'session_id' as s_v3c \gset
select lives_ok($$select brigade.close_session('$$ || :'s_v3c' || $$'::uuid)$$, 'fixture: V3 closes its second session');
select pg_temp.login(:'x1', true, 'Xena');
select r->>'team_id' as team_u, r->>'join_secret' as secret_u from brigade.create_team('Team U', 'Xena') as r \gset
-- Cross-team fixture (the P5-2 verifier's mutants 24/26/28/31/32): C1, M2 and V1 are ALSO active members of team U,
-- M2 and V1 with an open session there, so an administrative act on team T that reaches into another team — a gate
-- that accepts an active membership of ANY team, a close or a broadcast over the target's sessions in EVERY team —
-- is visible. Every count below is scoped to team T, so these rows change no other assertion.
select pg_temp.login(:'c1', true, 'Cleo');
select is((brigade.join_team(:'secret_u', 'Cleo'))->>'status', 'joined', 'fixture: C1 also joins team U, as an ordinary member there');
select pg_temp.login(:'m2', true, 'Max');
select is((brigade.join_team(:'secret_u', 'Max'))->>'status', 'joined', 'fixture: M2 also joins team U');
select (brigade.register_session(:'team_u'::uuid, 'm2-u'))->>'session_id' as s_m2u \gset
select pg_temp.login(:'v1', true, 'Vic');
select is((brigade.join_team(:'secret_u', 'Vic'))->>'status', 'joined', 'fixture: V1 also joins team U');
select (brigade.register_session(:'team_u'::uuid, 'v1-u'))->>'session_id' as s_v1u \gset
select pg_temp.logout();
select isnt(:'team_t'::uuid, :'team_u'::uuid, 'fixture: two distinct teams');
select is((select created_by from brigade.teams where id = :'team_t'::uuid), :'c1'::uuid, 'fixture (as postgres): C1 is created_by of team T');
select is((select secret_version from brigade.teams where id = :'team_t'::uuid), 1, 'fixture (as postgres): team T is at secret_version 1');
select secret_hash as hash_v1 from brigade.teams where id = :'team_t'::uuid \gset
select 'brigade:session:' || :'s_m1a' as topic_m1a \gset
select 'brigade:session:' || :'s_m1b' as topic_m1b \gset
select 'brigade:session:' || :'s_m1c' as topic_m1c \gset
select 'brigade:session:' || :'s_m2' as topic_m2 \gset
select 'brigade:session:' || :'s_v1' as topic_v1 \gset
select 'brigade:session:' || :'s_v3a' as topic_v3a \gset
select 'brigade:session:' || :'s_v3c' as topic_v3c \gset

-- 1. The creator gate: one text, four causes. Positive controls first (the harmless arguments of pg_temp.admin_calls),
--    then the three non-creators against team T and against a random uuid, byte for byte, with the non-vacuity
--    control; a refused rotate leaves secret_version alone; and the creator that left is refused until it rejoins.
select pg_temp.login(:'c1', true, 'Cleo');
select is((brigade.revoke_membership(:'team_t'::uuid, gen_random_uuid()))->>'changed', 'false', 'gate positive control: the creator passes revoke_membership (a stranger target is a no-op)');
select is((brigade.revoke_memberships_by_version(:'team_t'::uuid, 0))->>'revoked', '0', 'gate positive control: the creator passes revoke_memberships_by_version (version 0 revokes nobody)');
select is((brigade.transfer_team(:'team_t'::uuid, :'c1'::uuid))->>'transferred', 'true', 'gate positive control: the creator passes transfer_team (a self-transfer is an accepted no-op)');
select pg_temp.logout();
select is((select created_by from brigade.teams where id = :'team_t'::uuid), :'c1'::uuid, 'self-transfer (as postgres): created_by is unchanged');
select pg_temp.login(:'m1', true, 'Mia');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'gate: M1 (an active member, not the creator) is brigade:unauthorized: ' || q)
  from unnest(pg_temp.admin_calls(:'team_t')) q;
select is(pg_temp.err(u.q), pg_temp.err(u.r), 'gate: M1: byte-identical against a random team id (no creator-identity, no team-existence oracle): ' || u.q)
  from unnest(pg_temp.admin_calls(:'team_t'), pg_temp.admin_calls(gen_random_uuid()::text)) as u(q, r);
select is((select secret_version from brigade.teams where id = :'team_t'::uuid), 1, 'gate: a refused rotate did not change secret_version') ;
select pg_temp.login(:'m3');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'gate: M3 (a member of nothing) is brigade:unauthorized: ' || q)
  from unnest(pg_temp.admin_calls(:'team_t')) q;
select is(pg_temp.err(u.q), pg_temp.err(u.r), 'gate: M3: byte-identical against a random team id: ' || u.q)
  from unnest(pg_temp.admin_calls(:'team_t'), pg_temp.admin_calls(gen_random_uuid()::text)) as u(q, r);
select pg_temp.login(:'x1', true, 'Xena');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'gate: X1 (the creator of ANOTHER team) is brigade:unauthorized: ' || q)
  from unnest(pg_temp.admin_calls(:'team_t')) q;
select is(pg_temp.err(u.q), pg_temp.err(u.r), 'gate: X1: byte-identical against a random team id: ' || u.q)
  from unnest(pg_temp.admin_calls(:'team_t'), pg_temp.admin_calls(gen_random_uuid()::text)) as u(q, r);
select lives_ok($$select brigade.revoke_memberships_by_version('$$ || :'team_u' || $$'::uuid, 0)$$, 'gate control: X1 administers its OWN team');
select isnt(pg_temp.err($$select brigade.rotate_join_secret(gen_random_uuid())$$), pg_temp.err($$select brigade.fetch_inbox(gen_random_uuid())$$),
            'control: unauthorized and not_found are different texts, so the identities above are not vacuous');
select pg_temp.logout();
select is((select secret_version from brigade.teams where id = :'team_t'::uuid), 1, 'gate (as postgres): no refused call changed secret_version');
select is((select created_by from brigade.teams where id = :'team_t'::uuid), :'c1'::uuid, 'gate (as postgres): no refused call changed created_by');
-- The creator that ran team leave: created_by survives (schema :279-280) but the gate needs an active membership OF
-- THIS TEAM — C1 is still an active member of team U (the cross-team fixture), and that must not be enough.
select pg_temp.login(:'c1', true, 'Cleo');
select is((brigade.leave_team(:'team_t'::uuid))->>'left', 'true', 'creator leaves: leave_team answers left = true');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'creator that left: brigade:unauthorized (created_by alone is not enough): ' || q)
  from unnest(pg_temp.admin_calls(:'team_t')) q;
select is(pg_temp.err(u.q), pg_temp.err(u.r), 'creator that left: byte-identical against a random team id: ' || u.q)
  from unnest(pg_temp.admin_calls(:'team_t'), pg_temp.admin_calls(gen_random_uuid()::text)) as u(q, r);
select pg_temp.logout();
select is((select created_by from brigade.teams where id = :'team_t'::uuid), :'c1'::uuid, 'creator that left (as postgres): created_by survived the leave');
select pg_temp.login(:'c1', true, 'Cleo');
select r as c1_rejoin from brigade.join_team(:'secret_v1') as r \gset
select is((:'c1_rejoin'::jsonb)->>'status', 'joined', 'creator rejoins with the current secret: joined');
select is((:'c1_rejoin'::jsonb)->>'rejoined', 'true', 'creator rejoins: rejoined = true');
select is((brigade.revoke_memberships_by_version(:'team_t'::uuid, 0))->>'revoked', '0', 'creator rejoined: administration is restored');
select is((select joined_secret_version from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'c1'::uuid), 1, 'creator rejoined at the current version, still 1');
-- The rest of the fixture: C1's session (the leave closed none, it had none) and one message each way.
select (brigade.register_session(:'team_t'::uuid, 'c1-main'))->>'session_id' as s_c1 \gset
select (brigade.send_message(:'s_c1'::uuid, :'s_m1a'::uuid, 'c1 to m1', 'k1'))->>'message_id' as m_c1_m1 \gset
select pg_temp.login(:'m1', true, 'Mia');
select (brigade.send_message(:'s_m1a'::uuid, :'s_c1'::uuid, 'm1 to c1', 'k2'))->>'message_id' as m_m1_c1 \gset
select pg_temp.logout();
select 'brigade:session:' || :'s_c1' as topic_c1 \gset

-- 2. Rotation (I-21's first half): version 1 → 2, hash changed, rotated_at set, the secret in the create_team shape
--    naming the team; the old secret fails and the new works for a fresh principal (P1); existing members untouched;
--    a second rotation (2 → 3) kills the first new secret (P2 fails with it, then joins with the newest).
select pg_temp.login(:'c1', true, 'Cleo');
select r as rot1 from brigade.rotate_join_secret(:'team_t'::uuid) as r \gset
select is((:'rot1'::jsonb)->>'status', 'rotated', 'rotate: status rotated');
select is((:'rot1'::jsonb)->>'team_id', :'team_t', 'rotate: team_id');
select is((:'rot1'::jsonb)->>'team_name', 'Team T', 'rotate: team_name');
select is(((:'rot1'::jsonb)->>'secret_version')::int, 2, 'rotate: secret_version 1 → 2 in the answer');
select is((select secret_version from brigade.teams where id = :'team_t'::uuid), 2, 'rotate (as the creator, teams is readable): secret_version 2 in the table');
select ok((:'rot1'::jsonb)->>'secret_rotated_at' is not null, 'rotate: secret_rotated_at is set in the answer');
select (:'rot1'::jsonb)->>'join_secret' as secret_v2 \gset
select matches(:'secret_v2'::text, '^brg1\.' || :'team_t' || '\.[0-9a-f]{32}$', 'rotate: the new secret is brg1.<team_id>.<32 hex>, the create_team shape naming this team');
select isnt(:'secret_v2'::text, :'secret_v1', 'rotate: the new secret differs from the old');
select pg_temp.logout();
select ok((select secret_rotated_at = now() from brigade.teams where id = :'team_t'::uuid), 'rotate (as postgres): secret_rotated_at = now()');
select isnt((select secret_hash from brigade.teams where id = :'team_t'::uuid), :'hash_v1', 'rotate (as postgres): secret_hash changed');
select pg_temp.new_user() as p1 \gset
select pg_temp.login(:'p1', true, 'Pia');
select is((brigade.join_team(:'secret_v1', 'Pia'))->>'status', 'invalid_secret', 'old secret fails: a fresh principal joining with the v1 secret is invalid_secret');
select r as p1_join from brigade.join_team(:'secret_v2', 'Pia') as r \gset
select is((:'p1_join'::jsonb)->>'status', 'joined', 'new secret works: the same principal joins with the v2 secret');
select is((:'p1_join'::jsonb)->>'rejoined', 'false', 'new secret works: a fresh membership, rejoined = false');
select pg_temp.logout();
select is((select joined_secret_version from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'p1'::uuid), 2, 'new secret works: joined_secret_version = 2');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m1'::uuid), 'active', 'existing members unaffected: M1 is still active after the rotation');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m2'::uuid), 'active', 'existing members unaffected: M2 is still active after the rotation');
select is((select joined_secret_version from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m1'::uuid), 1, 'existing members unaffected: M1 keeps joined_secret_version 1');
select is((select joined_secret_version from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m2'::uuid), 1, 'existing members unaffected: M2 keeps joined_secret_version 1');
select is((select count(*) from brigade.memberships where team_id = :'team_t'::uuid and status <> 'active'), 0::bigint, 'existing members unaffected: rotation revoked nobody');
select is((select count(*) from brigade.sessions where team_id = :'team_t'::uuid and closed_at is null), 6::bigint, 'existing members unaffected: rotation closed no session (six open)');
select pg_temp.login(:'m1', true, 'Mia');
select is(jsonb_array_length(brigade.fetch_inbox(:'s_m1a'::uuid)), 1, 'existing members unaffected: M1 still drains its inbox after the rotation');
select lives_ok($$select brigade.list_sessions('$$ || :'team_t' || $$'::uuid)$$, 'existing members unaffected: M1 still lists sessions');
select is(jsonb_array_length(brigade.list_members(:'team_t'::uuid)->'members'), 7, 'existing members unaffected: M1 still lists the roster (C1, M1, M2, V1, V2, V3, P1)');
select pg_temp.login(:'c1', true, 'Cleo');
select r as rot2 from brigade.rotate_join_secret(:'team_t'::uuid) as r \gset
select is(((:'rot2'::jsonb)->>'secret_version')::int, 3, 'rotate again: secret_version 2 → 3');
select (:'rot2'::jsonb)->>'join_secret' as secret_v3 \gset
select isnt(:'secret_v3'::text, :'secret_v2', 'rotate again: a third distinct secret');
select pg_temp.logout();
select pg_temp.new_user() as p2 \gset
select pg_temp.login(:'p2', true, 'Pol');
select is((brigade.join_team(:'secret_v2', 'Pol'))->>'status', 'invalid_secret', 'rotate again: the FIRST new secret now fails');
select r as p2_join from brigade.join_team(:'secret_v3', 'Pol') as r \gset
select is((:'p2_join'::jsonb)->>'status', 'joined', 'rotate again: the newest secret works');
select pg_temp.logout();
select is((select joined_secret_version from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'p2'::uuid), 3, 'rotate again: P2 joined at version 3');

-- 3. revoke_membership (I-16's table and RPC halves through the admin path). Baseline, then C1 revokes M1: two open
--    sessions closed, one membership_revoked row on each open session's topic and none on the closed one, every
--    verb and every table gone for M1, M1 gone from the roster, a send to M1's session byte-identical to a random
--    id; idempotent (changed = false, no further row) before the rejoin; a stranger target is a no-op that inserts
--    nothing; the self-target is refused; then M1 rejoins with the current secret.
select pg_temp.login(:'m1', true, 'Mia');
select is(jsonb_array_length(brigade.fetch_inbox(:'s_m1a'::uuid)), 1, 'baseline: M1 drains its inbox (one message from C1)');
select ok(brigade.owns_session_topic(:'topic_m1a'), 'baseline: owns_session_topic is true for M1''s open session');
select is((select count(*) from brigade.sessions where team_id = :'team_t'::uuid), 8::bigint, 'baseline: M1 sees the team''s eight sessions');
select pg_temp.login(:'c1', true, 'Cleo');
select is((select count(*) from jsonb_array_elements(brigade.list_members(:'team_t'::uuid)->'members') m where m->>'principal_ref' = :'m1'), 1::bigint, 'baseline: M1 is in C1''s roster');
select is((select count(*) from brigade.sessions where owner_id = :'m1'::uuid and closed_at is null), 2::bigint, 'baseline: M1 has two open sessions');
select pg_temp.logout();
select is((select count(*) from realtime.messages where topic in (:'topic_m1a', :'topic_m1b', :'topic_m1c') and event = 'membership_revoked'), 0::bigint, 'baseline: no membership_revoked row on M1''s topics');
select is((select count(*) from brigade.memberships where team_id = :'team_t'::uuid), 8::bigint, 'baseline (as postgres): eight membership rows in team T');
select pg_temp.login(:'c1', true, 'Cleo');
select r as rev_m1 from brigade.revoke_membership(:'team_t'::uuid, :'m1'::uuid) as r \gset
select is((:'rev_m1'::jsonb)->>'status', 'revoked', 'revoke_membership: status revoked');
select is((:'rev_m1'::jsonb)->>'changed', 'true', 'revoke_membership: changed = true for an active row');
select is(((:'rev_m1'::jsonb)->>'sessions_closed')::int, 2, 'revoke_membership: sessions_closed = M1''s open-session count (2)');
select is((:'rev_m1'::jsonb)->>'principal_ref', :'m1', 'revoke_membership: principal_ref names the target');
select is((:'rev_m1'::jsonb)->>'team_id', :'team_t', 'revoke_membership: team_id');
select is((select count(*) from jsonb_array_elements(brigade.list_members(:'team_t'::uuid)->'members') m where m->>'principal_ref' = :'m1'), 0::bigint, 'revoked: M1 disappears from C1''s list_members');
select is((select count(*) from brigade.sessions where id in (:'s_m1a'::uuid, :'s_m1b'::uuid)), 0::bigint, 'revoked: C1 no longer sees M1''s sessions by direct select');
select is(pg_temp.err($$select brigade.send_message('$$ || :'s_c1' || $$'::uuid, '$$ || :'s_m1a' || $$'::uuid, 'x', 'krev')$$), pg_temp.err($$select brigade.send_message('$$ || :'s_c1' || $$'::uuid, gen_random_uuid(), 'x', 'krev2')$$),
          'revoked: a send to M1''s session is byte-identical to a send to a random uuid (C-08)');
select is(pg_temp.err($$select brigade.send_message('$$ || :'s_c1' || $$'::uuid, gen_random_uuid(), 'x', 'krev2')$$), 'PT404 brigade:not_found', 'revoked: that shared text is brigade:not_found (PT404)');
select pg_temp.login(:'m1', true, 'Mia');
select is((select count(*) from brigade.teams), 0::bigint, 'revoked: zero rows from teams by direct select');
select is((select count(*) from brigade.memberships), 0::bigint, 'revoked: zero rows from memberships');
select is((select count(*) from brigade.sessions), 0::bigint, 'revoked: zero rows from sessions (its own included)');
select is((select count(*) from brigade.messages), 0::bigint, 'revoked: zero rows from messages (its own sent and received included)');
select is(pg_temp.err($$select brigade.fetch_inbox('$$ || :'s_m1a' || $$'::uuid)$$), '42501 brigade:unauthorized', 'revoked: fetch_inbox is unauthorized (the drain that ends a watch, C-08)');
select is(pg_temp.err($$select brigade.ack_messages('$$ || :'s_m1a' || $$'::uuid, array['$$ || :'m_c1_m1' || $$'::uuid])$$), '42501 brigade:unauthorized', 'revoked: ack_messages is unauthorized');
select is(pg_temp.err($$select brigade.close_session('$$ || :'s_m1a' || $$'::uuid)$$), '42501 brigade:unauthorized', 'revoked: close_session is unauthorized');
select is(pg_temp.err($$select brigade.register_session('$$ || :'team_t' || $$'::uuid, 'again')$$), '42501 brigade:unauthorized', 'revoked: register_session is unauthorized');
select is(pg_temp.err($$select brigade.list_sessions('$$ || :'team_t' || $$'::uuid)$$), '42501 brigade:unauthorized', 'revoked: list_sessions is unauthorized');
select is(pg_temp.err($$select brigade.list_members('$$ || :'team_t' || $$'::uuid)$$), '42501 brigade:unauthorized', 'revoked: list_members is unauthorized');
select is(pg_temp.err($$select brigade.session_heartbeat('$$ || :'s_m1a' || $$'::uuid)$$), '42501 brigade:unauthorized', 'revoked: heartbeat on its now-closed session is unauthorized, not conflict (membership before state, 4.5.7)');
select is(pg_temp.err($$select brigade.send_message('$$ || :'s_m1a' || $$'::uuid, '$$ || :'s_c1' || $$'::uuid, 'x', 'krev3')$$), '42501 brigade:unauthorized', 'revoked: send from its now-closed session is unauthorized, not conflict');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'revoked: the admin RPCs are unauthorized too: ' || q) from unnest(pg_temp.admin_calls(:'team_t')) q;
select ok(not brigade.owns_session_topic(:'topic_m1a'), 'revoked: owns_session_topic is false for M1''s former session');
select pg_temp.logout();
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m1'::uuid), 'revoked', 'revoked (as postgres): the row is revoked');
select ok((select revoked_at = now() from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m1'::uuid), 'revoked (as postgres): revoked_at = now()');
select is((select count(*) from brigade.sessions where id in (:'s_m1a'::uuid, :'s_m1b'::uuid) and closed_at is not null and activity = 'idle'), 2::bigint, 'revoked (as postgres): both open sessions are closed and idle');
select is((select owner_id from brigade.sessions where id = :'s_m1a'::uuid), :'m1'::uuid, 'revoked (as postgres): the closed session keeps its owner (the identity trigger allowed the close)');
select is((select count(*) from realtime.messages where topic = :'topic_m1a' and event = 'membership_revoked'), 1::bigint, 'hint: one membership_revoked row on the topic of the open session m1-a');
select is((select count(*) from realtime.messages where topic = :'topic_m1b' and event = 'membership_revoked'), 1::bigint, 'hint: one membership_revoked row on the topic of the open session m1-b');
select is((select count(*) from realtime.messages where topic = :'topic_m1c' and event = 'membership_revoked'), 0::bigint, 'hint: none on the topic of m1-closed, closed before the revoke');
select is((select array_agg(k order by k) from realtime.messages r, jsonb_object_keys(r.payload) k where r.topic = :'topic_m1a' and r.event = 'membership_revoked'), array['id', 'session_id'],
          'hint: the payload carries exactly session_id (plus the id realtime.send adds), ids only (T4)');
select is((select payload->>'session_id' from realtime.messages where topic = :'topic_m1a' and event = 'membership_revoked'), :'s_m1a', 'hint: session_id names the session whose topic carries the row');
select is((select extension from realtime.messages where topic = :'topic_m1b' and event = 'membership_revoked'), 'broadcast', 'hint: extension broadcast');
select is((select private from realtime.messages where topic = :'topic_m1b' and event = 'membership_revoked'), true, 'hint: the broadcast is private');
select pg_temp.login(:'c1', true, 'Cleo');
select r as rev_m1_again from brigade.revoke_membership(:'team_t'::uuid, :'m1'::uuid) as r \gset
select is((:'rev_m1_again'::jsonb)->>'changed', 'false', 'revoke_membership again: changed = false on an already-revoked row');
select is(((:'rev_m1_again'::jsonb)->>'sessions_closed')::int, 0, 'revoke_membership again: sessions_closed 0');
select pg_temp.logout();
select is((select count(*) from realtime.messages where topic in (:'topic_m1a', :'topic_m1b', :'topic_m1c') and event = 'membership_revoked'), 2::bigint, 'revoke_membership again: no further broadcast row');
select pg_temp.login(:'c1', true, 'Cleo');
select r as rev_none from brigade.revoke_membership(:'team_t'::uuid, gen_random_uuid()) as r \gset
select is((:'rev_none'::jsonb)->>'changed', 'false', 'stranger target: changed = false');
select is(((:'rev_none'::jsonb)->>'sessions_closed')::int, 0, 'stranger target: sessions_closed 0');
select is((:'rev_none'::jsonb)->>'status', 'revoked', 'stranger target: the answer still names the requested status');
select is(pg_temp.err($$select brigade.revoke_membership('$$ || :'team_t' || $$'::uuid, '$$ || :'c1' || $$'::uuid)$$), '22023 brigade:invalid_input:principal_ref', 'self-target: revoke_membership on the caller is invalid_input:principal_ref');
select is(pg_temp.err($$select brigade.revoke_membership('$$ || :'team_t' || $$'::uuid, '$$ || :'c1' || $$'::uuid, true)$$), '22023 brigade:invalid_input:principal_ref', 'self-target: a self-ban is refused with the same text');
select pg_temp.logout();
select is((select count(*) from brigade.memberships where team_id = :'team_t'::uuid), 8::bigint, 'stranger target (as postgres): the membership count is unchanged (update only, never an insert)');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'c1'::uuid), 'active', 'self-target (as postgres): C1 is still active');
select pg_temp.login(:'m1', true, 'Mia');
select r as m1_rejoin from brigade.join_team(:'secret_v3') as r \gset
select is((:'m1_rejoin'::jsonb)->>'status', 'joined', 'rejoin: M1 joins again with the current secret');
select is((:'m1_rejoin'::jsonb)->>'rejoined', 'true', 'rejoin: rejoined = true (the same principal, the same row)');
select is(jsonb_array_length(brigade.fetch_inbox(:'s_m1a'::uuid)), 1, 'rejoin: M1 owns its closed session again and the parked message survived');
select pg_temp.logout();
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m1'::uuid), 'active', 'rejoin (as postgres): active again');
select is((select joined_secret_version from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m1'::uuid), 3, 'rejoin (as postgres): joined_secret_version is now the current 3');

-- 4. banned: the oracle, three ways (I-20). C1 bans M2; M2's join_team with the CURRENT, CORRECT secret is the
--    whole invalid_secret document, byte-identical to a fresh principal's wrong secret and to another fresh
--    principal's unknown team (three principals, so the limiter never fires); the attempt lands in join_attempts;
--    then the un-ban and the rejoin.
select pg_temp.login(:'c1', true, 'Cleo');
select r as ban_m2 from brigade.revoke_membership(:'team_t'::uuid, :'m2'::uuid, true) as r \gset
select is((:'ban_m2'::jsonb)->>'status', 'banned', 'ban: status banned');
select is((:'ban_m2'::jsonb)->>'changed', 'true', 'ban: changed = true');
select is(((:'ban_m2'::jsonb)->>'sessions_closed')::int, 1, 'ban: M2''s one open session closed');
select pg_temp.logout();
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m2'::uuid), 'banned', 'ban (as postgres): the row is banned');
select is((select count(*) from realtime.messages where topic = :'topic_m2' and event = 'membership_revoked'), 1::bigint, 'ban: one membership_revoked row on M2''s open session topic');
select ok((select closed_at is null from brigade.sessions where id = :'s_m2u'::uuid), 'ban (as postgres): M2''s open session in team U is untouched — the close is scoped to team T');
select is((select count(*) from realtime.messages where topic = 'brigade:session:' || :'s_m2u' and event = 'membership_revoked'), 0::bigint, 'ban: no membership_revoked row on M2''s team-U session topic — the broadcast is scoped to team T');
-- Backdate the ban's stamp (now() is one value for this whole transaction) so the un-ban's preservation of revoked_at is observable.
update brigade.memberships set revoked_at = now() - interval '1 hour' where team_id = :'team_t'::uuid and user_id = :'m2'::uuid;
select (select count(*) from brigade.join_attempts where user_id = :'m2'::uuid) as attempts_before \gset
select pg_temp.login(:'m2', true, 'Max');
select brigade.join_team(:'secret_v3')::text as j_banned \gset
select is((:'j_banned'::jsonb)->>'status', 'invalid_secret', 'banned cannot rejoin: the CURRENT, CORRECT secret answers invalid_secret');
select lives_ok($$select brigade.fetch_inbox('$$ || :'s_m2u' || $$'::uuid)$$, 'ban: M2 still drains its team-U inbox — the ban is team T''s alone');
select pg_temp.logout();
select pg_temp.new_user() as w1 \gset
select pg_temp.login(:'w1', true, 'Wrong');
select brigade.join_team('brg1.' || :'team_t' || '.' || repeat('0', 32))::text as j_wrong \gset
select pg_temp.logout();
select pg_temp.new_user() as z1 \gset
select pg_temp.login(:'z1', true, 'Zed');
select brigade.join_team('brg1.' || gen_random_uuid()::text || '.' || repeat('0', 32))::text as j_unknown \gset
select is(:'j_banned'::text, :'j_wrong', 'oracle: the banned member''s whole answer is byte-identical to a stranger''s wrong secret');
select is(:'j_banned'::text, :'j_unknown', 'oracle: ... and to a stranger''s secret for an unknown team');
select isnt(:'j_banned'::text, :'p2_join', 'control: a joined answer differs, so the identities above are not vacuous');
select pg_temp.logout();
select is((select count(*) from brigade.join_attempts where user_id = :'m2'::uuid), :'attempts_before'::bigint + 1, 'ban: the banned attempt is recorded in join_attempts (schema :260)');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m2'::uuid), 'banned', 'ban: the refused join left the row banned');
select pg_temp.login(:'c1', true, 'Cleo');
select r as ban_none from brigade.revoke_membership(:'team_t'::uuid, gen_random_uuid(), true) as r \gset
select is((:'ban_none'::jsonb)->>'changed', 'false', 'ban of a stranger: changed = false');
select is((:'ban_none'::jsonb)->>'status', 'banned', 'ban of a stranger: the answer names the requested status');
select is((select count(*) from jsonb_array_elements(brigade.list_members(:'team_t'::uuid)->'members') m where m->>'principal_ref' = :'m2'), 0::bigint, 'ban: a banned member is not listed by list_members (the un-ban needs the ref written down)');
select r as unban_m2 from brigade.revoke_membership(:'team_t'::uuid, :'m2'::uuid, false) as r \gset
select is((:'unban_m2'::jsonb)->>'status', 'revoked', 'un-ban: revoke_membership(…, false) on a banned row answers revoked');
select is((:'unban_m2'::jsonb)->>'changed', 'true', 'un-ban: changed = true (banned → revoked)');
select pg_temp.logout();
select is((select count(*) from brigade.memberships where team_id = :'team_t'::uuid), 8::bigint, 'ban of a stranger (as postgres): no row was inserted');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m2'::uuid), 'revoked', 'un-ban (as postgres): the row is revoked, not banned');
select is((select revoked_at from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m2'::uuid), now() - interval '1 hour', 'un-ban (as postgres): revoked_at keeps the ban''s stamp — only an active row is stamped now()');
select pg_temp.login(:'m2', true, 'Max');
select r as m2_rejoin from brigade.join_team(:'secret_v3') as r \gset
select is((:'m2_rejoin'::jsonb)->>'status', 'joined', 'un-ban: M2 rejoins with the current secret');
select is((:'m2_rejoin'::jsonb)->>'rejoined', 'true', 'un-ban: rejoined = true');
select pg_temp.logout();
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m2'::uuid), 'active', 'un-ban (as postgres): active again');

-- 5. revoke_memberships_by_version (I-21's second half). Versions now: C1, V1, V2, V3 at 1; P1 at 2; M1, M2, P2 at 3;
--    M4 joins at 3. V2 is banned first. A sweep at 1 revokes V1 and V3 only — not the caller, not the banned row,
--    not the later versions — closes their open sessions with one row each; a sweep at 2 revokes P1; at 0 and at 1
--    again nobody; V1 rejoins at the current version.
select pg_temp.new_user() as m4 \gset
select pg_temp.login(:'m4', true, 'Moe');
select is((brigade.join_team(:'secret_v3', 'Moe'))->>'status', 'joined', 'fixture: M4 joins at version 3');
select pg_temp.login(:'c1', true, 'Cleo');
select is((brigade.revoke_membership(:'team_t'::uuid, :'v2'::uuid, true))->>'status', 'banned', 'fixture: V2 (version 1) is banned');
select pg_temp.logout();
select is((select array_agg(user_id order by user_id) from brigade.memberships where team_id = :'team_t'::uuid and status = 'active' and joined_secret_version <= 1),
          (select array_agg(u order by u) from unnest(array[:'c1'::uuid, :'v1'::uuid, :'v3'::uuid]) u), 'fixture (as postgres): the active version-1 rows are exactly C1, V1 and V3');
select is((select count(*) from brigade.memberships where team_id = :'team_t'::uuid and status = 'active'), 8::bigint, 'fixture (as postgres): eight active rows (C1, M1, M2, V1, V3, P1, P2, M4)');
select is((select count(*) from realtime.messages where topic in (:'topic_v1', :'topic_v3a', :'topic_v3c') and event = 'membership_revoked'), 0::bigint, 'baseline: no membership_revoked row on V1''s and V3''s topics');
select pg_temp.login(:'c1', true, 'Cleo');
select r as sweep1 from brigade.revoke_memberships_by_version(:'team_t'::uuid, 1) as r \gset
select is(((:'sweep1'::jsonb)->>'revoked')::int, 2, 'sweep at 1: revoked = 2, the active version-1 members other than the caller (V1, V3)');
select is(((:'sweep1'::jsonb)->>'sessions_closed')::int, 2, 'sweep at 1: sessions_closed = 2 (v1-main, v3-a; v3-closed was already closed)');
select is(((:'sweep1'::jsonb)->>'max_version')::int, 1, 'sweep at 1: max_version echoed');
select is((:'sweep1'::jsonb)->>'team_id', :'team_t', 'sweep at 1: team_id');
select is(jsonb_array_length(brigade.list_members(:'team_t'::uuid)->'members'), 6, 'sweep at 1: the roster is C1, M1, M2, P1, P2, M4');
select is((brigade.revoke_memberships_by_version(:'team_t'::uuid, 0))->>'revoked', '0', 'sweep at 0: revokes nobody');
select is((brigade.revoke_memberships_by_version(:'team_t'::uuid, 1))->>'revoked', '0', 'sweep at 1 again: revokes nobody (already revoked rows are not active)');
select pg_temp.logout();
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'c1'::uuid), 'active', 'sweep at 1: the CALLER (version 1) is still active — the exclusion');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'v1'::uuid), 'revoked', 'sweep at 1: V1 is revoked');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'v3'::uuid), 'revoked', 'sweep at 1: V3 is revoked');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'v2'::uuid), 'banned', 'sweep at 1: the banned version-1 row stays banned, never revoked');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m4'::uuid), 'active', 'sweep at 1: M4 (version 3) is still active');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'p1'::uuid), 'active', 'sweep at 1: P1 (version 2) is still active');
select is((select count(*) from brigade.memberships where team_id = :'team_t'::uuid and status = 'revoked' and revoked_at = now()), 2::bigint, 'sweep at 1 (as postgres): exactly two rows carry revoked_at = now()');
select is((select count(*) from brigade.sessions where id in (:'s_v1'::uuid, :'s_v3a'::uuid) and closed_at is not null and activity = 'idle'), 2::bigint, 'sweep at 1 (as postgres): the two open sessions are closed and idle');
select ok((select closed_at is null from brigade.sessions where id = :'s_c1'::uuid), 'sweep at 1 (as postgres): the caller''s session is untouched');
select is((select count(*) from realtime.messages where topic = :'topic_v1' and event = 'membership_revoked'), 1::bigint, 'sweep hint: one membership_revoked row on v1-main''s topic');
select is((select count(*) from realtime.messages where topic = :'topic_v3a' and event = 'membership_revoked'), 1::bigint, 'sweep hint: one membership_revoked row on v3-a''s topic');
select is((select count(*) from realtime.messages where topic = :'topic_v3c' and event = 'membership_revoked'), 0::bigint, 'sweep hint: none on v3-closed''s topic');
select ok((select closed_at is null from brigade.sessions where id = :'s_v1u'::uuid), 'sweep at 1 (as postgres): V1''s open session in team U is untouched — the close is scoped to team T');
select is((select count(*) from realtime.messages where topic = 'brigade:session:' || :'s_v1u' and event = 'membership_revoked'), 0::bigint, 'sweep hint: none on V1''s team-U session topic — the broadcast is scoped to team T');
select is((select array_agg(k order by k) from realtime.messages r, jsonb_object_keys(r.payload) k where r.topic = :'topic_v1' and r.event = 'membership_revoked'), array['id', 'session_id'],
          'sweep hint: the payload is ids only');
select pg_temp.login(:'v3', true, 'Vin');
select is(pg_temp.err($$select brigade.fetch_inbox('$$ || :'s_v3a' || $$'::uuid)$$), '42501 brigade:unauthorized', 'sweep at 1: V3''s fetch_inbox is unauthorized');
select pg_temp.login(:'c1', true, 'Cleo');
select r as sweep2 from brigade.revoke_memberships_by_version(:'team_t'::uuid, 2) as r \gset
select is(((:'sweep2'::jsonb)->>'revoked')::int, 1, 'sweep at 2: revoked = 1 (P1 at version 2; the version-1 rows are no longer active)');
select is(((:'sweep2'::jsonb)->>'sessions_closed')::int, 0, 'sweep at 2: P1 had no session');
select pg_temp.logout();
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'p1'::uuid), 'revoked', 'sweep at 2: P1 is revoked');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'c1'::uuid), 'active', 'sweep at 2: the caller is still active');
select is((select count(*) from brigade.memberships where team_id = :'team_t'::uuid and status = 'active'), 5::bigint, 'sweep at 2 (as postgres): five active rows remain (C1, M1, M2, P2, M4)');
select pg_temp.login(:'v1', true, 'Vic');
select lives_ok($$select brigade.fetch_inbox('$$ || :'s_v1u' || $$'::uuid)$$, 'sweep at 1: V1 still drains its team-U inbox — the sweep is team T''s alone');
select is((brigade.join_team(:'secret_v2'))->>'status', 'invalid_secret', 'evicted: V1 cannot come back with a superseded secret');
select r as v1_rejoin from brigade.join_team(:'secret_v3') as r \gset
select is((:'v1_rejoin'::jsonb)->>'status', 'joined', 'evicted: V1 rejoins with the current secret');
select is((:'v1_rejoin'::jsonb)->>'rejoined', 'true', 'evicted: rejoined = true');
select pg_temp.logout();
select is((select joined_secret_version from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'v1'::uuid), 3, 'evicted: V1 is now at version 3');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'v1'::uuid), 'active', 'evicted: V1 is active again');

-- 6. transfer_team (the plan row's last acceptance sentence, asserted literally). C1 hands team T to M1: every admin
--    RPC of C1 is the uniform unauthorized, byte-identical to a random team id; M1 succeeds at all four; C1 stays an
--    ordinary active member with its session open; a non-member, a revoked member and a random uuid are ONE
--    invalid_input text and change nothing; the self-transfer is a no-op; then M1 hands the team back.
select pg_temp.login(:'m1', true, 'Mia');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'transfer control: before it, M1 is refused: ' || q) from unnest(pg_temp.admin_calls(:'team_t')) q;
select pg_temp.login(:'c1', true, 'Cleo');
select is(pg_temp.err($$select brigade.transfer_team('$$ || :'team_t' || $$'::uuid, '$$ || :'m3' || $$'::uuid)$$), '22023 brigade:invalid_input:principal_ref', 'transfer to a member of nothing: invalid_input:principal_ref');
select is(pg_temp.err($$select brigade.transfer_team('$$ || :'team_t' || $$'::uuid, '$$ || :'m3' || $$'::uuid)$$), pg_temp.err($$select brigade.transfer_team('$$ || :'team_t' || $$'::uuid, gen_random_uuid())$$),
          'transfer: a member of nothing and a uuid naming no auth.users row are byte-identical (no auth.users oracle)');
select is(pg_temp.err($$select brigade.transfer_team('$$ || :'team_t' || $$'::uuid, '$$ || :'v3' || $$'::uuid)$$), '22023 brigade:invalid_input:principal_ref', 'transfer to a REVOKED member: the same invalid_input (active membership required)');
select is(pg_temp.err($$select brigade.transfer_team('$$ || :'team_t' || $$'::uuid, '$$ || :'v2' || $$'::uuid)$$), '22023 brigade:invalid_input:principal_ref', 'transfer to a BANNED member: the same invalid_input');
select is(pg_temp.err($$select brigade.transfer_team('$$ || :'team_t' || $$'::uuid, null)$$), '22023 brigade:invalid_input:principal_ref', 'transfer to null: the same invalid_input');
select is((brigade.transfer_team(:'team_t'::uuid, :'c1'::uuid))->>'transferred', 'true', 'self-transfer: transferred = true');
select pg_temp.logout();
select is((select created_by from brigade.teams where id = :'team_t'::uuid), :'c1'::uuid, 'transfer refusals and the self-transfer (as postgres): created_by is unchanged');
select pg_temp.login(:'c1', true, 'Cleo');
select r as xfer from brigade.transfer_team(:'team_t'::uuid, :'m1'::uuid) as r \gset
select is((:'xfer'::jsonb)->>'transferred', 'true', 'transfer_team: transferred = true');
select is((:'xfer'::jsonb)->>'principal_ref', :'m1', 'transfer_team: principal_ref names the new creator');
select is((:'xfer'::jsonb)->>'team_id', :'team_t', 'transfer_team: team_id');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'after transfer: the OLD creator gets brigade:unauthorized: ' || q) from unnest(pg_temp.admin_calls(:'team_t')) q;
select is(pg_temp.err(u.q), pg_temp.err(u.r), 'after transfer: the old creator''s refusal is byte-identical to a random team id: ' || u.q)
  from unnest(pg_temp.admin_calls(:'team_t'), pg_temp.admin_calls(gen_random_uuid()::text)) as u(q, r);
select is(jsonb_array_length(brigade.fetch_inbox(:'s_c1'::uuid)), 1, 'after transfer: the old creator still drains its inbox (an ordinary active member)');
select lives_ok($$select brigade.session_heartbeat('$$ || :'s_c1' || $$'::uuid)$$, 'after transfer: the old creator''s session is still open (heartbeat works)');
select pg_temp.logout();
select is((select created_by from brigade.teams where id = :'team_t'::uuid), :'m1'::uuid, 'after transfer (as postgres): created_by = M1');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'c1'::uuid), 'active', 'after transfer (as postgres): the old creator''s membership is still active');
select ok((select closed_at is null from brigade.sessions where id = :'s_c1'::uuid), 'after transfer (as postgres): the old creator''s session is open');
select pg_temp.login(:'m1', true, 'Mia');
select is((select count(*) from jsonb_array_elements(brigade.list_members(:'team_t'::uuid)->'members') m where m->>'principal_ref' = :'c1'), 1::bigint, 'after transfer: the old creator is in the new creator''s roster');
select is((brigade.revoke_membership(:'team_t'::uuid, gen_random_uuid()))->>'changed', 'false', 'new creator: revoke_membership passes the gate');
select is((brigade.revoke_memberships_by_version(:'team_t'::uuid, 0))->>'revoked', '0', 'new creator: revoke_memberships_by_version passes the gate');
select is((brigade.transfer_team(:'team_t'::uuid, :'m1'::uuid))->>'transferred', 'true', 'new creator: a self-transfer passes the gate');
select is(((brigade.rotate_join_secret(:'team_t'::uuid))->>'secret_version')::int, 4, 'new creator: rotate_join_secret passes the gate (version 3 → 4)');
select is(pg_temp.err($$select brigade.revoke_membership('$$ || :'team_t' || $$'::uuid, '$$ || :'m1' || $$'::uuid)$$), '22023 brigade:invalid_input:principal_ref', 'new creator: the self-target rule follows created_by (M1 may not revoke M1)');
select is((brigade.revoke_membership(:'team_t'::uuid, :'v3'::uuid, true))->>'status', 'banned', 'new creator: may ban a member (V3)');
select is((brigade.transfer_team(:'team_t'::uuid, :'c1'::uuid))->>'transferred', 'true', 'transfer back: M1 hands the team to C1');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'transfer back: M1 is refused again: ' || q) from unnest(pg_temp.admin_calls(:'team_t')) q;
select pg_temp.login(:'c1', true, 'Cleo');
select is((brigade.revoke_memberships_by_version(:'team_t'::uuid, 0))->>'revoked', '0', 'transfer back: C1 administers again');
select pg_temp.logout();
select is((select created_by from brigade.teams where id = :'team_t'::uuid), :'c1'::uuid, 'transfer back (as postgres): created_by = C1');
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'m1'::uuid), 'active', 'transfer back (as postgres): M1 stays an active member');

-- 7. Unauthenticated: every admin RPC without a JWT is brigade:unauthenticated before any gate (the schema :193 pattern).
select is(pg_temp.err(q), '28000 brigade:unauthenticated', 'no JWT: ' || q) from unnest(pg_temp.admin_calls(:'team_t')) q;

select * from finish();
rollback;
