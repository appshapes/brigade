-- Brigade session human-label migration (capability session.human_label; C-45). Applied after
-- 20260910193200_session_model_context.sql. It appends ONE parameter to brigade.register_session:
-- p_human_label, the harness's DEFAULT label for its principal (docs/protocol-v1.md 4.4.2 `human_label`,
-- ≤ max_human_label_chars = 128 code points, unverified display text).
--
-- Why this exists: every membership created before the label did has an empty human_label, and the owner does not
-- want anyone to rejoin a team to acquire one. Registration happens at every session start and at every watcher
-- replacement, so the plugin update itself delivers the fill.
--
-- The rule, and it is the whole rule: the label is adopted ONLY when the membership has none. The membership lookup
-- that already reads human_label into v_label is unchanged; when that v_label is empty and p_human_label is not
-- null, the adoption writes that one membership row and sets v_label, so the record this call returns already
-- carries the adopted label. It sits after the validations (a registration this call is about to refuse writes
-- nothing) and before the branch that builds the row. A membership that HAS a label is never written — no
-- registration, of any harness, at any time, can overwrite a label a member chose; `label: none` in the plugin
-- simply sends no p_human_label at all. `coalesce(…, '') = ''` rather than `is null` so a membership row carrying
-- an empty string, whatever wrote it, counts as unlabelled too.
--
-- Migrations are append-only once applied (`supabase db push` tracks them by file name), so the schema file and
-- 20260910193200 are NOT edited: the body below repeats 20260910193200's register_session byte for byte apart from
-- the appended parameter, its validation and the five adoption lines.
--
-- DROP FUNCTION rather than CREATE OR REPLACE, for the reason 20260910193200 gives at length: a replace with a
-- different parameter list OVERLOADS (a second pg_proc row), PostgREST answers a named-argument RPC matching two
-- candidates with 300 Multiple Choices, and supabase/tests/functions.sql pins the pg_proc row count. The drop takes
-- the old ACL with it, so revoke and grant are restated with the FULL new argument-type list, and
-- scripts/ci/advisor-lints.sql's expected-signature row is updated in the same commit. Every existing positional
-- caller keeps working because the parameter is APPENDED with default null; an adapter whose backend lacks this
-- migration gets PGRST202 and retries without the parameter (internal/adapters/supabase/compat.go).
--
-- The cap is checked twice, as workspace_label's and model's are: the RPC raises the D15 text
-- 'brigade:invalid_input:human_label' (errcode 22023, so the adapter answers invalid_input with
-- details.field = human_label), and memberships.human_label's own column constraint is the belt beneath it.

drop function brigade.register_session(uuid, text, text, text, text, text, text, text, integer, uuid, text, bigint);
create function brigade.register_session(
  p_team_id uuid, p_name text, p_description text default null, p_activity text default 'idle',
  p_inbound text default null, p_harness text default null, p_harness_version text default null,
  p_workspace_label text default null, p_lease_seconds integer default 90, p_resume_session_id uuid default null,
  p_model text default null, p_context_used_tokens bigint default null, p_human_label text default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_row brigade.sessions%rowtype; v_resumed boolean := false; v_label text;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  select human_label into v_label from brigade.memberships where team_id = p_team_id and user_id = v_uid and status = 'active';
  if not found then raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  if p_name is null or char_length(p_name) not between 1 and 64 then raise exception 'brigade:invalid_input:session_name' using errcode = '22023'; end if;
  if p_description is not null and char_length(p_description) > 256 then raise exception 'brigade:invalid_input:session_description' using errcode = '22023'; end if;
  if p_activity is null or p_activity not in ('busy','idle') then raise exception 'brigade:invalid_input:activity' using errcode = '22023'; end if;
  if p_inbound is not null and p_inbound not in ('accept','hold','refuse') then raise exception 'brigade:invalid_input:inbound' using errcode = '22023'; end if;
  if p_harness is not null and char_length(p_harness) > 32 then raise exception 'brigade:invalid_input:harness' using errcode = '22023'; end if;
  if p_harness_version is not null and char_length(p_harness_version) > 32 then raise exception 'brigade:invalid_input:harness_version' using errcode = '22023'; end if;
  if p_workspace_label is not null and char_length(p_workspace_label) > 128 then raise exception 'brigade:invalid_input:workspace_label' using errcode = '22023'; end if;
  if p_human_label is not null and char_length(p_human_label) > 128 then raise exception 'brigade:invalid_input:human_label' using errcode = '22023'; end if;
  if p_lease_seconds is null or p_lease_seconds not between 30 and 600 then raise exception 'brigade:invalid_input:lease_seconds' using errcode = '22023'; end if;
  if p_model is not null and char_length(p_model) > 128 then raise exception 'brigade:invalid_input:model' using errcode = '22023'; end if;
  if p_context_used_tokens is not null and p_context_used_tokens < 0 then raise exception 'brigade:invalid_input:context_used_tokens' using errcode = '22023'; end if;

  -- C-45: fill an EMPTY membership label, never replace one. It runs after every validation, so a registration this
  -- call is about to refuse writes nothing, and before the record is built, so the returned record already carries
  -- the adopted label. The update repeats `human_label is null` in its predicate as well as in the branch, so two
  -- concurrent registrations cannot both write it.
  if coalesce(v_label, '') = '' and p_human_label is not null then
    update brigade.memberships set human_label = p_human_label
     where team_id = p_team_id and user_id = v_uid and status = 'active' and coalesce(human_label, '') = '';
    v_label := p_human_label;
  end if;

  if p_resume_session_id is not null then
    -- 4.5.8 / C-19b: an owned session that is open with a valid lease is live; refuse before touching the row.
    if exists (select 1 from brigade.sessions
                where id = p_resume_session_id and owner_id = v_uid and team_id = p_team_id and closed_at is null
                  and last_seen_at + make_interval(secs => lease_seconds) > now()) then
      raise exception 'brigade:conflict:session_live' using errcode = 'P0001';   -- 4.5.8: two processes never drain one inbox
    end if;
    update brigade.sessions
       set closed_at = null, last_seen_at = now(), name = p_name, description = p_description, activity = p_activity,
           inbound = p_inbound, harness = p_harness, harness_version = p_harness_version,
           workspace_label = p_workspace_label, lease_seconds = p_lease_seconds,
           model = p_model, context_used_tokens = p_context_used_tokens
     where id = p_resume_session_id and owner_id = v_uid and team_id = p_team_id
    returning * into v_row;                                                 -- single row variable: valid INTO
    if not found then raise exception 'brigade:not_found' using errcode = 'PT404'; end if;
    v_resumed := true;
  else
    -- 120 new sessions per principal per hour. The cap bounds a registration flood (every row is a session the whole
    -- team lists and can be messaged), and its floor is the conformance suite's own budget: one --slow run registers
    -- about 55 sessions on its busiest principal, which the earlier cap of 30 refused with
    -- rate_limited:register_session on eleven cases (C-29, C-29b, C-30, C-31, C-32, C-34, C-35, C-36, C-39, C-41,
    -- C-42). Resumes do not count (they re-open a row). The window and the retry trailer are unchanged.
    if (select count(*) from brigade.sessions where owner_id = v_uid and created_at > now() - interval '1 hour') >= 120 then
      raise exception 'brigade:rate_limited:register_session:3600' using errcode = 'P0001'; end if;
    insert into brigade.sessions (team_id, owner_id, name, description, activity, inbound, harness, harness_version, workspace_label, lease_seconds,
                                  model, context_used_tokens)
    values (p_team_id, v_uid, p_name, p_description, p_activity, p_inbound, p_harness, p_harness_version, p_workspace_label, p_lease_seconds,
            p_model, p_context_used_tokens)
    returning * into v_row;
  end if;
  return brigade.session_record(v_row, v_label)
      || jsonb_build_object('resumed', v_resumed, 'lease_seconds', v_row.lease_seconds, 'server_time', now());
end $$;
revoke execute on function brigade.register_session(uuid, text, text, text, text, text, text, text, integer, uuid, text, bigint, text) from public, anon;
grant  execute on function brigade.register_session(uuid, text, text, text, text, text, text, text, integer, uuid, text, bigint, text) to authenticated;
