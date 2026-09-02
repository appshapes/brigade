-- scripts/ci/advisor-lints.sql (plan P2-5, I-11): local mirrors of ten Supabase Security Advisor lints, run by
-- `make advisor-lints` as `psql -v ON_ERROR_STOP=1` inside the supabase_db_brigade container (no host psql needed)
-- and identically in CI. Every query below collects findings into a temp table; the DO block at the end fails the
-- run (a raised exception, so psql exits non-zero) on any finding that is not on the documented expected list, AND
-- on any expected finding that is missing, so a lint that silently stopped detecting is caught too.
--
-- Scope: the API-exposed schemas of supabase/config.toml `[api] schemas = ["public", "graphql_public", "brigade"]`
-- (the hosted advisor scans what the Data API exposes), except policy_exists_rls_disabled which scans every schema.
--
-- EXPECTED FINDINGS (by design; plan 5.3 and the P2-5 row):
--   rls_enabled_no_policy on brigade.join_attempts: RLS is enabled, there is deliberately NO policy and NO grant to
--     any API role; the table is written only by the join_team security-definer RPC and read by nobody but postgres
--     (the join limiter must never be readable through the Data API: it would be a team-existence and attempt oracle).
--   authenticated_security_definer_function_executable on exactly the 13 RPCs plan 5.4 grants to authenticated:
--     my_team_ids, create_team, join_team, leave_team, register_session, session_heartbeat, close_session,
--     list_sessions, list_members, send_message, fetch_inbox, ack_messages, owns_session_topic. Security definer with
--     `set search_path = ''` and an explicit revoke from public/anon IS the design (the tables have no write policy;
--     the RPCs are the only write path, D22). Every other brigade function (helpers, trigger functions, gc_expired)
--     must NOT appear here: it is granted to nobody (functions.sql asserts the same over pg_catalog).
-- Anything else is a failure.

\set ON_ERROR_STOP on
\pset format aligned
\pset tuples_only off

create temp table advisor_findings (lint text not null, object text not null, detail text);
create temp table advisor_expected (lint text not null, object text not null, primary key (lint, object));
insert into advisor_expected values
  ('rls_enabled_no_policy', 'brigade.join_attempts'),
  ('authenticated_security_definer_function_executable', 'brigade.my_team_ids()'),
  ('authenticated_security_definer_function_executable', 'brigade.create_team(text, text)'),
  ('authenticated_security_definer_function_executable', 'brigade.join_team(text, text)'),
  ('authenticated_security_definer_function_executable', 'brigade.leave_team(uuid)'),
  ('authenticated_security_definer_function_executable', 'brigade.register_session(uuid, text, text, text, text, text, text, text, integer, uuid)'),
  ('authenticated_security_definer_function_executable', 'brigade.session_heartbeat(uuid, text, text, text, text, integer)'),
  ('authenticated_security_definer_function_executable', 'brigade.close_session(uuid)'),
  ('authenticated_security_definer_function_executable', 'brigade.list_sessions(uuid, boolean, integer)'),
  ('authenticated_security_definer_function_executable', 'brigade.list_members(uuid)'),
  ('authenticated_security_definer_function_executable', 'brigade.send_message(uuid, uuid, text, text, text, uuid)'),
  ('authenticated_security_definer_function_executable', 'brigade.fetch_inbox(uuid, integer)'),
  ('authenticated_security_definer_function_executable', 'brigade.ack_messages(uuid, uuid[])'),
  ('authenticated_security_definer_function_executable', 'brigade.owns_session_topic(text)');

create temp view api_schemas as select unnest(array['public', 'graphql_public', 'brigade']) as nspname;
-- Function lints scan only the schemas Brigade owns objects in: graphql_public holds nothing but Supabase's own
-- pg_graphql wrapper graphql_public.graphql(...) (kept exposed by config.toml to avoid a config push diff, plan 5.3),
-- whose proconfig Brigade neither writes nor can fix, and whose signature changes with the platform version; it is
-- excluded rather than allow-listed by signature. The table, view and materialized-view lints scan all three.
create temp view fn_schemas as select unnest(array['public', 'brigade']) as nspname;

-- 1. rls_disabled_in_public: a table in an API-exposed schema without row level security.
insert into advisor_findings
select 'rls_disabled_in_public', n.nspname || '.' || c.relname, 'relkind ' || c.relkind::text || ', relrowsecurity false'
  from pg_class c join pg_namespace n on n.oid = c.relnamespace
 where n.nspname in (select nspname from api_schemas) and c.relkind in ('r', 'p') and not c.relrowsecurity;

-- 2. policy_exists_rls_disabled: a table (any schema) that has policies but RLS disabled, so the policies do nothing.
insert into advisor_findings
select distinct 'policy_exists_rls_disabled', n.nspname || '.' || c.relname, 'policy ' || p.polname || ' on a table with RLS disabled'
  from pg_policy p join pg_class c on c.oid = p.polrelid join pg_namespace n on n.oid = c.relnamespace
 where not c.relrowsecurity;

-- 3. rls_enabled_no_policy: RLS enabled but no policy at all (in an exposed schema). Expected: brigade.join_attempts.
insert into advisor_findings
select 'rls_enabled_no_policy', n.nspname || '.' || c.relname, 'RLS enabled, zero policies'
  from pg_class c join pg_namespace n on n.oid = c.relnamespace
 where n.nspname in (select nspname from api_schemas) and c.relkind in ('r', 'p') and c.relrowsecurity
   and not exists (select 1 from pg_policy p where p.polrelid = c.oid);

-- 4. security_definer_view: a view in an exposed schema that is not security_invoker (it runs with its owner's rights).
insert into advisor_findings
select 'security_definer_view', n.nspname || '.' || c.relname, 'view without security_invoker=true'
  from pg_class c join pg_namespace n on n.oid = c.relnamespace
 where n.nspname in (select nspname from api_schemas) and c.relkind = 'v'
   and not coalesce('security_invoker=true' = any(c.reloptions) or 'security_invoker=on' = any(c.reloptions), false);

-- 5. function_search_path_mutable: a function in an exposed schema whose proconfig does not pin search_path.
insert into advisor_findings
select 'function_search_path_mutable', n.nspname || '.' || p.proname || '(' || array_to_string(p.proargtypes::regtype[]::text[], ', ') || ')', 'no search_path in proconfig'
  from pg_proc p join pg_namespace n on n.oid = p.pronamespace
 where n.nspname in (select nspname from fn_schemas) and p.prokind in ('f', 'p')
   and not exists (select 1 from unnest(coalesce(p.proconfig, '{}')) c where c like 'search_path=%');

-- 6. anon_security_definer_function_executable: a security definer function in an exposed schema that anon can execute.
insert into advisor_findings
select 'anon_security_definer_function_executable', n.nspname || '.' || p.proname || '(' || array_to_string(p.proargtypes::regtype[]::text[], ', ') || ')', 'security definer, executable by anon'
  from pg_proc p join pg_namespace n on n.oid = p.pronamespace
 where n.nspname in (select nspname from fn_schemas) and p.prosecdef and has_function_privilege('anon', p.oid, 'execute');

-- 7. authenticated_security_definer_function_executable: the same for authenticated. Expected: the 13 granted RPCs only.
insert into advisor_findings
select 'authenticated_security_definer_function_executable', n.nspname || '.' || p.proname || '(' || array_to_string(p.proargtypes::regtype[]::text[], ', ') || ')', 'security definer, executable by authenticated'
  from pg_proc p join pg_namespace n on n.oid = p.pronamespace
 where n.nspname in (select nspname from fn_schemas) and p.prosecdef and has_function_privilege('authenticated', p.oid, 'execute');

-- 8. rls_references_user_metadata: a policy whose USING/WITH CHECK reads user_metadata (client-writable) from the JWT
--    or auth.users.raw_user_meta_data.
insert into advisor_findings
select 'rls_references_user_metadata', n.nspname || '.' || c.relname || ' policy ' || p.polname, 'references user_metadata / raw_user_meta_data'
  from pg_policy p join pg_class c on c.oid = p.polrelid join pg_namespace n on n.oid = c.relnamespace
 where n.nspname in (select nspname from api_schemas)
   and (coalesce(pg_get_expr(p.polqual, p.polrelid), '') ~* 'user_metadata|raw_user_meta_data'
        or coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '') ~* 'user_metadata|raw_user_meta_data');

-- 9. permissive_rls_policy: a permissive policy that lets everything through — its USING (and, for writes, WITH
--    CHECK) expression is absent or a constant true — or several permissive policies for the same table, command
--    and role (which OR together; the advisor's multiple_permissive_policies). Both shapes are reported here.
insert into advisor_findings
select 'permissive_rls_policy', n.nspname || '.' || c.relname || ' policy ' || p.polname,
       'always-true policy (using: ' || coalesce(pg_get_expr(p.polqual, p.polrelid), '<none>') || ', with check: ' || coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '<none>') || ')'
  from pg_policy p join pg_class c on c.oid = p.polrelid join pg_namespace n on n.oid = c.relnamespace
 where n.nspname in (select nspname from api_schemas) and p.polpermissive
   and (   (p.polcmd in ('r', 'w', 'd', '*') and coalesce(lower(pg_get_expr(p.polqual, p.polrelid)), 'true') = 'true')
        or (p.polcmd in ('a', 'w', '*') and coalesce(lower(pg_get_expr(p.polwithcheck, p.polrelid)), 'true') = 'true'));
insert into advisor_findings
select 'permissive_rls_policy', n.nspname || '.' || c.relname, 'multiple permissive policies for command ' || p.polcmd::text || ' and role ' || r.rolname || ': ' || string_agg(p.polname, ', ' order by p.polname)
  from pg_policy p join pg_class c on c.oid = p.polrelid join pg_namespace n on n.oid = c.relnamespace
  cross join lateral unnest(case when p.polroles = '{0}'::oid[] then array['public'::text] else (select array_agg(rolname::text) from pg_roles where oid = any(p.polroles)) end) as r(rolname)
 where n.nspname in (select nspname from api_schemas) and p.polpermissive
 group by n.nspname, c.relname, p.polcmd, r.rolname having count(*) > 1;

-- 10. materialized_view_in_api: a materialized view in an exposed schema (they bypass RLS and are readable by every API role).
insert into advisor_findings
select 'materialized_view_in_api', n.nspname || '.' || c.relname, 'materialized view exposed through the Data API'
  from pg_class c join pg_namespace n on n.oid = c.relnamespace
 where n.nspname in (select nspname from api_schemas) and c.relkind = 'm';

-- Report every finding, then verdict.
select f.lint, f.object, case when e.lint is null then 'UNEXPECTED' else 'expected' end as status, f.detail
  from advisor_findings f left join advisor_expected e on e.lint = f.lint and e.object = f.object
 order by status, f.lint, f.object;

do $$
declare v_unexpected text; v_missing text;
begin
  select string_agg(f.lint || ' ' || f.object, '; ' order by f.lint, f.object) into v_unexpected
    from advisor_findings f left join advisor_expected e on e.lint = f.lint and e.object = f.object where e.lint is null;
  select string_agg(e.lint || ' ' || e.object, '; ' order by e.lint, e.object) into v_missing
    from advisor_expected e left join advisor_findings f on f.lint = e.lint and f.object = e.object where f.lint is null;
  if v_unexpected is not null then
    raise exception 'advisor-lints: UNEXPECTED finding(s): %', v_unexpected;
  end if;
  if v_missing is not null then
    raise exception 'advisor-lints: expected finding(s) MISSING (a lint mirror or the schema changed): %', v_missing;
  end if;
  raise notice 'advisor-lints: clean — % finding(s), all on the documented expected list', (select count(*) from advisor_findings);
end $$;
