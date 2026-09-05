# 5. Supabase adapter design

Design record from 2026-08-30. Where this text and the code disagree, the code, docs/protocol-v1.md and the execution log are authoritative; the corrections block below lists what changed.

## Corrections recorded in the execution log

- 2026-08-31 — "Plan corrections from E0-8" (archive) — 5.1: the adapter must not honour `NO_PROXY` for loopback inside the sandbox, and the sandbox allowlist matches the LITERAL host as written in the URL, not a resolved address.
- 2026-08-31 — "Plan corrections required before Phase 2 (from E0-6)" (archive) — 5.1: "a token two or more steps behind revokes the family" is WRONG on GoTrue v2.196.0 — the stale token is refused and the family survives, so `refresh_token_already_used` must not be treated as terminal.
- 2026-08-31 — "Plan corrections required before Phase 2 (from E0-6)" (archive) — 5.1 profile lock: the lock is fine, the 100 ms `LOCK_NB` poll is the cost (23 of 80 racers contended at a median 101.1 ms while the lock is held 36 ms); block with a 10 s timeout or drop the poll to 5-10 ms.
- 2026-08-31 — "Plan corrections required before Phase 2 (from E0-6)" (archive) — 5.1: one-behind refresh tolerance is LOAD-BEARING for crash recovery, not a convenience — without it any crash between the refresh answer and the atomic write strands the profile.
- 2026-08-31 — "Plan corrections required before Phase 2 (from E0-6)" (archive) — 5.1: PostgREST allows ~30 s of clock skew past `exp`, so a JWT-expired answer is not a precise expiry signal, and only the 90 s margin trigger fires in normal operation.
- 2026-09-05 — "P5-3 DONE" — 5.8: the anonymous-user cleanup is a separate `brigade.gc_anonymous_users()` (plpgsql, its own exception handler answering -1) called LAST from `gc_expired()`, not an inline delete — `gc_expired()` is `language sql` and cannot trap, and it is called from `session_heartbeat` at p=0.02, so an uncaught 23503 would fail a member's heartbeat and lapse its lease (proven by mutation: without the handler both the housekeeping statements and the heartbeat abort). The predicate excludes the creator of ANY team (the plan's "live team": teams have no live state) and any principal with a membership row of any status; `order by created_at limit 1000`.
- 2026-09-05 — "P5-3 DONE" — 5.1 / 5.8: a reaped anonymous principal's unexpired access token is inert until expiry (≤ 3600 s): `my_team_ids()` answers an empty set, `create_team`/`join_team` answer 23503 (measured, rolled back). `auth.users` has 12 referencing foreign keys, all `on delete cascade` except `teams.created_by` (`restrict`).

- 2026-09-05 — "P5-2 DONE" — 5.10: the teams column is `secret_version` and the memberships column `joined_secret_version` (not `join_secret_version`); the signatures are `revoke_membership(p_team_id, p_user_id, p_ban)` and `revoke_memberships_by_version(p_team_id, p_max_version)` (`p_principal_ref`/`p_version` were slips); `team rotate-secret` never prints the secret (protocol 4.5 rule 14) — `--secret-file` is mandatory; a revoked member's channel ends within milliseconds on the shipped path because `revoke_membership` writes the same `membership_revoked` broadcast `leave_team` writes — "until the JWT expires … at most 1 h" is only the no-hint row, unreachable by this adapter. 4.2/4.7 and `docs/protocol-v1.md` needed no change: the frozen document already provided for these verbs.
<!-- verbatim from the one-file plan -->
## 5. Supabase adapter design

Everything in this section was verified on a local stack on 2026-08-30 unless marked otherwise: CLI 2.116.0, Postgres 17.6, GoTrue v2.196.0, PostgREST v16.1, Realtime v2.129.3, Kong 2.8.1; the morning checks used supabase-js 2.112.4 (auth digest section 9; realtime digest section 1) and the afternoon checks repeated the request and response shapes with a hand-rolled Go client (Go 1.27.0, `coder/websocket` v1.8.15; the supabase-in-go digest, A.7), which is what the adapter now implements. The SQL merges the auth digest's live-tested schema (ported from `public` to `brigade` and from direct inserts to RPC-only writes), the realtime digest's lease/ack/trigger design and the threat model's membership status and limits. The merged text has not itself been executed; it is applied and covered by pgTAP or an integration test in Phase 2 before it is trusted, and it already fixes the four defects the judges verified in the security-risk-first candidate on the Postgres 17.6 image (a rowtype inside a multi-item `INTO` list; a deliberate `23505` swallowed by the function's own `unique_violation` handler; `rejoined` computed after `FOUND` had been overwritten; `envelope()` applied to a subquery alias of type `record`).

### 5.1 Principal and credentials

- One anonymous Supabase user per adapter profile: `POST /auth/v1/signup` with the body auth-js's `signInAnonymously()` sends, `{"data":{},"gotrue_meta_security":{}}`; the answer is `{access_token, token_type, expires_in, expires_at, refresh_token, user:{id, aud, role, is_anonymous:true, …}}` and the JWT carries `role: authenticated`, `is_anonymous: true`, `sub` = `user.id` and a `session_id` claim [verified live, A.7]. Membership, not anonymity, is the authorization gate; policies never read `is_anonymous`.
- The publishable key (`sb_publishable_…`) is configuration and may ship in a profile or docs ("Safe to expose online … CLIs, source code", https://supabase.com/docs/guides/api/api-keys) [verified]. Secret and service-role keys never appear in the adapter, the plugin, the repo or the CI variables used by tests that ship.
- Anonymous sign-up happens only inside `team create` and `team join`. Every other command loads `session.json` first and maps a missing or unparsable file to `unauthenticated` (T6: routine commands never mint principals; C-01, C-06).
- Client (`internal/adapters/supabase`, hand-rolled on `net/http`, D35): one `http.Client` (20 s per request, 10 s connect); `X-Client-Info: brigade-adapter-supabase/<version>`; the publishable key as the `apikey` header on every request to every service (local Kong 2.8.1 does not enforce it on `/auth/v1` and `/rest/v1`, the hosted gateway is documented to [likely], and Realtime validates it itself at the WebSocket upgrade [verified, A.7]); `X-Supabase-Api-Version: 2024-01-01` on GoTrue requests so error bodies arrive as `{"code":"<snake_case>","message":"…"}` (without the header they are `{"code":<number>,"error_code":"…","msg":"…"}`; the decoder accepts both shapes) [verified, A.7]. TLS: the binary embeds the Mozilla root bundle (`golang.org/x/crypto/x509roots/fallback` plus `//go:debug x509usefallbackroots=1` in `package main`) because Go's darwin verifier cannot reach Security.framework inside the Bash sandbox (`x509: OSStatus -26276`) and minimal Linux containers ship no CA bundle [verified, A.7]; `SSL_CERT_FILE`/`SSL_CERT_DIR` are honoured when non-empty so corporate CAs keep working; `HTTPS_PROXY` is honoured by the default transport, which is how a sandboxed `brigade send` reaches the backend through the sandbox's proxy. The three GoTrue calls: sign-up as above; refresh `POST /auth/v1/token?grant_type=refresh_token` with body `{"refresh_token":"…"}` (200 → the sign-up shape with a new access token and a new refresh token); global sign-out `POST /auth/v1/logout?scope=global` with `Authorization: Bearer <access_token>` (204, empty body) [verified, A.7].
- Credential file `profiles/<name>/session.json`: the sign-up or refresh response as returned (`access_token`, `refresh_token`, `expires_at`, `expires_in`, `user`), written by `adapterkit.WriteAtomic` (`os.CreateTemp` 0600 in the 0700 profile directory, write, fsync, rename, fsync the directory [verified, A.7]). Every read-refresh-write runs under an advisory `flock(LOCK_EX)` on the sidecar `session.json.lock` (never on the file that is renamed over: a lock on the old inode would not protect the new one [verified, A.7]) with a 10 s bound (`LOCK_NB` polled every 100 ms, then `unavailable`); after acquiring, the file is re-read and the refresh is skipped when another process already stored a token with more than 90 s left. Refresh happens when fewer than 90 s remain before `expires_at` (auth-js's margin) and after a `PGRST303` (`JWT expired`) answer. Server rules measured today (GoTrue v2.196.0, rotation on, reuse interval 10 s): two concurrent refreshes with the same token both succeed and receive the same new refresh token; a token one step behind the active one is accepted and answered with the active token; a token two or more steps behind revokes the family (`refresh_token_already_used`) [verified, A.7]. That is why two Brigade processes on one file are safe with the lock, and why the sandboxed CLI's in-memory refresh is safe without persisting.
- Read-only fallback: when the profile directory cannot be written (`EPERM`/`EROFS`; the Bash sandbox denies writes under `~/.config` and `~/.local/state` [verified, A.7]), the adapter refreshes in memory, uses the new access token for the command and does not persist; the file keeps the previous refresh token, which the next writer (normally the watcher, which runs unsandboxed) redeems as "one step behind". E0-6 measures this combination.
- Terminal credential errors: `refresh_token_already_used` → re-read the file once under the lock (another process may have rotated it), retry once with the newer token, then terminal; `refresh_token_not_found`, `session_not_found`, `session_expired`, `user_not_found` → terminal at once. Terminal means: clear `session.json`, exit 4 with the message "credential revoked; run `brigade team join` again". Never retry `/token` in a loop (the IP limit of 1800/h is shared behind a NAT).
- Revocation of a leaked credential: Supabase refresh tokens never expire on their own (rotation only) [verified, auth digest 1.5], so a copied `session.json` stays usable until its refresh-token family is revoked server-side. `profile reset` therefore calls the global sign-out best-effort (network failure ignored, 5 s cap) before deleting the files, and `profile revoke-credentials` performs only the sign-out: afterwards both the current and the previous refresh token answer `refresh_token_not_found`, while the access JWT stays valid on PostgREST until its expiry [verified live, A.7; docs: https://supabase.com/docs/reference/javascript/auth-signout]. The user then runs `brigade team join` again with the same profile, which mints a new principal (the accepted trade-off of the logical plan). Integration test (P2-6): after `profile reset`, a copy of the previous `session.json` yields `refresh_token_not_found` on refresh.
- The adapter never verifies JWTs and reads only `exp` from the payload for the refresh margin: local user tokens are ES256 while the legacy anon key is HS256 [verified, A.7], so nothing may assume an algorithm.
- A pre-existing world-readable `profile.json` or `session.json` is refused with exit 11 `config` (U-10).

### 5.2 Profile file (adapter-specific, not protocol)

`${BRIGADE_CONFIG_DIR}/profiles/<name>/profile.json` (0600):

```json
{"version": 1, "adapter": "supabase", "url": "https://<ref>.supabase.co", "publishable_key": "sb_publishable_…",
 "team_ref": "…", "team_name": "ops", "principal_ref": "…", "human_label": "alice@example.com",
 "secret_store": "file", "created_at": "…"}
```

`profile init --url … --key … [--force]` writes `url` and `publishable_key` (both non-secret, so argv is acceptable; these flags are the Supabase adapter's extras beyond the convention grammar of 4.1) and exits 7 `conflict` on a profile that already has a backend unless `--force`, which rewrites the two backend fields only and never touches `session.json` or the team binding; `team join` also accepts them in `backend`. Both paths reject a `url` whose scheme is not `https:` unless the host is a loopback address (`127.0.0.1`, `localhost`, `::1`, for the local stack) with exit 3 `invalid_input` and the message "backend url must use https (http is allowed only for 127.0.0.1/localhost)": an `http://` backend would send the anonymous JWT and refresh token in clear on every command and Realtime connection, and the setup skill prints a URL the human pastes from a message. Unit test both branches (U-26). `team create` requires an initialised backend and an unbound profile (otherwise exit 7 `conflict`, `details.reason = "profile_bound"`; use `profile reset` or another `--profile`). `profile status` prints the principal id prefix, team name and token expiry, never tokens. `team leave` calls `leave_team` (5.4) and then clears `team_ref` and `team_name` from `profile.json`, keeping `session.json`, so `describe.profile.state` becomes `not_member` and a later `team join` with the secret reuses the principal. `profile reset` signs the principal out globally (best effort, 5.1) and deletes both files; `profile revoke-credentials` only signs out (5.1).

### 5.3 Schema (migration `supabase/migrations/20260830120000_brigade_schema.sql`)

```sql
create schema if not exists brigade;
create extension if not exists pgcrypto with schema extensions;      -- pre-installed on Supabase; no-op there [verified live]
revoke all on schema brigade from public;
grant usage on schema brigade to authenticated, service_role;       -- deliberately NOT anon (T4)
-- service_role gets schema usage only: BYPASSRLS skips row policies but not GRANTs, and this schema never
-- grants it table or function privileges (the Supabase custom-schema recipe grants ALL to service_role;
-- Brigade deliberately does not). Consequence: the Data API with the secret/service-role key cannot read or
-- write brigade.* (42501), and test fixtures are written as `postgres` with simulated JWT claims (9.3), never
-- through PostgREST with an admin key.
-- Every function created in this schema gets PostgreSQL's built-in EXECUTE ... TO PUBLIC. A per-schema
-- `alter default privileges in schema brigade revoke execute on functions from public` does NOT remove it:
-- per-schema defaults only add to the global ones, and the manual's own example says the statement "has no
-- effect" [verified live on 2026-08-30 (pg_default_acl stays empty, new functions keep proacl NULL) and in
-- https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html]. So every function in this schema
-- carries an explicit `revoke execute on function ... from public` right after its `create function` (5.4
-- lists them), and functions.sql (9.3) asserts that no brigade function has an ACL entry with an empty grantee.

create table brigade.teams (
  id                uuid primary key default gen_random_uuid(),
  name              text not null check (char_length(name) between 1 and 64),
  created_by        uuid references auth.users(id) on delete restrict,   -- the only administrative authority (5.10); never orphaned silently; P5-3 skips live-team creators
  secret_hash       text not null,                      -- bcrypt of the random part only
  secret_version    integer not null default 1,
  secret_rotated_at timestamptz,
  created_at        timestamptz not null default now()
);

create table brigade.memberships (
  team_id               uuid not null references brigade.teams(id) on delete cascade,
  user_id               uuid not null references auth.users(id) on delete cascade,
  human_label           text check (char_length(human_label) <= 128),
  status                text not null default 'active' check (status in ('active','revoked','banned')),
  joined_secret_version integer not null,
  joined_at             timestamptz not null default now(),
  revoked_at            timestamptz,
  primary key (team_id, user_id)
);
create index memberships_user_idx on brigade.memberships (user_id);   -- the PK only indexes team_id (RLS guide)

create table brigade.sessions (
  id               uuid primary key default gen_random_uuid(),
  team_id          uuid not null references brigade.teams(id) on delete cascade,
  owner_id         uuid not null references auth.users(id) on delete cascade,
  name             text not null check (char_length(name) between 1 and 64),
  description      text check (char_length(description) <= 256),
  activity         text not null default 'idle' check (activity in ('busy','idle')),
  inbound          text check (inbound in ('accept','hold','refuse')),
  harness          text check (char_length(harness) <= 32),
  harness_version  text check (char_length(harness_version) <= 32),
  workspace_label  text check (char_length(workspace_label) <= 128),
  lease_seconds    integer not null default 90 check (lease_seconds between 30 and 600),
  last_seen_at     timestamptz not null default now(),
  closed_at        timestamptz,
  created_at       timestamptz not null default now()
);
create index sessions_team_seen_idx on brigade.sessions (team_id, last_seen_at desc);
create index sessions_owner_idx     on brigade.sessions (owner_id, created_at desc);

create table brigade.messages (
  id                   uuid primary key default gen_random_uuid(),
  seq                  bigint generated always as identity,         -- ordering hint for one recipient's drain, never a cursor
  team_id              uuid not null references brigade.teams(id) on delete cascade,
  sender_user_id       uuid not null references auth.users(id) on delete cascade,
  sender_session_id    uuid not null references brigade.sessions(id) on delete cascade,
  recipient_session_id uuid not null references brigade.sessions(id) on delete cascade,
  kind                 text not null default 'text' check (kind = 'text'),
  summary              text check (char_length(summary) <= 200),
  body                 text not null check (octet_length(body) between 1 and 16384),
  body_hash            bytea not null,                               -- for the idempotency conflict check only
  reply_to             uuid references brigade.messages(id) on delete set null,
  hop_count            integer not null default 0 check (hop_count between 0 and 32),
  idempotency_key      text not null check (char_length(idempotency_key) between 1 and 128),
  delivery_state       text not null default 'accepted' check (delivery_state in ('accepted','injected')),
  created_at           timestamptz not null default now(),
  injected_at          timestamptz,
  unique (sender_session_id, idempotency_key)
);
create index messages_pending_idx       on brigade.messages (recipient_session_id, seq) where delivery_state = 'accepted';
create index messages_sender_recent_idx on brigade.messages (sender_session_id, created_at desc);
create index messages_created_idx       on brigade.messages (created_at);
create index messages_injected_idx      on brigade.messages (injected_at) where injected_at is not null;

create table brigade.join_attempts (
  id           bigint generated always as identity primary key,
  user_id      uuid not null,
  team_id      uuid,
  succeeded    boolean not null default false,
  attempted_at timestamptz not null default now()
);
create index join_attempts_user_time_idx on brigade.join_attempts (user_id, attempted_at desc);
create index join_attempts_team_time_idx on brigade.join_attempts (team_id, attempted_at desc);

alter table brigade.teams         enable row level security;
alter table brigade.memberships   enable row level security;
alter table brigade.sessions      enable row level security;
alter table brigade.messages      enable row level security;
alter table brigade.join_attempts enable row level security;   -- no policies, no grants: server-only (documented for the advisor lint)

-- Membership helper: the single source of team scoping (T4).
create or replace function brigade.my_team_ids()
returns setof uuid language sql stable security definer set search_path = ''
as $$ select m.team_id from brigade.memberships m where m.user_id = (select auth.uid()) and m.status = 'active' $$;
revoke execute on function brigade.my_team_ids() from public, anon;
grant  execute on function brigade.my_team_ids() to authenticated;

-- Read policies only. There are NO insert/update/delete policies on any table: writes are RPC-only (D22).
create policy teams_select on brigade.teams for select to authenticated
  using ( id in (select brigade.my_team_ids()) );
create policy memberships_select on brigade.memberships for select to authenticated
  using ( team_id in (select brigade.my_team_ids()) );            -- the roster is visible to every active member (D22)
-- sessions: caller must be an active member, and the row's owner must still be one (a revoked member's
-- sessions disappear from direct selects too, not only inside list_sessions).
create policy sessions_select on brigade.sessions for select to authenticated
  using ( team_id in (select brigade.my_team_ids())
          and exists (select 1 from brigade.memberships m
                      where m.team_id = sessions.team_id and m.user_id = sessions.owner_id and m.status = 'active') );
-- messages: ownership AND active membership. Without the membership predicate a revoked or banned principal
-- would keep reading every message addressed to its sessions.
create policy messages_select on brigade.messages for select to authenticated
  using ( team_id in (select brigade.my_team_ids())
          and ( sender_user_id = (select auth.uid())
                or exists (select 1 from brigade.sessions s
                           where s.id = messages.recipient_session_id and s.owner_id = (select auth.uid())) ) );

revoke all on all tables in schema brigade from anon, authenticated, service_role;
grant select (id, name, secret_version, created_at) on brigade.teams to authenticated;   -- never secret_hash
grant select on brigade.memberships, brigade.sessions, brigade.messages to authenticated;
-- join_attempts: nothing. service_role: nothing (see the schema comment). No views in v1 (if one is added: with (security_invoker = true)).
```

Notes: policies name `to authenticated` (anonymous users run as `authenticated` [verified]); `(select auth.uid())` and the helper are wrapped for per-statement evaluation (https://supabase.com/docs/guides/database/postgres/row-level-security); columns inside policy subqueries are qualified (`messages.recipient_session_id`) because an unqualified name binds to the subquery's table (a bug the auth digest caught by testing); `select('*')` on `teams` is `42501` because of the column-level grant, so the adapter names columns [verified live]. Data API exposure: local `config.toml` `[api] schemas = ["public", "graphql_public", "brigade"]` (keeping `graphql_public` avoids a `config push` diff); hosted Dashboard > Project Settings > API > Exposed schemas, or `supabase config push` [verified: https://supabase.com/docs/guides/api/using-custom-schemas].

### 5.4 RPCs (all `security definer`, `set search_path = ''`, `revoke execute … from public, anon`, `grant execute … to authenticated` unless stated)

Error convention (D15): `join_team` returns a `status` instead of raising for expected failures so the attempt row commits (PostgREST wraps each request in one transaction; a `RAISE` rolls the row back [verified live]). Every other failure raises with a message of the form `brigade:<code>[:<detail>]`; the adapter maps on the prefix first and on SQLSTATE second. Business errors that the adapter must distinguish from database errors use SQLSTATE `P0001` on purpose; in particular the idempotency conflict is never raised with `23505`, so the `unique_violation` handler around the insert cannot swallow it. PostgREST maps these SQLSTATEs to HTTP statuses of its own (`P0001` and `22023` → 400, `42501` → 403 with a JWT, `28000` → 403, `P0002` → 500 [verified, A.7; documented at https://docs.postgrest.org/en/latest/references/errors.html]); the adapter maps on the body first, so the 500 for a routine not-found is cosmetic (gateway logs), and replacing `P0002` with a custom `PT404` code is open question 11.2 (9).

| Failure | errcode | message | HTTP status from PostgREST [verified, A.7] |
| --- | --- | --- | --- |
| no JWT | `28000` | `brigade:unauthenticated` | 403 (only reachable by a role with USAGE on the schema, i.e. `service_role` or a JWT with a null `sub`; the `anon` role is stopped before the function with 401 `permission denied for schema brigade`) |
| not an active member | `42501` | `brigade:unauthorized` | 403 |
| unknown, foreign or not-owned session; unknown `reply_to` | `P0002` | `brigade:not_found` | 500 |
| bad input | `22023` | `brigade:invalid_input:<field>` | 400 |
| idempotency key with a different payload; closed sender; resume of a live session (`session_live`) | `P0001` | `brigade:conflict:<detail>` | 400 |
| limits | `P0001` | `brigade:rate_limited:<reason>:<retry_after_seconds>` | 400 |
| loop | `P0001` | `brigade:loop_detected:<reason>` | 400 |

```sql
-- Helpers (execute granted to nobody; called only from the RPCs below).
create or replace function brigade.session_record(s brigade.sessions, p_label text)
returns jsonb language sql stable set search_path = ''
as $$ select jsonb_build_object(
  'session_id', s.id, 'session_name', s.name, 'session_description', s.description,
  'principal_ref', s.owner_id, 'human_label', p_label,
  'state', case when s.closed_at is not null or s.last_seen_at < now() - make_interval(secs => s.lease_seconds) then 'offline'
                when s.activity = 'busy' then 'active' else 'idle' end,
  'activity', s.activity, 'inbound', s.inbound,
  'last_seen_at', s.last_seen_at, 'lease_until', s.last_seen_at + make_interval(secs => s.lease_seconds),
  'harness', s.harness, 'harness_version', s.harness_version, 'workspace_label', s.workspace_label,
  'created_at', s.created_at) $$;
revoke execute on function brigade.session_record(brigade.sessions, text) from public, anon, authenticated;

create or replace function brigade.envelope(m brigade.messages)
returns jsonb language sql stable set search_path = ''
as $$ select jsonb_build_object('protocol_version', '1', 'kind', m.kind, 'message_id', m.id, 'team_ref', m.team_id,
  'sender', jsonb_build_object('principal_ref', m.sender_user_id,
     'human_label', (select mm.human_label from brigade.memberships mm where mm.team_id = m.team_id and mm.user_id = m.sender_user_id),
     'session_id', m.sender_session_id,
     'session_name', (select s.name from brigade.sessions s where s.id = m.sender_session_id)),
  'recipient_session_id', m.recipient_session_id, 'summary', m.summary, 'body', m.body, 'reply_to', m.reply_to,
  'hop_count', m.hop_count, 'created_at', m.created_at, 'delivery_state', m.delivery_state) $$;
revoke execute on function brigade.envelope(brigade.messages) from public, anon, authenticated;

create or replace function brigade.message_result(m brigade.messages, p_dup boolean)
returns jsonb language sql stable set search_path = ''
as $$ select jsonb_build_object('message_id', m.id, 'recipient_session_id', m.recipient_session_id,
             'created_at', m.created_at, 'duplicate', p_dup, 'hop_count', m.hop_count) $$;
revoke execute on function brigade.message_result(brigade.messages, boolean) from public, anon, authenticated;

-- create_team: the caller becomes the first member; the secret is generated server-side and returned once.
create or replace function brigade.create_team(p_name text, p_human_label text default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_id uuid; v_random text;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if p_name is null or char_length(p_name) not between 1 and 64 then
    raise exception 'brigade:invalid_input:team_name' using errcode = '22023'; end if;
  if p_human_label is not null and char_length(p_human_label) > 128 then
    raise exception 'brigade:invalid_input:human_label' using errcode = '22023'; end if;
  if (select count(*) from brigade.teams where created_by = v_uid and created_at > now() - interval '1 hour') >= 5 then
    raise exception 'brigade:rate_limited:team_create:3600' using errcode = 'P0001'; end if;
  v_random := encode(extensions.gen_random_bytes(16), 'hex');              -- 128 bits
  insert into brigade.teams (name, created_by, secret_hash)
  values (p_name, v_uid, extensions.crypt(v_random, extensions.gen_salt('bf', 10)))
  returning id into v_id;
  insert into brigade.memberships (team_id, user_id, human_label, joined_secret_version)
  values (v_id, v_uid, p_human_label, 1);
  return jsonb_build_object('status', 'created', 'team_id', v_id, 'team_name', p_name,
                            'join_secret', 'brg1.' || v_id::text || '.' || v_random);
end $$;

-- join_team: uniform failure, persisted attempts, per-principal hard limiter (the per-team count is advisory),
-- one bcrypt of the same cost even for unknown teams.
create or replace function brigade.join_team(p_join_secret text, p_human_label text default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare
  v_uid uuid := auth.uid(); v_team brigade.teams%rowtype; v_team_id uuid; v_random text;
  v_window constant interval := interval '15 minutes';
  v_user_fail integer; v_team_fail integer; v_oldest timestamptz;
  v_existing brigade.memberships%rowtype; v_rejoined boolean := false;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if p_join_secret is null or octet_length(p_join_secret) > 120
     or p_join_secret !~ '^brg1\.[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.[0-9a-f]{32}$' then
    return jsonb_build_object('status', 'invalid_input');
  end if;
  v_team_id := split_part(p_join_secret, '.', 2)::uuid;
  v_random  := split_part(p_join_secret, '.', 3);

  select count(*), min(attempted_at) into v_user_fail, v_oldest from brigade.join_attempts
   where user_id = v_uid and not succeeded and attempted_at > now() - v_window;
  if v_user_fail >= 5 then                                                   -- the only hard limit (D6)
    return jsonb_build_object('status', 'rate_limited',
      'retry_after_seconds', greatest(60, coalesce(extract(epoch from (v_oldest + v_window - now()))::integer, 900)));
  end if;
  select count(*) into v_team_fail from brigade.join_attempts                -- advisory only: the team id is not secret,
   where team_id = v_team_id and not succeeded and attempted_at > now() - v_window;   -- so it must never refuse a join
  if v_team_fail >= 20 then raise notice 'brigade:join_team:team_failures:%', v_team_fail; end if;

  select * into v_team from brigade.teams where id = v_team_id;
  if not found then
    -- Equalise latency (I-18): one bcrypt at the real cost against a fresh salt. No constant to paste, no
    -- placeholder that crypt() would reject as an invalid salt.
    perform extensions.crypt(v_random, extensions.gen_salt('bf', 10));
    insert into brigade.join_attempts (user_id, team_id) values (v_uid, v_team_id);
    return jsonb_build_object('status', 'invalid_secret');
  end if;
  if v_team.secret_hash <> extensions.crypt(v_random, v_team.secret_hash) then
    insert into brigade.join_attempts (user_id, team_id) values (v_uid, v_team_id);
    return jsonb_build_object('status', 'invalid_secret');
  end if;

  select * into v_existing from brigade.memberships where team_id = v_team.id and user_id = v_uid;
  v_rejoined := found;                                                       -- captured before later statements reset FOUND
  if v_rejoined and v_existing.status = 'banned' then
    insert into brigade.join_attempts (user_id, team_id) values (v_uid, v_team_id);
    return jsonb_build_object('status', 'invalid_secret');                 -- banned looks like a wrong secret
  end if;
  insert into brigade.memberships (team_id, user_id, human_label, joined_secret_version)
  values (v_team.id, v_uid, p_human_label, v_team.secret_version)
  on conflict (team_id, user_id) do update
    set status = 'active', revoked_at = null,
        joined_secret_version = excluded.joined_secret_version,
        human_label = coalesce(excluded.human_label, brigade.memberships.human_label);
  insert into brigade.join_attempts (user_id, team_id, succeeded) values (v_uid, v_team.id, true);
  delete from brigade.join_attempts where user_id = v_uid and not succeeded;
  return jsonb_build_object('status', 'joined', 'team_id', v_team.id, 'team_name', v_team.name, 'rejoined', v_rejoined,
                            'team_failures', v_team_fail);                  -- advisory; the adapter logs it at warn when > 0
end $$;

-- leave_team: the caller revokes its own membership and closes its open sessions in the team. Idempotent and uniform:
-- an unknown team, a non-member and an already-revoked member all answer left = true (the team id is not secret, and
-- there is nothing to disclose). A banned row stays banned. created_by is untouched, so a creator who leaves regains
-- administration by rejoining with the secret (5.10). join_team's upsert re-activates the row later (rejoined = true).
create or replace function brigade.leave_team(p_team_id uuid)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_status text;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  select status into v_status from brigade.memberships where team_id = p_team_id and user_id = v_uid;
  if found and v_status = 'active' then
    update brigade.memberships set status = 'revoked', revoked_at = now() where team_id = p_team_id and user_id = v_uid;
    update brigade.sessions set closed_at = coalesce(closed_at, now()), activity = 'idle'
     where team_id = p_team_id and owner_id = v_uid and closed_at is null;    -- ownership untouched: the UPDATE trigger allows it
  end if;
  return jsonb_build_object('team_id', p_team_id, 'left', true);
end $$;

-- register_session: new session, or re-open an owned one by Brigade id that is closed or expired (uniform not_found for
-- a foreign or unknown id; conflict:session_live while the session's lease is still valid, 4.5.8).
create or replace function brigade.register_session(
  p_team_id uuid, p_name text, p_description text default null, p_activity text default 'idle',
  p_inbound text default null, p_harness text default null, p_harness_version text default null,
  p_workspace_label text default null, p_lease_seconds integer default 90, p_resume_session_id uuid default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_row brigade.sessions%rowtype; v_resumed boolean := false; v_label text;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  select human_label into v_label from brigade.memberships where team_id = p_team_id and user_id = v_uid and status = 'active';
  if not found then raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  if p_name is null or char_length(p_name) not between 1 and 64 then raise exception 'brigade:invalid_input:session_name' using errcode = '22023'; end if;
  if p_description is not null and char_length(p_description) > 256 then raise exception 'brigade:invalid_input:session_description' using errcode = '22023'; end if;
  if p_activity not in ('busy','idle') then raise exception 'brigade:invalid_input:activity' using errcode = '22023'; end if;
  if p_inbound is not null and p_inbound not in ('accept','hold','refuse') then raise exception 'brigade:invalid_input:inbound' using errcode = '22023'; end if;
  if p_lease_seconds not between 30 and 600 then raise exception 'brigade:invalid_input:lease_seconds' using errcode = '22023'; end if;

  if p_resume_session_id is not null then
    if exists (select 1 from brigade.sessions
                where id = p_resume_session_id and owner_id = v_uid and closed_at is null
                  and last_seen_at + make_interval(secs => lease_seconds) > now()) then
      raise exception 'brigade:conflict:session_live' using errcode = 'P0001';   -- 4.5.8: two processes never drain one inbox
    end if;
    update brigade.sessions
       set closed_at = null, last_seen_at = now(), name = p_name, description = p_description, activity = p_activity,
           inbound = p_inbound, harness = p_harness, harness_version = p_harness_version,
           workspace_label = p_workspace_label, lease_seconds = p_lease_seconds
     where id = p_resume_session_id and owner_id = v_uid and team_id = p_team_id
    returning * into v_row;                                                 -- single row variable: valid INTO
    if not found then raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
    v_resumed := true;
  else
    if (select count(*) from brigade.sessions where owner_id = v_uid and created_at > now() - interval '1 hour') >= 30 then
      raise exception 'brigade:rate_limited:register_session:3600' using errcode = 'P0001'; end if;
    insert into brigade.sessions (team_id, owner_id, name, description, activity, inbound, harness, harness_version, workspace_label, lease_seconds)
    values (p_team_id, v_uid, p_name, p_description, p_activity, p_inbound, p_harness, p_harness_version, p_workspace_label, p_lease_seconds)
    returning * into v_row;
  end if;
  return brigade.session_record(v_row, v_label)
      || jsonb_build_object('resumed', v_resumed, 'lease_seconds', v_row.lease_seconds, 'server_time', now());
end $$;

create or replace function brigade.session_heartbeat(
  p_session_id uuid, p_activity text default null, p_name text default null, p_description text default null,
  p_inbound text default null, p_lease_seconds integer default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_row brigade.sessions%rowtype;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if p_lease_seconds is not null and p_lease_seconds not between 30 and 600 then raise exception 'brigade:invalid_input:lease_seconds' using errcode = '22023'; end if;
  select * into v_row from brigade.sessions where id = p_session_id and owner_id = v_uid;
  if not found then raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
  if v_row.closed_at is not null then raise exception 'brigade:conflict:session_closed' using errcode = 'P0001'; end if;
  if not exists (select 1 from brigade.memberships m where m.team_id = v_row.team_id and m.user_id = v_uid and m.status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  update brigade.sessions
     set last_seen_at = now(), activity = coalesce(p_activity, activity), name = coalesce(p_name, name),
         description = coalesce(p_description, description), inbound = coalesce(p_inbound, inbound),
         lease_seconds = coalesce(p_lease_seconds, lease_seconds)
   where id = p_session_id
  returning * into v_row;
  if random() < 0.02 then perform brigade.gc_expired(); end if;             -- works without pg_cron
  return jsonb_build_object('session_id', v_row.id,
    'state', case when v_row.activity = 'busy' then 'active' else 'idle' end,
    'lease_until', v_row.last_seen_at + make_interval(secs => v_row.lease_seconds), 'server_time', now());
end $$;

-- owned_active_session: the shared ownership + active-membership check for the per-session RPCs.
-- not_found for an unknown, foreign or not-owned id (uniform); unauthorized for an owned session whose
-- owner's membership is no longer active (revocation is immediate for inbox access, D22).
create or replace function brigade.owned_active_session(p_session_id uuid, p_uid uuid)
returns brigade.sessions language plpgsql stable set search_path = ''
as $$
declare v_row brigade.sessions%rowtype;
begin
  select * into v_row from brigade.sessions where id = p_session_id and owner_id = p_uid;
  if not found then raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
  if not exists (select 1 from brigade.memberships m where m.team_id = v_row.team_id and m.user_id = p_uid and m.status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  return v_row;
end $$;
revoke execute on function brigade.owned_active_session(uuid, uuid) from public, anon, authenticated;

create or replace function brigade.close_session(p_session_id uuid)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid();
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.owned_active_session(p_session_id, v_uid);
  update brigade.sessions set closed_at = coalesce(closed_at, now()), activity = 'idle' where id = p_session_id;
  return jsonb_build_object('session_id', p_session_id, 'state', 'offline');
end $$;

-- list_sessions: computed state, owner label from the membership row, capped; caller must be an active member.
create or replace function brigade.list_sessions(p_team_id uuid, p_include_offline boolean default false, p_limit integer default 200)
returns jsonb language plpgsql stable security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_rows jsonb; v_cap integer := least(greatest(coalesce(p_limit, 200), 1), 500);
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if not exists (select 1 from brigade.memberships where team_id = p_team_id and user_id = v_uid and status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  select coalesce(jsonb_agg(r.rec order by (r.rec->>'state') = 'offline', r.last_seen_at desc), '[]'::jsonb) into v_rows
    from (select brigade.session_record(s, m.human_label) as rec, s.last_seen_at
            from brigade.sessions s
            join brigade.memberships m on m.team_id = s.team_id and m.user_id = s.owner_id and m.status = 'active'
           where s.team_id = p_team_id
             and (p_include_offline or (s.closed_at is null and s.last_seen_at >= now() - make_interval(secs => s.lease_seconds)))
           order by s.last_seen_at desc
           limit v_cap + 1) r;
  return jsonb_build_object('team_ref', p_team_id, 'team_name', (select t.name from brigade.teams t where t.id = p_team_id),
                            'server_time', now(), 'truncated', jsonb_array_length(v_rows) > v_cap,
                            'sessions', (select coalesce(jsonb_agg(e), '[]'::jsonb) from (select e from jsonb_array_elements(v_rows) e limit v_cap) x));
end $$;

-- list_members: the roster for every active member (D22). unauthorized with byte-identical text for a team the
-- caller does not belong to and for a random uuid (no team-existence oracle). v1 lists active members only.
create or replace function brigade.list_members(p_team_id uuid)
returns jsonb language plpgsql stable security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid();
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if not exists (select 1 from brigade.memberships where team_id = p_team_id and user_id = v_uid and status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  return jsonb_build_object('team_ref', p_team_id, 'team_name', (select t.name from brigade.teams t where t.id = p_team_id),
    'server_time', now(),
    'members', coalesce((select jsonb_agg(jsonb_build_object(
        'principal_ref', m.user_id, 'human_label', m.human_label, 'status', m.status, 'joined_at', m.joined_at,
        'joined_secret_version', m.joined_secret_version,
        'last_seen_at', (select max(s.last_seen_at) from brigade.sessions s where s.team_id = m.team_id and s.owner_id = m.user_id),
        'session_count', (select count(*) from brigade.sessions s where s.team_id = m.team_id and s.owner_id = m.user_id))
      order by m.joined_at)
      from brigade.memberships m where m.team_id = p_team_id and m.status = 'active'), '[]'::jsonb));
end $$;

-- send_message: stamps identity; uniform not_found; deterministic idempotency; layered limits; hop_count.
create or replace function brigade.send_message(
  p_sender_session_id uuid, p_recipient_session_id uuid, p_body text, p_idempotency_key text,
  p_summary text default null, p_reply_to uuid default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare
  v_uid uuid := auth.uid(); v_sender brigade.sessions%rowtype; v_recipient brigade.sessions%rowtype;
  v_row brigade.messages%rowtype; v_reply brigade.messages%rowtype;
  v_hash bytea; v_hops integer := 0; v_min integer; v_hour integer; v_pmin integer; v_phour integer;
  v_unacked integer; v_pair_unacked integer;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  if p_body is null or octet_length(p_body) = 0 or octet_length(p_body) > 16384 then
    raise exception 'brigade:invalid_input:body' using errcode = '22023'; end if;
  if p_summary is not null and char_length(p_summary) > 200 then
    raise exception 'brigade:invalid_input:summary' using errcode = '22023'; end if;
  if p_idempotency_key is null or char_length(p_idempotency_key) not between 1 and 128 then
    raise exception 'brigade:invalid_input:idempotency_key' using errcode = '22023'; end if;

  -- sender: owned by the caller (uniform not_found otherwise) and not closed; NO lease check (D12)
  select * into v_sender from brigade.sessions where id = p_sender_session_id and owner_id = v_uid;
  if not found then raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
  if v_sender.closed_at is not null then raise exception 'brigade:conflict:sender_closed' using errcode = 'P0001'; end if;
  if not exists (select 1 from brigade.memberships where team_id = v_sender.team_id and user_id = v_uid and status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;

  -- recipient: same team and an active member's session; the same not_found as for a random id (C-25)
  select s.* into v_recipient from brigade.sessions s
   where s.id = p_recipient_session_id and s.team_id = v_sender.team_id
     and exists (select 1 from brigade.memberships m where m.team_id = s.team_id and m.user_id = s.owner_id and m.status = 'active');
  if not found then raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
  if v_recipient.id = v_sender.id then raise exception 'brigade:invalid_input:recipient_is_self' using errcode = '22023'; end if;

  v_hash := extensions.digest(p_body, 'sha256');

  -- idempotency (fast path; the unique index is the authority under races)
  select * into v_row from brigade.messages where sender_session_id = v_sender.id and idempotency_key = p_idempotency_key;
  if found then
    if v_row.body_hash <> v_hash or v_row.recipient_session_id <> p_recipient_session_id then
      raise exception 'brigade:conflict:idempotency_key' using errcode = 'P0001';   -- never 23505 (see 5.4 intro)
    end if;
    return brigade.message_result(v_row, true);
  end if;

  -- rate limits (D17): per sender session, then per principal (summed over all of the principal's sessions,
  -- so registering more sessions does not multiply the budget), then per (sender, recipient) pair, then recipient-wide.
  select count(*) filter (where created_at > now() - interval '1 minute'), count(*) into v_min, v_hour
    from brigade.messages where sender_session_id = v_sender.id and created_at > now() - interval '1 hour';
  if v_min >= 20 then raise exception 'brigade:rate_limited:send_per_minute:60' using errcode = 'P0001'; end if;
  if v_hour >= 200 then raise exception 'brigade:rate_limited:send_per_hour:3600' using errcode = 'P0001'; end if;
  select count(*) filter (where created_at > now() - interval '1 minute'), count(*) into v_pmin, v_phour
    from brigade.messages where sender_user_id = v_uid and created_at > now() - interval '1 hour';
  if v_pmin >= 60 then raise exception 'brigade:rate_limited:principal_per_minute:60' using errcode = 'P0001'; end if;
  if v_phour >= 600 then raise exception 'brigade:rate_limited:principal_per_hour:3600' using errcode = 'P0001'; end if;
  select count(*) filter (where sender_session_id = v_sender.id), count(*) into v_pair_unacked, v_unacked
    from brigade.messages where recipient_session_id = v_recipient.id and delivery_state = 'accepted';
  if v_pair_unacked >= 15 then raise exception 'brigade:rate_limited:sender_quota_for_recipient:60' using errcode = 'P0001'; end if;
  if v_unacked >= 60 then raise exception 'brigade:rate_limited:recipient_inbox_full:60' using errcode = 'P0001'; end if;

  -- hop_count is server-computed. Explicit: reply_to must be a message this sender session received (no existence
  -- oracle). Implicit: with no reply_to, the most recent message the recipient sent to this sender within 10 minutes
  -- is treated as the message being answered, so a pair of models that never label replies is still bounded.
  if p_reply_to is not null then
    select * into v_reply from brigade.messages where id = p_reply_to and recipient_session_id = v_sender.id;
    if not found then raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
    v_hops := v_reply.hop_count + 1;
  else
    select * into v_reply from brigade.messages
     where sender_session_id = v_recipient.id and recipient_session_id = v_sender.id
       and created_at > now() - interval '10 minutes'
     order by created_at desc limit 1;
    if found then v_hops := v_reply.hop_count + 1; end if;
  end if;
  if v_hops > 32 then raise exception 'brigade:loop_detected:max_hops' using errcode = 'P0001'; end if;

  update brigade.sessions set last_seen_at = now() where id = v_sender.id;   -- a send proves liveness (D12)

  begin
    insert into brigade.messages (team_id, sender_user_id, sender_session_id, recipient_session_id,
                                  summary, body, body_hash, reply_to, hop_count, idempotency_key)
    values (v_sender.team_id, v_uid, v_sender.id, v_recipient.id, p_summary, p_body, v_hash, p_reply_to, v_hops, p_idempotency_key)
    returning * into v_row;
  exception when unique_violation then                                         -- a concurrent retry won the race
    select * into v_row from brigade.messages where sender_session_id = v_sender.id and idempotency_key = p_idempotency_key;
    if v_row.body_hash <> v_hash or v_row.recipient_session_id <> p_recipient_session_id then
      raise exception 'brigade:conflict:idempotency_key' using errcode = 'P0001';   -- propagates out of the handler
    end if;
    return brigade.message_result(v_row, true);
  end;
  return brigade.message_result(v_row, false);
end $$;

-- fetch_inbox: the unacknowledged set for an owned session, oldest first (the cursor, 5.7).
create or replace function brigade.fetch_inbox(p_session_id uuid, p_limit integer default 100)
returns jsonb language plpgsql stable security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid();
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.owned_active_session(p_session_id, v_uid);               -- not_found / unauthorized (revoked)
  return coalesce((
    select jsonb_agg(brigade.envelope(x.m) order by (x.m).seq)
      from (select m from brigade.messages m                                   -- whole-row reference: type brigade.messages
             where m.recipient_session_id = p_session_id and m.delivery_state = 'accepted'
             order by m.seq limit least(greatest(coalesce(p_limit, 100), 1), 200)) x), '[]'::jsonb);
end $$;

-- ack_messages: {acked, unknown}; already-injected owned ids count as acked; foreign ids are unknown, never errors.
create or replace function brigade.ack_messages(p_session_id uuid, p_message_ids uuid[])
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_mine uuid[]; v_unknown uuid[];
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  perform brigade.owned_active_session(p_session_id, v_uid);               -- not_found / unauthorized (revoked)
  update brigade.messages set delivery_state = 'injected', injected_at = now()
   where id = any(p_message_ids) and recipient_session_id = p_session_id and delivery_state = 'accepted';
  select coalesce(array_agg(m.id), '{}') into v_mine from brigade.messages m
   where m.id = any(p_message_ids) and m.recipient_session_id = p_session_id;
  select coalesce(array_agg(u.id), '{}') into v_unknown from unnest(p_message_ids) as u(id) where u.id <> all(v_mine);
  return jsonb_build_object('acked', to_jsonb(v_mine), 'unknown', to_jsonb(v_unknown));
end $$;
```

Grants: `revoke execute … from public, anon; grant execute … to authenticated`, written immediately after each `create function`, for `create_team`, `join_team`, `leave_team`, `register_session`, `session_heartbeat`, `close_session`, `list_sessions`, `list_members`, `send_message`, `fetch_inbox`, `ack_messages`. `gc_expired`, `my_team_ids`'s siblings and the helpers (`session_record`, `envelope`, `message_result`, `owned_active_session`), the trigger functions of 5.5 and `notify_message_inserted` (5.6) get `revoke execute … from public` with no grant, so their `proacl` is non-null and carries no `=X` entry, which `functions.sql` (9.3) asserts for every function in the schema. Because the per-schema default-privileges statement is a no-op (5.3), these explicit revokes are the only thing that stops `service_role` from executing the RPCs: on today's stack, where they were missing, `service_role` could call every RPC and failed only inside it with `28000 brigade:unauthenticated` (`auth.uid()` null) [verified, A.7]; with the revokes in place it gets `42501 permission denied for function` before the body runs, which E0-1 (i) confirms. `session_heartbeat` keeps its inline ownership-then-membership checks, which are the same two checks `owned_active_session` performs in the same order.

### 5.5 Stamping triggers (second layer; the RPCs already do this)

```sql
create or replace function brigade.stamp_message()
returns trigger language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_team uuid;
begin
  if v_uid is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  new.sender_user_id := v_uid; new.created_at := now(); new.delivery_state := 'accepted'; new.injected_at := null;
  new.body_hash := extensions.digest(new.body, 'sha256');
  select team_id into v_team from brigade.sessions where id = new.sender_session_id and owner_id = v_uid;
  if v_team is null then raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
  new.team_id := v_team;
  if not exists (select 1 from brigade.sessions where id = new.recipient_session_id and team_id = v_team) then
    raise exception 'brigade:not_found' using errcode = 'P0002'; end if;
  return new;
end $$;
create trigger messages_stamp before insert on brigade.messages for each row execute function brigade.stamp_message();

-- INSERT: stamp owner and created_at from the caller's claims and require an active membership.
create or replace function brigade.stamp_session()
returns trigger language plpgsql security definer set search_path = ''
as $$
begin
  if auth.uid() is null then raise exception 'brigade:unauthenticated' using errcode = '28000'; end if;
  new.owner_id := auth.uid(); new.created_at := now();
  if not exists (select 1 from brigade.memberships where team_id = new.team_id and user_id = new.owner_id and status = 'active') then
    raise exception 'brigade:unauthorized' using errcode = '42501'; end if;
  return new;
end $$;
create trigger sessions_stamp before insert on brigade.sessions for each row execute function brigade.stamp_session();

-- UPDATE: never rewrite ownership; only assert that the identity columns are unchanged. An earlier draft ran the
-- INSERT stamp on UPDATE as well, which would have re-assigned any row updated on another user's behalf (a Phase 5
-- revoke_membership closing a revoked member's sessions, a support fix, a loosened policy) to the caller, i.e. a
-- mailbox-theft path in the layer meant to prevent one. No claims are required here: the RPCs already check the
-- caller, and administrative or fixture updates (backdating last_seen_at/closed_at as postgres) must stay possible.
create or replace function brigade.assert_session_identity()
returns trigger language plpgsql security definer set search_path = ''
as $$
begin
  if new.owner_id <> old.owner_id or new.team_id <> old.team_id or new.created_at <> old.created_at or new.id <> old.id then
    raise exception 'brigade:conflict:session_identity_immutable' using errcode = 'P0001'; end if;
  return new;
end $$;
create trigger sessions_identity before update on brigade.sessions for each row execute function brigade.assert_session_identity();
```

The three trigger functions also get `revoke execute … from public` (a direct call fails anyway with "trigger functions can only be called as triggers", but the hygiene assertion of 9.3 is uniform over every function in the schema). `auth.uid()` reads the request GUCs and works inside `security definer` bodies and triggers regardless of the executing role [verified live]; it is null when no claims are set (auth digest section 7), so a plain `postgres` or `service_role` insert with no simulated `sub` raises `brigade:unauthenticated` rather than being stamped, which is why fixtures set the claims (9.3). `messages` has no UPDATE trigger: `ack_messages` is the only updater and fixtures backdate `created_at`/`injected_at` as `postgres`. `gc_expired()` runs under pg_cron with no JWT and only deletes, so the triggers never fire for it. Because RPCs are the only write path, the triggers are belt-and-braces: they hold if a policy or grant is ever loosened by mistake (auth digest section 7, all layers exercised live for the `public` variant).

### 5.6 Realtime (migration `20260830120100_brigade_realtime.sql`, D21)

```sql
create or replace function brigade.notify_message_inserted()
returns trigger language plpgsql security definer set search_path = ''   -- REQUIRED: runs as postgres (BYPASSRLS) [verified live]
as $$
begin
  perform realtime.send(jsonb_build_object('message_id', new.id, 'seq', new.seq),   -- ids only; never the body (T4)
                        'message_accepted', 'brigade:session:' || new.recipient_session_id::text, true);
  return null;
end $$;
create trigger messages_notify after insert on brigade.messages for each row execute function brigade.notify_message_inserted();

create or replace function brigade.owns_session_topic(p_topic text) returns boolean
language sql stable security definer set search_path = ''
as $$ select exists (select 1 from brigade.sessions s
                     join brigade.memberships m on m.team_id = s.team_id and m.user_id = s.owner_id and m.status = 'active'
                     where s.owner_id = (select auth.uid()) and s.closed_at is null
                       and p_topic = 'brigade:session:' || s.id::text) $$;   -- revoked members lose the topic at the next join/JWT check
revoke execute on function brigade.owns_session_topic(text) from public, anon;
grant  execute on function brigade.owns_session_topic(text) to authenticated;

-- Receive-only authorization on the topic. No insert policy: clients cannot broadcast; only the database emits.
create policy brigade_session_topic_read on realtime.messages for select to authenticated
  using ( realtime.messages.extension = 'broadcast' and brigade.owns_session_topic((select realtime.topic())) );
```

Facts this relies on: `realtime.send(payload jsonb, event text, topic text, private boolean default true)` exists with exactly this argument order [verified today: https://supabase.com/docs/guides/realtime/broadcast; body copied live from v2.129.3] and swallows errors into a `WARNING`, so a Realtime outage cannot fail `send_message`; the trigger must be `security definer` because `authenticated` has no insert policy on `realtime.messages` and the insert would otherwise fail silently [verified live: role flags]; `realtime.topic()` returns the bare topic without the `realtime:` prefix (verified for send, and for join by the refusal reason, which names `brigade:session:<sid>` without the prefix [verified, A.7]; E0-2 still asserts it with a deliberately wrong topic); private-channel authorization is evaluated at join and when a new JWT arrives, cached per connection, and the client is disconnected when its JWT expires [verified: https://supabase.com/docs/guides/realtime/authorization; observed today: an `access_token` push re-runs the policy within 2 ms, and a channel whose JWT expires receives a `system` error and `phx_close` at the exact `exp`, A.7]; rows in `realtime.messages` are deleted after 3 days [verified today]. Hosted: disable "Allow public access" in Realtime settings [verified: https://supabase.com/docs/guides/realtime/settings]; a public join is then refused with `PrivateOnly` ("this project only allows private channels") [verified today: https://supabase.com/docs/guides/realtime/error_codes]. The local stack has no such toggle (`config.toml` `[realtime]` exposes only `enabled` and `ip_version` [verified today: https://supabase.com/docs/guides/local-development/cli/config]; `max_header_length` per the local-dev digest), so locally a public join to `brigade:session:<sid>` succeeds; what holds locally is isolation, not refusal: "a public broadcast only reaches public channels and a private broadcast only reaches private channels" [verified today: https://supabase.com/docs/guides/realtime/broadcast], so the public subscriber receives nothing, and the isolation gate that matters is the `Unauthorized` on a foreign or wrong private topic (E0-2 (b)). The `PrivateOnly` refusal is asserted hosted only (P5-1, I-13 hosted half).

Why Broadcast over `postgres_changes` (realtime digest 3.5): per-topic authorization at join instead of one RLS evaluation per subscriber per event; no publication change, no `REPLICA IDENTITY FULL`, no unfiltered DELETE fan-out; no second replication slot or connection pool on a Free-tier database; Supabase's current guidance. Disagreement noted: the auth digest verified `postgres_changes` live and called it fine at this scale; the realtime, local-dev and threat-model digests recommend Broadcast. Broadcast is chosen; `postgres_changes` with the same drain loop is the fallback.

Watch loop (adapter `message watch`, Go edition; every frame shape below was exchanged with Realtime v2.129.3 today [verified, A.7]): connect with `github.com/coder/websocket` to `wss://<host>/realtime/v1/websocket?apikey=<publishable key>&vsn=1.0.0` (`ws://` for a loopback backend; a bad or missing key is refused at the upgrade with HTTP 401 after about 2 s; `vsn=1.0.0` makes every frame a JSON object `{"topic","event","payload","ref"[,"join_ref"]}`, whereas the realtime-js default `vsn=2.0.0` delivers database broadcasts as binary frames in realtime-js's own framing); `SetReadLimit(1 MiB)`; one reader goroutine; writes serialised by the library. Join: push `phx_join` on topic `realtime:brigade:session:<sid>` with payload `{"access_token": <user JWT>, "config": {"broadcast": {"ack": false, "self": false}, "presence": {"enabled": false, "key": ""}, "postgres_changes": [], "private": true}}` and wait for the `phx_reply` carrying the same `ref`; `status: "ok"` arrives in 5-12 ms locally, while a refusal (foreign or unknown topic, missing, expired or garbage token) arrives only after the server's fixed 5 s backoff with `status: "error"` and a `reason` string, so the join timeout is 15 s and anything under 5 s would misreport refusals as timeouts. Drain on join ok, on every `broadcast` event whose payload `event` is `message_accepted` (its payload carries `message_id`, `seq` and the `realtime.messages` row id, never a body), and on a safety timer (30 s while joined, 10 s while not, with a `status` event reporting `polling`); `drain()` coalesces concurrent triggers into at most one run plus one follow-up and pages `fetch_inbox` in batches of 100 until a short page; a broadcast arrives within about 2 ms of the sender's RPC locally, so no extra delay is needed. Heartbeat: push `heartbeat` on topic `phoenix` every 25 s (a socket that stops heartbeating is closed by the server after about 66 s [verified, A.7]). Token: the adapter refreshes under the flock when fewer than 90 s remain and pushes `access_token` `{"access_token": <new JWT>}` on the channel after every refresh (no reply is sent; the push also re-runs the topic policy immediately, which is how a revocation reaches an open channel before the JWT expires [verified, A.7]). Channel down: a `system` event with `status: "error"` (`Token has expired …`, `You do not have permissions …`), `phx_close`, `phx_error`, a WebSocket close or a bare EOF (observed about 45 s after a channel had been closed for expiry; cause not established [uncertain]) all mean "rejoin": refresh the token first when the reason names the JWT, then reconnect and rejoin with backoff `1, 2, 5, 10` s (realtime-js's `RECONNECT_INTERVALS`) and a 30-minute budget before exit 9; the drain keeps running on the 10 s timer meanwhile, so delivery never depends on the channel. Reason mapping (join reply `reason` or `system` message, matched by prefix; every name verified against https://supabase.com/docs/guides/realtime/error_codes): `Unauthorized`/`RlsPolicyError` → `unauthorized` (fatal, exit 5; it recurs); `ConnectionRateLimitReached`/`JoinsRateLimitReached`/`ClientJoinRateLimitReached`/`ChannelRateLimitReached`/`MessagePerSecondRateLimitReached` → `rate_limited` (backoff, then exit 8, `details.reason = "realtime"`); `InvalidJWTToken`/`JwtSignatureError`/`MalformedJWT` → force one token refresh and rejoin, then `unauthenticated` (exit 4) if it recurs; `PrivateOnly` → `config` (exit 11; the client joined without `private: true`, a bug); `RealtimeDisabledForTenant`/`DatabaseLackOfConnections` → keep polling, `status: polling`; `unmatched topic` → `internal` (the `realtime:` prefix was omitted, a bug). On shutdown: `phx_leave`, close the socket. The `supabase-community` Go libraries were evaluated and rejected: `realtime-go` never sends `access_token` and cannot authorize a private channel, `auth-go` has no anonymous sign-in and stringifies error codes, `postgrest-go` drags `lib/pq` and testcontainers into the module graph for what is one `http.Do` with four headers [verified, A.7]; the only dependency is `coder/websocket` (`Dial`, `Read`, `Write`, `Close`, `SetReadLimit`, `CloseError`).

### 5.7 Catch-up cursor

There is no client-side cursor. The set of rows with `delivery_state = 'accepted'` for the session is the cursor; `fetch_inbox` returns them oldest first; the harness acks each after injection; a watcher restart re-emits anything not yet acked (at-least-once). `seq` orders a batch but is never a watermark: a transaction holding seq 41 can commit after 42 is visible, so `seq > last` would skip 41 forever (realtime digest 6.2). `message receive` is the same query without the subscription. Idempotent, cannot skip, survives clock skew.

### 5.8 Retention and cleanup (migration `20260830120200_brigade_housekeeping.sql`, D13)

```sql
create or replace function brigade.gc_expired()
returns void language sql security definer set search_path = ''
as $$
  delete from brigade.messages where ctid in (
    select ctid from brigade.messages
     where (injected_at is not null and injected_at < now() - interval '24 hours')
        or created_at < now() - interval '7 days'
     order by created_at limit 5000);                                      -- bounded work per call
  delete from brigade.sessions
   where (closed_at is not null and closed_at < now() - interval '7 days')
      or last_seen_at + make_interval(secs => lease_seconds) < now() - interval '7 days';
  delete from brigade.join_attempts where attempted_at < now() - interval '24 hours';
  -- Abandoned teams: anyone with the publishable key can mint principals (30/h/IP hosted) and create 5 teams each,
  -- so teams would otherwise accumulate forever. A team older than 7 days with at most one membership and no session
  -- activity (no session row at all, or none seen within 7 days) is deleted; memberships cascade, and the Phase 5
  -- anonymous-user rule then reclaims the creator (a principal with no membership).
  delete from brigade.teams t
   where t.created_at < now() - interval '7 days'
     and (select count(*) from brigade.memberships m where m.team_id = t.id) <= 1
     and not exists (select 1 from brigade.sessions s where s.team_id = t.id and s.last_seen_at > now() - interval '7 days');
  -- Phase 5 (P5-3): anonymous auth.users with no membership row (of any status) older than 7 days; never principals that
  -- hold a membership row, and never the creator of a live team: teams.created_by is on delete restrict (5.3), so such a
  -- delete would abort this whole function; the cleanup excludes them explicitly and retention.sql asserts it.
$$;
revoke execute on function brigade.gc_expired() from public, anon, authenticated;

create extension if not exists pg_cron with schema pg_catalog;
grant usage on schema cron to postgres;
do $$ begin
  perform cron.schedule('brigade_gc', '17 * * * *', $job$select brigade.gc_expired()$job$);
  perform cron.schedule('brigade_cron_log_gc', '23 3 * * *', $job$delete from cron.job_run_details where end_time < now() - interval '7 days'$job$);
exception when others then raise notice 'pg_cron not available: %', sqlerrm;    -- correctness never depends on cron
end $$;
```

pg_cron is preloaded on the local image (`cron.database_name = postgres`, pg_cron 1.6.4) [verified live, local-dev digest] and available hosted via Integrations > Cron (https://supabase.com/docs/guides/cron). "Preloaded" means the shared library is loaded; the extension object exists only after this migration's `create extension`: the afternoon's minimal-stack run, whose migration never ran it, saw no `pg_cron` row in `pg_extension` [verified, A.7], so E0-1 (h) confirms that the statement succeeds on the minimal stack, and the `exception when others` wrapper keeps correctness independent of the answer. `realtime.messages` is Supabase-managed (3-day partitions). The docs' generic anonymous-user cleanup is not used because Brigade principals are anonymous users and deleting them cascades to memberships (https://supabase.com/docs/guides/auth/auth-anonymous) [verified].

### 5.9 Local development and hosted deployment

Local (`supabase/config.toml`; only the lines that differ from `npx supabase init` output, the rest of the template stays):

```toml
project_id = "brigade"
[api]
schemas = ["public", "graphql_public", "brigade"]   # brigade added; graphql_public kept to avoid a config-push diff
extra_search_path = ["public", "extensions"]
max_rows = 1000
[db]
major_version = 17                                  # must equal the hosted project's version
[db.seed]
enabled = true
sql_paths = ["./seed.sql"]                          # file exists and is empty
[realtime]
enabled = true
[studio]
enabled = false
[local_smtp]
enabled = false
[storage]
enabled = false
[edge_runtime]
enabled = false
[analytics]
enabled = false                                     # template default is true; avoids Logflare + Vector
[auth]
jwt_expiry = 3600
enable_refresh_token_rotation = true
refresh_token_reuse_interval = 10
enable_signup = true                                # GoTrue returns signup_disabled for anonymous sign-ups otherwise [verified in source]
enable_anonymous_sign_ins = true
[auth.rate_limit]
anonymous_users = 1000                              # local/CI only; every test principal comes from 127.0.0.1 (hosted default 30/h/IP)
token_refresh = 150
[auth.email]
enable_signup = false
[auth.sms]
enable_signup = false
```

Commands (Makefile, 7.4): `make supabase-start` = `npx supabase start -x studio,postgres-meta,imgproxy,storage-api,edge-runtime,mailpit,logflare,vector,supavisor`; `make supabase-env` writes `.env.test` from `supabase status -o env --override-name …` (the local publishable/secret keys are well-known constants; tests read them from `.env.test`, never embed them); `make test-db` = `npx supabase test db` (pgTAP; creates and drops `pgtap` itself; each file in its own rolled-back transaction) [verified in the local-dev digest]. `supabase status -o env` prints `API_URL`, `DB_URL`, `PUBLISHABLE_KEY`, `SECRET_KEY`, `JWT_SECRET` and still the legacy `ANON_KEY`/`SERVICE_ROLE_KEY` HS256 JWTs on 2.116.0 [verified, A.7]; only `SUPABASE_URL`, `SUPABASE_PUBLISHABLE_KEY` and `SUPABASE_DB_URL` are read by the tests. No generated database types exist any more: the Go adapter's request and response structs are hand-written against the RPC signatures of 5.4, and the integration suite (9.4) is what catches drift. Only one local stack can bind 54321/54322 at a time.

Hosted (team administrator, once, Phase 5, D32):

1. Create a single-purpose project. Authentication > Sign In / Providers > Enable Anonymous Sign-Ins; leave CAPTCHA off (a CLI cannot solve a browser challenge; `/signup` sits behind the CAPTCHA middleware [verified in GoTrue source]); do not enable Pro session time-box/inactivity limits (they silently kill idle principals); Realtime settings: disable "Allow public access".
2. `npx supabase login` (PAT) or `SUPABASE_ACCESS_TOKEN`; `make supabase-link project=<ref>`; `make supabase-push-dry`; `make supabase-push`; `make supabase-config-push` (maps `enable_anonymous_sign_ins` → `external_anonymous_users_enabled`, `anonymous_users` → `rate_limit_anonymous_users`, `api.schemas` → PostgREST config) [verified in CLI source and https://supabase.com/docs/reference/cli/supabase-db-push].
3. Enable the Cron integration (or accept opportunistic gc only).
4. `supabase projects api-keys --project-ref <ref>`; distribute the project URL, the publishable key and the join secret from `team create`. The PAT, database password and secret key never leave the administrator's machine or CI secrets.

`make backend-install project=<ref>` chains link, push, config push and prints the keys (P5-1). Free-tier projects pause after a week of inactivity; the adapter reports `unavailable` with `details.reason = "project_paused"` when the response is recognisable (the exact body is captured in E0-10 or P5-1).

### 5.10 Team administration (Phase 5, capability `team.admin`)

`rotate_join_secret(p_team_id)` (creator only; new random part; `secret_version + 1`; returns the new secret once; existing memberships unaffected, as the logical plan accepts), `revoke_membership(p_team_id, p_user_id, p_ban boolean default false)` and `revoke_memberships_by_version(p_team_id, p_max_version)` (creator only; `status := revoked|banned`; also sets `closed_at` on the member's open sessions, which the UPDATE trigger allows because ownership is not touched). "Creator only" means `teams.created_by = auth.uid()` and an active membership; a creator who ran `team leave` rejoins first. Revocation is immediate for every table and RPC path because `my_team_ids()` and every reading RPC filter on `status = 'active'` (5.3, 5.4; the schema and RPCs ship in Phase 2 and the pgTAP proof of it is in `rls_isolation.sql`, Phase 2, not Phase 5); realtime channels already joined keep receiving ids until the JWT expires or the next authorization check, at most 1 h (I-16 lag, measured in Phase 5). Adapter commands `team rotate-secret` (prints once; `--secret-file`) and `team revoke-member` (stdin `{principal_ref, ban}` or `{joined_secret_version_lte}`).

Roster. Every active member sees the whole roster (D22): `memberships_select` is team-scoped (5.3), and `list_members(p_team_id)` (`security definer`, `search_path = ''`; ships in Phase 2, P2-2, SQL in 5.4) returns `[{principal_ref, human_label, status, joined_at, joined_secret_version, last_seen_at, session_count}]` for every active member of the team, where `last_seen_at` is the maximum over the member's sessions (null if none) and `session_count` counts the member's not-yet-deleted sessions; a caller that is not an active member gets `brigade:unauthorized` with byte-identical text for a team it does not belong to and for a random team id (no team-existence oracle), asserted by pgTAP in `rls_isolation.sql` from Phase 2. Adapter command `team members --profile <p>` (capability `team.roster`; stdout the array; never a secret), reachable as `brigade team members` from a terminal and, sanitised, from the model. Phase 5 may extend the answer with revoked and banned rows for the creator; v1 lists active members only. The revoke procedure in `docs/setup.md` (Phase 5): `brigade team members` → copy the `principal_ref` → `brigade team revoke-member` with stdin `{"principal_ref": "…", "ban": true|false}`; `ban` blocks rejoin with the current secret (the banned principal sees the same `unauthorized` as a wrong secret), `revoked` allows rejoin. Without the roster the creator would have no source for the `principal_ref` of a member with no live session, and no member could tell which `from-principal` in a frame belongs to whom; `brigade sessions` shows only sessions with a valid lease (or, with `--all`, ones not yet deleted 7 days after expiry).

Creator loss. `teams.created_by` is the only administrative authority, and the creator's `profiles/<name>/session.json` is the only credential that can exercise it. Anonymous credentials are unrecoverable by design (logical plan trade-offs; D23 terminal errors; `profile reset` mints a new principal), so if the creator runs `profile reset`, loses the file, or triggers refresh-token reuse detection, no one can rotate the secret or revoke members. The recovery is to create a new team and re-invite everyone, or, in Phase 5, `transfer_team(p_team_id, p_new_creator)` (creator only; `p_new_creator` must be an active member; sets `created_by`; adapter command `team transfer` with stdin `{principal_ref}`), run by the current creator before the loss. Administrators should keep a 0700 backup of the profile directory. `created_by` is `on delete restrict` (5.3), so a creator of a live team is never deleted: the P5-3 anonymous-user cleanup excludes such creators explicitly (a restricted delete would abort `gc_expired()`), and `retention.sql` asserts that a creator with a membership row is never deleted. The creator's own `team leave` revokes the membership but leaves `created_by` in place, so a rejoin with the secret restores administration. All of this is stated in `docs/security.md` and `docs/setup.md` (P5-7).

### 5.11 Adapter internals

- Entry: `brigade adapter supabase <group> <verb> [flags]`, a hidden entry in the multi-call dispatch table (`internal/app`), sharing the dispatcher, the interspersed-flag parser (`--profile`, `--session`, `--include-offline`, `--limit`, `--log-level`, `--json` plus the adapter's `--url`/`--key`/`--force`/`--name`/`--label`/`--prompt`/`--secret-file` extras) and the result printer with the fs adapter (`internal/adapterkit`, `internal/cli`). Every handler reads the stdin document (bounded at 1 MiB with `io.LimitReader`, decoded with `encoding/json/v2`, which rejects invalid UTF-8 and duplicate member names and ignores unknown members by default [verified, A.7]; a wrongly-cased member is silently zero, which is why `Validate()` checks required fields), calls `Validate()`, runs, prints exactly one JSON object and returns an exit status; only `main` calls `os.Exit`, after stdout is flushed. Commands that take no input never touch stdin; commands that do and find a terminal on stdin print usage instead of blocking.
- Packages: `internal/adapters/supabase/{client.go (http.Client, embedded roots, headers), gotrue.go, postgrest.go, realtime.go (Phoenix on coder/websocket), credentials.go (session.json + flock), errors.go (one mapping table), commands/*.go, watch.go}`; `internal/harness/**` never imports it (depguard, 7.3).
- `describe`: reads `profile.json` and checks that `session.json` exists and parses; never signs in, never fetches (C-01; `BRIGADE_TEST_OFFLINE=1` makes the client's dialer fail loudly on any connection).
- PostgREST: `POST /rest/v1/rpc/<fn>` with `apikey`, `Authorization: Bearer <access_token>`, `Content-Type: application/json`, `Accept-Profile: brigade` and `Content-Profile: brigade` (both, on every request; `Content-Profile` alone suffices for `POST /rpc/*`, and without it PostgREST looks for `public.<fn>` and answers `404 PGRST202`); never `?apikey=` as a query parameter on `/rest/v1` (parsed as a column filter, `400 PGRST100`) [verified, A.7]. The body of a 200 is the function's `jsonb` verbatim; a `Proxy-Status: PostgREST; error=<code>` header accompanies every error.
- Error mapping (`errors.go`, one table, U-24): parse the PostgREST error body `{"code","message","details","hint"}` first, whatever the HTTP status; `message` starting with `brigade:` → the named code, the detail after the second colon into `details.reason`, and for `rate_limited` the trailing `retry_after_seconds` into `retry_after_ms`; else by SQLSTATE per 4.6 (`P0002` arrives as HTTP 500, `42501` as 403 with a JWT and as 401 without a usable one, `P0001`/`22023` as 400, `28000` as 403 [verified, A.7]); `PGRST301`/`PGRST303` → refresh once, retry once, then `unauthenticated`; `PGRST202`/`PGRST205` (schema cache miss: unknown function, wrong argument names, unknown table) → `internal` with the hint "migration drift between adapter and backend"; a body that is not PostgREST JSON with a 5xx status, a transport error, a DNS failure, or `x509.UnknownAuthorityError` and other TLS verification failures → `unavailable` (the last with `details.reason = "tls"` and the message "certificate verification failed; install ca-certificates or set SSL_CERT_FILE"); the paused-project body → `unavailable` with `details.reason = "project_paused"` once P5-1 has captured it. Raw server text goes to stderr at debug, redacted, never to stdout.
- `message watch` (5.6): one long-lived process; stdin NDJSON command reader (`ack`, `heartbeat`, `close`; the same drop-and-continue reader as the harness, 7.3); stdout NDJSON event writer behind a mutex with a flush per event; the credential's terminal errors → a fatal `error` event and exit 4; exits within 5 s of stdin EOF, a `close` command or SIGTERM (C-38) after `phx_leave` and, for `close`, a best-effort `close_session`.
- `team create`: `{team_name, human_label?}` from stdin JSON, from `--name`/`--label`, or from `--prompt` on a TTY (name and label echoed; the TTY test is `x/term.IsTerminal` on the stdin descriptor, never `os.ModeCharDevice`, which is true for `/dev/null` as well [verified, A.7]); exits 7 `conflict` (`profile_bound`) when `profile.json` already carries a `team_ref`; signs up anonymously if `session.json` is absent; calls `create_team`; prints the secret once on stdout, or writes it 0600 to `--secret-file` and omits it from stdout (C-03, C-03b).
- `team join`: reads `{join_secret, human_label?}` from stdin JSON, or with `--prompt` on a TTY reads the secret with `x/term.ReadPassword` (no echo) and then the label (echoed, optional; `--label` answers it in advance); refuses `--join-secret` on argv with exit 2 (a poison flag registered on the flag set so its mere presence is `usage`, C-05); exits 7 `conflict` (`profile_bound`) when the profile already carries a different `team_ref` than the one inside the secret (a secret for the bound team is a rejoin, 4.4.10); signs up anonymously if `session.json` is absent; calls `join_team`; maps `invalid_secret` to exit 5 `unauthorized` with one fixed message for a wrong secret, an unknown team and a banned principal (4.5.7); writes `team_ref`/`team_name`/`principal_ref` to `profile.json`; never persists the secret; logs `team_failures` at warn when non-zero.
- `team leave`: no stdin; calls `leave_team(team_ref)` (5.4), then clears `team_ref` and `team_name` from `profile.json` and keeps `session.json`; on an unbound profile it answers `{team_ref: null, principal_ref, left: true}` with no network call (idempotent, C-08). A running `message watch` of this profile gets `unauthorized` on its next drain and exits 5, so the plugin's watcher stops and the next `prompt` hook prints its notice (6.6).
- `team members`: calls `list_members(team_ref)` and prints the result (capability `team.roster`, D22).
- `profile init --url … --key … [--force]`, `profile status`, `profile reset`, `profile revoke-credentials`: as in 5.2 and 5.1; `profile status` prints the principal id prefix, the team name and the access token's remaining lifetime, never tokens.
- Logging: the redacting `slog` JSON handler of `adapterkit` (keys `token`, `secret`, `authorization`, `apikey`, `access_token`, `refresh_token`, `join_secret`, `password` replaced; JWT, `brg1.`, `sb_secret_`, `Bearer` patterns and the exact messaging token redacted inside every string, message and error value; struct and map values are never passed because `ReplaceAttr` does not visit their fields, so `slog.Any` is banned by lint outside `internal/adapterkit/log` [verified, A.7]); written to `${BRIGADE_STATE_DIR}/logs/adapter-<profile>.log` when it can be opened, otherwise to stderr (the sandbox denies the file), never to stdout.
- Environment overrides for a human terminal, CI and containers (ignored inside a Claude Code session, 3.2): `BRIGADE_CONFIG_DIR`, `BRIGADE_STATE_DIR`, `BRIGADE_PROFILE`, `BRIGADE_LOG_LEVEL`, `BRIGADE_LOG_FORMAT=json|pretty`, `BRIGADE_SECRET_STORE=file|os|auto` (Phase 5), `BRIGADE_TEST_OFFLINE=1` (any network dial fails loudly; used by C-01), `BRIGADE_SUPABASE_REALTIME_VSN` (test-only switch that selects the `2.0.0` binary-frame decoder, kept in the tree in case a Realtime release drops `vsn=1.0.0`).
---
