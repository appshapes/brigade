#!/bin/sh
# usage: scripts/ci/keepalive.sh            (no arguments; the working directory does not matter)
#        BRIGADE_SUPABASE_URL and BRIGADE_SUPABASE_PUBLISHABLE_KEY name the hosted project.
#
# Keeps the hosted Supabase project out of the Free plan's inactivity pause (plan P5-0). Supabase "pauses Free
# Plan projects that show low activity over a 7-day period" and says that "typically a few user requests to
# the database each day over the previous week is enough to keep the project from being paused"
# (https://supabase.com/docs/guides/platform/free-project-pausing, read 2026-09-04). The requests must reach
# the DATABASE, so a health probe alone would not count: this script climbs four rungs and prints one line
# for each, then a summary.
#
#   0. configuration   BRIGADE_SUPABASE_URL + BRIGADE_SUPABASE_PUBLISHABLE_KEY, both repository VARIABLES and
#                      both public by design. Neither set: a `::notice::` and exit 0 -- a fork, or a
#                      repository whose administrator has not set them yet (docs/setup.md). Exactly one set:
#                      exit 1, because a half-configured repository is a misconfiguration, not a fork.
#   1. reachability    GET  /auth/v1/health               200 expected, with retries so a transient blip does
#                      not page anyone. Anything else is THE ALERT: paused, deleted or unreachable. Exit 1.
#   2. database write  POST /auth/v1/signup               200 expected. An anonymous sign-up inserts into
#                      auth.users (plus the session and refresh-token rows), which is the activity Supabase
#                      counts. The body is byte-identical to the adapter's (gotrue.go, signUpAnonymous).
#   3. Data API        POST /rest/v1/rpc/my_team_ids      200 expected -- brigade.my_team_ids() is `security
#                      definer` and granted to `authenticated` (supabase/migrations/2026...brigade_schema.sql),
#                      so it answers an empty set for a principal with no membership. A 404 is a `::warning::`
#                      and not a failure: applying the migrations to the hosted project is P5-1's job, and
#                      rung 2 has already generated the day's activity.
#   4. sign-out        POST /auth/v1/logout?scope=global  204 expected; anything else is a `::warning::`,
#                      never a failure. The one anonymous user a day is deliberate: P5-3's gc removes it.
#
# Credentials: the URL and the publishable key are the two values every member of a team receives and are not
# secret (plugin/README.md, "Administrator: create a team"); the service-role key, the secret key and the
# personal access token are never here. The ACCESS TOKEN rung 2 mints is a bearer credential for the rest of
# the run, so it and the key reach curl through 0600 header files (`-H @<file>`) and never on argv -- a
# runner's `ps` and the Actions debug log both see argv (CLAUDE.md). No response body is ever printed: rung
# 2's carries the token, and the log should not train anyone to paste the key around either.
#
# Output: every line goes to stdout, the annotations (`::error::`, `::warning::`, `::notice::`) included, so
# that GitHub renders them on the run page; the sibling gates print their human summary to stdout as well.
set -eu

# 0600 for the header files below and 0700 for the temporary directory, set before anything is created.
umask 077

say()  { printf 'keepalive: %s\n' "$1"; }
note() { printf '::notice::keepalive: %s\n' "$1"; }
warn() { printf '::warning::keepalive: %s\n' "$1"; }
die()  { printf '::error::keepalive: %s\n' "$1"; exit 1; }

# ---- 0. configuration -----------------------------------------------------------------------------------
url=${BRIGADE_SUPABASE_URL:-}
key=${BRIGADE_SUPABASE_PUBLISHABLE_KEY:-}
unset BRIGADE_SUPABASE_PUBLISHABLE_KEY   # curl is spawned below; nothing it runs needs the key in its environment

nothing_to_do='BRIGADE_SUPABASE_URL and BRIGADE_SUPABASE_PUBLISHABLE_KEY are not set; nothing to do (a fork, or the variables are not configured yet)'
if [ -z "$url" ] && [ -z "$key" ]; then
  say "$nothing_to_do"
  note "$nothing_to_do"
  exit 0
fi
[ -n "$url" ] || die 'BRIGADE_SUPABASE_PUBLISHABLE_KEY is set but BRIGADE_SUPABASE_URL is not: a half-configured repository is a misconfiguration, not a fork -- set both or neither (docs/setup.md)'
[ -n "$key" ] || die 'BRIGADE_SUPABASE_URL is set but BRIGADE_SUPABASE_PUBLISHABLE_KEY is not: a half-configured repository is a misconfiguration, not a fork -- set both or neither (docs/setup.md)'

url=${url%/}                             # one trailing slash is the likeliest way for the variable to be pasted wrong
# The https-only rule of 5.2, the same one the adapter enforces on a profile's backend url (profile.go,
# checkBackendURL): http would put the anonymous JWT on the wire in clear. Loopback is the exception the
# local stack and this script's own tests run on.
case $url in
  https://*) ;;
  http://127.0.0.1|http://127.0.0.1:*|http://127.0.0.1/*|http://localhost|http://localhost:*|http://localhost/*) ;;
  *) die "BRIGADE_SUPABASE_URL must use https (http is allowed only for 127.0.0.1/localhost): $url" ;;
esac
host=${url#*://}
host=${host%%/*}

for tool in curl jq; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not installed, and the keep-alive needs it (the Ubuntu runners preinstall both)"
done

tmp=$(mktemp -d) || die 'cannot create a temporary directory'
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
body=$tmp/response.json                  # every response body lands here and is read by jq, never printed
hdr=$tmp/headers.apikey                  # 0600: apikey only
hdr_auth=$tmp/headers.bearer             # 0600: apikey + the access token rung 2 mints
printf 'apikey: %s\n' "$key" > "$hdr"

# curl_status <output-file> <curl argument>... -> prints the HTTP status code on stdout.
# Every request goes through here so the four flags that must never be forgotten are written once: -sS (no
# progress meter, but a transport failure still reaches stderr), --max-time 30, -o (so no body reaches the
# log) and -w '%{http_code}'. A transport failure prints 000, which no rung accepts.
curl_status() {
  out=$1
  shift
  curl -sS --max-time 30 -o "$out" -w '%{http_code}' "$@"
}

# ---- 1. reachability ------------------------------------------------------------------------------------
# --retry covers curl's transient set (408, 429, 500, 502, 503, 504 and a timeout) and --retry-all-errors
# adds the connection-level failures; a project that is genuinely paused answers something else and fails at
# once, which is the point of this rung.
health=$(curl_status "$body" --retry 3 --retry-delay 10 --retry-all-errors -H @"$hdr" "$url/auth/v1/health") || true
[ "$health" = 200 ] || die "the project at $host did not answer /auth/v1/health (HTTP $health): paused, deleted or unreachable -- see docs/setup.md"
say "health ok (HTTP 200): $host answers"

# ---- 2. the database write ------------------------------------------------------------------------------
signup_body='{"data":{},"gotrue_meta_security":{}}'   # byte-identical to gotrue.go's signUpAnonymous (5.1)
signup=$(curl_status "$body" -H @"$hdr" -H 'Content-Type: application/json' --data "$signup_body" "$url/auth/v1/signup") || true
if [ "$signup" != 200 ]; then
  # GoTrue's error_code and msg carry no credential, so they are the only two fields quoted back.
  error_code=$(jq -r '.error_code // .code // empty' < "$body" 2>/dev/null) || error_code=''
  msg=$(jq -r '.msg // .message // empty' < "$body" 2>/dev/null) || msg=''
  case "$error_code $msg" in
    *anonymous_provider_disabled*|*signup_disabled*|*'nonymous sign-ins are disabled'*|*'ignups not allowed'*)
      die "anonymous sign-up refused (HTTP $signup, $error_code): $msg -- turn the setting on in the dashboard: Authentication -> Sign In / Providers -> Allow anonymous sign-ins (plugin/README.md, Administrator)" ;;
  esac
  die "anonymous sign-up failed (HTTP $signup): error_code=$error_code msg=$msg -- see docs/setup.md"
fi
token=$(jq -r '.access_token // empty' < "$body" 2>/dev/null) || token=''
[ -n "$token" ] || die 'the anonymous sign-up answered HTTP 200 without an access_token: the response is not a GoTrue session'
{ printf 'apikey: %s\n' "$key"; printf 'Authorization: Bearer %s\n' "$token"; } > "$hdr_auth"
unset token                              # from here on the token exists only in the 0600 header file
say 'anonymous sign-up ok (HTTP 200): one auth.users row -- database activity generated'

# ---- 3. the Data API ------------------------------------------------------------------------------------
# Both profile headers: Content-Profile alone suffices for POST /rpc/*, but without Accept-Profile PostgREST
# looks for public.<fn> and answers 404 PGRST202, which this rung would misread as "migrations not applied".
rpc_fn=my_team_ids
rpc=$(curl_status "$body" -H @"$hdr_auth" -H 'Accept-Profile: brigade' -H 'Content-Profile: brigade' -H 'Content-Type: application/json' --data '{}' "$url/rest/v1/rpc/$rpc_fn") || true
case $rpc in
  200) say "rpc ok (HTTP 200): brigade.$rpc_fn() answered -- the brigade schema is exposed and the migrations are applied" ;;
  404) warn "the brigade schema or $rpc_fn() is not on this project yet (P5-1 applies the migrations); the sign-up above already counted as activity" ;;
  *)   pg_code=$(jq -r '.code // empty' < "$body" 2>/dev/null) || pg_code=''
       pg_message=$(jq -r '.message // empty' < "$body" 2>/dev/null) || pg_message=''
       die "the Data API refused POST /rest/v1/rpc/$rpc_fn (HTTP $rpc): code=$pg_code message=$pg_message" ;;
esac

# ---- 4. sign-out ----------------------------------------------------------------------------------------
logout=$(curl_status "$body" -X POST -H @"$hdr_auth" "$url/auth/v1/logout?scope=global") || true
case $logout in
  204) say 'sign-out ok (HTTP 204): the daily anonymous session is revoked' ;;
  *)   warn "the global sign-out answered HTTP $logout, not 204: the activity above already happened, and the anonymous user is P5-3's gc to remove either way" ;;
esac

say "done -- rungs: health $health, signup $signup, rpc $rpc, logout $logout"
