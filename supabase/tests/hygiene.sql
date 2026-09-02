-- hygiene.sql (plan 9.3, I-10, I-30): mechanical over pg_catalog. Every table in brigade has RLS; every policy
-- names `to authenticated`; nothing in brigade is granted to anon; service_role holds schema usage only; the
-- authenticated grants are exactly the read grants of plan 5.3 (column-level on teams, never secret_hash);
-- join_attempts has RLS, no policy and no grant (server-only, documented for the advisor lint); no view without
-- security_invoker and no materialized view in any API-exposed schema.
begin;
\ir helpers/auth.sql
select plan(60);

-- 1. The table set and RLS on every one of them (5 + 5 assertions).
select tables_are('brigade', array['teams', 'memberships', 'sessions', 'messages', 'join_attempts'],
                  'brigade holds exactly the five tables of plan 5.3');
select ok(c.relrowsecurity, 'RLS enabled: brigade.' || c.relname)
  from pg_class c where c.relnamespace = 'brigade'::regnamespace and c.relkind = 'r' order by c.relname;
select is((select count(*) from pg_class c where c.relnamespace = 'brigade'::regnamespace and c.relkind = 'r' and not c.relrowsecurity),
          0::bigint, 'no table in brigade without RLS');
-- (the loop above emits one line per table; pin the number so a missing table cannot hide as a missing line)
select is((select count(*) from pg_class c where c.relnamespace = 'brigade'::regnamespace and c.relkind = 'r'), 5::bigint,
          'the RLS loop covered five tables');
select is((select count(*) from pg_class c where c.relnamespace = 'brigade'::regnamespace and c.relkind in ('p', 'f')), 0::bigint,
          'no partitioned or foreign tables in brigade (RLS check is complete)');

-- 2. Policies: exactly the four read policies of plan 5.3, each `for select` and each `to authenticated`.
select policies_are('brigade', 'teams', array['teams_select'], 'teams: only teams_select');
select policies_are('brigade', 'memberships', array['memberships_select'], 'memberships: only memberships_select (D22)');
select policies_are('brigade', 'sessions', array['sessions_select'], 'sessions: only sessions_select');
select policies_are('brigade', 'messages', array['messages_select'], 'messages: only messages_select');
select policies_are('brigade', 'join_attempts', array[]::text[], 'join_attempts: no policy by design (server-only)');
select is((select count(*) from pg_policy p join pg_class c on c.oid = p.polrelid where c.relnamespace = 'brigade'::regnamespace),
          4::bigint, 'exactly four policies in brigade');
select ok(p.polroles = array['authenticated'::regrole]::oid[], 'policy to authenticated only: ' || p.polname)
  from pg_policy p join pg_class c on c.oid = p.polrelid where c.relnamespace = 'brigade'::regnamespace order by p.polname;
select ok(p.polcmd = 'r', 'policy is for select (writes are RPC-only, D22): ' || p.polname)
  from pg_policy p join pg_class c on c.oid = p.polrelid where c.relnamespace = 'brigade'::regnamespace order by p.polname;
select ok(p.polpermissive, 'policy is permissive (the advisor flags restrictive ones): ' || p.polname)
  from pg_policy p join pg_class c on c.oid = p.polrelid where c.relnamespace = 'brigade'::regnamespace order by p.polname;
select is((select count(*) from pg_policy p join pg_class c on c.oid = p.polrelid
            where c.relnamespace = 'brigade'::regnamespace
              and (pg_get_expr(p.polqual, p.polrelid) ~* 'user_metadata|raw_user_meta_data'
                   or pg_get_expr(p.polwithcheck, p.polrelid) ~* 'user_metadata|raw_user_meta_data')),
          0::bigint, 'no policy references user_metadata (client-writable; advisor rls_references_user_metadata)');

-- 3. anon: nothing at all in brigade (schema usage, tables, columns, routines).
select ok(not has_schema_privilege('anon', 'brigade', 'usage'), 'anon has no usage on schema brigade (T4)');
select is((select count(*) from information_schema.role_table_grants where table_schema = 'brigade' and grantee = 'anon'),
          0::bigint, 'anon holds no table grant in brigade');
select is((select count(*) from information_schema.column_privileges where table_schema = 'brigade' and grantee = 'anon'),
          0::bigint, 'anon holds no column grant in brigade');
select is((select count(*) from information_schema.role_routine_grants where specific_schema = 'brigade' and grantee = 'anon'),
          0::bigint, 'anon holds no routine grant in brigade');
select is((select count(*) from information_schema.role_table_grants where table_schema = 'brigade' and grantee = 'public'),
          0::bigint, 'PUBLIC holds no table grant in brigade');

-- 4. service_role: schema usage only, no table, column or routine grant (plan 5.3; E0-1 (i)).
select ok(has_schema_privilege('service_role', 'brigade', 'usage'), 'service_role has usage on schema brigade (and nothing else)');
select is((select count(*) from information_schema.role_table_grants where table_schema = 'brigade' and grantee = 'service_role'),
          0::bigint, 'service_role holds no table grant in brigade');
select is((select count(*) from information_schema.column_privileges where table_schema = 'brigade' and grantee = 'service_role'),
          0::bigint, 'service_role holds no column grant in brigade');
select is((select count(*) from information_schema.role_routine_grants where specific_schema = 'brigade' and grantee = 'service_role'),
          0::bigint, 'service_role holds no routine grant in brigade');
set local role service_role;
select throws_ok($$select * from brigade.teams$$, '42501', 'permission denied for table teams', 'service_role: 42501 on teams');
select throws_ok($$select * from brigade.memberships$$, '42501', 'permission denied for table memberships', 'service_role: 42501 on memberships');
select throws_ok($$select * from brigade.sessions$$, '42501', 'permission denied for table sessions', 'service_role: 42501 on sessions');
select throws_ok($$select * from brigade.messages$$, '42501', 'permission denied for table messages', 'service_role: 42501 on messages');
select throws_ok($$select * from brigade.join_attempts$$, '42501', 'permission denied for table join_attempts', 'service_role: 42501 on join_attempts');
reset role;

-- 5. authenticated: exactly the read grants of plan 5.3, and nothing writable anywhere.
select table_privs_are('brigade', 'memberships', 'authenticated', array['SELECT'], 'authenticated: SELECT only on memberships');
select table_privs_are('brigade', 'sessions', 'authenticated', array['SELECT'], 'authenticated: SELECT only on sessions');
select table_privs_are('brigade', 'messages', 'authenticated', array['SELECT'], 'authenticated: SELECT only on messages');
select table_privs_are('brigade', 'join_attempts', 'authenticated', array[]::text[], 'authenticated: nothing on join_attempts');
select table_privs_are('brigade', 'teams', 'authenticated', array[]::text[], 'authenticated: no table-level grant on teams (column-level only)');
select is((select array_agg(column_name::text order by column_name) from information_schema.column_privileges
            where table_schema = 'brigade' and table_name = 'teams' and grantee = 'authenticated' and privilege_type = 'SELECT'),
          array['created_at', 'id', 'name', 'secret_version'], 'authenticated: column select on teams is exactly id, name, secret_version, created_at');
select ok(not has_column_privilege('authenticated', 'brigade.teams', 'secret_hash', 'select'), 'authenticated: never secret_hash');
select is((select count(*) from information_schema.role_table_grants
            where table_schema = 'brigade' and grantee = 'authenticated' and privilege_type <> 'SELECT'),
          0::bigint, 'authenticated: no INSERT/UPDATE/DELETE/TRUNCATE/REFERENCES/TRIGGER grant anywhere in brigade');
select pg_temp.new_user() as u1 \gset
select pg_temp.login(:'u1');
select throws_ok($$select * from brigade.teams$$, '42501', 'permission denied for table teams',
                 'authenticated: select(*) on teams is 42501 because of the column-level grant (the adapter names columns)');
select lives_ok($$select id, name, secret_version, created_at from brigade.teams$$, 'authenticated: the granted teams columns are selectable');
select throws_ok($$select * from brigade.join_attempts$$, '42501', 'permission denied for table join_attempts',
                 'authenticated: join_attempts is server-only');
select pg_temp.logout();

-- 6. Views and materialized views: none without security_invoker; none in an API-exposed schema.
select is((select count(*) from pg_class c where c.relnamespace = 'brigade'::regnamespace and c.relkind = 'v'
            and not coalesce('security_invoker=true' = any(c.reloptions) or 'security_invoker=on' = any(c.reloptions), false)),
          0::bigint, 'no view in brigade without security_invoker (plan 5.3: none in v1)');
select is((select count(*) from pg_class c where c.relnamespace = 'brigade'::regnamespace and c.relkind = 'm'),
          0::bigint, 'no materialized view in brigade');
select is((select count(*) from pg_class c join pg_namespace n on n.oid = c.relnamespace
            where n.nspname in ('public', 'graphql_public', 'brigade') and c.relkind = 'm'),
          0::bigint, 'no materialized view in any API-exposed schema (config.toml api.schemas)');
select is((select count(*) from pg_class c join pg_namespace n on n.oid = c.relnamespace
            where n.nspname in ('public', 'brigade') and c.relkind = 'r' and not c.relrowsecurity),
          0::bigint, 'no table without RLS in public or brigade (advisor rls_disabled_in_public)');

-- 7. The membership helper the policies depend on is scoped to authenticated only.
select function_privs_are('brigade', 'my_team_ids', array[]::text[], 'authenticated', array['EXECUTE'], 'my_team_ids: authenticated may execute');
select function_privs_are('brigade', 'my_team_ids', array[]::text[], 'anon', array[]::text[], 'my_team_ids: anon may not');
select function_privs_are('brigade', 'my_team_ids', array[]::text[], 'service_role', array[]::text[], 'my_team_ids: service_role may not');

select * from finish();
rollback;
