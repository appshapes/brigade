-- gateway.sql (card 48): the mail gateway's schema, 20261003160000_mail_gateway.sql. brigade_gateway is reachable by
-- postgres only: no API role has usage on the schema, every table has RLS and no policy, nothing is granted, and the
-- gc function is server-only. The brigade schema is untouched by that migration, which functions.sql and hygiene.sql
-- pin in full; this file pins the new schema the same way, so a later migration cannot quietly expose it.
begin;
\ir helpers/auth.sql
select plan(33);

-- 1. The schema and its three tables.
select has_schema('brigade_gateway', 'the brigade_gateway schema exists');
select tables_are('brigade_gateway', array['gateways', 'threads', 'received'], 'brigade_gateway holds exactly gateways, threads and received');
select ok(c.relrowsecurity, 'RLS enabled: brigade_gateway.' || c.relname)
  from pg_class c where c.relnamespace = 'brigade_gateway'::regnamespace and c.relkind = 'r' order by c.relname;
select is((select count(*) from pg_class c where c.relnamespace = 'brigade_gateway'::regnamespace and c.relkind = 'r'), 3::bigint,
          'the RLS loop covered three tables');
select is((select count(*) from pg_policy p join pg_class c on c.oid = p.polrelid where c.relnamespace = 'brigade_gateway'::regnamespace),
          0::bigint, 'no policy in brigade_gateway: postgres only, never an API role');

-- 2. No API role reaches the schema, its tables or its function.
select ok(not has_schema_privilege('anon', 'brigade_gateway', 'usage'), 'anon has no usage on brigade_gateway');
select ok(not has_schema_privilege('authenticated', 'brigade_gateway', 'usage'), 'authenticated has no usage on brigade_gateway');
select ok(not has_schema_privilege('service_role', 'brigade_gateway', 'usage'), 'service_role has no usage on brigade_gateway');
select is((select count(*) from information_schema.role_table_grants where table_schema = 'brigade_gateway'
            and grantee in ('anon', 'authenticated', 'service_role', 'public')), 0::bigint, 'no API role holds a table grant in brigade_gateway');
select is((select count(*) from information_schema.role_routine_grants where specific_schema = 'brigade_gateway'
            and grantee in ('anon', 'authenticated', 'service_role', 'public')), 0::bigint, 'no API role holds a routine grant in brigade_gateway');
select functions_are('brigade_gateway', array['gc'], 'brigade_gateway holds exactly gc');
select ok(p.prosecdef, 'gc is security definer') from pg_proc p where p.pronamespace = 'brigade_gateway'::regnamespace and p.proname = 'gc';
select ok(exists (select 1 from pg_proc p, unnest(coalesce(p.proconfig, '{}')) c
                   where p.pronamespace = 'brigade_gateway'::regnamespace and p.proname = 'gc' and c ~ '^search_path=("")?$'),
          'gc pins an empty search_path');
select ok(not exists (select 1 from pg_proc p, aclexplode(p.proacl) a
                       where p.pronamespace = 'brigade_gateway'::regnamespace and p.proname = 'gc' and a.grantee <> p.proowner),
          'gc is granted to nobody but the owner');
set local role service_role;
select throws_ok($$select * from brigade_gateway.threads$$, '42501', 'permission denied for schema brigade_gateway',
                 'service_role: no usage on the schema, so no table is reachable');
reset role;

-- 3. The brigade schema is what it was: the gateway is an ordinary member and nothing was granted for it.
select is((select count(*) from information_schema.role_routine_grants where specific_schema = 'brigade' and grantee = 'service_role'),
          0::bigint, 'service_role still holds no routine grant in brigade (the gateway is a member, not a grant)');
select is((select count(*) from pg_class c where c.relnamespace = 'brigade'::regnamespace and c.relkind = 'r'), 5::bigint,
          'brigade still holds its five tables');

-- 4. The shape the functions rely on: a gateway row ties a team to a member and a session; a thread row is keyed by the
--    Brigade message and found by the mail id; a received row is the dedupe set. Written as postgres, as the functions do.
select pg_temp.new_user() as creator \gset
select pg_temp.new_user() as gw \gset
select pg_temp.login(:'creator');
select (brigade.create_team('gw-test', 'creator')->>'team_id') as team \gset
select pg_temp.logout();
insert into brigade.memberships (team_id, user_id, human_label, joined_secret_version) values (:'team', :'gw', 'mail gateway', 1);
select pg_temp.as_user(:'gw');
select (brigade.register_session(:'team', 'mail-gateway', 'email gateway: test', 'idle', 'accept', 'gateway-email', '0.1.0', null, 600, null)->>'session_id') as sid \gset
select pg_temp.logout();
select lives_ok(format($$insert into brigade_gateway.gateways (team_id, user_id, session_id) values (%L, %L, %L)$$, :'team', :'gw', :'sid'),
                'a gateways row ties the team, the member and the session');
select throws_ok(format($$insert into brigade_gateway.gateways (team_id, user_id, session_id) values (%L, %L, %L)$$, :'team', :'gw', :'sid'),
                 '23505', null, 'one gateway per team and kind (the default kind is email)');
-- The Slack half (20261003210000_slack_gateway.sql): a second kind for the same team, and nothing else.
select lives_ok(format($$insert into brigade_gateway.gateways (team_id, user_id, session_id, kind) values (%L, %L, %L, 'slack')$$, :'team', :'gw', :'sid'),
                'a slack gateway beside the email one');
select throws_ok(format($$insert into brigade_gateway.gateways (team_id, user_id, session_id, kind) values (%L, %L, %L, 'sms')$$, :'team', :'gw', :'sid'),
                 '23514', null, 'kind is email or slack');
select lives_ok(format($$insert into brigade_gateway.threads (brigade_message_id, team_id, direction, session_id, address, mail_message_id, kind, actor)
                        values (gen_random_uuid(), %L, 'out', %L, 'C0123ABCD', '1759500000.000100', 'slack', 'U0123ABCD')$$, :'team', :'sid'),
                'a slack thread row: channel, root ts and the person');
select is((select actor from brigade_gateway.threads where mail_message_id = '1759500000.000100'), 'U0123ABCD',
          'the slack row carries the person it is about');
select lives_ok(format($$insert into brigade_gateway.threads (brigade_message_id, team_id, direction, session_id, address, mail_message_id, subject)
                        values (gen_random_uuid(), %L, 'in', %L, 'alice@example.com', '<abc@mail.example.com>', 'hello')$$, :'team', :'sid'),
                'an inbound thread row');
select throws_ok(format($$insert into brigade_gateway.threads (brigade_message_id, team_id, direction, session_id, address)
                         values (gen_random_uuid(), %L, 'sideways', %L, 'alice@example.com')$$, :'team', :'sid'),
                 '23514', null, 'direction is in or out');
select is((select address from brigade_gateway.threads where mail_message_id = '<abc@mail.example.com>'), 'alice@example.com',
          'a thread row is found by its mail id');
select is((select kind from brigade_gateway.threads where mail_message_id = '<abc@mail.example.com>'), 'email',
          'a thread row written without a kind is an email row');
select lives_ok($$insert into brigade_gateway.received (provider_mail_id) values ('re_1')$$, 'a received row');
select lives_ok($$insert into brigade_gateway.received (provider_mail_id) values ('re_1') on conflict do nothing$$,
                'a second webhook for the same mail is a no-op (the functions insert ... on conflict do nothing returning)');
select is((select count(*) from brigade_gateway.received where provider_mail_id = 're_1'), 1::bigint, 'and the row is still one');
select is((select state from jsonb_to_record(brigade.session_record((select s from brigade.sessions s where s.id = :'sid'), 'mail gateway')) as x(state text)),
          'idle', 'the gateway session reads as an ordinary idle member session');

select * from finish();
rollback;
