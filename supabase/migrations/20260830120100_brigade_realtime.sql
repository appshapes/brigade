-- Brigade realtime migration (plan 5.6, D21): Broadcast from Database as a wake-up hint.
-- DRAFT (E0-1): P2-3 finishes and commits this file.
--
-- An AFTER INSERT trigger on brigade.messages calls realtime.send with an ids-only payload
-- on the private topic brigade:session:<recipient_session_id>. The durable delivery path is
-- fetch_inbox draining delivery_state = 'accepted' rows; realtime is never a transport.
-- Deliberately NO publication change and no REPLICA IDENTITY: Broadcast-from-DB is chosen
-- over postgres_changes (plan 5.6 rationale; postgres_changes is the E0-2 fallback).
--
-- Applied after 20260830120000 (schema, RPCs, triggers): brigade.messages, brigade.sessions
-- and brigade.memberships already exist.

-- Notify the recipient session's topic on every accepted message.
-- security definer is REQUIRED: the trigger otherwise runs as authenticated, which has no
-- insert policy on realtime.messages, and realtime.send downgrades the refused insert to a
-- WARNING — the message row would commit and no notification would ever be sent [verified
-- live, realtime digest 3.3]. As definer it runs as postgres (BYPASSRLS).
create or replace function brigade.notify_message_inserted()
returns trigger language plpgsql security definer set search_path = ''
as $$
begin
  perform realtime.send(jsonb_build_object('message_id', new.id, 'seq', new.seq),   -- ids only; never the body (T4)
                        'message_accepted', 'brigade:session:' || new.recipient_session_id::text, true);
  return null;
end $$;
-- Server-only trigger function: revoke the built-in PUBLIC EXECUTE and grant nobody, so its
-- proacl is non-null with no empty-grantee (=X) entry (functions.sql, plan 9.3). A direct call
-- fails anyway ("trigger functions can only be called as triggers"); the hygiene is uniform.
revoke execute on function brigade.notify_message_inserted() from public, anon;

create trigger messages_notify after insert on brigade.messages for each row execute function brigade.notify_message_inserted();

-- Topic ownership check for the realtime.messages select policy below. Evaluated by Realtime
-- in the join authorization query (and on every access_token push), so it must be callable by
-- authenticated. Requires an active membership: a revoked member loses the topic at the next
-- join or JWT check (D22; rls_isolation.sql asserts false for a revoked member's own session).
create or replace function brigade.owns_session_topic(p_topic text) returns boolean
language sql stable security definer set search_path = ''
as $$ select exists (select 1 from brigade.sessions s
                     join brigade.memberships m on m.team_id = s.team_id and m.user_id = s.owner_id and m.status = 'active'
                     where s.owner_id = (select auth.uid()) and s.closed_at is null
                       and p_topic = 'brigade:session:' || s.id::text) $$;   -- revoked members lose the topic at the next join/JWT check
revoke execute on function brigade.owns_session_topic(text) from public, anon;
grant  execute on function brigade.owns_session_topic(text) to authenticated;

-- Receive-only authorization on the topic. No insert policy: clients cannot broadcast; only the database emits.
-- realtime.topic() returns the bare topic without the 'realtime:' prefix (plan 5.6, verified; E0-2 (b) re-asserts).
create policy brigade_session_topic_read on realtime.messages for select to authenticated
  using ( realtime.messages.extension = 'broadcast' and brigade.owns_session_topic((select realtime.topic())) );
