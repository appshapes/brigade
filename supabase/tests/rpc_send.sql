-- rpc_send.sql (plan 9.3, I-02, I-06, I-07, I-26, I-27, I-28, I-29): send_message ownership and the uniform
-- not_found (foreign-team recipient, revoked member's session and a random uuid byte-identical, with positive
-- controls), closed sender -> conflict while a closed recipient is accepted and readable (C-31), self-send,
-- idempotency (same key + same body -> the ORIGINAL answer with duplicate = true; different body OR different
-- recipient -> conflict, never duplicate), acks (C-30), the six rate limits in order (per session 20/min and 200/h,
-- per principal 60/min and 600/h shared across sessions, per-pair 15 BEFORE recipient 60 with both caps at their
-- limit at once), reply_to not received -> not_found, the explicit hop chain (rotating, ack-free) and the implicit
-- chain (one pair, acked mid-chain) to loop_detected, the 600 s implicit window, the 16 KiB body in bytes,
-- last_seen_at touched by a send, and fetch_inbox order and paging.
begin;
\ir helpers/auth.sql
select plan(107);

create function pg_temp.err(q text) returns text language plpgsql as $$
begin
  execute q;
  return null;
exception when others then
  return sqlstate || ' ' || sqlerrm;
end $$;

-- send_hop(sender, recipient, body, key): the hop_count of an RPC send as text, or '<sqlstate> <message>' when it
-- raises, so an assertion on the hop reports the rule (e.g. loop_detected) instead of aborting the file.
create function pg_temp.send_hop(p_sender uuid, p_recipient uuid, p_body text, p_key text) returns text language plpgsql as $$
begin
  return (brigade.send_message(p_sender, p_recipient, p_body, p_key)->>'hop_count');
exception when others then
  return sqlstate || ' ' || sqlerrm;
end $$;

-- seed(sender, recipient, n, age, acked): n messages written as postgres with the sender owner's claims (the
-- stamping trigger runs), then time-shifted; acked rows are marked injected so they never count towards the
-- unacknowledged caps. Call as postgres (it logs out at the end).
create function pg_temp.seed(p_sender uuid, p_recipient uuid, p_n int, p_age interval, p_acked boolean) returns int language plpgsql as $$
declare v_owner uuid; v_team uuid; i int; v_id uuid;
begin
  select owner_id, team_id into v_owner, v_team from brigade.sessions where id = p_sender;
  perform pg_temp.as_user(v_owner);
  for i in 1..p_n loop
    insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id, body, body_hash, idempotency_key)
    values (v_team, v_owner, p_sender, p_recipient, 'seed', '\x00', 'seed-' || gen_random_uuid()::text) returning id into v_id;
    update brigade.messages set created_at = now() - p_age,
           delivery_state = case when p_acked then 'injected' else 'accepted' end,
           injected_at = case when p_acked then now() - p_age else null end
     where id = v_id;
  end loop;
  perform pg_temp.logout();
  return p_n;
end $$;

-- send_n(owner, sender, recipient, n, prefix): n RPC sends as the owner; returns the statuses (ok or the error text).
create function pg_temp.send_n(p_owner uuid, p_sender uuid, p_recipient uuid, p_n int, p_prefix text) returns text[] language plpgsql as $$
declare i int; r text[] := '{}';
begin
  perform pg_temp.login(p_owner);
  for i in 1..p_n loop
    begin
      perform brigade.send_message(p_sender, p_recipient, 'bulk ' || i, p_prefix || '-' || i);
      r := array_append(r, 'ok');
    exception when others then
      r := array_append(r, sqlstate || ' ' || sqlerrm);
    end;
  end loop;
  perform pg_temp.logout();
  return r;
end $$;

-- seed_spread(sessions, recipient, target, window, session_cap, age): seed acked messages over several sessions of
-- one principal until the principal holds `target` messages inside `window`, never letting one session reach
-- `session_cap` inside it, so the next refusal can only be the principal's cap. Returns the number seeded.
create function pg_temp.seed_spread(p_sessions uuid[], p_recipient uuid, p_target int, p_window interval, p_session_cap int, p_age interval) returns int language plpgsql as $$
declare v_owner uuid; v_have int; v_cur int; v_n int; v_total int := 0; s uuid;
begin
  select owner_id into v_owner from brigade.sessions where id = p_sessions[1];
  foreach s in array p_sessions loop
    select count(*) into v_have from brigade.messages where sender_user_id = v_owner and created_at > now() - p_window;
    exit when v_have >= p_target;
    select count(*) into v_cur from brigade.messages where sender_session_id = s and created_at > now() - p_window;
    v_n := least(p_session_cap - 1 - v_cur, p_target - v_have);
    if v_n > 0 then v_total := v_total + pg_temp.seed(s, p_recipient, v_n, p_age, true); end if;
  end loop;
  return v_total;
end $$;

-- Fixtures through the RPCs. Team A: A (a1..a4), B (b1, b2), C (c1), D (d1), R (r1, revoked later), the explicit
-- chain trio P/Q/W (p1, q1, w1), the implicit pair U/V (u1, v1) and the window pair X/Y (x1, y1). Team Z: Z (z1).
select pg_temp.new_user() as ua \gset
select pg_temp.new_user() as ub \gset
select pg_temp.new_user() as uc \gset
select pg_temp.new_user() as ud \gset
select pg_temp.new_user() as ur \gset
select pg_temp.new_user() as up \gset
select pg_temp.new_user() as uq \gset
select pg_temp.new_user() as uw \gset
select pg_temp.new_user() as uu \gset
select pg_temp.new_user() as uv \gset
select pg_temp.new_user() as ux \gset
select pg_temp.new_user() as uy \gset
select pg_temp.new_user() as uz \gset
select pg_temp.login(:'ua', true, 'Alice');
select r->>'team_id' as team_a, r->>'join_secret' as secret_a from brigade.create_team('Team A', 'Alice') as r \gset
select (brigade.register_session(:'team_a'::uuid, 'a1'))->>'session_id' as a1 \gset
select (brigade.register_session(:'team_a'::uuid, 'a2'))->>'session_id' as a2 \gset
select (brigade.register_session(:'team_a'::uuid, 'a3'))->>'session_id' as a3 \gset
select (brigade.register_session(:'team_a'::uuid, 'a4'))->>'session_id' as a4 \gset
create function pg_temp.join_and_register(p_uid uuid, p_label text, p_secret text, p_team uuid, p_names text[]) returns uuid[] language plpgsql as $$
declare ids uuid[] := '{}'; n text;
begin
  perform pg_temp.login(p_uid, true, p_label);
  if (brigade.join_team(p_secret, p_label)->>'status') <> 'joined' then raise exception 'fixture join failed for %', p_label; end if;
  foreach n in array p_names loop ids := ids || (brigade.register_session(p_team, n)->>'session_id')::uuid; end loop;
  perform pg_temp.logout();
  return ids;
end $$;
select pg_temp.logout();
select (pg_temp.join_and_register(:'ub', 'Bob', :'secret_a', :'team_a'::uuid, array['b1', 'b2'])) as bs \gset
select (:'bs'::uuid[])[1] as b1, (:'bs'::uuid[])[2] as b2 \gset
select (pg_temp.join_and_register(:'uc', 'Cy', :'secret_a', :'team_a'::uuid, array['c1']))[1] as c1 \gset
select (pg_temp.join_and_register(:'ud', 'Di', :'secret_a', :'team_a'::uuid, array['d1']))[1] as d1 \gset
select (pg_temp.join_and_register(:'ur', 'Rae', :'secret_a', :'team_a'::uuid, array['r1']))[1] as r1 \gset
select (pg_temp.join_and_register(:'up', 'Pat', :'secret_a', :'team_a'::uuid, array['p1']))[1] as p1 \gset
select (pg_temp.join_and_register(:'uq', 'Quin', :'secret_a', :'team_a'::uuid, array['q1']))[1] as q1 \gset
select (pg_temp.join_and_register(:'uw', 'Wil', :'secret_a', :'team_a'::uuid, array['w1']))[1] as w1 \gset
select (pg_temp.join_and_register(:'uu', 'Uma', :'secret_a', :'team_a'::uuid, array['u1']))[1] as u1 \gset
select (pg_temp.join_and_register(:'uv', 'Val', :'secret_a', :'team_a'::uuid, array['v1']))[1] as v1 \gset
select (pg_temp.join_and_register(:'ux', 'Xen', :'secret_a', :'team_a'::uuid, array['x1']))[1] as x1 \gset
select (pg_temp.join_and_register(:'uy', 'Yul', :'secret_a', :'team_a'::uuid, array['y1']))[1] as y1 \gset
select pg_temp.login(:'uz', true, 'Zed');
select r->>'team_id' as team_z from brigade.create_team('Team Z', 'Zed') as r \gset
select (brigade.register_session(:'team_z'::uuid, 'z1'))->>'session_id' as z1 \gset
select pg_temp.logout();
select is((select count(*) from brigade.memberships where team_id = :'team_a'::uuid and status = 'active'), 12::bigint, 'fixture: twelve active members of team A');

-- 1. A successful send (the positive control for everything below), then ownership and the uniform not_found.
select pg_temp.login(:'ua', true, 'Alice');
select brigade.send_message(:'a1'::uuid, :'b1'::uuid, 'hello', 'k-hello', 'a summary') as first \gset
select is((:'first'::jsonb)->>'duplicate', 'false', 'send: duplicate = false on a first send');
select is(((:'first'::jsonb)->>'hop_count')::int, 0, 'send: a first message between fresh sessions is hop 0');
select is((:'first'::jsonb)->>'recipient_session_id', :'b1', 'send: recipient_session_id echoed');
select is(((:'first'::jsonb)->>'created_at')::timestamptz, now(), 'send: created_at is now()');
select (:'first'::jsonb)->>'message_id' as m_hello \gset
select is(pg_temp.err($$select brigade.send_message('$$ || :'b1' || $$', '$$ || :'a1' || $$', 'x', 'k-own')$$), 'PT404 brigade:not_found', 'send from a session the caller does not own is brigade:not_found (PT404)');
select is(pg_temp.err($$select brigade.send_message('$$ || :'b1' || $$', '$$ || :'a1' || $$', 'x', 'k-own')$$), pg_temp.err($$select brigade.send_message(gen_random_uuid(), '$$ || :'a1' || $$', 'x', 'k-own')$$), 'send: not-owned sender and unknown sender are byte-identical');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'z1' || $$', 'x', 'k-z')$$), pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', gen_random_uuid(), 'x', 'k-z')$$), 'send: a foreign-team recipient is byte-identical to a random uuid (C-25)');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', gen_random_uuid(), 'x', 'k-z')$$), 'PT404 brigade:not_found', 'send: that text is brigade:not_found');
select isnt(pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', gen_random_uuid(), 'x', 'k-z')$$), pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'a1' || $$', 'x', 'k-self')$$), 'control: not_found and the self-send error differ, so the identities are not vacuous');
select throws_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'a1' || $$', 'x', 'k-self')$$, '22023', 'brigade:invalid_input:recipient_is_self', 'send to the sender session itself is invalid_input:recipient_is_self');
select lives_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'a2' || $$', 'to my other session', 'k-a2')$$, 'send to another session of the same principal is accepted');
select is(((brigade.send_message(:'a1'::uuid, :'r1'::uuid, 'before revocation', 'k-r-before'))->>'duplicate'), 'false', 'baseline: a send to R''s session succeeds while R is active');
select pg_temp.logout();
update brigade.memberships set status = 'revoked', revoked_at = now() where team_id = :'team_a'::uuid and user_id = :'ur'::uuid;
select pg_temp.login(:'ua', true, 'Alice');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'r1' || $$', 'x', 'k-r-after')$$), pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', gen_random_uuid(), 'x', 'k-r-after')$$), 'send to a revoked member''s session: byte-identical to a random uuid (C-08)');
select pg_temp.logout();
select throws_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', 'k-noauth')$$, '28000', 'brigade:unauthenticated', 'send without claims is unauthenticated');

-- 2. Input caps: body in BYTES (1..16384), summary and key in code points.
select pg_temp.login(:'ua', true, 'Alice');
select throws_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', '', 'k-empty')$$, '22023', 'brigade:invalid_input:body', 'send: an empty body is invalid_input:body');
select throws_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', repeat('x', 16385), 'k-big')$$, '22023', 'brigade:invalid_input:body', 'send: 16385 bytes is invalid_input:body');
select throws_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', repeat('€', 5462), 'k-big-mb')$$, '22023', 'brigade:invalid_input:body', 'send: 5462 three-byte code points (16386 bytes) is invalid_input:body (octet_length, not char_length)');
select lives_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', repeat('x', 16384), 'k-max')$$, 'send: exactly 16384 bytes is accepted');
select throws_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', 'k-sum', repeat('s', 201))$$, '22023', 'brigade:invalid_input:summary', 'send: a 201-code-point summary is invalid_input:summary');
select throws_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', repeat('k', 129))$$, '22023', 'brigade:invalid_input:idempotency_key', 'send: a 129-code-point key is invalid_input:idempotency_key');
select throws_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', '')$$, '22023', 'brigade:invalid_input:idempotency_key', 'send: an empty key is invalid_input:idempotency_key');
select pg_temp.logout();

-- 3. Closed sessions (C-15, C-31): send FROM a closed session is conflict; send TO a closed session is accepted and
--    the owner can read and ack it on the closed session.
select pg_temp.login(:'ua', true, 'Alice');
select lives_ok($$select brigade.close_session('$$ || :'a2' || $$')$$, 'fixture: a2 closed');
select throws_ok($$select brigade.send_message('$$ || :'a2' || $$', '$$ || :'b1' || $$', 'x', 'k-closed')$$, 'P0001', 'brigade:conflict:sender_closed', 'send FROM a closed session is conflict:sender_closed');
select pg_temp.login(:'ub', true, 'Bob');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a2' || $$', '$$ || :'b1' || $$', 'x', 'k-closed-foreign')$$), pg_temp.err($$select brigade.send_message(gen_random_uuid(), '$$ || :'b1' || $$', 'x', 'k-closed-foreign')$$), 'send from a CLOSED session the caller does not own is byte-identical to an unknown sender (ownership is checked before the closed state: no closed-state oracle)');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a2' || $$', '$$ || :'b1' || $$', 'x', 'k-closed-foreign')$$), 'PT404 brigade:not_found', 'send from a closed session the caller does not own is brigade:not_found, never conflict:sender_closed');
select pg_temp.login(:'ua', true, 'Alice');
select (brigade.send_message(:'a1'::uuid, :'a2'::uuid, 'parked on a closed session', 'k-park'))->>'message_id' as m_park \gset
select is((select count(*) from jsonb_array_elements(brigade.fetch_inbox(:'a2'::uuid)) e where e->>'message_id' = :'m_park'), 1::bigint, 'send TO a closed session is accepted and fetch_inbox on the closed owned session shows it (C-31)');
select is((brigade.ack_messages(:'a2'::uuid, array[:'m_park'::uuid]))->'acked', to_jsonb(array[:'m_park'::uuid]), 'ack_messages on the closed owned session acks it');
select pg_temp.logout();

-- 4. Idempotency (4.5.4; C-21, C-22).
select pg_temp.login(:'ua', true, 'Alice');
select brigade.send_message(:'a1'::uuid, :'b1'::uuid, 'same body', 'k-idem') as i1 \gset
select brigade.send_message(:'a1'::uuid, :'b1'::uuid, 'same body', 'k-idem') as i2 \gset
select is((:'i1'::jsonb)->>'duplicate', 'false', 'idempotency: first send is not a duplicate');
select is((:'i2'::jsonb)->>'duplicate', 'true', 'idempotency: same session + same key + same body is duplicate = true');
select is((:'i2'::jsonb) - 'duplicate', (:'i1'::jsonb) - 'duplicate', 'idempotency: the duplicate answer is the ORIGINAL response (message_id, recipient, created_at, hop_count)');
select throws_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'different body', 'k-idem')$$, 'P0001', 'brigade:conflict:idempotency_key', 'idempotency: same key with a different body is conflict:idempotency_key, never duplicate');
select throws_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b2' || $$', 'same body', 'k-idem')$$, 'P0001', 'brigade:conflict:idempotency_key', 'idempotency: same key and body with a DIFFERENT recipient is conflict, never duplicate');
select is(((brigade.send_message(:'a3'::uuid, :'b1'::uuid, 'same body', 'k-idem'))->>'duplicate'), 'false', 'idempotency: the key is scoped to the sender session (another session of the same principal sends a new message)');
select pg_temp.logout();
select is((select count(*) from brigade.messages where sender_session_id = :'a1'::uuid and idempotency_key = 'k-idem'), 1::bigint, 'idempotency: exactly one row for the key');

-- 5. Acks (4.5.3; C-30): owned + pending -> acked; owned + already injected -> acked again; not owned or unknown ->
--    unknown, never an error; both arrays always present.
select pg_temp.login(:'ub', true, 'Bob');
select brigade.ack_messages(:'b1'::uuid, array[:'m_hello'::uuid, :'m_park'::uuid, gen_random_uuid()]) as ack1 \gset
select is((:'ack1'::jsonb)->'acked', to_jsonb(array[:'m_hello'::uuid]), 'ack: the owned pending message is acked');
select is(jsonb_array_length((:'ack1'::jsonb)->'unknown'), 2, 'ack: a message addressed to another session and a random id are unknown (not errors)');
select is((brigade.ack_messages(:'b1'::uuid, array[:'m_hello'::uuid]))->'acked', to_jsonb(array[:'m_hello'::uuid]), 'ack: acking an already injected owned message is acked again (idempotent)');
select is(brigade.ack_messages(:'b1'::uuid, '{}'::uuid[]), '{"acked": [], "unknown": []}'::jsonb, 'ack: both arrays present as [] when empty');
select pg_temp.logout();
select id as m_other from brigade.messages where sender_session_id = :'a1'::uuid and idempotency_key = 'k-a2' \gset
select pg_temp.login(:'ub', true, 'Bob');
select is((brigade.ack_messages(:'b1'::uuid, array[:'m_other'::uuid]))->'unknown', to_jsonb(array[:'m_other'::uuid]), 'ack: a PENDING message addressed to another session is unknown');
select pg_temp.logout();
select is((select delivery_state from brigade.messages where id = :'m_other'::uuid), 'accepted', 'ack: ... and it stays accepted (an ack through one session never flips another session''s message)');
select pg_temp.login(:'ub', true, 'Bob');
select is((select count(*) from jsonb_array_elements(brigade.fetch_inbox(:'b1'::uuid)) e where e->>'message_id' = :'m_hello'), 0::bigint, 'ack: fetch_inbox no longer returns the acked message');
select pg_temp.login(:'ua', true, 'Alice');
select is((brigade.ack_messages(:'a1'::uuid, array[:'m_hello'::uuid]))->'unknown', to_jsonb(array[:'m_hello'::uuid]), 'ack: the sender acking its own SENT message through its session gets unknown (it does not own the recipient side)');
select pg_temp.logout();
select is((select delivery_state from brigade.messages where id = :'m_hello'::uuid), 'injected', 'ack: delivery_state injected after the ack');
select is((select injected_at from brigade.messages where id = :'m_hello'::uuid), now(), 'ack: injected_at stamped');

-- 6. Rate limits (4.5.12; C-28), in order. The transaction's now() is fixed, so every row written here is "within
--    the last minute" unless backdated.
-- 6a. per sender session, 20/min: a3 is fresh in this minute apart from one idempotency send.
select is((select count(*) from brigade.messages where sender_session_id = :'a3'::uuid), 1::bigint, 'fixture: a3 has sent one message so far');
select is((select count(*) from unnest(pg_temp.send_n(:'ua'::uuid, :'a3'::uuid, :'b2'::uuid, 10, 'min') || pg_temp.send_n(:'ua'::uuid, :'a3'::uuid, :'d1'::uuid, 9, 'min2')) s where s = 'ok'), 19::bigint, 'a3 sends 19 more (20 in the minute, split over two recipients so no pair reaches its cap of 15)');
select pg_temp.login(:'ua', true, 'Alice');
select pg_temp.err($$select brigade.send_message('$$ || :'a3' || $$', '$$ || :'b1' || $$', 'x', 'min-21')$$) as e_min \gset
select is(:'e_min'::text, 'P0001 brigade:rate_limited:send_per_minute:60', 'the 21st send from one session within a minute is rate_limited:send_per_minute:60');
select pg_temp.logout();
update brigade.messages set created_at = now() - interval '2 minutes' where sender_session_id = :'a3'::uuid;
select pg_temp.login(:'ua', true, 'Alice');
select lives_ok($$select brigade.send_message('$$ || :'a3' || $$', '$$ || :'b1' || $$', 'x', 'min-after')$$, 'after backdating those sends by 2 minutes the session may send again (the window is one minute)');
select pg_temp.logout();
-- 6b. per sender session, 200/h: a3 now has 21 in the hour; seed 179 acked ones aged 30 minutes.
select is(pg_temp.seed(:'a3'::uuid, :'b2'::uuid, 179, interval '30 minutes', true), 179, 'seed: 179 acked messages from a3, 30 minutes old');
select is((select count(*) from brigade.messages where sender_session_id = :'a3'::uuid and created_at > now() - interval '1 hour'), 200::bigint, 'fixture: a3 has 200 messages in the hour');
select pg_temp.login(:'ua', true, 'Alice');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a3' || $$', '$$ || :'b1' || $$', 'x', 'hour-201')$$), 'P0001 brigade:rate_limited:send_per_hour:3600', 'the 201st send from one session within an hour is rate_limited:send_per_hour:3600');
select pg_temp.logout();
update brigade.messages set created_at = created_at - interval '2 hours' where sender_session_id = :'a3'::uuid;
select pg_temp.login(:'ua', true, 'Alice');
select lives_ok($$select brigade.send_message('$$ || :'a3' || $$', '$$ || :'b1' || $$', 'x', 'hour-after')$$, 'after aging them past the hour the session may send again');
select pg_temp.logout();
-- 6c. per principal, 60/min, shared across sessions: top A up to 60 in the minute from a4 (acked seeds, age 0).
select (60 - count(*))::int as need_min from brigade.messages where sender_user_id = :'ua'::uuid and created_at > now() - interval '1 minute' \gset
select ok(:need_min between 1 and 59, 'fixture: A needs ' || :need_min || ' more messages in the minute to reach 60');
select is(pg_temp.seed_spread(array[:'a2'::uuid, :'a4'::uuid, :'a3'::uuid, :'a1'::uuid], :'b2'::uuid, 60, interval '1 minute', 20, interval '0'), :need_min, 'seed: A reaches 60 in the minute, spread over its sessions');
select is((select count(*) from brigade.messages where sender_user_id = :'ua'::uuid and created_at > now() - interval '1 minute'), 60::bigint, 'fixture: A holds exactly 60 messages in the minute');
select ok((select max(c) from (select count(*) as c from brigade.messages where sender_user_id = :'ua'::uuid and created_at > now() - interval '1 minute' group by sender_session_id) x) < 20, 'fixture: no single session of A is at its own 20/min cap, so the next refusal can only be the principal cap');
select pg_temp.login(:'ua', true, 'Alice');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', 'pmin-a1')$$), 'P0001 brigade:rate_limited:principal_per_minute:60', 'the 61st message of the principal within a minute is rate_limited:principal_per_minute:60 (from a1)');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a4' || $$', '$$ || :'b1' || $$', 'x', 'pmin-a4')$$), 'P0001 brigade:rate_limited:principal_per_minute:60', 'the budget is shared: a4 is refused the same way');
select pg_temp.logout();
update brigade.messages set created_at = now() - interval '2 minutes' where sender_user_id = :'ua'::uuid and idempotency_key like 'seed-%' and created_at = now();
select pg_temp.login(:'ua', true, 'Alice');
select lives_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', 'pmin-after')$$, 'after backdating the minute''s seeds by 2 minutes the principal may send again');
select pg_temp.logout();
-- 6d. per principal, 600/h: top A up to 600 in the hour (acked seeds from a4 aged 30 minutes).
select (600 - count(*))::int as need_hour from brigade.messages where sender_user_id = :'ua'::uuid and created_at > now() - interval '1 hour' \gset
select ok(:need_hour between 1 and 599, 'fixture: A needs ' || :need_hour || ' more messages in the hour to reach 600');
select is(pg_temp.seed_spread(array[:'a2'::uuid, :'a4'::uuid, :'a3'::uuid, :'a1'::uuid], :'b2'::uuid, 600, interval '1 hour', 200, interval '30 minutes'), :need_hour, 'seed: A reaches 600 in the hour, spread over its sessions');
select is((select count(*) from brigade.messages where sender_user_id = :'ua'::uuid and created_at > now() - interval '1 hour'), 600::bigint, 'fixture: A holds exactly 600 messages in the hour');
select ok((select max(c) from (select count(*) as c from brigade.messages where sender_user_id = :'ua'::uuid and created_at > now() - interval '1 hour' group by sender_session_id) x) < 200, 'fixture: no single session of A is at its own 200/h cap, so the next refusal can only be the principal cap');
select pg_temp.login(:'ua', true, 'Alice');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', 'phour-a1')$$), 'P0001 brigade:rate_limited:principal_per_hour:3600', 'the 601st message of the principal within an hour is rate_limited:principal_per_hour:3600');
select pg_temp.logout();
update brigade.messages set created_at = created_at - interval '2 hours' where sender_user_id = :'ua'::uuid;   -- A's history is now out of every window
select pg_temp.login(:'ua', true, 'Alice');
select lives_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', 'phour-after')$$, 'after aging A''s history past the hour the principal may send again');
select pg_temp.logout();
update brigade.messages set created_at = created_at - interval '2 hours' where sender_user_id = :'ua'::uuid;
-- 6e. per-pair unacknowledged cap (15), checked BEFORE the recipient-wide cap (60). b1 -> c1: 15 accepted, the 16th
--     refused while a different sender to the same recipient still succeeds.
select is((select count(*) from unnest(pg_temp.send_n(:'ub'::uuid, :'b1'::uuid, :'c1'::uuid, 15, 'pair')) s where s = 'ok'), 15::bigint, 'b1 -> c1: 15 unacknowledged messages accepted');
select pg_temp.login(:'ub', true, 'Bob');
select is(pg_temp.err($$select brigade.send_message('$$ || :'b1' || $$', '$$ || :'c1' || $$', 'x', 'pair-16')$$), 'P0001 brigade:rate_limited:sender_quota_for_recipient:60', 'the 16th unacknowledged message from one sender to one recipient is rate_limited:sender_quota_for_recipient:60');
select pg_temp.login(:'ud', true, 'Di');
select lives_ok($$select brigade.send_message('$$ || :'d1' || $$', '$$ || :'c1' || $$', 'x', 'pair-other')$$, 'a different sender to the same recipient still succeeds (the cap is per pair)');
select pg_temp.logout();
-- 6f. Both caps at their limit at once: c1 now holds 16 unacked (15 from b1, 1 from d1); fill to 60 with 44 from
--     another principal's three sessions (a1 15, a3 15, a4 14), then the probe sender must be told
--     sender_quota_for_recipient, not recipient_inbox_full; a fresh sender is told recipient_inbox_full.
select is((select count(*) from brigade.messages where recipient_session_id = :'c1'::uuid and delivery_state = 'accepted'), 16::bigint, 'fixture: c1 holds 16 unacknowledged messages');
select is((select count(*) from unnest(pg_temp.send_n(:'ua'::uuid, :'a1'::uuid, :'c1'::uuid, 15, 'fill-a1') || pg_temp.send_n(:'ua'::uuid, :'a3'::uuid, :'c1'::uuid, 15, 'fill-a3') || pg_temp.send_n(:'ua'::uuid, :'a4'::uuid, :'c1'::uuid, 14, 'fill-a4')) s where s = 'ok'), 44::bigint,
          'A''s three sessions add 44 unacknowledged messages to c1 (each under its pair cap and the principal under 60/min)');
select is((select count(*) from brigade.messages where recipient_session_id = :'c1'::uuid and delivery_state = 'accepted'), 60::bigint, 'fixture: c1 holds exactly 60 unacknowledged messages: the recipient cap AND b1''s pair cap are both at their limit');
select pg_temp.login(:'ub', true, 'Bob');
select is(pg_temp.err($$select brigade.send_message('$$ || :'b1' || $$', '$$ || :'c1' || $$', 'x', 'both-b1')$$), 'P0001 brigade:rate_limited:sender_quota_for_recipient:60', 'cap order: with both caps at their limit the probe sender is told sender_quota_for_recipient (the per-pair cap is checked first; mutant_caporder)');
select pg_temp.login(:'ud', true, 'Di');
select is(pg_temp.err($$select brigade.send_message('$$ || :'d1' || $$', '$$ || :'c1' || $$', 'x', 'both-d1')$$), 'P0001 brigade:rate_limited:recipient_inbox_full:60', 'cap order: a sender under its pair cap is told recipient_inbox_full at 60');
select pg_temp.login(:'ux', true, 'Xen');
select is(pg_temp.err($$select brigade.send_message('$$ || :'x1' || $$', '$$ || :'c1' || $$', 'x', 'both-x1')$$), 'P0001 brigade:rate_limited:recipient_inbox_full:60', 'cap order: a sender with NO messages to c1 is told recipient_inbox_full at 60');
-- C acks one of b1's messages: the pair drops to 14 and the inbox to 59, so b1 may send once more (back to 60), and
-- a fresh sender is still refused.
select pg_temp.login(:'uc', true, 'Cy');
select (select array_agg(id) from (select id from brigade.messages where recipient_session_id = :'c1'::uuid and sender_session_id = :'b1'::uuid and delivery_state = 'accepted' limit 1) x) as ack_one \gset
select is(jsonb_array_length((brigade.ack_messages(:'c1'::uuid, :'ack_one'::uuid[]))->'acked'), 1, 'C acks one of b1''s messages');
select pg_temp.login(:'ub', true, 'Bob');
select lives_ok($$select brigade.send_message('$$ || :'b1' || $$', '$$ || :'c1' || $$', 'x', 'after-ack')$$, 'after the ack the probe sender may send again (pair 14 -> 15, inbox 59 -> 60)');
select pg_temp.login(:'ux', true, 'Xen');
select is(pg_temp.err($$select brigade.send_message('$$ || :'x1' || $$', '$$ || :'c1' || $$', 'x', 'both-x1-again')$$), 'P0001 brigade:rate_limited:recipient_inbox_full:60', 'and the recipient cap binds again for a fresh sender');
select pg_temp.logout();
-- Every refusal carries retry_after_seconds > 0 as its last component.
select is((select count(*) from unnest(array['send_per_minute:60', 'send_per_hour:3600', 'principal_per_minute:60', 'principal_per_hour:3600', 'sender_quota_for_recipient:60', 'recipient_inbox_full:60']) r
            where ('brigade:rate_limited:' || r) ~ '^brigade:rate_limited:[a-z_]+:[1-9][0-9]*$'), 6::bigint, 'every rate_limited text above matches brigade:rate_limited:<reason>:<retry_after_seconds> with a positive integer');

-- 7. Hop counting (4.5.12; C-29, C-29b).
-- 7a. reply_to must be a message the SENDER SESSION RECEIVED: a message it sent, and a random id, are the same not_found.
select pg_temp.login(:'ua', true, 'Alice');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', 'reply-sent', null, '$$ || :'m_hello' || $$')$$), 'PT404 brigade:not_found', 'reply_to naming a message the sender SENT (not received) is brigade:not_found');
select is(pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', 'reply-sent', null, '$$ || :'m_hello' || $$')$$), pg_temp.err($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'x', 'reply-rand', null, gen_random_uuid())$$), 'reply_to: sent-not-received and a random id are byte-identical');
select pg_temp.login(:'ub', true, 'Bob');
select brigade.send_message(:'b1'::uuid, :'a1'::uuid, 'a real reply', 'reply-ok', null, :'m_hello'::uuid) as rep \gset
select is(((:'rep'::jsonb)->>'hop_count')::int, 1, 'reply_to a message the sender received: hop = its hop + 1 = 1 (positive control)');
select pg_temp.logout();
select is((select reply_to from brigade.messages where id = ((:'rep'::jsonb)->>'message_id')::uuid), :'m_hello'::uuid, 'reply_to stored on the row');
-- 7b. Explicit chain, rotating over p1 -> q1 -> w1 -> p1 ... (ack-free: each pair carries 11 messages, under the
--     pair cap; each session sends 11 in the minute). Hops 0..32, then the 34th send is loop_detected.
create function pg_temp.explicit_chain(p_owners uuid[], p_sessions uuid[], p_max int, out hops int[], out last_id uuid) language plpgsql as $$
declare k int; r jsonb; prev uuid;
begin
  hops := '{}';
  for k in 0..p_max loop
    perform pg_temp.login(p_owners[(k % 3) + 1]);
    r := brigade.send_message(p_sessions[(k % 3) + 1], p_sessions[((k + 1) % 3) + 1], 'hop ' || k, 'chain-' || k, null, prev);
    hops := hops || (r->>'hop_count')::int;
    prev := (r->>'message_id')::uuid;
  end loop;
  last_id := prev;
  perform pg_temp.logout();
end $$;
select * from pg_temp.explicit_chain(array[:'up'::uuid, :'uq'::uuid, :'uw'::uuid], array[:'p1'::uuid, :'q1'::uuid, :'w1'::uuid], 32) \gset
select is(:'hops'::int[], (select array_agg(k) from generate_series(0, 32) k), 'explicit chain: hop_count is exactly k for message k, 0..32 (one more per hop)');
select pg_temp.login(:'up', true, 'Pat');   -- message 33 would be sent by sessions[(33 % 3) + 1] = p1, replying to m32 (received by p1)
select throws_ok($$select brigade.send_message('$$ || :'p1' || $$', '$$ || :'q1' || $$', 'one hop too many', 'chain-33', null, '$$ || :'last_id' || $$')$$, 'P0001', 'brigade:loop_detected:max_hops', 'explicit chain: the reply that would be hop 33 is loop_detected:max_hops');
select isnt(pg_temp.err($$select brigade.send_message('$$ || :'p1' || $$', '$$ || :'q1' || $$', 'one hop too many', 'chain-33', null, '$$ || :'last_id' || $$')$$), pg_temp.err($$select brigade.send_message('$$ || :'p1' || $$', '$$ || :'q1' || $$', 'x', 'chain-rand', null, gen_random_uuid())$$), 'control: loop_detected and not_found differ');
select pg_temp.logout();
select is((select count(*) from brigade.messages where idempotency_key like 'chain-%'), 33::bigint, 'explicit chain: 33 messages stored, none for the refused hop');
-- 7c. Implicit chain on ONE pair (u1 <-> v1), no reply_to at all; the recipient acks what it received before
--     replying (the natural drain, and what keeps the pair under its cap of 15). Hops 0..32, then loop_detected.
create function pg_temp.implicit_chain(p_owners uuid[], p_sessions uuid[], p_max int, out hops int[], out last_id uuid) language plpgsql as $$
declare k int; r jsonb; prev uuid; s int;
begin
  hops := '{}';
  for k in 0..p_max loop
    s := (k % 2) + 1;
    perform pg_temp.login(p_owners[s]);
    if prev is not null then perform brigade.ack_messages(p_sessions[s], array[prev]); end if;
    r := brigade.send_message(p_sessions[s], p_sessions[3 - s], 'implicit ' || k, 'impl-' || k);
    hops := hops || (r->>'hop_count')::int;
    prev := (r->>'message_id')::uuid;
  end loop;
  last_id := prev;
  perform pg_temp.logout();
end $$;
select * from pg_temp.implicit_chain(array[:'uu'::uuid, :'uv'::uuid], array[:'u1'::uuid, :'v1'::uuid], 32) \gset
select is(:'hops'::int[], (select array_agg(k) from generate_series(0, 32) k), 'implicit chain: alternating sends with no reply_to are hop 0..32 (one more per message)');
select is((select count(*) from brigade.messages where idempotency_key like 'impl-%' and delivery_state = 'injected'), 32::bigint, 'implicit chain: 32 of the 33 were acked mid-chain (only the last is pending)');
select pg_temp.login(:'uv', true, 'Val');   -- message 33 would be v1 -> u1 (k = 33 is odd)
select throws_ok($$select brigade.send_message('$$ || :'v1' || $$', '$$ || :'u1' || $$', 'one hop too many', 'impl-33')$$, 'P0001', 'brigade:loop_detected:max_hops', 'implicit chain: the 34th alternating message (hop 33) is loop_detected:max_hops');
select pg_temp.logout();
-- The 600 s window: after a backdated gap of 11 minutes the next implicit message restarts at 0.
update brigade.messages set created_at = created_at - interval '11 minutes' where idempotency_key like 'impl-%';
select pg_temp.login(:'uv', true, 'Val');
select is(pg_temp.send_hop(:'v1'::uuid, :'u1'::uuid, 'after the gap', 'impl-gap'), '0', 'implicit chain: a message after an 11-minute gap restarts at hop 0 (the 600 s implicit_reply_window; an unbounded window would answer loop_detected here)');
select pg_temp.logout();
-- ... and within the window the chain continues: x1 -> y1 (0), 9-minute gap, y1 -> x1 is 1; then 11 minutes, x1 -> y1 is 0.
select pg_temp.login(:'ux', true, 'Xen');
select is(pg_temp.send_hop(:'x1'::uuid, :'y1'::uuid, 'first', 'win-0'), '0', 'window: a first message between fresh sessions is hop 0');
select pg_temp.logout();
update brigade.messages set created_at = created_at - interval '9 minutes' where idempotency_key = 'win-0';
select pg_temp.login(:'uy', true, 'Yul');
select is(pg_temp.send_hop(:'y1'::uuid, :'x1'::uuid, 'reply within 600 s', 'win-1'), '1', 'window: an implicit reply 9 minutes later (within 600 s) is hop 1');
select pg_temp.logout();
update brigade.messages set created_at = created_at - interval '11 minutes' where idempotency_key = 'win-1';
select pg_temp.login(:'ux', true, 'Xen');
select is(pg_temp.send_hop(:'x1'::uuid, :'y1'::uuid, 'reply after 600 s', 'win-2'), '0', 'window: an implicit reply 11 minutes later (past 600 s) is hop 0');
select pg_temp.logout();

-- 8. A send proves liveness (D12): last_seen_at is touched, and no valid lease is required to send.
update brigade.sessions set last_seen_at = now() - interval '1 hour' where id = :'a1'::uuid;   -- lease 90: long expired
select pg_temp.login(:'ua', true, 'Alice');
select lives_ok($$select brigade.send_message('$$ || :'a1' || $$', '$$ || :'b1' || $$', 'still here', 'k-liveness')$$, 'send from an expired (not closed) session is accepted (no lease check on send)');
select pg_temp.logout();
select is((select last_seen_at from brigade.sessions where id = :'a1'::uuid), now(), 'send: the sender''s last_seen_at is touched');

-- 9. fetch_inbox: oldest first by seq, the envelope shape, paging by p_limit.
select pg_temp.login(:'ub', true, 'Bob');
select brigade.fetch_inbox(:'b2'::uuid) as inbox \gset
select is(jsonb_array_length(:'inbox'::jsonb), 10, 'fetch_inbox: b2 holds the 10 pending bulk messages from a3 (the seeds were acked)');
select is((select array_agg(m.seq order by x.ord) from jsonb_array_elements(:'inbox'::jsonb) with ordinality as x(e, ord) join brigade.messages m on m.id = (x.e->>'message_id')::uuid),
          (select array_agg(m.seq order by m.seq) from jsonb_array_elements(:'inbox'::jsonb) as x(e) join brigade.messages m on m.id = (x.e->>'message_id')::uuid),
          'fetch_inbox: ascending seq (oldest first)');
select is(jsonb_array_length(brigade.fetch_inbox(:'b2'::uuid, 2)), 2, 'fetch_inbox: p_limit pages the drain');
select is((brigade.fetch_inbox(:'b2'::uuid, 2))->0, (:'inbox'::jsonb)->0, 'fetch_inbox: the first page starts at the oldest pending message');
select is((:'inbox'::jsonb)->0->>'protocol_version', '1', 'envelope: protocol_version 1');
select is((:'inbox'::jsonb)->0->'sender'->>'human_label', 'Alice', 'envelope: sender.human_label from the membership');
select is((:'inbox'::jsonb)->0->'sender'->>'session_name', 'a3', 'envelope: sender.session_name');
select is((:'inbox'::jsonb)->0->'sender'->>'principal_ref', :'ua', 'envelope: sender.principal_ref');
select is((:'inbox'::jsonb)->0->>'delivery_state', 'accepted', 'envelope: delivery_state accepted while pending');
select is((:'inbox'::jsonb)->0->>'recipient_session_id', :'b2', 'envelope: recipient_session_id');
select is((:'inbox'::jsonb)->0->>'team_ref', :'team_a', 'envelope: team_ref');
select pg_temp.logout();

select * from finish();
rollback;
