#!/bin/sh
# usage: scripts/backend-settings.sh <project-ref> [--dry-run]
#        SUPABASE_ACCESS_TOKEN holds a personal access token (sbp_...). SUPABASE_API_URL overrides the
#        Management API base for the tests; it defaults to https://api.supabase.com/v1.
#
# Sets the hosted settings a Brigade backend needs (plan D32, 5.6, 5.9; P5-1) one FIELD at a time through the
# Management API, and reads every one of them back. `make backend-install` runs it after `db push`.
#
# Why not `supabase config push`: that command sends this repository's config.toml WHOLE, and this config.toml
# describes the LOCAL stack. Measured from CLI 2.116.0's own config/push/SIDE_EFFECTS.md (P5-1 brief 1.4), it
# PATCHes ~100 auth fields and PUTs the Postgres settings on a gate that is "always processed"; two of the
# values it would carry are actively harmful on a production project -- [auth.rate_limit] anonymous_users =
# 1000 against a hosted default of 30 per hour per IP, and site_url = http://127.0.0.1:3000 -- and `--yes`,
# which an unattended run needs, auto-confirms every diff. This script therefore never sends those two fields
# at all; scripts/ci/backend_settings_test.go asserts that from the recorded curl argv and request bodies.
#
# The three groups, in the order they must run (PostgREST first is NOT a preference: with `brigade` exposed and
# no schema behind it PostgREST loops on `3F000 schema "brigade" does not exist` and never turns healthy, E0-1 --
# which is why `make backend-install` pushes the migrations before it calls this script):
#
#   1. /projects/<ref>/postgrest        db_schema gains `brigade`, APPENDED to whatever the GET returned. The
#                                       value is never typed from memory, and no other field of that body is
#                                       sent: db_extra_search_path, max_rows, db_pool and the pool timeout stay
#                                       exactly as the project has them.
#   2. /projects/<ref>/config/auth      the eight D32 fields: anonymous sign-ins on, sign-ups not disabled,
#                                       CAPTCHA off, jwt_exp 3600, no session time-box, no inactivity timeout,
#                                       refresh-token rotation on, reuse interval 10 s. Only the fields that are
#                                       wrong are sent, in one small body. NEVER rate_limit_anonymous_users
#                                       (the hosted 30/h/IP ceiling is a threat-model property) and NEVER
#                                       site_url.
#   3. /projects/<ref>/config/realtime  private_only = true, which is the dashboard's "Allow public access" off.
#                                       A public join is then refused with `PrivateOnly` (5.6, I-13 hosted half).
#
# Idempotent by construction: every group is GET -> compare -> PATCH only what differs -> GET and check. A second
# run issues zero PATCHes. `--dry-run` reports the same lines and issues none at all.
#
# Credentials: the access token is a bearer credential for the whole run, so it reaches curl through a 0600
# header file (`-H @<file>`) and never on argv -- `ps` and any debug log see argv (CLAUDE.md). No response body
# is ever printed: the postgrest GET carries `jwt_secret` and the auth GET carries ~24 `external_*_secret`
# fields, `smtp_pass`, `sms_vonage_api_secret`, `nimbus_oauth_client_secret` and five `hook_*_secrets` arrays.
# Only the named scalar fields this script compares are ever read out of a body, through a jq allow-list.
#
# Exit: 0 when every field ends at its target (or would, under --dry-run); non-zero on an HTTP failure, a
# missing token, or a read-back that does not match.
set -eu

# 0600 for the header file and 0700 for the temporary directory, set before anything is created.
umask 077

say()  { printf 'backend-settings: %s\n' "$1"; }
die()  { printf 'backend-settings: %s\n' "$1" >&2; exit 1; }

ref=${1:-}
mode=${2:-}
[ -n "$ref" ] || die 'usage: scripts/backend-settings.sh <project-ref> [--dry-run]'
case $ref in -*) die 'usage: scripts/backend-settings.sh <project-ref> [--dry-run]' ;; esac
dry=''
case $mode in
  '')         ;;
  --dry-run)  dry=1 ;;
  *)          die "unknown argument: $mode (usage: scripts/backend-settings.sh <project-ref> [--dry-run])" ;;
esac

token=${SUPABASE_ACCESS_TOKEN:-}
[ -n "$token" ] || die 'SUPABASE_ACCESS_TOKEN is not set: this script needs a Supabase personal access token (sbp_...) in the environment. It is never a repository secret of the keep-alive, never a file path and never an argument.'
unset SUPABASE_ACCESS_TOKEN   # curl is spawned below; nothing it runs needs the token in its environment

api=${SUPABASE_API_URL:-https://api.supabase.com/v1}
api=${api%/}

for tool in curl jq; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not installed, and this script needs it"
done

tmp=$(mktemp -d) || die 'cannot create a temporary directory'
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
body=$tmp/response.json                  # every response body lands here and is read by jq, never printed
hdr=$tmp/headers.bearer                  # 0600: the personal access token
printf 'Authorization: Bearer %s\n' "$token" > "$hdr"
unset token                              # from here on the token exists only in the 0600 header file

failed=0

# api_get <path> -> writes the body to $body, prints nothing, dies on a non-2xx.
api_get() {
  code=$(curl -sS --max-time 60 -o "$body" -w '%{http_code}' -H @"$hdr" -H 'Accept: application/json' "$api$1") || code=000
  case $code in 2*) ;; *) die "GET $1 answered HTTP $code" ;; esac
}

# api_patch <path> <json-file> -> dies on a non-2xx. The body file is built by jq, never by string paste.
api_patch() {
  code=$(curl -sS --max-time 60 -X PATCH -o "$body" -w '%{http_code}' -H @"$hdr" \
           -H 'Accept: application/json' -H 'Content-Type: application/json' --data-binary @"$2" "$api$1") || code=000
  case $code in 2*) ;; *) die "PATCH $1 answered HTTP $code" ;; esac
}

# report <field> <before> <after> -> one line per setting, always, whether or not it moved.
report() {
  if [ "$2" = "$3" ]; then say "$1: $2 (already correct, no PATCH)"
  elif [ -n "$dry" ];  then say "$1: $2 -> $3 (dry run: not applied)"
  else                      say "$1: $2 -> $3"
  fi
}

# ---- 1. PostgREST: expose the brigade schema --------------------------------------------------------------
api_get "/projects/$ref/postgrest"
db_schema_before=$(jq -r '.db_schema // empty' < "$body")
[ -n "$db_schema_before" ] || die "GET /projects/$ref/postgrest returned no db_schema"
# Append to what the GET returned, matching its own separator; never construct the whole list from memory.
# The membership test is a shell `case` over the raw value rather than a pipeline: this script runs on a
# restricted PATH in its tests (curl, jq, mktemp, rm only), and an idempotence guard that silently fails
# because a helper binary is missing would append `brigade` on every run. Both separator styles are matched
# because the hosted GET answers `public,graphql_public` while the 406 hint renders it `public, graphql_public`.
case ",$db_schema_before," in
  *,brigade,*|*", brigade,"*) db_schema_after=$db_schema_before ;;
  *)                          db_schema_after="$db_schema_before,brigade" ;;
esac
report 'postgrest.db_schema' "$db_schema_before" "$db_schema_after"
if [ "$db_schema_before" != "$db_schema_after" ] && [ -z "$dry" ]; then
  jq -n --arg s "$db_schema_after" '{db_schema: $s}' > "$tmp/postgrest.patch.json"
  api_patch "/projects/$ref/postgrest" "$tmp/postgrest.patch.json"
  api_get "/projects/$ref/postgrest"
  read_back=$(jq -r '.db_schema // empty' < "$body")
  if [ "$read_back" != "$db_schema_after" ]; then
    say "FAILED read-back postgrest.db_schema: expected $db_schema_after, got $read_back"; failed=1
  fi
fi

# ---- 2. Auth: the eight D32 fields ------------------------------------------------------------------------
# The pairs below are `<field> <target>`. rate_limit_anonymous_users and site_url are deliberately absent:
# see the header. sessions_timebox and sessions_inactivity_timeout are handled just after, because the API
# reports "no limit" as either 0 or null and both are already right.
api_get "/projects/$ref/config/auth"
auth_before=$tmp/auth.before.json
jq '{external_anonymous_users_enabled, disable_signup, security_captcha_enabled, jwt_exp,
     sessions_timebox, sessions_inactivity_timeout, refresh_token_rotation_enabled,
     security_refresh_token_reuse_interval}' < "$body" > "$auth_before"

: > "$tmp/auth.pairs"
printf '%s\n' \
  'external_anonymous_users_enabled true' \
  'disable_signup false' \
  'security_captcha_enabled false' \
  'jwt_exp 3600' \
  'refresh_token_rotation_enabled true' \
  'security_refresh_token_reuse_interval 10' >> "$tmp/auth.pairs"

# Each field that differs appends one `<field> <json-value>` line here; the PATCH body is then built from
# the whole file in ONE jq call. Nothing rewrites a file in place, so no `mv` is needed and the script keeps
# to the four tools its tests put on the restricted PATH.
: > "$tmp/auth.changed"
while read -r field want; do
  [ -n "$field" ] || continue
  have=$(jq -r --arg f "$field" '.[$f] | tostring' < "$auth_before")
  report "auth.$field" "$have" "$want"
  [ "$have" = "$want" ] && continue
  printf '%s %s\n' "$field" "$want" >> "$tmp/auth.changed"
done < "$tmp/auth.pairs"

# The two session limits: D32 forbids both, and the API says "none" as 0 or null. Anything else is a real
# limit that would silently kill idle principals (5.9), so it is set to 0 rather than left alone.
for field in sessions_timebox sessions_inactivity_timeout; do
  have=$(jq -r --arg f "$field" '.[$f] | tostring' < "$auth_before")
  case $have in
    0|null) report "auth.$field" "$have" "$have" ;;
    *)      report "auth.$field" "$have" '0'; printf '%s 0\n' "$field" >> "$tmp/auth.changed" ;;
  esac
done

if [ -s "$tmp/auth.changed" ] && [ -z "$dry" ]; then
  jq -n --rawfile changed "$tmp/auth.changed" \
    '$changed | split("\n") | map(select(length > 0) | split(" ") | {key: .[0], value: (.[1] | fromjson)})
              | from_entries' > "$tmp/auth.patch.json"
  api_patch "/projects/$ref/config/auth" "$tmp/auth.patch.json"
  api_get "/projects/$ref/config/auth"
  jq '{external_anonymous_users_enabled, disable_signup, security_captcha_enabled, jwt_exp,
       sessions_timebox, sessions_inactivity_timeout, refresh_token_rotation_enabled,
       security_refresh_token_reuse_interval}' < "$body" > "$tmp/auth.after.json"
  while read -r field want; do
    [ -n "$field" ] || continue
    have=$(jq -r --arg f "$field" '.[$f] | tostring' < "$tmp/auth.after.json")
    if [ "$have" != "$want" ]; then say "FAILED read-back auth.$field: expected $want, got $have"; failed=1; fi
  done < "$tmp/auth.pairs"
  for field in sessions_timebox sessions_inactivity_timeout; do
    have=$(jq -r --arg f "$field" '.[$f] | tostring' < "$tmp/auth.after.json")
    case $have in 0|null) ;; *) say "FAILED read-back auth.$field: expected 0 or null, got $have"; failed=1 ;; esac
  done
fi

# ---- 3. Realtime: private channels only -------------------------------------------------------------------
api_get "/projects/$ref/config/realtime"
private_before=$(jq -r '.private_only | tostring' < "$body")
report 'realtime.private_only' "$private_before" 'true'
if [ "$private_before" != true ] && [ -z "$dry" ]; then
  printf '{"private_only":true}\n' > "$tmp/realtime.patch.json"
  api_patch "/projects/$ref/config/realtime" "$tmp/realtime.patch.json"
  api_get "/projects/$ref/config/realtime"
  read_back=$(jq -r '.private_only | tostring' < "$body")
  if [ "$read_back" != true ]; then
    say "FAILED read-back realtime.private_only: expected true, got $read_back"; failed=1
  fi
fi

[ "$failed" = 0 ] || die 'one or more settings did not read back at their target (above)'
if [ -n "$dry" ]; then say 'dry run: nothing was changed'; else say 'done -- every setting reads back at its target'; fi
