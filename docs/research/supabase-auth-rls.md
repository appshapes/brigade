# Supabase auth + RLS for the Brigade default adapter

Date: 2026-08-30. Written for the Brigade default (Supabase) adapter described in
`.context/plans/claude-code-team-messaging-logical-plan.md`. Every claim below is tagged
`[verified]` (read in official docs, source, or observed live today), `[likely]` (strongly
implied by docs/source but not directly observed), or `[uncertain]`.

Live verification: sections 5-7 were applied to a local Supabase stack today (CLI 2.116.0,
Postgres 17.6, GoTrue v2.196.0, Realtime v2.129.3, supabase-js 2.112.4 on Node 24.16.0) and
exercised with four anonymous principals; 37 checks, all passing after two test-side fixes.
Section 9 lists them. The exact SQL and scripts sit next to this file:
`supabase-auth-rls.schema.sql`, `supabase-auth-rls.test.mjs`, `supabase-auth-rls.test3.mjs`.

Versions observed today: `@supabase/supabase-js` 2.112.4 (published 2026-08-24, `engines.node
>= 22.0.0`; a `3.0.0-next.29` prerelease exists under the `next` dist-tag) [verified via `npm
view`]; Supabase CLI 2.116.0 [verified via `npx supabase --version`]; GoTrue (supabase/auth)
`master` source as of today; Node 24.16.0 on this machine.

---

## 0. Decisions in one screen

1. Ship the project's **publishable key** (`sb_publishable_...`) in the plugin/CLI config. Never
   a secret key. Legacy `anon`/`service_role` JWT keys are deprecated ("by the end of 2026").
2. Each local adapter profile calls `signInAnonymously()` once. The resulting user has Postgres
   role `authenticated` and JWT claim `is_anonymous: true`. Membership, not anonymity, is the
   authorization gate; RLS never needs to look at `is_anonymous` for Brigade.
3. Persist the whole auth-js `Session` JSON (access + refresh token) in a 0600 file under
   `CLAUDE_PLUGIN_DATA`, via a custom `storage` adapter that re-reads the file on every
   `getItem`. auth-js reads storage on every `getSession()`; that gives the short-lived CLI and
   the long-lived watcher a coherent view without a shared lock.
4. Short-lived CLI invocations: `autoRefreshToken: false` (on-demand refresh still happens inside
   `getSession()` when the token is within 90 s of expiry). Long-lived watcher:
   `autoRefreshToken: true` (default; timer is `unref()`ed) and `auth.dispose()` on exit.
5. Losing the refresh token (or tripping reuse detection) kills the principal. The client then
   clears the session file and emits `SIGNED_OUT`; the adapter must re-sign-in anonymously and the
   user must re-join the team. Design the join flow to be cheap; do not enable Pro-plan session
   time-box/inactivity limits on the Brigade project.
6. Team join is a `SECURITY DEFINER` RPC that verifies a bcrypt hash with `pgcrypto`
   `crypt()`, rate-limits attempts in a table, and **returns** a status instead of raising, so the
   attempt row is not rolled back with the failed request.
7. Sender identity is stamped by the database: `default auth.uid()` on owner/sender columns,
   column-level `INSERT` grants that exclude those columns, a `BEFORE INSERT` trigger that
   overwrites them regardless, and `WITH CHECK` policies as the last line.
8. Do not enable CAPTCHA on the Brigade project. Anonymous sign-in goes through `/auth/v1/signup`,
   which sits behind the CAPTCHA middleware, and a CLI cannot solve a browser challenge. Abuse
   control is the built-in 30/hour/IP anonymous sign-in limit plus join-attempt rate limiting in
   the database plus periodic cleanup of anonymous users with no memberships.

---

## 1. Anonymous sign-ins

### 1.1 Enabling

- Hosted dashboard: Authentication > Sign In / Providers > "Enable Anonymous Sign-Ins"
  (`https://supabase.com/dashboard/project/_/auth/providers`). [verified]
  Source: https://supabase.com/docs/guides/auth/auth-anonymous
- Local `supabase/config.toml` (CLI 2.116.0 generates this key with default `false`): [verified]

  ```toml
  [auth]
  enable_signup = true                # must stay true: GoTrue rejects anonymous sign-ups when signups are disabled
  enable_anonymous_sign_ins = true
  ```

  Source: https://supabase.com/docs/guides/local-development/cli/config and the file emitted by
  `npx supabase init` today.
- Self-hosted env var: `GOTRUE_EXTERNAL_ANONYMOUS_USERS_ENABLED=true` [verified: this is what
  the CLI sets on the local `supabase_auth` container when `enable_anonymous_sign_ins = true`;
  the related env vars observed were `GOTRUE_RATE_LIMIT_ANONYMOUS_USERS=30`,
  `GOTRUE_JWT_DEFAULT_GROUP_NAME=authenticated`, `GOTRUE_SECURITY_CAPTCHA_ENABLED=false`,
  `GOTRUE_SECURITY_CAPTCHA_PROVIDER`, `GOTRUE_SECURITY_CAPTCHA_SECRET`,
  `GOTRUE_SECURITY_REFRESH_TOKEN_ROTATION_ENABLED=true`,
  `GOTRUE_SECURITY_REFRESH_TOKEN_REUSE_INTERVAL=10`, `GOTRUE_DISABLE_SIGNUP=false`].
- Management API field: `external_anonymous_users_enabled` on
  `PATCH /v1/projects/{ref}/config/auth` [verified in the Management API OpenAPI document at
  https://api.supabase.com/api/v1-json].
- `enable_signup = false` (`disable_signup` in the Management API) also blocks anonymous sign-ins:
  `SignupAnonymously` returns `signup_disabled` when `config.DisableSignup` is set. [verified in
  `internal/api/anonymous.go`]

### 1.2 Wire and client call

- HTTP: `POST /auth/v1/signup` with a body that has no `email`/`phone`; the router dispatches to
  `SignupAnonymously` when the anonymous-users feature is on, else returns
  `anonymous_provider_disabled` (422). [verified in `internal/api/api.go`]
- supabase-js: [verified in `GoTrueClient.ts`]

  ```ts
  const { data, error } = await supabase.auth.signInAnonymously({
    options: { data: { harness: 'claude-code' }, captchaToken: undefined },
  })
  // data.session: { access_token, refresh_token, expires_in, expires_at, token_type, user }
  // data.user.is_anonymous === true
  ```

  The body sent is `{ data, gotrue_meta_security: { captcha_token } }`. On success the client
  saves the session to `storage` and emits `SIGNED_IN`.
  Source: https://supabase.com/docs/reference/javascript/auth-signinanonymously

### 1.3 JWT claims and Postgres role

- The access token of an anonymous user carries `role: "authenticated"`, `is_anonymous: true`,
  `sub: <user uuid>`, `aud: "authenticated"`, `session_id`, `aal`, `amr`, `app_metadata`,
  `user_metadata`, `iss`, `iat`, `exp`. [verified: `internal/tokens/service.go` copies
  `params.User.Role` and `params.User.IsAnonymous` into the claims; `signupNewUser` sets the role
  to `config.JWT.DefaultGroupName`, which Supabase configures as `authenticated`; the anonymous
  sign-in guide states "Anonymous users receive the `authenticated` Postgres role". Confirmed live
  in section 9.]
- Documentation hazard: the JWT-fields page
  (https://supabase.com/docs/guides/auth/jwt-fields) shows an example payload with
  `role: "anon"` and calls it an "anonymous" token. That example is the legacy **anon API key**
  JWT (no `sub`, has `ref`), not an anonymous **user** token. Do not build RLS on the assumption
  that anonymous users hit the `anon` role; they do not.
- `is_anonymous` is a boolean claim ("Whether the user is anonymous"). In SQL it is read with
  `(auth.jwt() ->> 'is_anonymous')::boolean` or `(auth.jwt() -> 'is_anonymous')::boolean`.
  [verified] Note the claim only changes when a new access token is issued (refresh or re-login).

### 1.4 Rate limits and abuse controls

- Anonymous sign-ins: 30 requests per hour per IP, burst 30, keyed on `/auth/v1/signup`
  without email/phone. [verified] Source: https://supabase.com/docs/guides/auth/rate-limits
- Is it configurable? The rate-limits table says "Customizable: No", but the anonymous guide
  says the limit "can be modified in your dashboard", the CLI config exposes
  `[auth.rate_limit] anonymous_users = 30` ("per hour per IP address"), and the Management API
  `UpdateAuthConfigBody` has `rate_limit_anonymous_users` (integer, min 1). Treat it as
  configurable via config.toml / Management API; the table row is stale. [verified conflict;
  configurability via API = likely]
- Token refresh: 1800/hour/IP, burst 30 (`/auth/v1/token`); CLI config
  `[auth.rate_limit] token_refresh = 150` per 5 minutes per IP; Management API
  `rate_limit_token_refresh`. [verified]
- CAPTCHA: providers `hcaptcha` and `turnstile`; dashboard path Authentication > Bot and Abuse
  Protection; config.toml:

  ```toml
  # [auth.captcha]
  # enabled = true
  # provider = "hcaptcha"   # or "turnstile"
  # secret = ""             # use env(SUPABASE_AUTH_CAPTCHA_SECRET)
  ```

  [verified from the generated config.toml; Management API fields `security_captcha_enabled`,
  `security_captcha_provider`, `security_captcha_secret`.]
  The `/signup` route is wrapped with `api.verifyCaptcha`, so a project with CAPTCHA enabled will
  reject `signInAnonymously()` from a CLI with `captcha_failed` unless a valid token is supplied.
  [verified in `internal/api/api.go`] A headless CLI cannot obtain one without a browser step, so
  Brigade's project must leave CAPTCHA off. The anonymous guide "strongly recommend[s]" CAPTCHA;
  the compensating controls are the IP limit, the join-attempt limiter (section 6) and cleanup
  (section 1.6).
  Source: https://supabase.com/docs/guides/auth/auth-captcha
- There is no built-in rate limit on PostgREST (`/rest/v1/...`) or on RPC calls; database-side
  limiting is the only option for the join RPC. [likely: no such limit is documented anywhere in
  the rate-limits page or the API docs]

### 1.5 Session and refresh-token lifetime

- Access token: default 1 hour; `jwt_expiry` in config.toml ("maximum 604,800 (1 week)");
  Management API `jwt_exp` (0..604800). Docs discourage values under 5 minutes (the client refreshes
  proactively 90 s before expiry) and over 1 hour. [verified]
  Source: https://supabase.com/docs/guides/auth/sessions
- Refresh tokens "never expire but can only be used once" (with rotation on, the default). A
  single-use token becomes a dead credential only if reused outside the rules below. [verified]
- Reuse rules, as implemented on GoTrue `master` (`internal/tokens/service.go`): [verified in
  source; docs describe the same behavior at a higher level]
  - Reuse inside `refresh_token_reuse_interval` (default 10 s) is allowed and returns the
    currently active token.
  - Reuse of the token immediately behind the active one ("parent" on the v1 path, "counter
    difference == 1" on the newer counter-based path) is allowed: the server assumes the client
    failed to save the previous response and returns the active token.
  - Any other reuse with rotation enabled terminates the **whole session** (`LogoutSession` /
    `RevokeTokenFamily`) and returns HTTP 400 with `code: "refresh_token_already_used"`
    ("Invalid Refresh Token: Already Used"). With rotation disabled the request fails but the
    session survives.
  - A token whose session no longer exists returns `refresh_token_not_found` ("Session containing
    the refresh token not found"); a session superseded by a newer login under single-session
    mode returns `session_expired`.
  - Observed live on GoTrue v2.196.0 (section 9): reuse of a two-steps-stale token 11.5 s after
    its rotation returned `400 refresh_token_already_used` and marked every token of the session
    revoked, **but** `RevokeTokenFamily` also bumps `updated_at = now()` on the revoked rows, so
    the still-current token kept working for one more reuse interval (10 s) and then failed with
    the same code. The `auth.sessions` row is not deleted; it is simply left without a usable
    token. Design consequence: a watcher that sees `refresh_token_already_used` must not assume the
    very next call will fail, and must not keep retrying either; treat the code as terminal.
  - The local v2.196.0 stack issued 12-character legacy refresh tokens (the "v1" path);
    `master` also has a counter-based token format with the same three rules
    (concurrent-within-interval, one-behind, otherwise terminate). The
    `auth.sessions.refresh_token_counter` column already exists in the local schema.
  Error codes: https://supabase.com/docs/guides/auth/debugging/error-codes
- Config keys: `enable_refresh_token_rotation = true`, `refresh_token_reuse_interval = 10`
  (config.toml); `refresh_token_rotation_enabled`, `security_refresh_token_reuse_interval`
  (Management API). [verified]
- Pro-plan session limits: time-box (`[auth.sessions] timebox`), inactivity
  (`inactivity_timeout`), single session per user (`sessions_single_per_user`). Enforcement
  happens at the next refresh, not proactively. For Brigade these would silently kill idle
  principals; leave them unset. [verified that they exist and how they are enforced]

### 1.6 Cleanup, linking and upgrading

- Cleanup SQL from the guide: `delete from auth.users where is_anonymous is true and created_at <
  now() - interval '30 days';` [verified]. For Brigade this must exclude principals that still hold
  a membership, e.g. `and id not in (select user_id from public.team_members)`, or scope it to
  `last_sign_in_at`. Deleting an `auth.users` row cascades to Brigade rows if the foreign keys are
  declared `on delete cascade` (section 5).
- Upgrade to a permanent user: `updateUser({ email })` then, after verification,
  `updateUser({ password })`; or `linkIdentity({ provider })` (OAuth), which requires manual
  linking to be enabled (`enable_manual_linking = true` in config.toml;
  `GOTRUE_SECURITY_MANUAL_LINKING_ENABLED=true` self-hosted; `security_manual_linking_enabled` in
  the Management API; otherwise `manual_linking_disabled`). The user id (`sub`) is retained, so
  memberships keyed on `auth.uid()` survive an upgrade. [verified for the calls and config; id
  retention = likely, since the operations mutate the existing user rather than creating one]
  Sources: https://supabase.com/docs/guides/auth/auth-anonymous,
  https://supabase.com/docs/guides/auth/auth-identity-linking
- Not in Brigade v1. The plan's accepted tradeoff stands: losing local credentials creates a new
  principal that must rejoin.

---

## 2. supabase-js v2 from a Node 24 CLI

### 2.1 `createClient` options for a CLI

```ts
import { createClient } from '@supabase/supabase-js'
import { fileSessionStorage } from './session-storage' // section 2.2

export function makeClient(profile: { url: string; publishableKey: string; sessionFile: string },
                           mode: 'cli' | 'watcher') {
  return createClient(profile.url, profile.publishableKey, {
    auth: {
      persistSession: true,
      storage: fileSessionStorage(profile.sessionFile),
      storageKey: 'brigade-session',      // key passed to the storage adapter
      autoRefreshToken: mode === 'watcher',
      detectSessionInUrl: false,          // browser-only feature; no window in Node anyway
      flowType: 'implicit',               // irrelevant for anonymous sign-in; PKCE is for OAuth
    },
    global: { headers: { 'X-Client-Info': 'brigade-adapter/0.1' } },
  })
}
```

Facts behind each line (all `[verified]` in `packages/core/auth-js/src/GoTrueClient.ts` and
`lib/types.ts` at supabase-js 2.112.x unless noted):

- Defaults: `autoRefreshToken: true`, `persistSession: true`, `detectSessionInUrl: true`,
  `storageKey: 'supabase.auth.token'`.
- `persistSession: true` + `storage` provided → the adapter is used as-is. Without `storage`,
  Node has no `localStorage`, so auth-js silently falls back to an in-memory adapter and the
  session is lost at process exit. `persistSession: false` always uses memory.
- The storage adapter type is `SupportedStorage`: `getItem(key) => string | null | Promise`,
  `setItem(key, value) => void | Promise<void>`, `removeItem(key) => void | Promise<void>`,
  optional `isServer?: boolean` (leave unset; setting it makes `session.user` accesses print
  "insecure" warnings intended for cookie-based SSR).
- The value stored under `storageKey` is the JSON of the `Session` object (`access_token`,
  `refresh_token`, `expires_at`, `expires_in`, `token_type`, `user`). An experimental
  `userStorage` option splits the `user` object out to `storageKey + '-user'`; not needed.
- `detectSessionInUrl` only matters when `isBrowser()` (`window` and `document` defined); it
  is inert in Node, but setting it `false` documents intent.
- `lock` option: deprecated since 2.107.0 (warned since 2.112.4) and removed in v3. The default
  is "lockless coordination": in-process refreshes are single-flighted and cross-process races are
  left to the server's reuse rules (section 1.5). Do not pass `processLock`.
  Source: `packages/core/auth-js/migrations/lockless-coordination.md`.
- `global.fetch` defaults to the runtime's `fetch`; Node 24 has it built in.
- `createClient` runs `initialize()` immediately, which calls `_recoverAndRefresh()`: it loads
  the stored session, and if the token is within 90 s of expiry **and** `autoRefreshToken` is
  true, refreshes it. Then `_handleVisibilityChange()` runs; in non-browser environments it
  unconditionally calls `startAutoRefresh()` when `autoRefreshToken` is true.

Reference: https://supabase.com/docs/reference/javascript/initializing

### 2.2 File-backed storage adapter (persist and restore the refresh token)

```ts
import { mkdirSync, readFileSync, writeFileSync, renameSync, unlinkSync, chmodSync } from 'node:fs'
import { dirname } from 'node:path'
import type { SupportedStorage } from '@supabase/supabase-js'

export function fileSessionStorage(path: string): SupportedStorage {
  return {
    getItem(key) {
      try {
        const doc = JSON.parse(readFileSync(path, 'utf8')) as Record<string, string>
        return doc[key] ?? null
      } catch { return null }
    },
    setItem(key, value) {
      mkdirSync(dirname(path), { recursive: true, mode: 0o700 })
      let doc: Record<string, string> = {}
      try { doc = JSON.parse(readFileSync(path, 'utf8')) } catch {}
      doc[key] = value
      const tmp = `${path}.${process.pid}.tmp`
      writeFileSync(tmp, JSON.stringify(doc), { mode: 0o600 })
      chmodSync(tmp, 0o600)
      renameSync(tmp, path)               // atomic replace; readers never see a torn file
    },
    removeItem(key) {
      try {
        const doc = JSON.parse(readFileSync(path, 'utf8'))
        delete doc[key]
        if (Object.keys(doc).length === 0) unlinkSync(path)
        else { const tmp = `${path}.${process.pid}.tmp`; writeFileSync(tmp, JSON.stringify(doc), { mode: 0o600 }); renameSync(tmp, path) }
      } catch {}
    },
  }
}
```

Why this shape:

- auth-js does **not** cache the session in memory between calls: `__loadSession()` (the body of
  `getSession()`), `_autoRefreshTokenTick()`, and `_callRefreshToken()` all call
  `getItemAsync(this.storage, this.storageKey)` fresh. A read-through file adapter therefore makes
  every process see the latest rotated token before it decides to refresh. [verified in source]
- `_callRefreshToken()` snapshots storage before the `/token` call and re-reads it afterwards; if
  another writer changed the refresh token in between, the rotated tokens are discarded
  (`AuthRefreshDiscardedError`) rather than clobbering the newer state. [verified] Combined with
  the server's "one step behind is fine" rule, a CLI process and the watcher can both refresh from
  the same file with no lock. The residual failure mode (a process holding a token two or more
  rotations stale and refreshing more than 10 s after the last rotation) cannot happen when every
  refresh re-reads the file first.
- Location: `$CLAUDE_PLUGIN_DATA/<profile>/session.json` (the plugin data directory lives under
  `CLAUDE_CONFIG_DIR`, which on this machine is non-default; never hardcode `~/.claude`). The
  refresh token is a long-lived bearer credential; 0600 file + 0700 directory is the minimum. OS
  keychain storage is an optional later upgrade (macOS `security`, `secret-tool`, `cmdkey`); it
  would need a spawn per read because Node has no built-in keychain API and the plugin cannot run
  native-module build scripts.
- Explicit restore APIs, if the adapter ever needs them: `auth.setSession({ access_token,
  refresh_token })` (refreshes if the access token is expired; throws on invalid tokens) and
  `auth.refreshSession({ refresh_token })` (always hits `/token`). [verified]
  Sources: https://supabase.com/docs/reference/javascript/auth-setsession,
  https://supabase.com/docs/reference/javascript/auth-refreshsession

### 2.3 `autoRefreshToken` in a short-lived CLI vs a long-lived watcher

Implementation facts [verified in `GoTrueClient.ts` and `lib/constants.ts`]:

- `_startAutoRefresh()` creates a `setInterval` of `AUTO_REFRESH_TICK_DURATION_MS = 30_000` and
  calls `ticker.unref()` when the timer object supports it, so the interval does **not** keep a
  Node process alive. It also schedules one immediate tick via `setTimeout(..., 0)` (also
  `unref()`ed).
- A tick refreshes when the access token expires within `AUTO_REFRESH_TICK_THRESHOLD = 3` ticks,
  i.e. `EXPIRY_MARGIN_MS = 90_000`. With a 1-hour token that is one `/token` call per hour per
  process.
- Independently of `autoRefreshToken`, `getSession()` refreshes on demand when the stored token
  is within 90 s of expiry (`__loadSession` → `_callRefreshToken`). So a CLI process with
  `autoRefreshToken: false` still gets a valid token for its request; it just never refreshes in
  the background.
- After a failed refresh the client caches the failure for `REFRESH_FAILURE_COOLDOWN_MS =
  60_000` for that same refresh token, so the next tick or `getSession()` does not hammer
  `/token`.
- Network failures (`AuthRetryableFetchError`) are retried with 200/400/800 ms backoff as long as
  the retries fit inside one 30 s tick; then the next tick tries again. Server rejections
  (`AuthApiError`, e.g. `refresh_token_already_used`, `refresh_token_not_found`,
  `session_expired`) are not retried. If the access token is still inside its real expiry the
  session is preserved ("proactive-preserve"); if it has actually expired the client calls
  `_removeSession()`, which **deletes the stored session** and emits `SIGNED_OUT`.
- Observed live (section 9, check B1): `setSession()` with an access token whose `exp` is in the
  past and a refresh token that the server had already revoked returned
  `refresh_token_already_used`, `getSession()` then returned `null`, and `onAuthStateChange`
  emitted `SIGNED_OUT`. The storage adapter's `removeItem` was called.
- `auth.dispose()` (added with lockless coordination) tears down the interval, listeners and
  subscribers; call it on watcher shutdown. `stopAutoRefresh()` only stops the ticker.
  Reference: https://supabase.com/docs/reference/javascript/auth-startautorefresh

Recommendation:

| Process | `autoRefreshToken` | Notes |
| --- | --- | --- |
| `describe`, `session list`, `message send`, `session register` (seconds) | `false` | On-demand refresh inside `getSession()` is enough; no timer means no unexpected `/token` calls from many short processes. |
| `message watch` / heartbeat loop (hours) | `true` (default) | Subscribe to `onAuthStateChange`; on `SIGNED_OUT` exit non-zero with the `unauthenticated` protocol error so the harness can re-run sign-in/join. Call `auth.dispose()` on SIGTERM. |

The watcher's realtime socket gets the refreshed JWT automatically: `SupabaseClient` subscribes to
`onAuthStateChange` and calls `realtime.setAuth(token)` on `SIGNED_IN`, `INITIAL_SESSION` and
`TOKEN_REFRESHED`, and `realtime-js` additionally pulls a fresh token through its `accessToken`
callback before (re)subscribing. [verified in `SupabaseClient.ts` `_handleTokenChanged` and
`RealtimeClient.ts`] The Postgres-Changes guide's advice to call `realtime.setAuth()` manually
applies when using `realtime-js` directly or a custom `accessToken` option.

### 2.4 What the CLI must do when the session is gone

`getSession()` returns `{ session: null }` (with or without an error) in three situations: no
file yet, the stored JSON failed `_isValidSession`, or a refresh was rejected after real expiry.
In all three the adapter should:

1. Sign in anonymously again (`signInAnonymously`), which creates a **new** `auth.users` row and
   therefore a new `principal_ref`.
2. Report `unauthenticated`/`not a member` to the harness; the user re-runs the join command with
   the team's join secret.
3. Never retry `/token` in a loop with the same refresh token: the server has already terminated
   the session, and the IP limit (1800/h) is shared with every other Supabase user behind the same
   NAT.

Optional mitigation to discuss: store the join secret alongside the session file (same 0600
file) so the adapter can rejoin automatically after principal loss. The file already holds a
credential (the refresh token) that grants team access, so the marginal exposure is small; the cost
is that a leaked file now also grants *future* joins after the principal is revoked. Default to
not storing it and document the manual rejoin.

### 2.5 Reading claims locally

`supabase.auth.getClaims()` verifies the access token against the project's JWKS
(`/auth/v1/.well-known/jwks.json`, cached 10 minutes) when the project uses asymmetric signing
keys (ES256/RS256), and falls back to a `getUser()` round-trip on legacy HS256 projects. It
returns the claims (`sub`, `role`, `is_anonymous`, `session_id`, ...). Use it if the watcher
needs `sub` without trusting an unverified decode. [verified]
Sources: https://supabase.com/docs/reference/javascript/auth-getclaims,
https://supabase.com/docs/guides/auth/signing-keys

---

## 3. API keys in 2026 and what the CLI ships

| Key | Format | Role when no user JWT | Role with a user JWT | Ship in CLI? |
| --- | --- | --- | --- | --- |
| Publishable `sb_publishable_...` | opaque | `anon` | `authenticated` (the user's JWT decides) | Yes |
| Legacy `anon` | long-lived JWT (`role: anon`) | `anon` | `authenticated` | Only for old projects; deprecated |
| Secret `sb_secret_...` | opaque | `service_role` (bypasses RLS) | runs as the user if a user JWT is attached | Never |
| Legacy `service_role` | long-lived JWT | `service_role` | same | Never |

[verified] Source: https://supabase.com/docs/guides/api/api-keys. Quotes: publishable keys are
"Safe to expose online: web page, mobile or desktop app, GitHub actions, CLIs, source code."
Legacy keys "will be deprecated by the end of 2026, and you should now use the publishable
(`sb_publishable_xxx`) and secret (`sb_secret_xxx`) keys instead". A secret key "bypasses RLS only
when the request carries no user access token."

How supabase-js uses the key: it is sent as the `apikey` header on every request; when there is
no session, `_getAccessToken()` falls back to the same key for `Authorization: Bearer`, which is
the one case the gateway accepts a publishable key in that header. Once signed in, the user's JWT
goes in `Authorization` and the publishable key stays in `apikey`. [verified in
`SupabaseClient.ts`; gateway rule from the API-keys guide]

Local development: `supabase start` / `supabase status -o json` on CLI 2.116.0 prints
`PUBLISHABLE_KEY` (`sb_publishable_...`) and `SECRET_KEY` (`sb_secret_...`) alongside the legacy
`ANON_KEY`/`SERVICE_ROLE_KEY` JWTs and `JWT_SECRET` [verified today]. The publishable key worked
for anonymous sign-in, PostgREST, RPC and Realtime against the local stack. The adapter should
accept whatever string is configured and not try to parse it.

Signing keys: hosted projects now use asymmetric JWT signing keys with a JWKS endpoint; the legacy
JWT secret is what the legacy `anon`/`service_role` keys were minted with, which is why they are
being retired. Nothing in Brigade needs the JWT secret. [verified]

---

## 4. Which Postgres role, and how RLS should distinguish

- Anonymous users execute as role `authenticated`, like every signed-in user. `auth.uid()`
  returns their `sub`; `auth.jwt()` returns the full claims JSON. [verified]
- Policies must therefore target `to authenticated`, and the *membership table* is what separates
  teams. `is_anonymous` is irrelevant to Brigade's authorization; keep it available for a future
  "permanent users only" rule using the documented **restrictive** pattern:

  ```sql
  -- Only if a future feature needs it: block anonymous principals from an action.
  create policy "permanent users only"
  on public.some_table as restrictive for insert
  to authenticated
  with check ( (select (auth.jwt() ->> 'is_anonymous')::boolean) is false );
  ```

  The guide stresses `as restrictive` so the rule ANDs with the permissive membership policies
  instead of ORing with them. [verified] Source:
  https://supabase.com/docs/guides/auth/auth-anonymous#access-control
- `auth.uid()` returns `null` for `anon`-role requests; `null = user_id` is never true, so
  policies that compare to `auth.uid()` are safe, but the docs recommend targeting
  `to authenticated` explicitly so the policy body is not even evaluated for `anon`. [verified]
  Source: https://supabase.com/docs/guides/database/postgres/row-level-security
- Performance rules from the RLS guide that the sketch below follows: wrap `auth.uid()` and
  security-definer helpers in `(select ...)` so they are evaluated once per statement; index every
  column a policy filters on; remember that a composite primary key `(team_id, user_id)` only
  indexes `team_id`, so add a separate index on `user_id`; break policy recursion with
  `security definer` functions in a private schema with `set search_path = ''`. [verified]

---

## 5. Schema and RLS sketch for teams, sessions, messages

Goal: a principal (auth user) is a member of zero or more teams; sessions belong to one team and
one owner; messages are addressed to a session in the same team; a member of team A can neither
list nor message a session in team B. Brigade v1 binds one adapter profile to one team, but the
schema allows one principal in several teams so a second profile on the same machine can reuse the
same anonymous user if desired.

The SQL below is exactly what was applied and tested today (`supabase-auth-rls.schema.sql`). It
contains the tables, the `security definer` helper, RLS policies, column grants, the join RPCs
(section 6) and the stamping triggers (section 7).

```sql
create extension if not exists pgcrypto with schema extensions;
create schema if not exists private;
revoke all on schema private from public;
grant usage on schema private to authenticated;

create table public.teams (
  id               uuid primary key default gen_random_uuid(),
  name             text not null check (length(name) between 1 and 80),
  join_secret_hash text not null,
  secret_version   integer not null default 1,
  created_by       uuid references auth.users(id) on delete set null,
  created_at       timestamptz not null default now()
);
create table public.team_members (
  team_id     uuid not null references public.teams(id) on delete cascade,
  user_id     uuid not null default auth.uid() references auth.users(id) on delete cascade,
  human_label text check (length(human_label) <= 120),
  joined_at   timestamptz not null default now(),
  primary key (team_id, user_id)
);
create index team_members_user_id_idx on public.team_members (user_id);
create table public.sessions (
  id             uuid primary key default gen_random_uuid(),
  team_id        uuid not null references public.teams(id) on delete cascade,
  owner_user_id  uuid not null default auth.uid() references auth.users(id) on delete cascade,
  name           text not null check (length(name) between 1 and 64),
  description    text check (length(description) <= 500),
  state          text not null default 'active' check (state in ('active','idle','offline')),
  harness        text, harness_version text,
  last_seen_at   timestamptz not null default now(),
  lease_until    timestamptz not null default now() + interval '5 minutes',
  created_at     timestamptz not null default now()
);
create index sessions_team_id_idx on public.sessions (team_id);
create index sessions_owner_idx   on public.sessions (owner_user_id);
create table public.messages (
  id                    uuid primary key default gen_random_uuid(),
  team_id               uuid not null references public.teams(id) on delete cascade,
  sender_user_id        uuid not null default auth.uid() references auth.users(id) on delete cascade,
  sender_session_id     uuid not null references public.sessions(id) on delete cascade,
  recipient_session_id  uuid not null references public.sessions(id) on delete cascade,
  kind                  text not null default 'text' check (kind = 'text'),
  summary               text check (length(summary) <= 200),
  body                  text not null check (length(body) <= 16384),
  reply_to              uuid references public.messages(id) on delete set null,
  idempotency_key       text not null,
  created_at            timestamptz not null default now(),
  acked_at              timestamptz,
  unique (sender_session_id, idempotency_key)
);
create index messages_recipient_created_idx on public.messages (recipient_session_id, created_at);
create index messages_team_idx on public.messages (team_id);

create or replace function private.my_team_ids()
returns setof uuid language sql security definer stable set search_path = ''
as $$ select team_id from public.team_members where user_id = (select auth.uid()) $$;
revoke execute on function private.my_team_ids() from public;
grant execute on function private.my_team_ids() to authenticated;

alter table public.teams        enable row level security;
alter table public.team_members enable row level security;
alter table public.sessions     enable row level security;
alter table public.messages     enable row level security;
revoke all on all tables in schema public from anon;

create policy teams_select on public.teams for select to authenticated
  using ( id in (select private.my_team_ids()) );
create policy members_select on public.team_members for select to authenticated
  using ( team_id in (select private.my_team_ids()) );
create policy members_delete_self on public.team_members for delete to authenticated
  using ( user_id = (select auth.uid()) );
create policy sessions_select on public.sessions for select to authenticated
  using ( team_id in (select private.my_team_ids()) );
create policy sessions_insert on public.sessions for insert to authenticated
  with check ( owner_user_id = (select auth.uid()) and team_id in (select private.my_team_ids()) );
create policy sessions_update on public.sessions for update to authenticated
  using ( owner_user_id = (select auth.uid()) )
  with check ( owner_user_id = (select auth.uid()) and team_id in (select private.my_team_ids()) );
create policy sessions_delete on public.sessions for delete to authenticated
  using ( owner_user_id = (select auth.uid()) );
create policy messages_select on public.messages for select to authenticated
  using (
    messages.sender_user_id = (select auth.uid())
    or exists (select 1 from public.sessions s
               where s.id = messages.recipient_session_id and s.owner_user_id = (select auth.uid()))
  );
create policy messages_insert on public.messages for insert to authenticated
  with check (
    messages.sender_user_id = (select auth.uid())
    and exists (select 1 from public.sessions ss
                where ss.id = messages.sender_session_id and ss.owner_user_id = (select auth.uid())
                  and ss.team_id = messages.team_id)
    and exists (select 1 from public.sessions rs
                where rs.id = messages.recipient_session_id and rs.team_id = messages.team_id)
  );
create policy messages_update_ack on public.messages for update to authenticated
  using ( exists (select 1 from public.sessions s
                  where s.id = messages.recipient_session_id and s.owner_user_id = (select auth.uid())) )
  with check ( true );

revoke all on public.teams, public.team_members, public.sessions, public.messages from authenticated;
grant select (id, name, secret_version, created_at) on public.teams to authenticated;
grant select, delete on public.team_members to authenticated;
grant select on public.sessions to authenticated;
grant insert (team_id, name, description, state, harness, harness_version) on public.sessions to authenticated;
grant update (name, description, state, harness, harness_version, last_seen_at, lease_until) on public.sessions to authenticated;
grant delete on public.sessions to authenticated;
grant select on public.messages to authenticated;
grant insert (sender_session_id, recipient_session_id, kind, summary, body, reply_to, idempotency_key) on public.messages to authenticated;
grant update (acked_at) on public.messages to authenticated;

-- join attempts + RPCs
create table public.team_join_attempts (
  id           bigint generated always as identity primary key,
  user_id      uuid not null,
  team_id      uuid,
  attempted_at timestamptz not null default now(),
  succeeded    boolean not null default false
);
create index team_join_attempts_user_time_idx on public.team_join_attempts (user_id, attempted_at desc);
alter table public.team_join_attempts enable row level security;
revoke all on public.team_join_attempts from anon, authenticated;

create or replace function public.create_team(p_name text, p_join_secret text, p_human_label text default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_id uuid;
begin
  if v_uid is null then raise exception 'unauthenticated' using errcode = '28000'; end if;
  if p_join_secret is null or octet_length(p_join_secret) < 16 or octet_length(p_join_secret) > 72 then
    raise exception 'join secret must be 16..72 bytes' using errcode = '22023';
  end if;
  insert into public.teams (name, join_secret_hash, created_by)
  values (p_name, extensions.crypt(p_join_secret, extensions.gen_salt('bf', 10)), v_uid)
  returning id into v_id;
  insert into public.team_members (team_id, user_id, human_label) values (v_id, v_uid, p_human_label);
  return jsonb_build_object('status', 'created', 'team_id', v_id, 'team_name', p_name);
end $$;

create or replace function public.join_team(p_team_id uuid, p_join_secret text, p_human_label text default null)
returns jsonb language plpgsql security definer set search_path = ''
as $$
declare
  v_uid uuid := auth.uid(); v_team public.teams%rowtype;
  v_window interval := interval '15 minutes'; v_max integer := 5;
  v_failures integer; v_oldest timestamptz;
begin
  if v_uid is null then raise exception 'unauthenticated' using errcode = '28000'; end if;
  if p_join_secret is null or octet_length(p_join_secret) > 72 then
    return jsonb_build_object('status', 'invalid_input');
  end if;
  select count(*), min(attempted_at) into v_failures, v_oldest
  from public.team_join_attempts
  where user_id = v_uid and not succeeded and attempted_at > now() - v_window;
  if v_failures >= v_max then
    return jsonb_build_object('status', 'rate_limited',
      'retry_after_seconds', greatest(1, extract(epoch from (v_oldest + v_window - now()))::integer));
  end if;
  select * into v_team from public.teams where id = p_team_id;
  if not found or v_team.join_secret_hash <> extensions.crypt(p_join_secret, v_team.join_secret_hash) then
    insert into public.team_join_attempts (user_id, team_id) values (v_uid, p_team_id);
    return jsonb_build_object('status', 'invalid_secret');
  end if;
  insert into public.team_members (team_id, user_id, human_label)
  values (v_team.id, v_uid, p_human_label)
  on conflict (team_id, user_id) do update set human_label = coalesce(excluded.human_label, public.team_members.human_label);
  insert into public.team_join_attempts (user_id, team_id, succeeded) values (v_uid, v_team.id, true);
  delete from public.team_join_attempts where user_id = v_uid and not succeeded;
  return jsonb_build_object('status', 'joined', 'team_id', v_team.id, 'team_name', v_team.name);
end $$;
revoke execute on function public.create_team(text, text, text) from public, anon;
revoke execute on function public.join_team(uuid, text, text) from public, anon;
grant execute on function public.create_team(text, text, text) to authenticated;
grant execute on function public.join_team(uuid, text, text) to authenticated;

-- stamping triggers
create or replace function private.stamp_message()
returns trigger language plpgsql security definer set search_path = ''
as $$
declare v_uid uuid := auth.uid(); v_team uuid;
begin
  if v_uid is null then raise exception 'unauthenticated' using errcode = '28000'; end if;
  new.sender_user_id := v_uid;
  new.created_at := now();
  new.acked_at := null;
  select team_id into v_team from public.sessions where id = new.sender_session_id and owner_user_id = v_uid;
  if v_team is null then raise exception 'sender_session_id is not a session you own' using errcode = '42501'; end if;
  new.team_id := v_team;
  if not exists (select 1 from public.sessions where id = new.recipient_session_id and team_id = v_team) then
    raise exception 'recipient session not found in your team' using errcode = '42501';
  end if;
  return new;
end $$;
create trigger messages_stamp before insert on public.messages for each row execute function private.stamp_message();

create or replace function private.stamp_session()
returns trigger language plpgsql security definer set search_path = ''
as $$
begin
  if auth.uid() is null then raise exception 'unauthenticated' using errcode = '28000'; end if;
  new.owner_user_id := auth.uid();
  if tg_op = 'INSERT' then new.created_at := now(); end if;
  if not exists (select 1 from public.team_members where team_id = new.team_id and user_id = new.owner_user_id) then
    raise exception 'not a member of that team' using errcode = '42501';
  end if;
  return new;
end $$;
create trigger sessions_stamp before insert or update on public.sessions for each row execute function private.stamp_session();
alter publication supabase_realtime add table public.messages;
```

Notes on the schema and policies:

- Two mistakes were caught by testing the first draft and are worth remembering when writing
  policies: (1) inside a policy subquery an unqualified column such as `team_id` binds to the
  *subquery's* table, so `where ss.team_id = team_id` is always true; qualify with the policy's
  table name (`messages.team_id`). (2) PL/pgSQL `returns table (team_id ...)` OUT parameters
  collide with column names used in the body (`on conflict (team_id, user_id)`); returning
  `jsonb` sidesteps it.
- The `messages_insert` policy reads `public.sessions` as the caller, so `sessions_select` applies
  inside it: a recipient in another team is invisible, the `exists` fails, and the insert is
  rejected. `sessions_select` only calls the security-definer helper, so there is no policy
  cycle. [verified live]
- Cross-team isolation rests entirely on `private.my_team_ids()`. `set search_path = ''` plus
  fully-qualified names is mandatory for a `security definer` function (docs: "you must set the
  search_path"). [verified] Source: https://supabase.com/docs/guides/database/functions
- `revoke all ... from anon` plus `to authenticated` means a request carrying only the
  publishable key gets `42501 permission denied for table sessions` and
  `permission denied for function join_team`. [verified live]
- The client never sends `team_id` on `messages`: it is not in the insert grant and the
  `BEFORE INSERT` trigger derives it from the sender session. `not null` is checked after
  `BEFORE` row triggers, so the insert succeeds with the column omitted. [verified live]
- Column-level privileges are documented as "an advanced feature"; the caveat is that a role
  without full `select` cannot use `select *`. `authenticated` keeps full `select` on every table
  except `teams` (no `join_secret_hash`), so the adapter must name columns when reading `teams`;
  `select('*')` on `teams` returns `42501`. [verified live] Source:
  https://supabase.com/docs/guides/database/postgres/column-level-security
- Indexes follow the RLS guide: `team_members(user_id)` (the primary key only indexes
  `team_id`), `sessions(team_id)`, `sessions(owner_user_id)`,
  `messages(recipient_session_id, created_at)`.
- Realtime: a watcher subscribing to Postgres Changes on `public.messages` (filtered by
  `recipient_session_id=eq.<id>` or unfiltered) is authorized per event against
  `messages_select` using the subscriber's JWT; an anonymous user received the stamped row with
  `sender_user_id` set to the sender. `DELETE` events are not RLS-filtered (only the primary key
  is sent). The table must be in the `supabase_realtime` publication. Per-event per-subscriber
  authorization is fine at Brigade's scale; Broadcast-from-trigger is the escape hatch above
  thousands of subscribers. [verified live + docs] Sources:
  https://supabase.com/docs/guides/realtime/postgres-changes,
  https://supabase.com/docs/guides/realtime/authorization

---

## 6. `join_team` RPC: bcrypt via pgcrypto, rate-limited

### 6.1 Facts

- pgcrypto `crypt(password, salt)` and `gen_salt(type [, iter_count])`; `bf` (Blowfish, bcrypt
  "2a") has a 72-byte password limit, 128-bit salt, cost 4..31 (default 6). Verification is
  `crypt(entered, stored) = stored`. Argon2 is **not** in pgcrypto (only `bf`, `md5`, `xdes`,
  `des`, `sha256crypt`, `sha512crypt`). [verified] Source:
  https://www.postgresql.org/docs/current/pgcrypto.html
- On Supabase, pgcrypto is pre-installed in the `extensions` schema (`create extension ...
  with schema extensions` reports "already exists"), so inside a `search_path = ''` function the
  calls are `extensions.crypt(...)` and `extensions.gen_salt(...)`. [verified live]
- A join secret generated by the CLI as 32 random bytes in base64url is 43 characters, well
  under the 72-byte bcrypt limit; the RPC rejects inputs over 72 bytes so truncation is
  impossible.
- PostgREST executes each request in one transaction. If the function `RAISE`s, everything it
  wrote (including a "failed attempt" row) is rolled back. The limiter therefore only works if
  the function **returns** a status for wrong secrets instead of raising. [verified live: five
  `invalid_secret` returns were counted and the sixth call returned `rate_limited` with
  `retry_after_seconds: 900`; the correct secret was also refused while limited]
- There is no rate limit on PostgREST/RPC at the platform level, so this table is the only brake
  on online guessing apart from bcrypt's cost. [likely]

### 6.2 Behavior of the functions in the schema above

`create_team(p_name, p_join_secret, p_human_label)` → `{"status":"created","team_id":...,
"team_name":...}`; the caller becomes the first member; only the bcrypt hash (cost 10) is stored.

`join_team(p_team_id, p_join_secret, p_human_label)` → one of:

| `status` | Meaning | Adapter exit code |
| --- | --- | --- |
| `joined` | membership row created or label updated; failed-attempt rows for this principal cleared | 0 |
| `invalid_secret` | wrong secret **or** unknown team id (same answer, no enumeration); attempt row committed | unauthorized |
| `rate_limited` | ≥ 5 failed attempts in the last 15 minutes for this `auth.uid()`; `retry_after_seconds` included | rate limited |
| `invalid_input` | null or > 72-byte secret | invalid input |

Anonymous (no-session) callers get `42501 permission denied for function join_team` because
`execute` is revoked from `public`/`anon` and granted to `authenticated` only. [verified live]

Design points:

- Team lookup is by opaque `uuid`, so the "join code" the user types can be `<team_id>.<secret>`
  as one string.
- bcrypt cost 10 is tens of milliseconds per verify; that is itself a brake on guessing and is
  why the limiter can be generous (5 failures / 15 minutes / principal). Keying on `auth.uid()`
  is right because a headless attacker's only way to get more budget is to mint more anonymous
  users, which the 30/hour/IP sign-in limit bounds.
- The join secret travels in the RPC body over TLS, never on the command line (the CLI should
  read it from stdin or a prompt).
- Rotation (v2): `rotate_team_secret(team_id, new_secret)` restricted to `created_by`, bumping
  `secret_version`. Per the plan, rotation does not revoke existing memberships unless a separate
  `remove_member` RPC is added.
- Housekeeping: `delete from public.team_join_attempts where attempted_at < now() - interval
  '1 day'` from pg_cron; never from the client.
- Functions in `public` are exposed as RPC by default and callable by every role unless revoked.
  The docs recommend `alter default privileges in schema public revoke execute on functions from
  public, anon, authenticated;` once, then granting per function. [verified] Source:
  https://supabase.com/docs/guides/database/functions

---

## 7. Database stamps sender identity; client-supplied sender fields are rejected

Four layers, cheapest first. Any one of them is sufficient against an honest client; together they
survive a future mistake in one layer. All four are in the schema above and all were exercised.

1. **Defaults**: `sender_user_id uuid not null default auth.uid()`, `owner_user_id ... default
   auth.uid()`, `created_at default now()`. `auth.uid()` is
   `coalesce(nullif(current_setting('request.jwt.claim.sub', true), ''),
   (nullif(current_setting('request.jwt.claims', true), '')::jsonb ->> 'sub'))::uuid`
   [verified live], so it works in column defaults and inside `security definer` bodies.
2. **Column privileges**: the insert grants omit `sender_user_id`, `owner_user_id`, `id`,
   `created_at`, `acked_at`. A request whose JSON names `sender_user_id` (or `created_at`, or
   `owner_user_id` on `sessions`) fails with `42501 permission denied for table messages` before
   any row is written. [verified live]
3. **Triggers** (`private.stamp_message`, `private.stamp_session`): overwrite rather than trust.
   `sender_user_id`/`owner_user_id` are set to `auth.uid()`, `created_at` to `now()`, `team_id`
   is derived from the sender's own session, and the function raises `42501` with a clear message
   when `sender_session_id` is not owned by the caller ("sender_session_id is not a session you
   own"), when the recipient is not in the caller's team ("recipient session not found in your
   team"), or when a session is registered in a team the caller has not joined ("not a member of
   that team"). [verified live] `security definer` lets the trigger read `public.sessions`
   without the caller's RLS, but everything is keyed on `auth.uid()`, which comes from the
   verified JWT.
4. **Policies** (`with check (messages.sender_user_id = (select auth.uid()) ...)`): evaluated
   after the trigger has rewritten `new`, so they pass for honest requests and still hold if a
   trigger is ever dropped.

Related write paths: `session heartbeat`/`close` are `update`/`delete` on `sessions` guarded by
`owner_user_id = auth.uid()` (another member's update affects 0 rows); `message ack` is an update
restricted to the `acked_at` column and to the recipient session's owner (the sender's ack attempt
affects 0 rows; the recipient's attempt to edit `body` is `42501`). A duplicate
`(sender_session_id, idempotency_key)` is `23505`, which the adapter maps to "same logical
message". [all verified live]

---

## 8. Open items and recommendations for the adapter implementation

- Pin `@supabase/supabase-js` to `^2.112` and revisit before `3.x` (currently `3.0.0-next.*`);
  v3 removes the `lock` option, which Brigade does not use, and raises the TypeScript floor.
- The plugin cannot run npm lifecycle scripts, so supabase-js's pure-JS dependency tree is fine;
  no native keychain module.
- `enable_signup` and `enable_anonymous_sign_ins` must both be on; CAPTCHA off; no session
  time-box/inactivity; refresh-token rotation on with the default 10 s reuse interval.
- Every adapter command that touches Supabase should call `getSession()` first and map
  `session: null` to the protocol's `unauthenticated` exit code; the watcher should map
  `SIGNED_OUT` from `onAuthStateChange` the same way and exit so the harness can restart the
  sign-in/join flow.
- Use `getClaims()` (not a hand-rolled base64 decode) when the adapter needs its own `sub`.
- Write RLS tests with pgTAP or a two-user script (as in section 9) before the vertical proof;
  the cross-team negative test is the one that matters.

---

## 9. Live verification log (2026-08-30, local stack)

Stack: `npx supabase@2.116.0 start` with `enable_anonymous_sign_ins = true` in config.toml
(Postgres 17.6, GoTrue v2.196.0, PostgREST, Kong, Realtime v2.129.3; other services excluded).
Client: `@supabase/supabase-js@2.112.4`, Node 24.16.0, publishable key only. Scripts:
`supabase-auth-rls.test.mjs` (34 checks) and `supabase-auth-rls.test3.mjs` (follow-ups).

| # | Check | Result |
| --- | --- | --- |
| 1 | `signInAnonymously()` for four clients | pass; 12-char refresh tokens |
| 2 | JWT `role` | `authenticated` |
| 3 | JWT `is_anonymous` | `true` (boolean) |
| 4 | JWT claims present | `aal, amr, app_metadata, aud=authenticated, email, exp, iat, is_anonymous, iss, phone, role, session_id, sub, user_metadata` |
| 5 | `create_team` via RPC as anonymous users | pass |
| 6 | `join_team` wrong secret ×2 → `invalid_secret`; correct → `joined`; unknown team id → `invalid_secret` | pass |
| 7 | Rate limiter: 5 × `invalid_secret`, 6th → `rate_limited (900s)`; correct secret refused while limited | pass |
| 8 | Publishable key with no session: RPC and `select` | `42501` both |
| 9 | Session register; `owner_user_id` stamped; two sessions with the same name coexist | pass |
| 10 | Client names `owner_user_id` on insert | `42501` |
| 11 | Register a session in a team not joined | `42501 not a member of that team` |
| 12 | Member of alpha lists sessions | sees alpha's two, not bravo's |
| 13 | Member of bravo lists sessions | sees only its own |
| 14 | `teams.select('*')` / `select('id,name,secret_version')` | `42501` / one row (own team) |
| 15 | Heartbeat another member's session | 0 rows |
| 16 | Roster with unverified `human_label` | pass |
| 17 | Message insert with `team_id` omitted | accepted; `sender_user_id` = caller, `team_id` derived |
| 18 | Duplicate idempotency key | `23505` |
| 19 | Client-supplied `sender_user_id` | `42501` |
| 20 | `sender_session_id` not owned | `42501 sender_session_id is not a session you own` |
| 21 | Recipient in another team | `42501 recipient session not found in your team` |
| 22 | Client-supplied `created_at` | `42501` |
| 23 | Recipient reads inbox; other team sees nothing | pass |
| 24 | Ack by sender / by recipient / recipient edits body | 0 rows / 1 row / `42501` |
| 25 | File storage adapter: written 0600, restored by a second client with no network call | pass; stored keys `access_token, token_type, expires_in, expires_at, refresh_token, user` |
| 26 | Refresh rotates the token | pass |
| 27 | Reuse of the previous token within 10 s | allowed, returns the active token |
| 28 | Reuse of the one-behind token after 12 s | allowed ("fail-to-save" rule) |
| 29 | Reuse of the two-behind token after 11.5 s | `400 refresh_token_already_used`; all tokens marked revoked |
| 30 | Active token used < 10 s after (29) | still accepted (grace, `updated_at` bumped) |
| 31 | Active token used 11 s after (29) | `400 refresh_token_already_used`; session dead; `auth.sessions` row remains |
| 32 | `setSession(expired access token, dead refresh token)` | error `refresh_token_already_used`; `getSession()` → `null`; `SIGNED_OUT` emitted |
| 33 | Realtime `postgres_changes` INSERT on `messages`, anonymous subscriber, filter `recipient_session_id=eq.<SB>` | delivered; `realtime.subscription` row shows `claims_role=authenticated`, `is_anonymous=true` |
| 34 | Same, unfiltered | delivered (RLS restricts to visible rows) |

One first-run realtime check did not receive an event within 10 s even though the channel reported
`SUBSCRIBED`; the rerun (33/34) delivered within a second. The cause was not isolated
[uncertain; most likely the subscription registration lagging the `SUBSCRIBED` reply]. The plan
already requires the watcher to run a cursor-based catch-up query after subscribing, which covers
this.

Not verified live (docs/source only): hosted-dashboard paths, the 30/hour anonymous IP limit, the
Management API fields, CAPTCHA enforcement on `/signup`, Pro-plan session limits, `getClaims()`
against asymmetric keys (the local stack uses the legacy HS256 secret).
