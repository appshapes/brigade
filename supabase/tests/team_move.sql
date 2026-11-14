-- team_move.sql (work item 273, part 2; ADR 0003; capability team.move of docs/protocol-v1.md 4.4.10): the record
-- of where a team went. record_team_move and clear_team_move answer the team's creator only, with the ONE text of
-- the creator gate, '42501 brigade:unauthorized', for an active member that is not the creator, a member of
-- nothing and the creator of another team, byte-identical against a random team id, and raised before any argument
-- is read; a record must name an adapter, a url and a ticket that is a join secret of THIS team, within the
-- protocol's limits; a second record replaces the first and no answer of the two carries the ticket.
-- read_team_move answers every active member, the creator included, with the destination and the ticket, and a
-- team with no record with moved false; a member of nothing, a member that was revoked, one that was banned and
-- one that left read the one text list_members raises, byte-identical against a random team id. The table is
-- closed to every API role, and ending the team takes its record. Every assertion is named after the rule it
-- checks, so a mutation of 20261114120000_team_move.sql maps to a line.
begin;
\ir helpers/auth.sql
select plan(66);

-- Local helper: run a statement as the CURRENT role and return '<sqlstate> <message>' (null when it succeeds), so
-- two error texts can be compared byte for byte (the rls_isolation.sql helper).
create function pg_temp.err(q text) returns text language plpgsql as $$
begin
  execute q;
  return null;
exception when others then
  return sqlstate || ' ' || sqlerrm;
end $$;

-- Local helper: the two creator-only RPCs against a team id, with a record that would be accepted, so the gate is
-- what decides.
create function pg_temp.move_calls(team text) returns text[] language sql as $$
  select array[
    'select brigade.record_team_move(''' || team || '''::uuid, ''rest'', ''https://brigade.example.test'', ''brg1.' || team || '.0123456789abcdef'')',
    'select brigade.clear_team_move(''' || team || '''::uuid)'
  ] $$;

-- Fixtures through the RPCs: team T (C1 creator; M1, M2, M3 and M4 join), N1 a member of nothing, X1 the creator of
-- team U. M2 is revoked, M3 banned and M4 leaves before anything is read.
select pg_temp.new_user() as c1 \gset
select pg_temp.new_user() as m1 \gset
select pg_temp.new_user() as m2 \gset
select pg_temp.new_user() as m3 \gset
select pg_temp.new_user() as m4 \gset
select pg_temp.new_user() as n1 \gset
select pg_temp.new_user() as x1 \gset
select pg_temp.login(:'c1', true, 'Cleo');
select r->>'team_id' as team_t, r->>'join_secret' as secret_t from brigade.create_team('Team T', 'Cleo') as r \gset
select pg_temp.login(:'m1', true, 'Mia');
select is((brigade.join_team(:'secret_t', 'Mia'))->>'status', 'joined', 'fixture: M1 joins team T');
select pg_temp.login(:'m2', true, 'Max');
select is((brigade.join_team(:'secret_t', 'Max'))->>'status', 'joined', 'fixture: M2 joins team T');
select pg_temp.login(:'m3', true, 'Moe');
select is((brigade.join_team(:'secret_t', 'Moe'))->>'status', 'joined', 'fixture: M3 joins team T');
select pg_temp.login(:'m4', true, 'Mel');
select is((brigade.join_team(:'secret_t', 'Mel'))->>'status', 'joined', 'fixture: M4 joins team T');
select is((brigade.leave_team(:'team_t'::uuid))->>'left', 'true', 'fixture: M4 leaves team T');
select pg_temp.login(:'x1', true, 'Xena');
select r->>'team_id' as team_u from brigade.create_team('Team U', 'Xena') as r \gset
select pg_temp.login(:'c1', true, 'Cleo');
select is((brigade.revoke_membership(:'team_t'::uuid, :'m2'::uuid))->>'status', 'revoked', 'fixture: the creator revokes M2');
select is((brigade.revoke_membership(:'team_t'::uuid, :'m3'::uuid, true))->>'status', 'banned', 'fixture: the creator bans M3');
select pg_temp.logout();
select 'brg1.' || :'team_t' || '.0123456789abcdef0123456789abcdef' as ticket_1 \gset
select 'brg1.' || :'team_t' || '.fedcba9876543210fedcba9876543210' as ticket_2 \gset
select gen_random_uuid() as nowhere \gset

-- 1. The creator gate of the two writing RPCs: one text, three kinds of caller, and a team that does not exist.
select pg_temp.login(:'m1', true, 'Mia');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'gate: an active member that is not the creator is refused: ' || split_part(q, '(', 1))
  from unnest(pg_temp.move_calls(:'team_t')) q;
select is(pg_temp.err((pg_temp.move_calls(:'team_t'))[1]), pg_temp.err((pg_temp.move_calls(:'nowhere'))[1]),
          'gate: a member''s record on its own team and on a random team id are the same bytes (no team-existence oracle)');
select is(pg_temp.err((pg_temp.move_calls(:'team_t'))[2]), pg_temp.err((pg_temp.move_calls(:'nowhere'))[2]),
          'gate: a member''s clear on its own team and on a random team id are the same bytes');
select is(pg_temp.err($$select brigade.record_team_move('$$ || :'team_t' || $$'::uuid, null, null, null)$$), '42501 brigade:unauthorized',
          'gate: it is raised before any argument is read (a record of nulls by a member that is not the creator)');
select pg_temp.login(:'n1', true, 'Ned');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'gate: a member of nothing is refused: ' || split_part(q, '(', 1))
  from unnest(pg_temp.move_calls(:'team_t')) q;
select pg_temp.login(:'x1', true, 'Xena');
select is(pg_temp.err(q), '42501 brigade:unauthorized', 'gate: the creator of another team is refused: ' || split_part(q, '(', 1))
  from unnest(pg_temp.move_calls(:'team_t')) q;
select pg_temp.logout();
select is(pg_temp.err(q), '28000 brigade:unauthenticated', 'gate: a caller with no identity is unauthenticated: ' || split_part(q, '(', 1))
  from unnest(pg_temp.move_calls(:'team_t')) q;
select is(pg_temp.err($$select brigade.read_team_move('$$ || :'team_t' || $$'::uuid)$$), '28000 brigade:unauthenticated',
          'read: a caller with no identity is unauthenticated');
select is((select count(*) from brigade.team_moves), 0::bigint, 'gate (as postgres): no refused call recorded anything');

-- 2. No record yet: an active member and the creator read moved false, and nothing else.
select pg_temp.login(:'m1', true, 'Mia');
select is(brigade.read_team_move(:'team_t'::uuid), jsonb_build_object('moved', false, 'team_id', :'team_t'::uuid),
          'read: a team with no record answers moved false and the team to a member');
select pg_temp.login(:'c1', true, 'Cleo');
select is(brigade.read_team_move(:'team_t'::uuid), jsonb_build_object('moved', false, 'team_id', :'team_t'::uuid),
          'read: and to its creator');
select is((brigade.clear_team_move(:'team_t'::uuid)), jsonb_build_object('moved', false, 'team_id', :'team_t'::uuid, 'cleared', false),
          'clear: with no record it changes nothing and says so');

-- 3. What a record must be: an adapter, a url and a ticket of this team, within the protocol's limits.
select is(pg_temp.err(format($$select brigade.record_team_move(%L::uuid, %L, %L, %L)$$, :'team_t', a, 'https://b.example', :'ticket_1')),
          '22023 brigade:invalid_input:adapter', 'record: refused adapter: ' || what)
  from (values (null, 'null'), ('', 'empty'), (repeat('a', 65), '65 characters')) v(a, what);
select is(pg_temp.err(format($$select brigade.record_team_move(%L::uuid, 'rest', %L, %L)$$, :'team_t', u, :'ticket_1')),
          '22023 brigade:invalid_input:url', 'record: refused url: ' || what)
  from (values (null, 'null'), ('', 'empty'), (repeat('u', 2049), '2049 characters')) v(u, what);
select is(pg_temp.err(format($$select brigade.record_team_move(%L::uuid, 'rest', 'https://b.example', %L, %L)$$, :'team_t', :'ticket_1', c)),
          '22023 brigade:invalid_input:certificate', 'record: refused certificate: ' || what)
  from (values ('', 'empty'), (repeat('c', 129), '129 characters')) v(c, what);
select is(pg_temp.err(format($$select brigade.record_team_move(%L::uuid, 'rest', 'https://b.example', %L)$$, :'team_t', t)),
          '22023 brigade:invalid_input:ticket', 'record: refused ticket: ' || what)
  from (values (null, 'null'), ('', 'empty'), ('not a ticket', 'no join secret'),
               ('brg1.' || :'team_u' || '.0123456789abcdef', 'a join secret of another team'),
               ('brg1.' || :'team_t' || '.', 'no secret after the team'),
               ('brg1.' || :'team_t', 'no dot after the team'),
               ('BRG1.' || :'team_t' || '.0123456789abcdef', 'the prefix in capitals'),
               ('brg1.' || :'team_t' || '.01234 56789', 'a space in the secret'),
               ('brg1.' || :'team_t' || '.0123' || chr(10) || '4567', 'a line end in the secret'),
               ('brg1.' || :'team_t' || '.' || repeat('f', 512), 'longer than 512 characters')) v(t, what);
select pg_temp.logout();
select is((select count(*) from brigade.team_moves), 0::bigint, 'record (as postgres): no refused record was kept');

-- 4. A record, its answer, and a member's reading of it.
select pg_temp.login(:'c1', true, 'Cleo');
select brigade.record_team_move(:'team_t'::uuid, 'rest', 'https://brigade.example.test', :'ticket_1') as recorded \gset
select pg_temp.logout();
select is(:'recorded'::jsonb - 'recorded_at', jsonb_build_object('moved', true, 'team_id', :'team_t'::uuid, 'adapter', 'rest',
          'url', 'https://brigade.example.test', 'certificate', null), 'record: the answer is the destination, with a null certificate');
select ok(:'recorded'::jsonb ? 'recorded_at' and not :'recorded'::jsonb ? 'ticket', 'record: the answer carries recorded_at and never the ticket');
select is((select recorded_by from brigade.team_moves where team_id = :'team_t'::uuid), :'c1'::uuid, 'record (as postgres): the row names who recorded it');
select pg_temp.login(:'m1', true, 'Mia');
select is(brigade.read_team_move(:'team_t'::uuid) - 'recorded_at', jsonb_build_object('moved', true, 'team_id', :'team_t'::uuid, 'adapter', 'rest',
          'url', 'https://brigade.example.test', 'certificate', null, 'ticket', :'ticket_1'),
          'read: an active member reads the destination and the ticket');
select is((brigade.read_team_move(:'team_t'::uuid))->>'recorded_at', (:'recorded'::jsonb)->>'recorded_at', 'read: recorded_at is the record''s');

-- 5. Nobody but an active member reads it: one text, byte-identical against a random team id.
select pg_temp.login(:'n1', true, 'Ned');
select is(pg_temp.err($$select brigade.read_team_move('$$ || :'team_t' || $$'::uuid)$$), '42501 brigade:unauthorized', 'read: a member of nothing is refused');
select is(pg_temp.err($$select brigade.read_team_move('$$ || :'team_t' || $$'::uuid)$$), pg_temp.err($$select brigade.read_team_move('$$ || :'nowhere' || $$'::uuid)$$),
          'read: a team the caller is not in and a random team id are the same bytes (no team-existence oracle)');
select pg_temp.login(:'m2', true, 'Max');
select is(pg_temp.err($$select brigade.read_team_move('$$ || :'team_t' || $$'::uuid)$$), '42501 brigade:unauthorized', 'read: a member that was revoked is refused');
select pg_temp.login(:'m3', true, 'Moe');
select is(pg_temp.err($$select brigade.read_team_move('$$ || :'team_t' || $$'::uuid)$$), '42501 brigade:unauthorized', 'read: a member that was banned is refused');
select pg_temp.login(:'m4', true, 'Mel');
select is(pg_temp.err($$select brigade.read_team_move('$$ || :'team_t' || $$'::uuid)$$), '42501 brigade:unauthorized', 'read: a member that left is refused');
select pg_temp.login(:'x1', true, 'Xena');
select is(pg_temp.err($$select brigade.read_team_move('$$ || :'team_t' || $$'::uuid)$$), '42501 brigade:unauthorized', 'read: the creator of another team is refused');
select is(brigade.read_team_move(:'team_u'::uuid), jsonb_build_object('moved', false, 'team_id', :'team_u'::uuid),
          'read: and its own team, which has no record, answers moved false (a record is one team''s)');

-- 6. The table is closed to every API role: only the three functions touch it.
select is(pg_temp.err('select * from brigade.team_moves'), '42501 permission denied for table team_moves', 'table: a member cannot read team_moves at all');
select is(pg_temp.err($$delete from brigade.team_moves$$), '42501 permission denied for table team_moves', 'table: nor change it');
select pg_temp.logout();
set local role service_role;
select is(pg_temp.err('select * from brigade.team_moves'), '42501 permission denied for table team_moves', 'table: service_role cannot read team_moves');
select is(pg_temp.err($$select brigade.read_team_move('$$ || :'team_t' || $$'::uuid)$$), '42501 permission denied for function read_team_move',
          'table: service_role cannot execute read_team_move');
reset role;
set local role anon;
select is(pg_temp.err($$select brigade.read_team_move('$$ || :'team_t' || $$'::uuid)$$), '42501 permission denied for schema brigade',
          'table: anon reaches nothing in brigade');
reset role;

-- 7. A second record replaces the first, with a certificate this time; the row is still one.
select pg_temp.login(:'c1', true, 'Cleo');
select is((brigade.record_team_move(:'team_t'::uuid, 'rest', 'https://second.example.test', :'ticket_2', 'sha256:00')) - 'recorded_at',
          jsonb_build_object('moved', true, 'team_id', :'team_t'::uuid, 'adapter', 'rest', 'url', 'https://second.example.test', 'certificate', 'sha256:00'),
          'record: a second record answers the new destination and its certificate');
select pg_temp.login(:'m1', true, 'Mia');
select is(brigade.read_team_move(:'team_t'::uuid) - 'recorded_at', jsonb_build_object('moved', true, 'team_id', :'team_t'::uuid, 'adapter', 'rest',
          'url', 'https://second.example.test', 'certificate', 'sha256:00', 'ticket', :'ticket_2'),
          'read: a member reads the second record, and the first ticket is gone');
select pg_temp.logout();
select is((select count(*) from brigade.team_moves where team_id = :'team_t'::uuid), 1::bigint, 'record (as postgres): one row for the team');

-- 8. The creator clears it; ending the team takes a record with it.
select pg_temp.login(:'c1', true, 'Cleo');
select is(brigade.clear_team_move(:'team_t'::uuid), jsonb_build_object('moved', false, 'team_id', :'team_t'::uuid, 'cleared', true),
          'clear: the creator clears the record');
select is(brigade.read_team_move(:'team_t'::uuid), jsonb_build_object('moved', false, 'team_id', :'team_t'::uuid), 'clear: the team then reads as one that has not moved');
select is((brigade.record_team_move(:'team_t'::uuid, 'rest', 'https://brigade.example.test', :'ticket_1'))->>'moved', 'true', 'record: and it can be recorded again');
select pg_temp.logout();
delete from brigade.teams where id = :'team_t'::uuid;
select is((select count(*) from brigade.team_moves), 0::bigint, 'the record goes with its team (as postgres: on delete cascade)');

select * from finish();
rollback;
