-- retention.sql (plan 9.3, I-24, I-31, I-32): gc_expired() deletes exactly the classes of plan 5.8 with
-- time-shifted fixtures (messages backdated as postgres; sessions' closed_at/last_seen_at backdated as postgres):
-- acknowledged messages 24 h after injected_at, unacknowledged ones 7 days after created_at, sessions 7 days after
-- closed_at or lease expiry (cascading their messages), join attempts after 24 h, and abandoned teams (older than
-- 7 days, at most one membership, no session seen within 7 days) with their memberships, while a two-member team of
-- the same age, a single-member team with a recent session and a young single-member team survive; dated principals
-- survive every call; the call is idempotent; server-only; scheduled under pg_cron on this stack (E0-1 (h)).
-- P5-3 (I-24, D13) adds brigade.gc_anonymous_users(), called last from gc_expired(): an anonymous principal older
-- than 7 days with no membership row of ANY status and no team of its own is deleted with its auth.sessions and
-- refresh tokens; a young one, a creator (member, left, or with no membership row at all), a banned member and a
-- named user all survive; the ±1 minute boundaries; the abandoned-team → orphaned-creator chain inside ONE
-- gc_expired() call; the count (never -1, the handler) and idempotence; the three grant denials; and four mutants
-- derived from the shipped definition into pg_temp, each killed by a named assertion with the real function as the
-- positive control.
begin;
\ir helpers/auth.sql
select plan(95);

-- Team A: A1 (a1, s_closed_old) and A2 (a2, s_closed_recent, s_expired_old, s_expired_recent); created now, so
-- team A itself is never an abandonment candidate.
select pg_temp.new_user() as a1 \gset
select pg_temp.new_user() as a2 \gset
select pg_temp.login(:'a1', true, 'Alice');
select r->>'team_id' as team_a, r->>'join_secret' as secret_a from brigade.create_team('Team A', 'Alice') as r \gset
select (brigade.register_session(:'team_a'::uuid, 'a1'))->>'session_id' as a1s \gset
select (brigade.register_session(:'team_a'::uuid, 'closed-old'))->>'session_id' as s_closed_old \gset
select pg_temp.login(:'a2', true, 'Bob');
select is((brigade.join_team(:'secret_a', 'Bob'))->>'status', 'joined', 'fixture: A2 joins team A');
select (brigade.register_session(:'team_a'::uuid, 'a2'))->>'session_id' as a2s \gset
select (brigade.register_session(:'team_a'::uuid, 'closed-recent'))->>'session_id' as s_closed_recent \gset
select (brigade.register_session(:'team_a'::uuid, 'expired-old'))->>'session_id' as s_expired_old \gset
select (brigade.register_session(:'team_a'::uuid, 'expired-recent'))->>'session_id' as s_expired_recent \gset
-- Messages a1 -> a2 (four retention classes) and a1 -> s_closed_old (recent, unacked: goes only by cascade).
select pg_temp.login(:'a1', true, 'Alice');
select (brigade.send_message(:'a1s'::uuid, :'a2s'::uuid, 'old acked', 'r1'))->>'message_id' as m_old_acked \gset
select (brigade.send_message(:'a1s'::uuid, :'a2s'::uuid, 'recent acked', 'r2'))->>'message_id' as m_recent_acked \gset
select (brigade.send_message(:'a1s'::uuid, :'a2s'::uuid, 'old unacked', 'r3'))->>'message_id' as m_old_unacked \gset
select (brigade.send_message(:'a1s'::uuid, :'a2s'::uuid, 'recent unacked', 'r4'))->>'message_id' as m_recent_unacked \gset
select (brigade.send_message(:'a1s'::uuid, :'s_closed_old'::uuid, 'parked on a session about to be reaped', 'r5'))->>'message_id' as m_cascade \gset
select pg_temp.login(:'a2', true, 'Bob');
select is(jsonb_array_length((brigade.ack_messages(:'a2s'::uuid, array[:'m_old_acked'::uuid, :'m_recent_acked'::uuid]))->'acked'), 2, 'fixture: two messages acked');
select pg_temp.logout();
-- Time shifting as postgres (messages have no UPDATE trigger; the sessions trigger allows closed_at/last_seen_at).
update brigade.messages set injected_at = now() - interval '25 hours' where id = :'m_old_acked'::uuid;
update brigade.messages set injected_at = now() - interval '23 hours' where id = :'m_recent_acked'::uuid;
update brigade.messages set created_at = now() - interval '8 days' where id = :'m_old_unacked'::uuid;
update brigade.messages set created_at = now() - interval '6 days' where id = :'m_recent_unacked'::uuid;
update brigade.sessions set closed_at = now() - interval '8 days' where id = :'s_closed_old'::uuid;              -- last_seen_at stays now(): closed_at alone qualifies
update brigade.sessions set closed_at = now() - interval '6 days' where id = :'s_closed_recent'::uuid;
update brigade.sessions set last_seen_at = now() - interval '8 days' where id = :'s_expired_old'::uuid;         -- lease 90 s: expired 8 days ago
update brigade.sessions set last_seen_at = now() - interval '6 days' where id = :'s_expired_recent'::uuid;      -- expired, but only 6 days ago
insert into brigade.join_attempts (user_id, team_id, attempted_at) values (:'a1'::uuid, :'team_a'::uuid, now() - interval '25 hours') returning id as ja_old \gset
insert into brigade.join_attempts (user_id, team_id, attempted_at) values (:'a1'::uuid, :'team_a'::uuid, now() - interval '23 hours') returning id as ja_recent \gset

-- Teams for the abandonment rule (all backdated 8 days except T_young).
select pg_temp.new_user() as ux \gset
select pg_temp.new_user() as uy \gset
select pg_temp.new_user() as uw \gset
select pg_temp.new_user() as uv \gset
select pg_temp.new_user() as uu \gset
select pg_temp.new_user() as ut \gset
select pg_temp.login(:'ux', true, 'X');
select r->>'team_id' as t_abandoned from brigade.create_team('T abandoned', 'X') as r \gset
select pg_temp.login(:'uy', true, 'Y');
select r->>'team_id' as t_two, r->>'join_secret' as secret_two from brigade.create_team('T two', 'Y') as r \gset
select pg_temp.login(:'uw', true, 'W');
select is((brigade.join_team(:'secret_two', 'W'))->>'status', 'joined', 'fixture: W joins T two');
select pg_temp.login(:'uv', true, 'V');
select r->>'team_id' as t_recent from brigade.create_team('T single recent session', 'V') as r \gset
select (brigade.register_session(:'t_recent'::uuid, 'v'))->>'session_id' as v_s \gset
select pg_temp.login(:'uu', true, 'U');
select r->>'team_id' as t_oldsession from brigade.create_team('T single old session', 'U') as r \gset
select (brigade.register_session(:'t_oldsession'::uuid, 'u'))->>'session_id' as u_s \gset
select pg_temp.login(:'ut', true, 'T');
select r->>'team_id' as t_young from brigade.create_team('T young', 'T') as r \gset
select pg_temp.logout();
update brigade.teams set created_at = now() - interval '8 days' where id in (:'t_abandoned'::uuid, :'t_two'::uuid, :'t_recent'::uuid, :'t_oldsession'::uuid);
update brigade.teams set created_at = now() - interval '1 day' where id = :'t_young'::uuid;
update brigade.sessions set last_seen_at = now() - interval '1 day' where id = :'v_s'::uuid;
update brigade.sessions set last_seen_at = now() - interval '8 days' where id = :'u_s'::uuid;

-- Pre-gc baseline: everything is there.
select is((select count(*) from brigade.messages where id in (:'m_old_acked'::uuid, :'m_recent_acked'::uuid, :'m_old_unacked'::uuid, :'m_recent_unacked'::uuid, :'m_cascade'::uuid)), 5::bigint, 'baseline: five fixture messages');
select is((select count(*) from brigade.sessions where team_id = :'team_a'::uuid), 6::bigint, 'baseline: six fixture sessions in team A');
select is((select count(*) from brigade.join_attempts where id in (:ja_old, :ja_recent)), 2::bigint, 'baseline: two fixture join attempts');
select is((select count(*) from brigade.teams where id in (:'t_abandoned'::uuid, :'t_two'::uuid, :'t_recent'::uuid, :'t_oldsession'::uuid, :'t_young'::uuid)), 5::bigint, 'baseline: five fixture teams');

-- gc_expired is server-only: a member cannot call it.
select pg_temp.login(:'a1', true, 'Alice');
select throws_ok($$select brigade.gc_expired()$$, '42501', 'permission denied for function gc_expired', 'gc_expired: authenticated may not execute it');
select pg_temp.logout();
select lives_ok($$select brigade.gc_expired()$$, 'gc_expired() runs as postgres');

-- Messages.
select is((select count(*) from brigade.messages where id = :'m_old_acked'::uuid), 0::bigint, 'gc: an acknowledged message injected 25 h ago is deleted');
select is((select count(*) from brigade.messages where id = :'m_recent_acked'::uuid), 1::bigint, 'gc: an acknowledged message injected 23 h ago is kept');
select is((select count(*) from brigade.messages where id = :'m_old_unacked'::uuid), 0::bigint, 'gc: an unacknowledged message created 8 days ago is deleted');
select is((select count(*) from brigade.messages where id = :'m_recent_unacked'::uuid), 1::bigint, 'gc: an unacknowledged message created 6 days ago is kept');
select is((select count(*) from brigade.messages where id = :'m_cascade'::uuid), 0::bigint, 'gc: a recent message addressed to a reaped session is gone with it (cascade)');
-- Sessions.
select is((select count(*) from brigade.sessions where id = :'s_closed_old'::uuid), 0::bigint, 'gc: a session closed 8 days ago is deleted');
select is((select count(*) from brigade.sessions where id = :'s_closed_recent'::uuid), 1::bigint, 'gc: a session closed 6 days ago is kept (a --resume within a week still finds its inbox)');
select is((select count(*) from brigade.sessions where id = :'s_expired_old'::uuid), 0::bigint, 'gc: a session whose lease expired 8 days ago is deleted');
select is((select count(*) from brigade.sessions where id = :'s_expired_recent'::uuid), 1::bigint, 'gc: a session whose lease expired 6 days ago is kept');
select is((select count(*) from brigade.sessions where id in (:'a1s'::uuid, :'a2s'::uuid)), 2::bigint, 'gc: live sessions are kept');
-- Join attempts.
select is((select count(*) from brigade.join_attempts where id = :ja_old), 0::bigint, 'gc: a join attempt from 25 h ago is deleted');
select is((select count(*) from brigade.join_attempts where id = :ja_recent), 1::bigint, 'gc: a join attempt from 23 h ago is kept');
-- Teams.
select is((select count(*) from brigade.teams where id = :'t_abandoned'::uuid), 0::bigint, 'gc: an 8-day-old single-member team with no session is deleted (abandoned)');
select is((select count(*) from brigade.memberships where team_id = :'t_abandoned'::uuid), 0::bigint, 'gc: its membership cascaded');
select is((select count(*) from brigade.teams where id = :'t_oldsession'::uuid), 0::bigint, 'gc: an 8-day-old single-member team whose only session was last seen 8 days ago is deleted');
select is((select count(*) from brigade.teams where id = :'t_two'::uuid), 1::bigint, 'gc: a two-member team of the same age survives');
select is((select count(*) from brigade.memberships where team_id = :'t_two'::uuid), 2::bigint, 'gc: with both memberships');
select is((select count(*) from brigade.teams where id = :'t_recent'::uuid), 1::bigint, 'gc: a single-member team with a session seen 1 day ago survives');
select is((select count(*) from brigade.sessions where id = :'v_s'::uuid), 1::bigint, 'gc: and that session survives (seen within 7 days)');
select is((select count(*) from brigade.teams where id = :'t_young'::uuid), 1::bigint, 'gc: a 1-day-old single-member team survives');
select is((select count(*) from brigade.teams where id = :'team_a'::uuid), 1::bigint, 'gc: team A (young, two members, live sessions) survives');
select is((select count(*) from brigade.memberships where team_id = :'team_a'::uuid), 2::bigint, 'gc: team A keeps both memberships');
-- Principals dated now() by new_user() are never deleted: gc_expired() now ends in the P5-3 anonymous-user rule (its own
-- section below), and every principal above is younger than its 7-day window — including ux, the creator of the
-- deleted abandoned team, now membership-less and team-less, i.e. reapable in every respect but age.
select is((select count(*) from auth.users where id in (:'a1'::uuid, :'a2'::uuid, :'ux'::uuid, :'uy'::uuid, :'uw'::uuid, :'uv'::uuid, :'uu'::uuid, :'ut'::uuid)), 8::bigint, 'gc: every fixture principal still exists, including the creator of the deleted abandoned team (young: the anonymous rule keys on age)');
-- Idempotent: a second run deletes nothing more.
select (select count(*) from brigade.messages) as msg_after, (select count(*) from brigade.sessions) as ses_after, (select count(*) from brigade.teams) as team_after, (select count(*) from brigade.join_attempts) as ja_after \gset
select lives_ok($$select brigade.gc_expired()$$, 'gc_expired() runs again');
select is((select count(*) from brigade.messages), :msg_after::bigint, 'gc: a second run deletes no message');
select is((select count(*) from brigade.sessions), :ses_after::bigint, 'gc: a second run deletes no session');
select is((select count(*) from brigade.teams), :team_after::bigint, 'gc: a second run deletes no team');
select is((select count(*) from brigade.join_attempts), :ja_after::bigint, 'gc: a second run deletes no join attempt');
-- Boundaries: the closed session at exactly 7 days minus a minute is kept; a message injected 24 h minus a minute ago is kept.
update brigade.sessions set closed_at = now() - interval '7 days' + interval '1 minute' where id = :'s_closed_recent'::uuid;
update brigade.messages set injected_at = now() - interval '24 hours' + interval '1 minute' where id = :'m_recent_acked'::uuid;
select lives_ok($$select brigade.gc_expired()$$, 'gc_expired() runs at the boundaries');
select is((select count(*) from brigade.sessions where id = :'s_closed_recent'::uuid), 1::bigint, 'gc: a session closed 7 days minus a minute ago is kept');
select is((select count(*) from brigade.messages where id = :'m_recent_acked'::uuid), 1::bigint, 'gc: a message injected 24 h minus a minute ago is kept');
update brigade.sessions set closed_at = now() - interval '7 days' - interval '1 minute' where id = :'s_closed_recent'::uuid;
update brigade.messages set injected_at = now() - interval '24 hours' - interval '1 minute' where id = :'m_recent_acked'::uuid;
select lives_ok($$select brigade.gc_expired()$$, 'gc_expired() runs past the boundaries');
select is((select count(*) from brigade.sessions where id = :'s_closed_recent'::uuid), 0::bigint, 'gc: a session closed 7 days plus a minute ago is deleted');
select is((select count(*) from brigade.messages where id = :'m_recent_acked'::uuid), 0::bigint, 'gc: a message injected 24 h plus a minute ago is deleted');

-- ===== P5-3 (I-24; plan 5.8, D13): brigade.gc_anonymous_users(), called last from gc_expired(). =====
-- Every principal above was dated now() by pg_temp.new_user() (P5-3 gave it `created`), so none of the gc_expired()
-- calls above could reap one; the matrix below is built AFTER them. auth.users.created_at is nullable with no default
-- and a NULL never satisfies the 7-day comparison, so the first assertion pins that the helper dates its rows: without
-- it every "survives" below would pass for the wrong reason and every "is deleted" would fail for a reason that looks
-- like a broken predicate (measured 2026-09-05: every fixture principal used to be in that state).
select isnt((select created_at from auth.users where id = :'a1'::uuid), null::timestamptz, 'fixture: new_user() dates the principal, or every anonymous-gc assertion below is vacuous');

-- The matrix (brief 3.4.1/3.4.2): 8 days old unless stated; every state built through the RPCs where one exists.
select pg_temp.new_user(true,  now() - interval '8 days') as g_plain \gset
select pg_temp.new_user(true,  now() - interval '3 days') as g_young \gset
select pg_temp.new_user(true,  now() - interval '8 days') as g_creator_member \gset
select pg_temp.new_user(true,  now() - interval '8 days') as g_creator_left \gset
select pg_temp.new_user(true,  now() - interval '8 days') as g_banned \gset
select pg_temp.new_user(false, now() - interval '8 days') as g_named \gset
select pg_temp.new_user(true,  now() - interval '8 days') as g_creator_orphaned \gset
select pg_temp.new_user(true,  now() - interval '8 days') as g_creator_bare \gset
select pg_temp.new_user(true,  now() - interval '7 days' + interval '1 minute') as g_boundary_in \gset
select pg_temp.new_user(true,  now() - interval '7 days' - interval '1 minute') as g_boundary_out \gset
select pg_temp.login(:'g_creator_member', true, 'GM');
select r->>'team_id' as t_member, r->>'join_secret' as secret_member from brigade.create_team('G member', 'GM') as r \gset
select pg_temp.login(:'g_creator_left', true, 'GL');
select r->>'team_id' as t_left from brigade.create_team('G left', 'GL') as r \gset
select is((brigade.leave_team(:'t_left'::uuid))->>'left', 'true', 'fixture: the creator of G left leaves it (row revoked, created_by intact)');
select pg_temp.login(:'g_banned', true, 'GB');
select is((brigade.join_team(:'secret_member', 'GB'))->>'status', 'joined', 'fixture: g_banned joins G member');
select pg_temp.login(:'g_creator_orphaned', true, 'GO');
select r->>'team_id' as t_orphan from brigade.create_team('G orphan', 'GO') as r \gset
select pg_temp.login(:'g_creator_bare', true, 'GX');
select r->>'team_id' as t_bare from brigade.create_team('G bare', 'GX') as r \gset
select pg_temp.logout();
-- As postgres: the banned row (the rpc_join.sql way; P5-2 owns the admin RPC), the orphan chain's abandoned team, and
-- the one state no code path produces — a creator with NO membership row (measured 2026-09-05: 0 of 4,713 anonymous
-- users on this stack; create_team always inserts the creator's row and nothing hard-deletes one). It is the state
-- the creator guard exists for: with the membership guard alone every creator is spared by its own row, so only this
-- witness makes the guard observable and the restricted FK (teams_created_by_fkey) reachable.
update brigade.memberships set status = 'banned' where team_id = :'t_member'::uuid and user_id = :'g_banned'::uuid;
update brigade.teams set created_at = now() - interval '8 days' where id = :'t_orphan'::uuid;
delete from brigade.memberships where team_id = :'t_bare'::uuid and user_id = :'g_creator_bare'::uuid;
-- g_plain gets a GoTrue-shaped auth.sessions row and a refresh token by hand, so the cascade is asserted over a
-- non-empty set (auth.refresh_tokens has no FK to auth.users; it cascades through auth.sessions).
insert into auth.sessions (id, user_id, created_at, updated_at) values (gen_random_uuid(), :'g_plain'::uuid, now(), now()) returning id as g_plain_session \gset
insert into auth.refresh_tokens (token, user_id, session_id, revoked, created_at, updated_at) values ('p5-3-' || :'g_plain_session', :'g_plain'::text, :'g_plain_session'::uuid, false, now(), now());
create temp table p53_matrix (name text primary key, id uuid not null unique);
insert into p53_matrix values
  ('g_plain', :'g_plain'), ('g_young', :'g_young'), ('g_creator_member', :'g_creator_member'), ('g_creator_left', :'g_creator_left'),
  ('g_banned', :'g_banned'), ('g_named', :'g_named'), ('g_creator_orphaned', :'g_creator_orphaned'), ('g_creator_bare', :'g_creator_bare'),
  ('g_boundary_in', :'g_boundary_in'), ('g_boundary_out', :'g_boundary_out');

-- Isolation from a SHARED stack, not an assertion: auth.users here may hold other developers' stale anonymous
-- principals (4,717 after three days of runs on the author's stack). Every count below is exact and every mutant
-- must reach its witness despite `limit 1000` (which takes the OLDEST rows first), so each row outside the matrix
-- that any predicate below could select is re-dated to now() for the length of this rolled-back transaction. On a
-- fresh database (CI) this touches nothing. Nothing is ever asserted on a global count.
select count(*) as background from auth.users where created_at < now() - interval '1 day' and id not in (select id from p53_matrix) \gset
update auth.users set created_at = now() where created_at < now() - interval '1 day' and id not in (select id from p53_matrix);
select diag('P5-3: ' || :background || ' stale principal(s) outside the matrix re-dated for this transaction');

-- Baseline: the matrix is what the comments say it is.
select is((select count(*) from auth.users where id in (select id from p53_matrix)), 10::bigint, 'baseline: the ten matrix principals exist');
select is((select status from brigade.memberships where team_id = :'t_left'::uuid and user_id = :'g_creator_left'::uuid), 'revoked', 'baseline: g_creator_left keeps a revoked membership row');
select is((select created_by from brigade.teams where id = :'t_left'::uuid), :'g_creator_left'::uuid, 'baseline: and stays created_by of G left');
select is((select count(*) from brigade.memberships where user_id = :'g_creator_bare'::uuid), 0::bigint, 'baseline: g_creator_bare has no membership row of any status');
select is((select created_by from brigade.teams where id = :'t_bare'::uuid), :'g_creator_bare'::uuid, 'baseline: but is created_by of G bare (the restricted FK is reachable only through it)');
select is((select count(*) from auth.refresh_tokens where session_id = :'g_plain_session'::uuid), 1::bigint, 'baseline: g_plain holds a refresh token under its auth.sessions row');

-- Server-only (brief 3.4.4): the three API roles are refused, mirroring functions.sql section 10.
select pg_temp.login(:'g_creator_member', true, 'GM');
select throws_ok($$select brigade.gc_anonymous_users()$$, '42501', 'permission denied for function gc_anonymous_users', 'gc_anonymous_users: authenticated may not execute it');
select pg_temp.logout();
set local role service_role;
select throws_ok($$select brigade.gc_anonymous_users()$$, '42501', 'permission denied for function gc_anonymous_users', 'gc_anonymous_users: service_role may not execute it');
reset role;
set local role anon;
select throws_ok($$select brigade.gc_anonymous_users()$$, '42501', 'permission denied for schema brigade', 'gc_anonymous_users: anon has no usage on schema brigade');
reset role;

-- The mutation harness (brief 3.4.5). Each mutant is derived from the SHIPPED function's own definition
-- (pg_get_functiondef) into pg_temp, so it has the real function's privileges (security definer, owner postgres)
-- and cannot outlive this transaction; a vanished anchor raises rather than producing a clone (manifests_test.go's
-- rule: no mutant can be vacuous). Each runs inside a savepoint that is rolled back, so the matrix is intact for the
-- next one and for the real function, whose assertions further down are the positive control. The real brigade.*
-- function is never replaced here: a crash mid-file would leave a mutant in the schema, and pg_temp cannot leak.
create or replace function pg_temp.mutant(p_old text, p_new text, p_name text)
returns void language plpgsql as $m$
declare src text; mutated text;
begin
  src := pg_get_functiondef('brigade.gc_anonymous_users'::regproc);
  if position(p_old in src) = 0 then
    raise exception 'mutation anchor % is gone: this mutant would be vacuous', p_old;
  end if;
  mutated := replace(replace(src, p_old, p_new), 'brigade.gc_anonymous_users(', 'pg_temp.' || p_name || '(');
  execute mutated;
end $m$;
select throws_ok($$select pg_temp.mutant('this anchor is not in the function', '', 'vacuous')$$, 'P0001',
                 'mutation anchor this anchor is not in the function is gone: this mutant would be vacuous',
                 'mutant harness: a vanished anchor raises instead of cloning the function');

-- Mutant 1, no creator guard. READ THIS ONE CAREFULLY: with the guard gone the delete reaches g_creator_bare, the
-- restricted FK raises 23503, the handler traps it and its subtransaction rolls the WHOLE statement back. So the
-- creator survives under the mutant too, and "the creator survived" cannot kill it; what kills it is the pair
-- "-1" and "g_plain, which the real function deletes, is still there". The WARNING in this run's output is the handler.
savepoint mutant_no_creator_guard;
select lives_ok($$select pg_temp.mutant('and not exists (select 1 from brigade.teams t where t.created_by = u.id)', '', 'no_creator_guard')$$, 'mutant no_creator_guard: derived from the shipped definition (anchor present)');
select is(pg_temp.no_creator_guard(), -1, 'mutant no_creator_guard: the restricted delete raises 23503, the handler fires and answers -1');
select is((select count(*) from auth.users where id = :'g_plain'::uuid), 1::bigint, 'mutant no_creator_guard: KILLED HERE — g_plain is still there, the handler rolled the whole statement back');
select is((select count(*) from auth.users where id = :'g_creator_bare'::uuid), 1::bigint, 'mutant no_creator_guard: the creator survived too — by the rollback, not by the guard (recorded; not the kill)');
rollback to savepoint mutant_no_creator_guard;

-- Mutant 2, no membership guard: g_banned (a row of status banned, no team of its own) is the witness; a creator
-- would still be spared by the creator guard, which is why it cannot be the witness.
savepoint mutant_no_membership_guard;
select lives_ok($$select pg_temp.mutant('and not exists (select 1 from brigade.memberships m where m.user_id = u.id)', '', 'no_membership_guard')$$, 'mutant no_membership_guard: derived from the shipped definition (anchor present)');
select is(pg_temp.no_membership_guard(), 3, 'mutant no_membership_guard: deletes three (g_plain, g_boundary_out and the witness)');
select is((select count(*) from auth.users where id = :'g_banned'::uuid), 0::bigint, 'mutant no_membership_guard: KILLED HERE — g_banned is deleted');
select is((select count(*) from auth.users where id = :'g_creator_member'::uuid), 1::bigint, 'mutant no_membership_guard: g_creator_member still survives through the creator guard');
rollback to savepoint mutant_no_membership_guard;

-- Mutant 3, a 1-day window: g_young (3 days) is the witness.
savepoint mutant_shifted_window;
select lives_ok($$select pg_temp.mutant('interval ''7 days''', 'interval ''1 day''', 'shifted_window')$$, 'mutant shifted_window: derived from the shipped definition (anchor present)');
select is(pg_temp.shifted_window(), 4, 'mutant shifted_window: deletes four (g_plain, g_boundary_out, g_boundary_in and the witness)');
select is((select count(*) from auth.users where id = :'g_young'::uuid), 0::bigint, 'mutant shifted_window: KILLED HERE — g_young (3 days) is deleted under a 1-day window');
rollback to savepoint mutant_shifted_window;

-- Mutant 4, no anonymous guard: g_named (is_anonymous false) is the witness.
savepoint mutant_no_anonymous_guard;
select lives_ok($$select pg_temp.mutant('where u.is_anonymous', 'where true', 'no_anonymous_guard')$$, 'mutant no_anonymous_guard: derived from the shipped definition (anchor present)');
select is(pg_temp.no_anonymous_guard(), 3, 'mutant no_anonymous_guard: deletes three (g_plain, g_boundary_out and the witness)');
select is((select count(*) from auth.users where id = :'g_named'::uuid), 0::bigint, 'mutant no_anonymous_guard: KILLED HERE — g_named (a named user) is deleted');
rollback to savepoint mutant_no_anonymous_guard;
select is((select count(*) from auth.users where id in (select id from p53_matrix)), 10::bigint, 'mutants: every savepoint rolled back, the matrix is intact for the shipped function');

-- The shipped function (the positive control for every mutant above). The count is asserted exactly: 2 is neither
-- 0 (nothing deletable) nor -1 (the handler fired), and the background was re-dated above.
select brigade.gc_anonymous_users() as rc_first \gset
select is(:rc_first::integer, 2, 'gc_anonymous_users(): the first call answers 2 — g_plain and g_boundary_out — not 0 and not -1');
select is((select count(*) from auth.users where id = :'g_plain'::uuid), 0::bigint, 'gc: an 8-day-old anonymous principal with no membership row and no team is deleted');
select is((select count(*) from auth.users where id = :'g_boundary_out'::uuid), 0::bigint, 'gc: at 7 days plus a minute it is deleted');
select is((select count(*) from auth.users where id = :'g_boundary_in'::uuid), 1::bigint, 'gc: at 7 days minus a minute it survives');
select is((select count(*) from auth.users where id = :'g_young'::uuid), 1::bigint, 'gc: a 3-day-old one survives');
select is((select count(*) from auth.users where id = :'g_creator_member'::uuid), 1::bigint, 'gc: a creator with an active membership row survives (the plan row''s named case)');
select is((select count(*) from auth.users where id = :'g_creator_left'::uuid), 1::bigint, 'gc: a creator who left (row revoked, created_by intact) survives — twice over');
select is((select count(*) from auth.users where id = :'g_creator_bare'::uuid), 1::bigint, 'gc: a creator with NO membership row survives through the creator guard alone, and the function did not abort');
select is((select count(*) from auth.users where id = :'g_banned'::uuid), 1::bigint, 'gc: a banned member survives (no membership row of ANY status)');
select is((select count(*) from auth.users where id = :'g_named'::uuid), 1::bigint, 'gc: a named (non-anonymous) user of the same age survives');
select is((select count(*) from auth.users where id = :'g_creator_orphaned'::uuid), 1::bigint, 'gc: the creator of a still-existing abandoned team survives a direct call (its team is not gone yet)');
-- Cascade (I-24 "cascades cleanly") and idempotence.
select is((select count(*) from auth.sessions where user_id = :'g_plain'::uuid), 0::bigint, 'gc: the deleted principal''s auth.sessions row cascaded');
select is((select count(*) from auth.refresh_tokens where session_id = :'g_plain_session'::uuid or user_id = :'g_plain'::text), 0::bigint, 'gc: and its refresh token went with the session (no orphan with a null session_id)');
select is(brigade.gc_anonymous_users(), 0, 'gc: a second call deletes nothing (0, not -1)');

-- The chain (brief 3.4.1, seventh case; 5.8's comment): ONE gc_expired() deletes the abandoned team, cascades its
-- membership, and then reaps its creator — which only holds because the anonymous rule runs LAST in gc_expired().
select lives_ok($$select brigade.gc_expired()$$, 'gc_expired() runs with the matrix in place');
select is((select count(*) from brigade.teams where id = :'t_orphan'::uuid), 0::bigint, 'chain: the 8-day-old single-member team G orphan is deleted (abandoned)');
select is((select count(*) from brigade.memberships where user_id = :'g_creator_orphaned'::uuid), 0::bigint, 'chain: its creator''s membership cascaded');
select is((select count(*) from auth.users where id = :'g_creator_orphaned'::uuid), 0::bigint, 'chain: and the creator, now membership-less and team-less, is reaped in the SAME gc_expired() call');
select is((select count(*) from auth.users where id in (select id from p53_matrix where name in ('g_young', 'g_creator_member', 'g_creator_left', 'g_creator_bare', 'g_banned', 'g_named', 'g_boundary_in'))), 7::bigint, 'chain: the seven other survivors are untouched by gc_expired()');

-- Scheduling on this stack (E0-1 (h): pg_cron exists on the -x stack of plan 5.9).
select is((select count(*) from pg_extension where extname = 'pg_cron'), 1::bigint, 'pg_cron is installed on the local stack');
select is((select schedule from cron.job where jobname = 'brigade_gc'), '17 * * * *', 'cron.job lists brigade_gc hourly at minute 17');
select is((select command from cron.job where jobname = 'brigade_gc'), 'select brigade.gc_expired()', 'brigade_gc runs gc_expired()');
select ok((select active from cron.job where jobname = 'brigade_gc'), 'brigade_gc is active');
select is((select schedule from cron.job where jobname = 'brigade_cron_log_gc'), '23 3 * * *', 'cron.job lists brigade_cron_log_gc daily');

select * from finish();
rollback;
