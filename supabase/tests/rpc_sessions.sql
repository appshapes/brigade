-- rpc_sessions.sql (plan 9.3, I-33, sessions-*): register_session, resume (owned + live -> conflict:session_live
-- with the row unchanged; owned + closed or expired -> resumed = true with pending messages kept; foreign ->
-- not_found byte-identical to a random uuid), session_heartbeat (another member -> not_found; after close ->
-- conflict:session_closed), close_session, list_sessions (state from time-shifted last_seen_at, cap + truncated,
-- online-before-offline when cut, revoked members hidden, no is_self), workspace_label and inbound round-trips,
-- the registration rate limit, input caps, and byte-identical brigade:unauthorized for register_session,
-- list_sessions and list_members against another team's id and a random uuid (timing recorded).
begin;
\ir helpers/auth.sql
select plan(113);

create function pg_temp.err(q text) returns text language plpgsql as $$
begin
  execute q;
  return null;
exception when others then
  return sqlstate || ' ' || sqlerrm;
end $$;

-- Fixtures: team A (A1 creator, A2 joiner, R joiner), team B (B1 creator).
select pg_temp.new_user() as a1 \gset
select pg_temp.new_user() as a2 \gset
select pg_temp.new_user() as rr \gset
select pg_temp.new_user() as b1 \gset
select pg_temp.login(:'a1', true, 'Alice');
select r->>'team_id' as team_a, r->>'join_secret' as secret_a from brigade.create_team('Team A', 'Alice') as r \gset
select pg_temp.login(:'a2', true, 'Bob');
select is((brigade.join_team(:'secret_a', 'Bob'))->>'status', 'joined', 'fixture: A2 joins team A');
select pg_temp.login(:'rr', true, 'Rae');
select is((brigade.join_team(:'secret_a', 'Rae'))->>'status', 'joined', 'fixture: R joins team A');
select pg_temp.login(:'b1', true, 'Bea');
select r->>'team_id' as team_b from brigade.create_team('Team B', 'Bea') as r \gset
select pg_temp.logout();

-- 1. register_session: the record, every field as given, human_label from the membership.
select pg_temp.login(:'a1', true, 'Alice');
select brigade.register_session(:'team_a'::uuid, 'main', 'working on X', 'busy', 'hold', 'claude-code', '2.1.251', '/Users/alice/proj', 120) as reg \gset
select (:'reg'::jsonb)->>'session_id' as s_a1 \gset
select ok((:'reg'::jsonb)->>'session_id' is not null, 'register: session_id present');
select is((:'reg'::jsonb)->>'resumed', 'false', 'register: resumed = false on a fresh registration');
select is((:'reg'::jsonb)->>'state', 'active', 'register: busy activity reports state active');
select is((:'reg'::jsonb)->>'activity', 'busy', 'register: activity as given');
select is((:'reg'::jsonb)->>'inbound', 'hold', 'register: inbound round-trips (hold)');
select is((:'reg'::jsonb)->>'harness', 'claude-code', 'register: harness as given');
select is((:'reg'::jsonb)->>'harness_version', '2.1.251', 'register: harness_version as given');
select is((:'reg'::jsonb)->>'workspace_label', '/Users/alice/proj', 'register: workspace_label returned exactly as given');
select is((:'reg'::jsonb)->>'session_name', 'main', 'register: session_name');
select is((:'reg'::jsonb)->>'session_description', 'working on X', 'register: session_description');
select is((:'reg'::jsonb)->>'human_label', 'Alice', 'register: human_label comes from the membership row (C-12)');
select is((:'reg'::jsonb)->>'principal_ref', :'a1', 'register: principal_ref is the caller');
select is(((:'reg'::jsonb)->>'lease_seconds')::int, 120, 'register: lease_seconds as given');
select is(((:'reg'::jsonb)->>'lease_until')::timestamptz, now() + interval '120 seconds', 'register: lease_until = last_seen_at + lease_seconds');
select is(((:'reg'::jsonb)->>'last_seen_at')::timestamptz, now(), 'register: last_seen_at = now()');
select is(((:'reg'::jsonb)->>'server_time')::timestamptz, now(), 'register: server_time = now()');
select ok(not ((:'reg'::jsonb) ? 'is_self'), 'register: no is_self in SQL (the adapter computes it)');
select brigade.register_session(:'team_a'::uuid, 'bare') as reg2 \gset
select (:'reg2'::jsonb)->>'session_id' as s_a1b \gset
select is((:'reg2'::jsonb)->>'state', 'idle', 'register (defaults): idle');
select ok((:'reg2'::jsonb)->'inbound' = 'null'::jsonb and (:'reg2'::jsonb)->'workspace_label' = 'null'::jsonb, 'register (defaults): inbound and workspace_label are null when not given (never a default label)');
select is(((:'reg2'::jsonb)->>'lease_seconds')::int, 90, 'register (defaults): lease_seconds 90');
select pg_temp.logout();
select is((select workspace_label from brigade.sessions where id = :'s_a1'::uuid), '/Users/alice/proj', 'register: workspace_label stored only as given');
select is((select workspace_label from brigade.sessions where id = :'s_a1b'::uuid), null::text, 'register: no workspace_label stored when none was given');
select is((select inbound from brigade.sessions where id = :'s_a1'::uuid), 'hold', 'register: inbound stored');

-- 2. Input caps (code points, not bytes; lease 30..600 is the Supabase adapter''s range, D12).
select pg_temp.login(:'a1', true, 'Alice');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', '')$$, '22023', 'brigade:invalid_input:session_name', 'register: empty name is invalid_input:session_name');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', repeat('n', 65))$$, '22023', 'brigade:invalid_input:session_name', 'register: a 65-code-point name is invalid_input:session_name');
select lives_ok($$select brigade.register_session('$$ || :'team_a' || $$', repeat('é', 64))$$, 'register: a 64-code-point name of 128 bytes is accepted (char_length)');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n', repeat('d', 257))$$, '22023', 'brigade:invalid_input:session_description', 'register: a 257-code-point description is invalid_input');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n', null, 'sleepy')$$, '22023', 'brigade:invalid_input:activity', 'register: an unknown activity is invalid_input');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n', null, 'idle', 'maybe')$$, '22023', 'brigade:invalid_input:inbound', 'register: an unknown inbound is invalid_input');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n', null, 'idle', null, repeat('h', 33))$$, '22023', 'brigade:invalid_input:harness', 'register: a 33-code-point harness is invalid_input');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n', null, 'idle', null, null, null, repeat('w', 129))$$, '22023', 'brigade:invalid_input:workspace_label', 'register: a 129-code-point workspace_label is invalid_input');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n', null, 'idle', null, null, null, null, 29)$$, '22023', 'brigade:invalid_input:lease_seconds', 'register: lease 29 is invalid_input (min 30)');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n', null, 'idle', null, null, null, null, 601)$$, '22023', 'brigade:invalid_input:lease_seconds', 'register: lease 601 is invalid_input (max 600)');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n', null, 'idle', null, null, null, null, null)$$, '22023', 'brigade:invalid_input:lease_seconds', 'register: a null lease is invalid_input');
select lives_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n30', null, 'idle', null, null, null, null, 30)$$, 'register: lease 30 accepted');
select lives_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n600', null, 'idle', null, null, null, null, 600)$$, 'register: lease 600 accepted');
select pg_temp.logout();
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'n')$$, '28000', 'brigade:unauthenticated', 'register without claims is unauthenticated');

-- 3. Uniform unauthorized (no team-existence oracle): another team's id and a random uuid, byte-identical, for
--    register_session, list_sessions and list_members; positive control first; timing recorded.
select pg_temp.login(:'a1', true, 'Alice');
select lives_ok($$select brigade.list_sessions('$$ || :'team_a' || $$')$$, 'positive control: list_sessions on the own team works');
select clock_timestamp() as t0 \gset
select pg_temp.err($$select brigade.register_session('$$ || :'team_b' || $$', 'n')$$) as e_reg_foreign \gset
select clock_timestamp() as t1 \gset
select pg_temp.err($$select brigade.register_session(gen_random_uuid(), 'n')$$) as e_reg_random \gset
select clock_timestamp() as t2 \gset
select is(:'e_reg_foreign'::text, '42501 brigade:unauthorized', 'register_session against team B (a real team) is brigade:unauthorized');
select is(:'e_reg_foreign'::text, :'e_reg_random', 'register_session: byte-identical text for a foreign team id and a random uuid');
select is(pg_temp.err($$select brigade.list_sessions('$$ || :'team_b' || $$')$$), pg_temp.err($$select brigade.list_sessions(gen_random_uuid())$$), 'list_sessions: byte-identical text for a foreign team id and a random uuid');
select is(pg_temp.err($$select brigade.list_sessions(gen_random_uuid())$$), '42501 brigade:unauthorized', 'list_sessions: that text is brigade:unauthorized');
select is(pg_temp.err($$select brigade.list_members('$$ || :'team_b' || $$')$$), pg_temp.err($$select brigade.list_members(gen_random_uuid())$$), 'list_members: byte-identical text for a foreign team id and a random uuid');
select isnt(pg_temp.err($$select brigade.list_sessions(gen_random_uuid())$$), pg_temp.err($$select brigade.session_heartbeat(gen_random_uuid())$$), 'control: unauthorized and not_found differ, so the identities are not vacuous');
select diag('timing (recorded, I-33): register_session foreign team ' || round(extract(epoch from (:'t1'::timestamptz - :'t0'::timestamptz))::numeric * 1000, 3) || ' ms; random uuid '
            || round(extract(epoch from (:'t2'::timestamptz - :'t1'::timestamptz))::numeric * 1000, 3) || ' ms');
select pg_temp.logout();

-- 4. Resume (4.5.8; C-19, C-19b). s_a1 is open with a valid lease: conflict:session_live and the row unchanged.
select to_jsonb(s) as row_before from brigade.sessions s where id = :'s_a1'::uuid \gset
select pg_temp.login(:'a1', true, 'Alice');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'renamed', null, 'idle', null, null, null, null, 90, '$$ || :'s_a1' || $$')$$,
                 'P0001', 'brigade:conflict:session_live', 'resume of an owned OPEN session with a valid lease is conflict:session_live');
select pg_temp.logout();
select is((select to_jsonb(s) from brigade.sessions s where id = :'s_a1'::uuid), :'row_before'::jsonb, 'resume refused: the row is byte-for-byte unchanged (name, last_seen_at, lease untouched)');
-- Foreign id: A2 resuming A1's session is not_found, byte-identical to a random uuid; the owner's own (different)
-- team id is not_found too (no cross-team leak of the live state).
select pg_temp.login(:'a2', true, 'Bob');
select is(pg_temp.err($$select brigade.register_session('$$ || :'team_a' || $$', 'steal', null, 'idle', null, null, null, null, 90, '$$ || :'s_a1' || $$')$$),
          pg_temp.err($$select brigade.register_session('$$ || :'team_a' || $$', 'steal', null, 'idle', null, null, null, null, 90, gen_random_uuid())$$),
          'resume by another member: byte-identical to a random session id');
select is(pg_temp.err($$select brigade.register_session('$$ || :'team_a' || $$', 'steal', null, 'idle', null, null, null, null, 90, gen_random_uuid())$$), 'PT404 brigade:not_found', 'resume of an unknown id is brigade:not_found (PT404)');
select (brigade.register_session(:'team_a'::uuid, 'a2-main'))->>'session_id' as s_a2 \gset
select (brigade.send_message(:'s_a2'::uuid, :'s_a1'::uuid, 'parked for the resume', 'k-park'))->>'message_id' as m_park \gset
select pg_temp.login(:'b1', true, 'Bea');
select is(pg_temp.err($$select brigade.register_session('$$ || :'team_b' || $$', 'steal', null, 'idle', null, null, null, null, 90, '$$ || :'s_a1' || $$')$$), 'PT404 brigade:not_found',
          'resume of a foreign session from another team is not_found (the team check comes first only for membership; the id is simply unknown)');
select pg_temp.logout();
select is((select to_jsonb(s) from brigade.sessions s where id = :'s_a1'::uuid), :'row_before'::jsonb, 'foreign resume attempts left the row unchanged');
-- Owned + closed: reopen in place, resumed = true, pending message kept, fields updated.
select pg_temp.login(:'a1', true, 'Alice');
select is((brigade.close_session(:'s_a1'::uuid))->>'state', 'offline', 'close_session: state offline');
select is(jsonb_array_length(brigade.fetch_inbox(:'s_a1'::uuid)), 1, 'fetch_inbox on a CLOSED owned session works and holds the parked message (C-31)');
select is(jsonb_array_length(brigade.list_sessions(:'team_a'::uuid)->'sessions') , (select count(*)::int from brigade.sessions where team_id = :'team_a'::uuid and closed_at is null), 'list_sessions (default) omits the closed session');
select brigade.register_session(:'team_a'::uuid, 'main again', 'resumed', 'busy', 'accept', null, null, '/elsewhere', 200, :'s_a1'::uuid) as res \gset
select is((:'res'::jsonb)->>'resumed', 'true', 'resume of an owned CLOSED session: resumed = true');
select is((:'res'::jsonb)->>'session_id', :'s_a1', 'resume: the same session_id');
select is((:'res'::jsonb)->>'session_name', 'main again', 'resume: name updated in place');
select is((:'res'::jsonb)->>'workspace_label', '/elsewhere', 'resume: workspace_label updated as given');
select is(((:'res'::jsonb)->>'lease_seconds')::int, 200, 'resume: lease updated');
select is((:'res'::jsonb)->>'state', 'active', 'resume: state from the new activity');
select is(jsonb_array_length(brigade.fetch_inbox(:'s_a1'::uuid)), 1, 'resume: the pending message is still there');
select is((brigade.fetch_inbox(:'s_a1'::uuid))->0->>'message_id', :'m_park', 'resume: and it is the parked one');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'x', null, 'idle', null, null, null, null, 90, '$$ || :'s_a1' || $$')$$,
                 'P0001', 'brigade:conflict:session_live', 'resume of the just-resumed (live again) session is conflict:session_live');
select pg_temp.logout();
select ok((select closed_at is null from brigade.sessions where id = :'s_a1'::uuid), 'resume: closed_at cleared');
select is((select owner_id from brigade.sessions where id = :'s_a1'::uuid), :'a1'::uuid, 'resume: owner unchanged');
-- Owned + expired (lease 200, last_seen_at backdated past it as postgres): resumed = true, last_seen_at fresh.
update brigade.sessions set last_seen_at = now() - interval '201 seconds' where id = :'s_a1'::uuid;
select pg_temp.login(:'a1', true, 'Alice');
select brigade.register_session(:'team_a'::uuid, 'after expiry', null, 'idle', null, null, null, null, 90, :'s_a1'::uuid) as res2 \gset
select is((:'res2'::jsonb)->>'resumed', 'true', 'resume of an owned EXPIRED session: resumed = true');
select is(((:'res2'::jsonb)->>'last_seen_at')::timestamptz, now(), 'resume: last_seen_at is now()');
select is(((:'res2'::jsonb)->>'lease_until')::timestamptz, now() + interval '90 seconds', 'resume: lease_until from the new lease');
select pg_temp.logout();

-- 5. Heartbeat: another member -> not_found (identical to a random id); the owner updates last_seen_at and fields;
--    input caps; after close -> conflict:session_closed.
select pg_temp.login(:'a2', true, 'Bob');
select is(pg_temp.err($$select brigade.session_heartbeat('$$ || :'s_a1' || $$')$$), pg_temp.err($$select brigade.session_heartbeat(gen_random_uuid())$$), 'heartbeat by another member: byte-identical to a random session id');
select is(pg_temp.err($$select brigade.session_heartbeat(gen_random_uuid())$$), 'PT404 brigade:not_found', 'heartbeat: that text is brigade:not_found');
select pg_temp.logout();
update brigade.sessions set last_seen_at = now() - interval '30 seconds' where id = :'s_a1'::uuid;
select pg_temp.login(:'a1', true, 'Alice');
select brigade.session_heartbeat(:'s_a1'::uuid, 'busy', 'hb-name', 'hb-desc', 'refuse', 300) as hb \gset
select is((:'hb'::jsonb)->>'session_id', :'s_a1', 'heartbeat: session_id echoed');
select is((:'hb'::jsonb)->>'state', 'active', 'heartbeat: state from the new activity');
select is(((:'hb'::jsonb)->>'lease_until')::timestamptz, now() + interval '300 seconds', 'heartbeat: lease_until = now() + the new lease');
select is(((:'hb'::jsonb)->>'server_time')::timestamptz, now(), 'heartbeat: server_time');
select lives_ok($$select brigade.session_heartbeat('$$ || :'s_a1' || $$')$$, 'heartbeat with no optional argument is accepted');
select throws_ok($$select brigade.session_heartbeat('$$ || :'s_a1' || $$', null, null, null, null, 29)$$, '22023', 'brigade:invalid_input:lease_seconds', 'heartbeat: lease 29 is invalid_input');
select throws_ok($$select brigade.session_heartbeat('$$ || :'s_a1' || $$', 'sleepy')$$, '22023', 'brigade:invalid_input:activity', 'heartbeat: an unknown activity is invalid_input');
select throws_ok($$select brigade.session_heartbeat('$$ || :'s_a1' || $$', null, repeat('n', 65))$$, '22023', 'brigade:invalid_input:session_name', 'heartbeat: a 65-code-point name is invalid_input');
select pg_temp.logout();
select is((select last_seen_at from brigade.sessions where id = :'s_a1'::uuid), now(), 'heartbeat: last_seen_at touched');
select is((select (activity, name, description, inbound, lease_seconds)::text from brigade.sessions where id = :'s_a1'::uuid), '(busy,hb-name,hb-desc,refuse,300)', 'heartbeat: activity, name, description, inbound and lease stored; a later null keeps them');
select pg_temp.login(:'a1', true, 'Alice');
select lives_ok($$select brigade.close_session('$$ || :'s_a1' || $$')$$, 'close_session on the open session');
select throws_ok($$select brigade.session_heartbeat('$$ || :'s_a1' || $$')$$, 'P0001', 'brigade:conflict:session_closed', 'heartbeat after close is conflict:session_closed');
select is((brigade.close_session(:'s_a1'::uuid))->>'state', 'offline', 'close_session is idempotent');
select is(pg_temp.err($$select brigade.close_session('$$ || :'s_a2' || $$')$$), 'PT404 brigade:not_found', 'close_session of another member''s session is not_found');
select pg_temp.logout();
select is((select activity from brigade.sessions where id = :'s_a1'::uuid), 'idle', 'close_session sets activity idle');

-- 6. list_sessions: state from time-shifted last_seen_at, offline omitted unless asked, cap + truncated, online
--    before offline when cut, human_label on every record, revoked members hidden.
select pg_temp.login(:'a1', true, 'Alice');
select (brigade.register_session(:'team_a'::uuid, 'l-idle', null, 'idle', null, null, null, null, 60))->>'session_id' as l1 \gset
select (brigade.register_session(:'team_a'::uuid, 'l-busy', null, 'busy', null, null, null, null, 60))->>'session_id' as l2 \gset
select (brigade.register_session(:'team_a'::uuid, 'l-stale', null, 'idle', null, null, null, null, 60))->>'session_id' as l5 \gset
select (brigade.register_session(:'team_a'::uuid, 'l-closed2', null, 'idle', null, null, null, null, 60))->>'session_id' as l6 \gset
select lives_ok($$select brigade.close_session('$$ || :'l6' || $$'::uuid)$$, 'fixture: l-closed2 (A1''s) closed, its last_seen_at stays now()');
select pg_temp.login(:'a2', true, 'Bob');
select (brigade.register_session(:'team_a'::uuid, 'l-expired', null, 'busy', null, null, null, null, 60))->>'session_id' as l3 \gset
select (brigade.register_session(:'team_a'::uuid, 'l-closed', null, 'idle', null, null, null, null, 60))->>'session_id' as l4 \gset
select lives_ok($$select brigade.close_session('$$ || :'l4' || $$')$$, 'fixture: l-closed closed (last_seen_at stays now(), the most recent of all)');
select pg_temp.logout();
update brigade.sessions set last_seen_at = now() - interval '61 seconds' where id = :'l3'::uuid;   -- lease 60: expired by 1 s
update brigade.sessions set last_seen_at = now() - interval '30 seconds' where id = :'l5'::uuid;   -- lease 60: still valid
update brigade.sessions set last_seen_at = now() - interval '10 seconds' where id = :'l1'::uuid;   -- lease 60: online, but seen LESS recently than the two closed sessions
update brigade.sessions set last_seen_at = now() - interval '20 seconds' where id = :'l2'::uuid;   -- lease 60: online
-- keep the earlier fixture sessions of this file out of the picture: close them and age them so they are offline
update brigade.sessions set closed_at = now(), last_seen_at = now() - interval '1 hour' where team_id = :'team_a'::uuid and id not in (:'l1'::uuid, :'l2'::uuid, :'l3'::uuid, :'l4'::uuid, :'l5'::uuid, :'l6'::uuid);
create function pg_temp.state_of(p_list jsonb, p_id uuid) returns text language sql as $$
  select s->>'state' from jsonb_array_elements(p_list->'sessions') s where s->>'session_id' = p_id::text $$;
select pg_temp.login(:'a1', true, 'Alice');
select brigade.list_sessions(:'team_a'::uuid) as ls \gset
select brigade.list_sessions(:'team_a'::uuid, true) as ls_all \gset
select is(jsonb_array_length((:'ls'::jsonb)->'sessions'), 3, 'list_sessions (default): only the three online sessions');
select is(pg_temp.state_of(:'ls'::jsonb, :'l1'::uuid), 'idle', 'list_sessions: idle session reported idle');
select is(pg_temp.state_of(:'ls'::jsonb, :'l2'::uuid), 'active', 'list_sessions: busy session reported active');
select is(pg_temp.state_of(:'ls'::jsonb, :'l5'::uuid), 'idle', 'list_sessions: last_seen_at 30 s ago with lease 60 is still idle (online)');
select is(pg_temp.state_of(:'ls'::jsonb, :'l3'::uuid), null::text, 'list_sessions (default): the expired session (61 s ago, lease 60) is omitted');
select is(pg_temp.state_of(:'ls'::jsonb, :'l4'::uuid), null::text, 'list_sessions (default): the closed session is omitted');
select is(pg_temp.state_of(:'ls_all'::jsonb, :'l3'::uuid), 'offline', 'list_sessions (include_offline): the expired session is offline');
select is(pg_temp.state_of(:'ls_all'::jsonb, :'l4'::uuid), 'offline', 'list_sessions (include_offline): the closed session is offline');
select is((select s->>'lease_until' from jsonb_array_elements((:'ls'::jsonb)->'sessions') s where s->>'session_id' = :'l5'), to_jsonb(now() + interval '30 seconds')#>>'{}', 'list_sessions: lease_until = last_seen_at + lease_seconds (30 s left)');
select is((:'ls'::jsonb)->>'truncated', 'false', 'list_sessions: truncated = false when nothing was cut');
select is((:'ls'::jsonb)->>'team_name', 'Team A', 'list_sessions: team_name');
select is((select count(*) from jsonb_array_elements((:'ls_all'::jsonb)->'sessions') s where s->>'human_label' is null), 0::bigint, 'list_sessions: every record carries human_label (C-12)');
select is((select s->>'human_label' from jsonb_array_elements((:'ls_all'::jsonb)->'sessions') s where s->>'session_id' = :'l3'), 'Bob', 'list_sessions: the label is the owner''s membership label');
select is((select count(*) from jsonb_array_elements((:'ls_all'::jsonb)->'sessions') s where s ? 'is_self'), 0::bigint, 'list_sessions: no is_self field (the adapter''s job)');
select brigade.list_sessions(:'team_a'::uuid, true, 2) as ls_cut \gset
select is(jsonb_array_length((:'ls_cut'::jsonb)->'sessions'), 2, 'list_sessions (limit 2): two records');
select is((:'ls_cut'::jsonb)->>'truncated', 'true', 'list_sessions (limit 2): truncated = true');
select is((select count(*) from jsonb_array_elements((:'ls_cut'::jsonb)->'sessions') s where s->>'state' <> 'offline'), 2::bigint,
          'list_sessions (include_offline, cut): online sessions come first, so the most recently seen but CLOSED session is not preferred over an online one');
select is((select array_agg(s->>'session_id' order by s->>'session_id') from jsonb_array_elements((:'ls_cut'::jsonb)->'sessions') s), (select array_agg(x order by x) from unnest(array[:'l1', :'l2']) x),
          'list_sessions (include_offline, cut): the two records are the two most recently seen ONLINE sessions, although two CLOSED sessions (l-closed, l-closed2) were seen more recently: the cut is applied after ordering online-first');
select is((brigade.list_sessions(:'team_a'::uuid, false, 3))->>'truncated', 'false', 'list_sessions (limit = count): not truncated');
select is((brigade.list_sessions(:'team_a'::uuid, false, 0))->>'truncated', 'true', 'list_sessions (limit 0 is clamped to 1): truncated');
select is(jsonb_array_length((brigade.list_sessions(:'team_a'::uuid, false, 0))->'sessions'), 1, 'list_sessions (limit 0): one record');
-- revoked members hidden (baseline: A2's sessions were listed above).
select pg_temp.logout();
update brigade.memberships set status = 'revoked', revoked_at = now() where team_id = :'team_a'::uuid and user_id = :'a2'::uuid;
select pg_temp.login(:'a1', true, 'Alice');
select is((select count(*) from jsonb_array_elements(brigade.list_sessions(:'team_a'::uuid, true)->'sessions') s where s->>'principal_ref' = :'a2'), 0::bigint, 'list_sessions: a revoked member''s sessions are hidden (baseline listed two of them)');
select is(jsonb_array_length(brigade.list_sessions(:'team_a'::uuid, true)->'sessions'), (select count(*)::int from brigade.sessions where team_id = :'team_a'::uuid and owner_id <> :'a2'::uuid), 'list_sessions (include_offline): every remaining session of active members is listed');
select pg_temp.logout();

-- 7. Registration rate limit: 30 per principal per hour, the 31st is rate_limited:register_session:3600.
create function pg_temp.register_n(p_uid uuid, p_team uuid, p_n int) returns int language plpgsql as $$
declare i int; n int := 0;
begin
  perform pg_temp.login(p_uid, true, 'Rae');
  for i in 1..p_n loop perform brigade.register_session(p_team, 'burst-' || i); n := n + 1; end loop;
  perform pg_temp.logout();
  return n;
end $$;
select is(pg_temp.register_n(:'rr'::uuid, :'team_a'::uuid, 30), 30, 'R registers 30 sessions within the hour');
select pg_temp.login(:'rr', true, 'Rae');
select throws_ok($$select brigade.register_session('$$ || :'team_a' || $$', 'one too many')$$, 'P0001', 'brigade:rate_limited:register_session:3600', 'the 31st registration in an hour is rate_limited:register_session:3600');
select pg_temp.logout();

select * from finish();
rollback;
