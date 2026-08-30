# Supabase in Go: a hand-rolled client for the Brigade adapter, validated against a real local stack

Date: 2026-08-30. Scope: decision D35 (all Go; the Supabase client is hand-rolled on `net/http` + `github.com/coder/websocket`). Everything below was run today against a fresh local stack unless marked otherwise. Confidence marks: `[verified]` = observed today (and, where a doc is cited, also documented), `[likely]` = documented or read in source but not exercised, `[uncertain]` = neither.

Raw evidence (verbatim request/response transcripts, tokens shortened for display only): `/private/tmp/claude-501/-Users-rjae-Development-appshapes-brigade/beee3690-4aa8-485b-9342-e1bb09c28aca/scratchpad/wf2/research/supabase-in-go-logs/{auth,refresh,refresh2,refresh3,logout,rpc,realtime,expiry,reauth,expjoin}.log`, plus the migration that was applied (`20260830120000_brigade_min.sql`). The Go module that produced them: `/private/tmp/claude-501/-Users-rjae-Development-appshapes-brigade/beee3690-4aa8-485b-9342-e1bb09c28aca/scratchpad/wf2/gotest/` (`go.mod` requires only `github.com/coder/websocket v1.8.15`; Go 1.27.0).

## 0. Verdict in one paragraph

A hand-rolled client is straightforward and is the right call. GoTrue anonymous sign-up, refresh and global sign-out are three plain JSON POSTs; PostgREST RPC is one POST with four headers; the Phoenix channel protocol needed for exactly one private broadcast channel is small (join, heartbeat, `access_token` push, leave, and reading `phx_reply`/`system`/`phx_close`/`broadcast`), and `coder/websocket` handles the transport. Two things a naive implementation would get wrong were found by running it: (1) with the current serializer version (`vsn=2.0.0`, realtime-js's default) the server delivers database broadcasts as **binary** WebSocket frames in realtime-js's private framing, not JSON; the client must either decode that framing (about 30 lines, shown below) or connect with `vsn=1.0.0`, where every frame is a JSON object; (2) PostgREST returns **HTTP 500** for the plan's `P0002 brigade:not_found`, so the adapter's error mapping must read the PostgREST error body's `message` prefix before it looks at the HTTP status, or every "not found" would be reported as `unavailable`. None of the three `supabase-community` Go libraries is worth depending on (section 7).

## 1. Environment (all `[verified]`)

- Supabase CLI 2.116.0 via `npx --yes supabase@2.116.0`; Docker 29; macOS arm64; Go 1.27.0.
- Images started: `supabase/postgres:17.6.1.165` (PostgreSQL 17.6), `supabase/gotrue:v2.196.0`, `supabase/postgrest:v16.1` (OpenAPI `info.version` = `16.1`), `supabase/realtime:v2.129.3`, `supabase/kong:2.8.1`. Excluded: studio, postgres-meta, imgproxy, storage-api, edge-runtime, mailpit, logflare, vector, supavisor.
- `config.toml` changes from the `init` template: `[api] schemas = ["public", "graphql_public", "brigade"]`; `[auth] enable_anonymous_sign_ins = true` (`enable_signup = true` is the template default); `[auth.rate_limit] anonymous_users = 1000`; `[studio]`, `[local_smtp]`, `[storage]`, `[edge_runtime]`, `[analytics]` `enabled = false`. `jwt_expiry` left at 3600, `enable_refresh_token_rotation = true`, `refresh_token_reuse_interval = 10` (template defaults).
- `supabase status -o env` keys (names as printed): `API_URL=http://127.0.0.1:54321`, `DB_URL=postgresql://postgres:postgres@127.0.0.1:54322/postgres`, `GRAPHQL_URL`, `REST_URL`, `JWT_SECRET=super-secret-jwt-token-with-at-least-32-characters-long`, `PUBLISHABLE_KEY=sb_publishable_ACJWlzQHlZjBrEguHvfOxg_3BJgxAaH`, `SECRET_KEY=sb_secret_…`, and the legacy `ANON_KEY` / `SERVICE_ROLE_KEY` HS256 JWTs (still emitted on 2.116.0). `supabase start` printed "WARN: no files matched pattern: supabase/seed.sql" (harmless) and then the same status JSON.
- The migration (schema `brigade`, two RLS tables, `security definer` RPCs, the 5.6 realtime trigger, `owns_session_topic()`, and the `realtime.messages` select policy) applied cleanly at `supabase start`. `realtime.messages`, `realtime.send(jsonb,text,text,boolean)` and `realtime.topic()` already exist in the `supabase/postgres:17.6.1.165` image before the Realtime container boots, so the plan's migration 5.6 can be an ordinary migration file (my file guarded with `to_regclass('realtime.messages')` and the guard passed; the policy `brigade_session_topic_read` was present after start). `pg_cron` was **not** among the installed extensions on this stack (`pg_extension` showed only `pgcrypto 1.3` of the four I looked for); the plan's 5.8 `create extension if not exists pg_cron` therefore needs the `[db]`/image support the local-dev digest describes, and its `exception when others` wrapper is the right shape.
- The local stack's user access tokens are signed with **ES256** (asymmetric signing key; JWT header `{"alg":"ES256"}`), while the legacy `ANON_KEY` is HS256 and a token I minted with the `JWT_SECRET` (HS256) was accepted by both PostgREST and Realtime. The adapter never verifies JWTs, but it must not assume HS256 anywhere (e.g. no local validation with the anon secret).

## 2. (a) GoTrue anonymous sign-up `[verified]`

Request (what auth-js `signInAnonymously()` sends, minus `X-Client-Info`):

```http
POST /auth/v1/signup HTTP/1.1
apikey: sb_publishable_ACJWlzQHlZjBrEguHvfOxg_3BJgxAaH
Content-Type: application/json;charset=UTF-8

{"data":{},"gotrue_meta_security":{}}
```

Response `200 OK`, `Content-Type: application/json`:

```json
{"access_token":"<ES256 JWT>","token_type":"bearer","expires_in":3600,"expires_at":1788114567,"refresh_token":"nipkyxvrjjfd",
 "user":{"id":"ad94ed01-90cd-4baa-ad19-38a5d028024c","aud":"authenticated","role":"authenticated","email":"","phone":"",
         "last_sign_in_at":"2026-08-30T17:29:27.825608761Z","app_metadata":{},"user_metadata":{},"identities":[],
         "created_at":"2026-08-30T17:29:27.822275Z","updated_at":"2026-08-30T17:29:27.827673Z","is_anonymous":true}}
```

JWT claims of `access_token`: `{"aal":"aal1","amr":[{"method":"anonymous","timestamp":1788110967}],"app_metadata":{},"aud":"authenticated","email":"","exp":1788114567,"iat":1788110967,"is_anonymous":true,"iss":"http://127.0.0.1:54321/auth/v1","phone":"","role":"authenticated","session_id":"e0910d1d-…","sub":"ad94ed01-…","user_metadata":{}}`. So: `role=authenticated`, `is_anonymous=true` (boolean), `sub` = `user.id`, `session_id` present. Refresh tokens are 12-character strings.

Variations, all `200` with a fresh anonymous user: legacy anon JWT as `apikey`; body `{}`; body `{"email":"","password":""}` (GoTrue treats empty email+phone as an anonymous sign-up when the feature is on, which matters for `auth-go`, section 7); `X-Supabase-Api-Version: 2024-01-01` header; an extra `Authorization: Bearer <publishable key>` (supabase-js sends this by default). Empty body (no JSON at all): `400 {"code":400,"error_code":"bad_json","msg":"Could not parse request body as JSON: unexpected end of JSON input"}`.

Gateway note: on this local stack, **Kong did not require an `apikey` at all** on `/auth/v1/*` or `/rest/v1/*` (requests with no `apikey`, a bogus one, or the secret key all reached the upstream). The hosted gateway is documented to require a key ("Safe to expose online … CLIs, source code" for publishable keys, https://supabase.com/docs/guides/api/api-keys) `[likely]`; the adapter must always send `apikey: <publishable key>`. Realtime, by contrast, validates the key itself (section 5).

Useful extra endpoints: `GET /auth/v1/user` with `Authorization: Bearer <access_token>` returns the user object; `GET /auth/v1/settings` (no auth) shows `"external":{"anonymous_users":true,…},"disable_signup":false`; `GET /auth/v1/health` returns `{"version":"v2.196.0","name":"GoTrue",…}`.

## 3. (b) Refresh: rotation, the race, reuse rules, terminal errors `[verified]`

Request: `POST /auth/v1/token?grant_type=refresh_token`, headers `apikey`, `Content-Type: application/json;charset=UTF-8`, body `{"refresh_token":"<token>"}`. Response `200` has exactly the sign-up shape: a **new** `refresh_token`, a new `access_token` (fresh `iat`/`exp`), `expires_in: 3600`, `expires_at`, and the `user` object.

Observed rules (GoTrue v2.196.0, rotation on, reuse interval 10 s):

| Scenario | Result |
| --- | --- |
| Two concurrent refreshes with the same token (two goroutines; repeated with two `curl` processes) | both `200`; **the same new refresh token** in both responses (`ac5nrkh2vgry` / `ac5nrkh2vgry`), two different but equally valid access tokens. No lockout. |
| Same token reused again < 10 s later | `200`, returns the currently active token (no new rotation) |
| Token one step behind the active one, reused 11 s later | `200`, returns the active token with a freshly signed access token ("fail-to-save" rule) |
| Token two or more steps behind, 11 s later | `400 {"code":400,"error_code":"refresh_token_already_used","msg":"Invalid Refresh Token: Already Used"}` |
| After that revocation: the active (never-used) token, 11 s later | `400 refresh_token_already_used` (family dead) |
| After that revocation: the active token used **within 10 s** | `200` with a **new** token, and that new token and its successors kept working 11 s and 22 s later (`refresh3.log`, `auth.refresh_tokens` rows show a normal chain). So the family survives if the legitimate client happens to refresh inside the grace window; it does not survive otherwise. |
| The revoked family's last access token on PostgREST | `200` until `exp` (JWTs are stateless); `GET /auth/v1/user` with it also still `200` (the `auth.sessions` row is kept) |
| Unknown or empty token | `400 {"code":400,"error_code":"validation_failed","msg":"Refresh token is not valid"}` |
| Unsupported `grant_type` | `400 {"code":400,"error_code":"invalid_credentials","msg":"unsupported_grant_type"}` |
| No `apikey` (local Kong) | processed anyway (see gateway note) |

Error body shape: without an `X-Supabase-Api-Version` header GoTrue returns `{"code":<HTTP status as a number>,"error_code":"<snake_case>","msg":"<text>"}`; with `X-Supabase-Api-Version: 2024-01-01` the same failure returns `{"code":"validation_failed","message":"Refresh token is not valid"}` (string `code`, `message` instead of `msg`) and echoes the header. auth-js sends the header and reads `code` when it is a string, else `error_code` (`packages/core/auth-js/src/lib/fetch.ts`, `handleError`). The Go client should send the header and decode both shapes into one struct (`Code any`, `ErrorCode`, `Msg`, `Message`).

Terminal codes for the adapter (documented at https://supabase.com/docs/guides/auth/debugging/error-codes `[verified: doc fetched today]`, observed where marked): `refresh_token_already_used` (observed), `refresh_token_not_found` (observed after sign-out: "Session containing the refresh token not found"), `session_expired` (Pro time-box/inactivity; not observed), `session_not_found` (observed on `/user` and `/logout` after sign-out), `user_not_found` (not observed). Practical rule: on `refresh_token_already_used`, re-read the credential file once (another process may have rotated it and the file holds a newer token), retry with that token at most once, then treat the profile as `unauthenticated` (exit 4, "run `team join` again"); never loop. Two Brigade processes sharing one file do not need a lock for correctness: a lost race returns the same new token to both, and the one-behind rule tolerates a stale reader for as long as it takes to re-read the file.

## 4. (c) Global sign-out `[verified]`

Request: `POST /auth/v1/logout?scope=global`, headers `apikey`, `Authorization: Bearer <access_token>`, no body. Response `204 No Content`, empty body.

Afterwards: refreshing with the current **or** the previous refresh token → `400 {"code":400,"error_code":"refresh_token_not_found","msg":"Invalid Refresh Token: Refresh Token Not Found"}`; PostgREST still accepts the access token until `exp`; `GET /auth/v1/user` and a second `/logout` with it → `403 {"code":403,"error_code":"session_not_found","msg":"Session from session_id claim in JWT does not exist"}`; garbage bearer → `403 {"code":403,"error_code":"bad_jwt","msg":"invalid JWT: unable to parse or verify signature, token is malformed: token contains an invalid number of segments"}`; no `Authorization` at all → `403 {"code":403,"error_code":"bad_jwt","msg":"invalid claim: missing sub claim"}`. `scope=others` keeps the current session (refresh still `200`); `scope=local` and **no scope** both revoke the current session (refresh → `refresh_token_not_found`). For an anonymous principal with one session `local`, `global` and the default are equivalent; use `scope=global` as the plan says (docs: "signs the user out of every device"; access tokens "cannot be revoked until expiration", https://supabase.com/docs/reference/javascript/auth-signout). After a global sign-out an anonymous principal is unrecoverable by design; `team join` mints a new one.

## 5. (d) PostgREST RPC, headers, and the exact error mapping `[verified]`

Request that works:

```http
POST /rest/v1/rpc/send_message HTTP/1.1
apikey: sb_publishable_…
Authorization: Bearer <user access_token>
Content-Type: application/json
Content-Profile: brigade
Accept-Profile: brigade

{"p_sender_session_id":"…","p_recipient_session_id":"…","p_body":"hello alpha"}
```

Response `200 OK`, headers `Content-Type: application/json; charset=utf-8`, `Content-Profile: brigade`, `Content-Range: 0-0/*`, body = the function's `jsonb` verbatim (pretty-printed by Postgres: `{"seq": 1, "created_at": "2026-08-30T17:29:28.005401+00:00", "message_id": "2ffcb14c-…", "recipient_session_id": "27e47a45-…"}`).

Schema headers: `Content-Profile: brigade` alone is sufficient for `POST /rpc/*`; `Accept-Profile` alone or neither → `404 {"code":"PGRST202","details":"Searched for the function public.whoami without parameters or with a single unnamed json/jsonb parameter, but no matches were found in the schema cache.","hint":null,"message":"Could not find the function public.whoami without parameters in the schema cache"}`. `GET /rest/v1/<table>` needs `Accept-Profile: brigade`. Send both on every request (postgrest-js sets `Accept-Profile` for GET/HEAD and `Content-Profile` otherwise, `PostgrestBuilder.ts` lines 283-289). Never pass `apikey` as a query parameter on `/rest/v1`: PostgREST parses it as a column filter (`400 PGRST100 "failed to parse filter (sb_publishable_…)"`).

Error body shape (PostgREST): `{"code":"<SQLSTATE or PGRSTnnn>","message":"<text>","details":<string|null>,"hint":<string|null>}` plus a `Proxy-Status: PostgREST; error=<code>` header. `RAISE … USING DETAIL/HINT` fills `details`/`hint`. HTTP statuses observed for the plan's 5.4 error table (PostgREST 16.1; the mapping documented at https://docs.postgrest.org/en/latest/references/errors.html agrees: `P0001 → 400`, other `P0* → 500`, `42501 → 403 if authenticated else 401`, `28* → 403`, default `400`):

| RAISE in the RPC | HTTP | body |
| --- | --- | --- |
| `P0002` `brigade:not_found` (unknown recipient, foreign sender session, foreign inbox: byte-identical bodies) | **500** | `{"code":"P0002","details":null,"hint":null,"message":"brigade:not_found"}` |
| `42501` `brigade:unauthorized` (with a JWT) | 403 | `{"code":"42501","details":null,"hint":null,"message":"brigade:unauthorized"}` |
| `P0001` `brigade:conflict:session_live` / `brigade:rate_limited:send_per_minute:60` / `brigade:loop_detected:…` | 400 | `{"code":"P0001",…,"message":"brigade:conflict:session_live"}` |
| `22023` `brigade:invalid_input:body` | 400 | `{"code":"22023",…,"message":"brigade:invalid_input:body"}` |
| `28000` `brigade:unauthenticated` (with a JWT) | 403 | `{"code":"28000",…,"message":"brigade:unauthenticated"}` |

Non-RAISE cases: no `Authorization` (apikey only, role `anon`) → `401`, `WWW-Authenticate: Bearer`, `{"code":"42501","details":null,"hint":null,"message":"permission denied for schema brigade"}` (anon has no USAGE on the schema, so every call fails uniformly before function resolution); the same 401 body for `Authorization: Bearer <legacy anon JWT>` and for `Authorization: Bearer <publishable key>` (PostgREST resolves both to role `anon`; the 401/403 choice is by role, not by header presence); bad signature → `401 {"code":"PGRST301","details":"None of the keys was able to decode the JWT","hint":null,"message":"No suitable key or wrong key type"}` with `WWW-Authenticate: Bearer error="invalid_token", error_description="No suitable key or wrong key type"`; expired → `401 {"code":"PGRST303","details":null,"hint":null,"message":"JWT expired"}`; unknown function → `404 PGRST202`; known function with wrong argument names → `404 PGRST202` ("Could not find the function brigade.register_session(wrong_arg) in the schema cache"); unknown table → `404 PGRST205`; direct `POST /rest/v1/messages` as a member → `403 {"code":"42501","details":null,"hint":"Grant the required privileges to the current role with: GRANT INSERT ON brigade.messages TO authenticated;","message":"permission denied for table messages"}`; secret key as `apikey` with no JWT → role `service_role`, `auth.uid()` null, `GET /rest/v1/sessions` → `403 42501 "permission denied for table sessions"` (no table grant, as the plan intends) but RPC **execution is allowed** for `service_role` (see gotcha G1), the RPC then raises `28000 brigade:unauthenticated` → 403. Direct `GET /rest/v1/sessions?select=*` and `/messages?select=*` with a member JWT return only RLS-visible rows (own sessions, own inbox).

Adapter mapping rule that follows: parse the body first; if `message` starts with `brigade:` map on that prefix regardless of HTTP status (the not_found case is a 500); else map `PGRST301`/`PGRST303` → `unauthenticated` (refresh once, then terminal), `42501` → `unauthorized` (with a 401 status it means "no usable JWT" → `unauthenticated`), `28000` → `unauthenticated`, `P0001`/`22023`/`23505` per the plan; only when the body is not PostgREST JSON at all treat 5xx/transport errors as `unavailable`. Consider `RAISE … USING ERRCODE = 'PT404'`-style custom codes if a 500 in gateway logs is undesirable (documented PostgREST feature `[likely]`; not tested).

## 6. (e) Realtime over WebSocket with a hand-rolled Phoenix client `[verified]`

Connection: `ws://127.0.0.1:54321/realtime/v1/websocket?apikey=<publishable key>&vsn=2.0.0` (hosted: `wss://<ref>.supabase.co/realtime/v1/websocket?…`). Upgrade → `HTTP 101`, `Server: Cowboy`. With a bogus key → `HTTP 401 {"error":"The token provided is not a valid JWT"}`; with no key → `HTTP 401 {"error":"API key is missing"}` (both after a deliberate ~2 s upstream delay, `X-Kong-Upstream-Latency: 2008`); `apikey` as an HTTP header instead of a query parameter → 401 (the server reads the query string). The legacy anon JWT also works as `apikey`. `coder/websocket` needs no headers or subprotocols.

Serializer versions (realtime-js `lib/constants.ts`: `DEFAULT_VSN = '2.0.0'`, `VSN_1_0_0 = '1.0.0'`; docs: "vsn … defaults to 1.0.0", https://supabase.com/docs/guides/realtime/protocol):

- `vsn=2.0.0`: text frames are JSON arrays `[join_ref, ref, topic, event, payload]`; **database broadcasts arrive as binary frames** (realtime-js `Serializer.KINDS.userBroadcast = 4`): byte 0 kind (4), byte 1 topic length, byte 2 event length, byte 3 metadata length, byte 4 payload encoding (1 = JSON), then topic, event, metadata JSON, payload JSON. Decoded, the first broadcast was `topic=realtime:brigade:session:<sid> event=broadcast payload={"event":"message_accepted","meta":{"id":"4ae59ee2-…"},"payload":{"id":"4ae59ee2-…","message_id":"f1c95d11-…","seq":2},"type":"broadcast"}`.
- `vsn=1.0.0`: every frame is a JSON object `{"event","topic","payload","ref"}` (plus `"join_ref"` on client pushes); the same broadcast arrived as text `{"event":"broadcast","payload":{"event":"message_accepted","meta":{"id":"…"},"payload":{"id": "…", "seq": 17, "message_id": "…"},"type":"broadcast"},"ref":null,"topic":"realtime:brigade:session:<sid>"}`; `phx_reply` carries `join_ref: null` in v1.
- No `vsn` parameter: the server spoke v1 (a v1 heartbeat got a v1 object reply).

The `payload.id` inside the broadcast is the `realtime.messages` row id that `realtime.send` adds (`jsonb_set(payload, '{id}', …)`), and `meta.id` repeats it; `message_id` and `seq` are Brigade's. Never the body (the trigger sends ids only).

Join (mirrors `RealtimeChannel.subscribe()`; presence now carries `enabled`):

```json
["2","2","realtime:brigade:session:<sid>","phx_join",
 {"access_token":"<user JWT>",
  "config":{"broadcast":{"ack":false,"self":false},"presence":{"enabled":false,"key":""},"postgres_changes":[],"private":true}}]
```

Replies:

| Case | Reply (v2 array shown as fields) | Time |
| --- | --- | --- |
| own topic, private, valid token | `phx_reply {"status":"ok","response":{"postgres_changes":[]}}`; no `system` message follows for a broadcast-only channel | 5-12 ms |
| another member's session topic | `phx_reply {"status":"error","response":{"reason":"Unauthorized: You do not have permissions to read from this Channel topic: brigade:session:<other sid>"}}` | **5.0 s** (the server sleeps `CHANNEL_ERROR_BACKOFF_MS` = 5 s before rejecting; named in realtime-js's `POSTGRES_CHANGES_WAIT_ERROR_GRACE` comment) |
| non-existent session id | identical `Unauthorized` reason with that topic (no existence oracle) | 5.0 s |
| private topic with no `access_token` in the join (apikey only) | `Unauthorized` | 5.0 s |
| topic without the `realtime:` prefix | `phx_reply {"status":"error","response":{"reason":"unmatched topic"}}` | immediate |
| correctly signed but expired JWT in the join | `{"status":"error","response":{"reason":"InvalidJWTToken: Token has expired 30 seconds ago"}}` | 5.0 s |
| garbage `access_token` | `{"status":"error","response":{"reason":"MalformedJWT: The token provided is not a valid JWT"}}` | 5.0 s |
| public join (`private:false`) of the same topic, with a token | `ok` immediately, and it received **none** of 3 subsequent private broadcasts within 4 s (private → private only) | |

The reason strings confirm `realtime.topic()` is the bare topic at join time (`brigade:session:<sid>`, no `realtime:` prefix), which is what the plan's policy compares against. Refused joins do not disturb an already-joined channel on the same socket; the socket stays usable (heartbeats keep answering).

Heartbeat: `[null,"1","phoenix","heartbeat",{}]` → `[null,"1","phoenix","phx_reply",{"status":"ok","response":{}}]` (v1: `{"event":"heartbeat","payload":{},"ref":"1","topic":"phoenix"}` → object reply). A socket that never heartbeats was closed by the server **66 s** after connecting with close code 1000 (Phoenix's 60 s transport timeout); realtime-js sends every 25 s and the docs say "at least every 25 seconds".

Latency (10 inserts by another principal through `send_message`, private channel already joined): broadcast received 1-2 ms after the RPC request started for 9 of 10 and 25 ms for the first (TLS-less local loopback; mean 4 ms, max 25 ms); in every case the event arrived within 2 ms **after** the RPC response, once before it. The WAL-driven broadcast is fast enough that the plan's "drain on hint" needs no extra delay.

`access_token` push: `["<join_ref>","4","realtime:brigade:session:<sid>","access_token",{"access_token":"<new JWT>"}]` gets **no reply** (fire-and-forget) and the channel keeps delivering. With a minted 40 s token: a channel that never renewed received, at the exact `exp`, `system {"message":"Token has expired 0 seconds ago","status":"error","extension":"system","channel":"brigade:session:<sid>"}` followed by `phx_close {}` for that channel (socket left open, heartbeats still answered, then the socket got EOF ~45 s later, cause not established `[uncertain]`); a channel that pushed a 120 s token at t=20 s kept receiving broadcasts through t=90 s. The push also **re-runs the policy immediately**: after the session row's `owner_id` was changed underneath a joined channel (as `postgres`), broadcasts kept flowing (policy cached per connection, as documented at https://supabase.com/docs/guides/realtime/authorization) until an `access_token` push, which within 2 ms produced `system {"message":"You do not have permissions to read from this Channel topic: brigade:session:<sid>","status":"error","extension":"system","channel":"…"}` and `phx_close`, and a rejoin was refused with `Unauthorized` after 5 s. So revocation takes effect at the next token push (the adapter pushes on every refresh) or JWT expiry, whichever comes first, which matches plan 5.10's "at most 1 h" lag.

Leave and duplicates: `phx_leave {}` → `phx_reply ok` then `phx_close {}` (with the channel's `join_ref`); rejoining the same topic on the same socket works; joining a topic twice on one socket yields a second `ok` and a `phx_close` for the older `join_ref` (Phoenix replaces the channel). A client broadcast pushed on the private topic (`["<join_ref>","3",topic,"broadcast",{"type":"broadcast","event":"message_accepted","payload":{…}}]`) produced no reply, no echo and no error with `ack:false` (silently dropped: no insert policy). `phx_error` was not observed; per Phoenix semantics it is sent when the channel process crashes and realtime-js's `Channel.rejoin()` with backoff `[1000, 2000, 5000, 10000]` ms (`RealtimeClient.ts` `RECONNECT_INTERVALS`) is the reference behaviour `[likely]`.

Recommended client behaviour for the adapter's watcher: connect with `vsn=1.0.0` **or** implement the v2 binary decoder (both shown below; v1 is simpler and is what `supabase-community/realtime-go` uses); join with `private:true` and the current access token; heartbeat every 25 s; on every token refresh push `access_token`; treat `phx_reply status=error`, `system status=error`, `phx_close`, `phx_error` and socket close all as "channel down → rejoin with backoff, refreshing the token first if the reason mentions the JWT"; treat an `Unauthorized` join reason as fatal `unauthorized` (exit 5) since it recurs; `InvalidJWTToken`/`MalformedJWT` → refresh once, then `unauthenticated`. Use a join timeout of at least 10 s (refusals take 5 s).

## 7. `supabase-community` Go libraries: evaluation `[verified from source fetched today]`

- `github.com/supabase-community/auth-go` (tags up to v2.0.0; `supabase-go` pins v1.4.0; last push 2025-12-12): no anonymous sign-in method; `Signup(types.SignupRequest)` is "Register a new user with an email and password" and would work only by accident (an empty email/password body creates an anonymous user, section 2); `RefreshToken(token)` exists; `Logout()` takes no `scope` (server default = revoke the current session, equivalent to global for one session); errors are returned as `fmt.Errorf("response status code %d: %s", …)` strings, so `error_code` would have to be re-parsed from text. Not worth it.
- `github.com/supabase-community/postgrest-go` (tags `v0.0.12`, `v0.010`; last push 2026-08-19): sets `Accept-Profile`/`Content-Profile` from `Schema()`, `Rpc(fn, args, opts)`, `SetAuthToken`, and a `PostgrestError{Message, Details, Hint, Code}`; but `go.mod` (`go 1.25`) requires `lib/pq`, `testcontainers-go`, `httpmock` and `testify` in the main module graph, and the umbrella `supabase-go` replaces it with a personal fork (`replace … => github.com/roja/postgrest-go v0.0.11`). The whole thing the adapter needs is one `http.Do` with four headers. Not worth it.
- `github.com/supabase-community/realtime-go` (v0.1.1, 15 stars, last push 2025-11-10): depends on `nhooyr.io/websocket v1.8.11` (the pre-rename of coder/websocket); hard-codes `wss://<ref>.supabase.co/…&vsn=1.0.0`; has `ChannelConfig.Private bool` but its join message is `{"type":"subscribe","topic":…,"event":"phx_join","payload":<config>}` with no `config` wrapper, no `ref`, and **no `access_token`** anywhere in the package (`grep -c access_token` = 0 in all files); `SetAuth` only stores a string; `Subscribe` reports joined without waiting for `phx_reply`. It cannot authorize a private channel. Not usable.

Conclusion: depend on `github.com/coder/websocket` only (v1.8.15 resolved today; API used: `websocket.Dial`, `Conn.Read`, `Conn.Write`, `Conn.Close`, `Conn.SetReadLimit`, `websocket.CloseError`).

## 8. Go code that worked (excerpts from the module in `scratchpad/wf2/gotest/`)

HTTP helper and GoTrue (from `http.go` / `gotrue.go`):

```go
type Session struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	RefreshToken string `json:"refresh_token"`
	User         struct {
		ID          string `json:"id"`
		Aud         string `json:"aud"`
		Role        string `json:"role"`
		IsAnonymous bool   `json:"is_anonymous"`
	} `json:"user"`
}

// GoTrue error body. Without X-Supabase-Api-Version: {"code":400,"error_code":"…","msg":"…"};
// with the 2024-01-01 header: {"code":"…","message":"…"}. Decode both.
type AuthError struct {
	Code      any    `json:"code"`
	ErrorCode string `json:"error_code"`
	Msg       string `json:"msg"`
	Message   string `json:"message"`
}

func authHeaders(apikey string) map[string]string {
	return map[string]string{"apikey": apikey, "Content-Type": "application/json;charset=UTF-8"}
}

// POST /auth/v1/signup with auth-js's body; 200 -> Session (user.is_anonymous == true).
func signUpAnonymous(ctx context.Context, apiURL, apikey string) (Session, Resp) {
	r := do(ctx, "POST", apiURL+"/auth/v1/signup", authHeaders(apikey),
		map[string]any{"data": map[string]any{}, "gotrue_meta_security": map[string]any{}})
	var s Session
	if r.Status == 200 { _ = json.Unmarshal(r.Body, &s) }
	return s, r
}

// POST /auth/v1/token?grant_type=refresh_token; 200 -> rotated Session; 400 -> AuthError.
func refresh(ctx context.Context, apiURL, apikey, refreshToken string) (Session, Resp) {
	r := do(ctx, "POST", apiURL+"/auth/v1/token?grant_type=refresh_token", authHeaders(apikey),
		map[string]string{"refresh_token": refreshToken})
	var s Session
	if r.Status == 200 { _ = json.Unmarshal(r.Body, &s) }
	return s, r
}

// POST /auth/v1/logout?scope=global with the access token; 204 on success; 403 session_not_found/bad_jwt otherwise.
func logout(ctx context.Context, apiURL, apikey, accessToken string) Resp {
	h := authHeaders(apikey)
	h["Authorization"] = "Bearer " + accessToken
	return do(ctx, "POST", apiURL+"/auth/v1/logout?scope=global", h, nil)
}

// do: build the request, set headers, read the whole body; Resp{Status, Header, Body, Elapsed}.
func do(ctx context.Context, method, url string, headers map[string]string, body any) Resp {
	var b []byte
	if body != nil { b, _ = json.Marshal(body) }
	req, _ := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(b))
	for k, v := range headers { req.Header.Set(k, v) }
	resp, err := http.DefaultClient.Do(req)
	if err != nil { return Resp{Status: -1, Body: []byte(err.Error())} }
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return Resp{Status: resp.StatusCode, Header: resp.Header, Body: rb}
}
```

PostgREST RPC (from `postgrest.go`):

```go
type PostgrestError struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Details *string `json:"details"`
	Hint    *string `json:"hint"`
}

func rpc(ctx context.Context, apiURL, apikey, jwt, fn string, args any) Resp {
	h := map[string]string{
		"apikey": apikey, "Content-Type": "application/json",
		"Accept-Profile": "brigade", "Content-Profile": "brigade",
	}
	if jwt != "" { h["Authorization"] = "Bearer " + jwt }
	if args == nil { args = map[string]any{} }
	return do(ctx, "POST", apiURL+"/rest/v1/rpc/"+fn, h, args)
}

// mapping sketch: body first, status second
// var e PostgrestError; if json.Unmarshal(r.Body, &e) == nil && e.Code != "" {
//   if strings.HasPrefix(e.Message, "brigade:") { code := strings.Split(e.Message, ":")[1] ... }
//   switch e.Code { case "PGRST301", "PGRST303": unauthenticated; case "42501": if r.Status == 401 {unauthenticated} else {unauthorized}; case "28000": unauthenticated; case "P0002": not_found; case "23505": conflict; ... }
// } else if r.Status >= 500 || r.Status < 0 { unavailable } else { internal }
```

Phoenix client core (from `phoenix.go`; `Msg`, `push`, `join`, binary decode):

```go
import "github.com/coder/websocket"

type Msg struct {
	JoinRef *string         `json:"join_ref"`
	Ref     *string         `json:"ref"`
	Topic   string          `json:"topic"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
	Binary  bool            `json:"-"`
}

func dial(ctx context.Context, apiURL, apikey, vsn string) (*websocket.Conn, error) {
	u := strings.Replace(apiURL, "http://", "ws://", 1) + "/realtime/v1/websocket?apikey=" + apikey
	if vsn != "" { u += "&vsn=" + vsn }        // hosted: https:// -> wss://
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{})
	if err != nil { return nil, fmt.Errorf("dial: %w (status %d)", err, statusOf(resp)) } // 401 = bad/missing apikey
	c.SetReadLimit(1 << 20)
	return c, nil
}

// vsn 2.0.0 wire form for a client push: [join_ref, ref, topic, event, payload]; vsn 1.0.0: {"topic","event","payload","ref","join_ref"}.
func encode(vsn string, joinRef *string, ref, topic, event string, payload any) []byte {
	pb, _ := json.Marshal(payload)
	if vsn == "2.0.0" {
		w, _ := json.Marshal([]any{joinRef, ref, topic, event, json.RawMessage(pb)})
		return w
	}
	obj := map[string]any{"topic": topic, "event": event, "payload": json.RawMessage(pb), "ref": ref}
	if joinRef != nil { obj["join_ref"] = *joinRef }
	w, _ := json.Marshal(obj)
	return w
}

// join payload (RealtimeChannel.subscribe): the join push's join_ref is its own ref.
func joinPayload(accessToken string) map[string]any {
	return map[string]any{
		"access_token": accessToken,
		"config": map[string]any{
			"broadcast":        map[string]any{"ack": false, "self": false},
			"presence":         map[string]any{"key": "", "enabled": false},
			"postgres_changes": []any{},
			"private":          true,
		},
	}
}
// heartbeat: encode(vsn, nil, ref, "phoenix", "heartbeat", map[string]any{})
// token push: encode(vsn, &joinRef, ref, topic, "access_token", map[string]any{"access_token": jwt})
// leave:      encode(vsn, &joinRef, ref, topic, "phx_leave", map[string]any{})

func decode(vsn string, typ websocket.MessageType, data []byte) (Msg, error) {
	if typ == websocket.MessageBinary { return decodeBinaryBroadcast(data) }
	if vsn == "2.0.0" {
		var arr []json.RawMessage
		if err := json.Unmarshal(data, &arr); err != nil || len(arr) != 5 { return Msg{}, fmt.Errorf("bad v2 frame") }
		var m Msg
		_ = json.Unmarshal(arr[0], &m.JoinRef); _ = json.Unmarshal(arr[1], &m.Ref)
		_ = json.Unmarshal(arr[2], &m.Topic);   _ = json.Unmarshal(arr[3], &m.Event)
		m.Payload = arr[4]
		return m, nil
	}
	var m Msg
	return m, json.Unmarshal(data, &m)
}

// realtime-js Serializer._decodeUserBroadcast (kind 4): how vsn 2.0.0 servers send broadcasts.
func decodeBinaryBroadcast(b []byte) (Msg, error) {
	if len(b) < 5 || b[0] != 4 { return Msg{}, errors.New("unknown binary frame") }
	topicLen, evLen, metaLen, enc := int(b[1]), int(b[2]), int(b[3]), b[4]
	off := 5
	topic := string(b[off : off+topicLen]); off += topicLen
	ev := string(b[off : off+evLen]);       off += evLen
	meta := b[off : off+metaLen];           off += metaLen
	out := map[string]any{"type": "broadcast", "event": ev}
	if enc == 1 { var pj any; _ = json.Unmarshal(b[off:], &pj); out["payload"] = pj } // 1 = JSON, 0 = raw bytes
	if metaLen > 0 { var mj any; _ = json.Unmarshal(meta, &mj); out["meta"] = mj }
	pb, _ := json.Marshal(out)
	return Msg{Topic: topic, Event: "broadcast", Payload: pb, Binary: true}, nil
}
```

Read loop pattern that worked: one goroutine calling `c.Read(context.Background())` forever, decoding into a buffered channel; on error, `errors.As(err, &websocket.CloseError{})` gives the close code (1000 observed for server-initiated closes; a plain EOF appeared once as `failed to read frame header: EOF`). Writers use `c.Write(ctx, websocket.MessageText, data)` from any goroutine (coder/websocket serialises writes).

## 9. Recommendations for the adapter (section 5 of the plan, Go edition)

1. Credentials file = the sign-up/refresh response as returned (`access_token`, `refresh_token`, `expires_at`, `user.id`); refresh when `expires_at - now < 90 s` (auth-js's margin) and on any `PGRST303`. Send `X-Supabase-Api-Version: 2024-01-01` to GoTrue and decode both error shapes.
2. On `refresh_token_already_used`: re-read the file, retry once with the newest token, then `unauthenticated`. On `refresh_token_not_found`/`session_not_found`: `unauthenticated` at once. Never retry `/token` in a loop.
3. Always send `apikey` (publishable key) to every service even though local Kong does not check it; Realtime checks it itself and 401s at the upgrade.
4. PostgREST: both profile headers; map errors from the body's `brigade:` prefix first; `P0002` arrives as HTTP 500.
5. Realtime: `vsn=1.0.0` for the simplest client (all-JSON) or `vsn=2.0.0` with the binary decoder above; join timeout ≥ 10 s; heartbeat 25 s; push `access_token` after every refresh (it also re-checks the policy); treat `system status=error` + `phx_close` as "rejoin"; keep the polling drain authoritative as planned.
6. Keep the plan's explicit per-function `revoke execute … from public` statements: see gotcha G1 (the default-privileges line alone did not take effect on this stack).

## 10. Gotchas (all observed today unless marked)

- G1. **`alter default privileges in schema brigade revoke execute on functions from public, anon, authenticated;` (plan 5.3) is a no-op**: `pg_proc.proacl` shows `{=X/postgres,postgres=X/postgres,authenticated=X/postgres}` on every RPC and `pg_default_acl` has no row for the schema; only `owns_session_topic`, which had an explicit `revoke execute … from public, anon`, lacks the `=X` entry. Consequence: `service_role` (which has USAGE on the schema in the plan) can execute every RPC; `anon` cannot only because it lacks USAGE. Per-schema default privileges cannot revoke the built-in PUBLIC EXECUTE (PostgreSQL rule, section 11). Every function needs its explicit `revoke … from public` (or the global `for role postgres` form), and the hygiene test in plan 9.3 (`functions.sql`) must assert no `=X` entry.
- G2. `P0002` → HTTP 500 from PostgREST (documented; verified). Map on the body.
- G3. vsn 2.0.0 broadcasts are binary frames; vsn 1.0.0 are JSON objects.
- G4. Refused private joins take 5 s; `Unauthorized` is uniform for foreign and non-existent topics.
- G5. A refresh-token family can survive reuse detection if the legitimate client refreshes within 10 s of the revocation; do not build on either outcome.
- G6. Local Kong 2.8.1 does not enforce `apikey` on `/auth/v1` and `/rest/v1` (a test that expects 401 there will pass hosted and fail locally); Realtime returns 401 after ~2 s for a bad key.
- G7. `?apikey=` on `/rest/v1` is parsed as a filter (400 PGRST100); use the header.
- G8. `service_role` JWT calling an RPC gets `28000 brigade:unauthenticated` (auth.uid() null), not `42501`: the plan's E0-1 expectation "a service_role request gets 42501 on every brigade.* object" holds for tables, not for functions, unless G1 is fixed.
- G9. The `expire` socket was dropped with a bare TCP EOF ~45 s after its only channel was closed for token expiry; treat EOF like a close.
- G10. Local access tokens are ES256; the legacy `ANON_KEY` is HS256; a minted HS256 token with the local `JWT_SECRET` is accepted (test-only convenience; never in the adapter).

## 11. Isolation of G1 (`ALTER DEFAULT PRIVILEGES`) `[verified]`

Re-run on a fresh start and again after `supabase db reset` (the CLI re-applies the migration as role `postgres`: schema, tables and functions are all owned by `postgres`):

- `pg_default_acl` has **no row for schema `brigade`** after the migration, and none after running `alter default privileges in schema brigade revoke execute on functions from public, anon, authenticated;` by hand as `postgres` (the statement returns `ALTER DEFAULT PRIVILEGES` and stores nothing); a function created immediately afterwards (`brigade.t_after_manual`) has `proacl = NULL`, i.e. the built-in default, PUBLIC EXECUTE. The same as `supabase_admin`. So this is PostgreSQL behaviour, not the CLI's migration runner: **per-schema default privileges are added to the global defaults and cannot revoke privileges that are granted globally, including the built-in `EXECUTE … TO PUBLIC` on functions; a per-schema REVOKE only reverses a previous per-schema GRANT** (PostgreSQL manual, `ALTER DEFAULT PRIVILEGES`, Notes, https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html, fetched today: "Default privileges that are specified per-schema are added to whatever the global default privileges are for the particular object type. This means you cannot revoke privileges per-schema if they are granted globally (either by default, or according to a previous ALTER DEFAULT PRIVILEGES command that did not specify a schema). Per-schema REVOKE is only useful to reverse the effects of a previous per-schema GRANT." Its own example is the plan's statement: "ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC; — This command has no effect, unless it is undoing a matching GRANT"; the working form is "ALTER DEFAULT PRIVILEGES FOR ROLE admin REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;").
- The image's own global entries explain what does work elsewhere: Supabase ships `pg_default_acl` rows for `public`, `storage`, `graphql`, `graphql_public`, `supabase_functions` (`{postgres=X, anon=X, authenticated=X, service_role=X}` on functions) for roles `postgres` and `supabase_admin`, but none for a new schema and no global (schema-less) entry for `postgres`.
- Fix for plan 5.3: either the global form `alter default privileges for role postgres revoke execute on functions from public;` (affects every schema for functions created by `postgres` from then on; `public` keeps its explicit role grants through Supabase's own per-schema entries) or, safer and self-contained, an explicit `revoke execute on function brigade.<fn>(…) from public;` right after every `create function` in the schema, including trigger functions and helpers; plus a `functions.sql` pgTAP assertion that no function in `brigade` has an ACL entry whose grantee is empty (`=X`) and that `proacl` is not NULL. The plan's per-RPC `revoke execute … from public, anon` statements already exist in 5.4's grants paragraph; the ineffective 5.3 line should be replaced rather than kept as a false safety net.

## 12. Open questions

1. Why the socket whose channel expired was closed with a bare EOF ~45 s later (server-side idle rule for channel-less sockets, or JWT-based socket teardown); realtime-js reconnects anyway.
2. Whether the hosted gateway's `apikey` enforcement returns the documented `{"message":"No API key found in request"}` shape (not testable locally); P5-1.
3. Whether custom `PT4xx` SQLSTATEs should replace `P0002` to avoid 500s in gateway logs (a naming change in the plan's 5.4 table; the body-prefix mapping makes it unnecessary for correctness).
4. Behaviour of realtime-js's `access_token` push when the token is unchanged (it only pushes on change); the hand-rolled client can push on every refresh.

## 13. Sources

- https://supabase.com/docs/guides/realtime/protocol (fetched today: vsn 1.0.0/2.0.0 formats, heartbeat 25 s, join/access_token/system/phx_close events)
- https://docs.postgrest.org/en/latest/references/errors.html (fetched today: SQLSTATE → HTTP table, `PTxxx` custom statuses)
- https://supabase.com/docs/guides/auth/auth-anonymous, https://supabase.com/docs/guides/auth/debugging/error-codes, https://supabase.com/docs/reference/javascript/auth-signout, https://supabase.com/docs/guides/api/api-keys (fetched today)
- https://supabase.com/docs/guides/realtime/authorization (policy caching and re-evaluation on a new JWT; cited by the plan; behaviour confirmed by the reauth experiment)
- realtime-js source (monorepo, fetched today): https://github.com/supabase/supabase-js/tree/master/packages/core/realtime-js/src (`RealtimeClient.ts`, `RealtimeChannel.ts`, `lib/constants.ts`, `lib/serializer.ts`); the old repo https://github.com/supabase/realtime-js is archived (2026-01-23) and points there. auth-js: `packages/core/auth-js/src/GoTrueClient.ts` (`signInAnonymously`, `_refreshAccessToken`, `_signOut`), `lib/fetch.ts` (`handleError`). postgrest-js: `packages/core/postgrest-js/src/PostgrestBuilder.ts` (profile headers). supabase-js latest release v2.112.4 (2026-08-24).
- https://github.com/supabase-community/auth-go, https://github.com/supabase-community/postgrest-go, https://github.com/supabase-community/realtime-go, https://github.com/supabase-community/supabase-go (source and `go.mod` fetched today)
- https://github.com/coder/websocket (v1.8.15 resolved by `go get` today)
