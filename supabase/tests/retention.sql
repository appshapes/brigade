-- retention.sql (plan 9.3, I-31, I-32; I-24 is Phase 5): gc_expired() deletes exactly the classes of plan 5.8 with
-- time-shifted fixtures (messages backdated as postgres; sessions' closed_at/last_seen_at backdated as postgres):
-- acknowledged messages 24 h after injected_at, unacknowledged ones 7 days after created_at, sessions 7 days after
-- closed_at or lease expiry (cascading their messages), join attempts after 24 h, and abandoned teams (older than
-- 7 days, at most one membership, no session seen within 7 days) with their memberships, while a two-member team of
-- the same age, a single-member team with a recent session and a young single-member team survive; principals are
-- never deleted; the call is idempotent; server-only; scheduled under pg_cron on this stack (E0-1 (h)).
begin;
\ir helpers/auth.sql
select plan(48);

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
-- Principals are never deleted (Phase 5 adds the anonymous-user rule).
select is((select count(*) from auth.users where id in (:'a1'::uuid, :'a2'::uuid, :'ux'::uuid, :'uy'::uuid, :'uw'::uuid, :'uv'::uuid, :'uu'::uuid, :'ut'::uuid)), 8::bigint, 'gc: every fixture principal still exists, including the creator of the deleted abandoned team');
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

-- Scheduling on this stack (E0-1 (h): pg_cron exists on the -x stack of plan 5.9).
select is((select count(*) from pg_extension where extname = 'pg_cron'), 1::bigint, 'pg_cron is installed on the local stack');
select is((select schedule from cron.job where jobname = 'brigade_gc'), '17 * * * *', 'cron.job lists brigade_gc hourly at minute 17');
select is((select command from cron.job where jobname = 'brigade_gc'), 'select brigade.gc_expired()', 'brigade_gc runs gc_expired()');
select ok((select active from cron.job where jobname = 'brigade_gc'), 'brigade_gc is active');
select is((select schedule from cron.job where jobname = 'brigade_cron_log_gc'), '23 3 * * *', 'cron.job lists brigade_cron_log_gc daily');

select * from finish();
rollback;
