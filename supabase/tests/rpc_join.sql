-- rpc_join.sql (plan 9.3, I-17, I-18, I-19, I-21, I-22): join_team answers one identical text for a wrong secret,
-- an unknown team and a banned principal (with the positive control of a correct join and a differing
-- invalid_input); five failures per principal per 15 minutes then rate_limited even with the correct secret (D6);
-- 25 failures against one team from five other principals never block a sixth principal (the per-team count is
-- advisory only and is reported as team_failures); attempts persist on failure and are cleared on success;
-- joined_secret_version is recorded; rejoined is true only on a second join; the raw secret is never stored.
begin;
\ir helpers/auth.sql
select plan(51);
set local client_min_messages = warning;   -- join_team raises an advisory NOTICE past 20 team failures; keep the TAP stream clean

-- Fixtures: team T (creator C) and team U (creator D); joiners J1, J2, J6; five other principals O1..O5; a banned Bn.
select pg_temp.new_user() as c \gset
select pg_temp.new_user() as d \gset
select pg_temp.new_user() as j1 \gset
select pg_temp.new_user() as j2 \gset
select pg_temp.new_user() as j6 \gset
select pg_temp.new_user() as bn \gset
select pg_temp.login(:'c', true, 'Cleo');
select r->>'team_id' as team_t, r->>'join_secret' as secret_t from brigade.create_team('Team T', 'Cleo') as r \gset
select pg_temp.login(:'d', true, 'Dan');
select r->>'team_id' as team_u, r->>'join_secret' as secret_u from brigade.create_team('Team U', 'Dan') as r \gset
select pg_temp.logout();
select matches(:'secret_t'::text, '^brg1\.[0-9a-f-]{36}\.[0-9a-f]{32}$', 'fixture: the join secret has the brg1.<team uuid>.<32 hex> shape');
select is(split_part(:'secret_t', '.', 2), :'team_t', 'fixture: the secret names the team id');
select split_part(:'secret_t', '.', 3) as random_t \gset
-- A wrong secret for T: same team id, a different random part. An unknown team: a random uuid with a valid random part.
select 'brg1.' || :'team_t' || '.' || repeat('0', 32) as wrong_t \gset
select 'brg1.' || gen_random_uuid()::text || '.' || :'random_t' as unknown_t \gset
select isnt(:'wrong_t'::text, :'secret_t', 'fixture: the wrong secret differs from the real one');

-- 1. The raw secret is never stored (C-05): only a bcrypt hash of the random part.
select matches((select secret_hash from brigade.teams where id = :'team_t'::uuid), '^\$2a\$10\$', 'teams.secret_hash is a bcrypt ($2a$, cost 10) hash');
select is((select count(*) from brigade.teams where id = :'team_t'::uuid and (secret_hash like '%' || :'random_t' || '%' or name like '%' || :'random_t' || '%')), 0::bigint,
          'the random part of the secret appears nowhere in the teams row');
select is((select secret_version from brigade.teams where id = :'team_t'::uuid), 1, 'teams.secret_version starts at 1');

-- 2. Uniform invalid_secret (banned principal first: Bn joins, is banned by a direct update, then joins again).
select pg_temp.login(:'bn', true, 'Banned');
select is((brigade.join_team(:'secret_t', 'Banned'))->>'status', 'joined', 'fixture: Bn joined T before being banned');
select pg_temp.logout();
update brigade.memberships set status = 'banned' where team_id = :'team_t'::uuid and user_id = :'bn'::uuid;
select pg_temp.login(:'bn', true, 'Banned');
select brigade.join_team(:'secret_t', 'Banned')::text as r_banned \gset
select pg_temp.login(:'j1', true, 'Jo');
select brigade.join_team(:'wrong_t', 'Jo')::text as r_wrong \gset
select brigade.join_team(:'unknown_t', 'Jo')::text as r_unknown \gset
select brigade.join_team('not a secret', 'Jo')::text as r_malformed \gset
select is(:'r_wrong'::text, '{"status": "invalid_secret"}', 'wrong secret: {status: invalid_secret} and nothing else');
select is(:'r_unknown'::text, :'r_wrong', 'unknown team: byte-identical to a wrong secret');
select is(:'r_banned'::text, :'r_wrong', 'banned principal with the CORRECT secret: byte-identical to a wrong secret (banned looks like a wrong secret)');
select is(:'r_malformed'::text, '{"status": "invalid_input"}', 'a malformed secret is invalid_input (a different text: the identity above is not vacuous)');
select isnt(:'r_malformed'::text, :'r_wrong', 'control: invalid_input and invalid_secret differ');
select pg_temp.logout();
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'bn'::uuid), 'banned', 'a banned row stays banned after the refused rejoin');
select is((select count(*) from brigade.join_attempts where user_id = :'bn'::uuid and not succeeded), 1::bigint, 'the banned principal''s refused join is recorded as a failed attempt');

-- 3. Timing parity (recorded, not asserted): one bcrypt on the unknown-team path and one on the wrong-secret path.
select pg_temp.login(:'j1', true, 'Jo');
select clock_timestamp() as t0 \gset
select brigade.join_team(:'unknown_t', 'Jo')->>'status' as st_unknown \gset
select clock_timestamp() as t1 \gset
select brigade.join_team(:'wrong_t', 'Jo')->>'status' as st_wrong \gset
select clock_timestamp() as t2 \gset
select diag('timing (recorded, I-18): unknown team ' || round(extract(epoch from (:'t1'::timestamptz - :'t0'::timestamptz)) * 1000) || ' ms; wrong secret '
            || round(extract(epoch from (:'t2'::timestamptz - :'t1'::timestamptz)) * 1000) || ' ms (one bcrypt each)');
select is(:'st_unknown' || '/' || :'st_wrong', 'invalid_secret/invalid_secret', 'both timed calls were refused (J1 now holds 4 failed attempts)');

-- 4. Attempts persist on failure and are cleared by a success; joined_secret_version is recorded; rejoined false first.
select pg_temp.logout();
select is((select count(*) from brigade.join_attempts where user_id = :'j1'::uuid and not succeeded), 4::bigint, 'J1: four failed attempts persisted (wrong, unknown, two timed)');
select pg_temp.login(:'j1', true, 'Jo');
select brigade.join_team(:'secret_t', 'Jo') as r_j1 \gset
select is((:'r_j1'::jsonb)->>'status', 'joined', 'J1: the fifth attempt with the correct secret joins (four failures are under the limit)');
select is((:'r_j1'::jsonb)->>'rejoined', 'false', 'J1: rejoined = false on a first join');
select is((:'r_j1'::jsonb)->>'team_id', :'team_t', 'J1: team_id');
select is((:'r_j1'::jsonb)->>'team_name', 'Team T', 'J1: team_name');
select is((:'r_j1'::jsonb)->>'team_failures', '3', 'J1: team_failures counts the failures recorded against T in the window: J1''s two wrong secrets plus Bn''s refused rejoin (the two unknown-team attempts were recorded against the random team id)');
select pg_temp.logout();
select is((select count(*) from brigade.join_attempts where user_id = :'j1'::uuid and not succeeded), 0::bigint, 'J1: a successful join clears the principal''s failed attempts');
select is((select count(*) from brigade.join_attempts where user_id = :'j1'::uuid and succeeded), 1::bigint, 'J1: the success is recorded');
select is((select joined_secret_version from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'j1'::uuid), 1, 'J1: joined_secret_version recorded as the team''s current version');
select is((select human_label from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'j1'::uuid), 'Jo', 'J1: human_label stored from the join');

-- 5. Five failures per principal per 15 minutes, then rate_limited even with the correct secret (D6).
select pg_temp.login(:'j2', true, 'Jay');
select array_agg(s) as j2_fails from (select brigade.join_team(:'wrong_t', 'Jay')->>'status' as s from generate_series(1, 5)) x \gset
select is(:'j2_fails'::text, '{invalid_secret,invalid_secret,invalid_secret,invalid_secret,invalid_secret}', 'J2: five wrong secrets are five invalid_secret answers (the fifth is not yet rate_limited)');
select brigade.join_team(:'secret_t', 'Jay') as r_j2 \gset
select is((:'r_j2'::jsonb)->>'status', 'rate_limited', 'J2: the sixth attempt is rate_limited even with the CORRECT secret');
select ok(((:'r_j2'::jsonb)->>'retry_after_seconds')::int between 60 and 900, 'J2: retry_after_seconds is within the 15-minute window (>= 60)');
select is((brigade.join_team(:'wrong_t', 'Jay'))->>'status', 'rate_limited', 'J2: a wrong secret while limited is rate_limited too (no oracle through the limiter)');
select pg_temp.logout();
select is((select count(*) from brigade.join_attempts where user_id = :'j2'::uuid and not succeeded), 5::bigint, 'J2: the five failures persisted; refused-by-limiter calls add no rows');
select is((select count(*) from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'j2'::uuid), 0::bigint, 'J2: never joined');
-- The window: age J2's failures past 15 minutes as postgres and the correct secret joins.
update brigade.join_attempts set attempted_at = now() - interval '16 minutes' where user_id = :'j2'::uuid;
select pg_temp.login(:'j2', true, 'Jay');
select is((brigade.join_team(:'secret_t', 'Jay'))->>'status', 'joined', 'J2: after the 15-minute window the correct secret joins');
select pg_temp.logout();

-- 6. No per-team hard limit: 25 failures against team U from five OTHER principals, then a sixth principal joins
--    with the correct secret and the answer carries team_failures = 25.
select 'brg1.' || :'team_u' || '.' || repeat('f', 32) as wrong_u \gset
select array_agg(pg_temp.new_user()) as others from generate_series(1, 5) \gset
-- join_as(uids, secret, n): every principal in uids attempts the join n times; returns the multiset of statuses.
create function pg_temp.join_as(p_uids uuid[], p_secret text, p_n int) returns text[] language plpgsql as $$
declare r text[] := '{}'; u uuid; i int;
begin
  foreach u in array p_uids loop
    perform pg_temp.login(u, true, 'other');
    for i in 1..p_n loop r := r || (brigade.join_team(p_secret, 'other')->>'status'); end loop;
    perform pg_temp.logout();
  end loop;
  return r;
end $$;
select is((select count(*) from unnest(pg_temp.join_as(:'others'::uuid[], :'wrong_u', 5)) s where s = 'invalid_secret'), 25::bigint,
          'five other principals each fail five times against U (25 invalid_secret answers, none rate_limited)');
select is((select count(*) from brigade.join_attempts where team_id = :'team_u'::uuid and not succeeded and attempted_at > now() - interval '15 minutes'), 25::bigint,
          'team U carries 25 failed attempts in the window');
select pg_temp.login(:'j6', true, 'Six');
select brigade.join_team(:'secret_u', 'Six') as r_j6 \gset
select is((:'r_j6'::jsonb)->>'status', 'joined', 'a sixth principal with the correct secret still joins U (no per-team hard limit)');
select is((:'r_j6'::jsonb)->>'team_failures', '25', 'its answer carries team_failures = 25 (advisory)');
select is((:'r_j6'::jsonb)->>'rejoined', 'false', 'J6: first join, rejoined = false');
select pg_temp.logout();
-- ... while the per-principal limit still binds each of the five: their sixth attempt is rate_limited.
select is(pg_temp.join_as(:'others'::uuid[], :'secret_u', 1), array_fill('rate_limited'::text, array[5]),
          'each of the five other principals is rate_limited on its sixth attempt even with the CORRECT secret (D6)');

-- 7. rejoined is true only on a second join; a rotated secret_version is recorded on rejoin.
select pg_temp.login(:'j1', true, 'Jo');
select is((brigade.leave_team(:'team_t'::uuid))->>'left', 'true', 'J1 leaves T');
select pg_temp.logout();
update brigade.teams set secret_version = 2, secret_rotated_at = now() where id = :'team_t'::uuid;   -- same hash, bumped version
select pg_temp.login(:'j1', true, 'Jo');
select brigade.join_team(:'secret_t') as r_re \gset
select is((:'r_re'::jsonb)->>'status', 'joined', 'J1 rejoins T');
select is((:'r_re'::jsonb)->>'rejoined', 'true', 'J1: rejoined = true on the second join');
select pg_temp.logout();
select is((select status from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'j1'::uuid), 'active', 'J1: the row is active again');
select is((select revoked_at from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'j1'::uuid), null::timestamptz, 'J1: revoked_at cleared');
select is((select joined_secret_version from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'j1'::uuid), 2, 'J1: joined_secret_version follows the rotated team version');
select is((select human_label from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'j1'::uuid), 'Jo', 'J1: a rejoin without a label keeps the stored label');
select is((select count(*) from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'j1'::uuid), 1::bigint, 'J1: still exactly one membership row (upsert, not a duplicate)');
select pg_temp.login(:'j1', true, 'Jo');
select is((brigade.join_team(:'secret_t', 'Jo again'))->>'rejoined', 'true', 'J1: joining while already active is a rejoin as well');
select pg_temp.logout();
select is((select human_label from brigade.memberships where team_id = :'team_t'::uuid and user_id = :'j1'::uuid), 'Jo again', 'J1: a rejoin with a label updates it');

-- 8. Input caps and the unauthenticated guard.
select pg_temp.login(:'j6', true, 'Six');
select throws_ok($$select brigade.join_team('$$ || :'secret_t' || $$', repeat('x', 129))$$, '22023', 'brigade:invalid_input:human_label', 'join_team: a 129-code-point label is invalid_input:human_label');
select is((brigade.join_team(:'secret_t', repeat('é', 128)))->>'status', 'joined', 'join_team: a 128-code-point label (256 bytes) is accepted (char_length, not bytes)');
select pg_temp.logout();
select throws_ok($$select brigade.join_team('$$ || :'secret_t' || $$')$$, '28000', 'brigade:unauthenticated', 'join_team without claims is unauthenticated');

select * from finish();
rollback;
