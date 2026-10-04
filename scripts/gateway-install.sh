#!/bin/sh
# usage: scripts/gateway-install.sh <project-ref> --from 'Name <address>' [--inbox <address>] [--public-address <address>]
#                                   [--team-file <path>] [--dry-run]
#
#   --public-address  the address people are told to write to, when the team routes one of its own (a Google
#                     Workspace alias with a routing rule, say) to the Resend receiving address; the receiving
#                     address stays the Reply-To and the one a mail must be addressed to. Default: the inbox.
#
#   SUPABASE_ACCESS_TOKEN  a personal access token (sbp_...), exactly as `make backend-install` takes it
#   RESEND_API_KEY         the Resend API key (re_...) of the account that sends for this team; from the
#                          environment, never from argv
#   SUPABASE               the CLI command (the Makefile passes its own; default `npx --yes supabase@2.116.0`)
#
# Installs the hosted mail gateway (Trello card 48; .context/plans/human-sessions-slack-email.md) on a team's
# Supabase project, idempotently, in this order:
#
#   1. `db push`: the brigade_gateway migration (supabase/migrations/*_mail_gateway.sql).
#   2. `functions deploy mail-in mail-out`, bundled server-side (--use-api: no Docker on the admin's machine),
#      with the platform's JWT check off: mail-in is authenticated by the provider's webhook signature and
#      mail-out by the tick token below, since neither caller holds a Supabase JWT.
#   3. Resend: the inbox that receives (an existing one for the --from address, else one created in forwarding
#      mode, whose Resend-managed receiving address needs no DNS), and the email.received webhook pointed at
#      mail-in. A webhook's signing secret is shown once by Resend, so an existing webhook on the same endpoint
#      is replaced rather than reused.
#   4. The gateway principal: when the team has none yet, one anonymous GoTrue sign-up through the project's
#      publishable key (what every member's `team join` does), then one SQL block through the Management API as
#      postgres: the membership, the gateway session (registered through brigade.register_session as that
#      member, harness `gateway-email`, lease 600 s) and the brigade_gateway.gateways row that ties them.
#   5. The functions' secrets, through the CLI from a 0600 file: team ref, from address, inbox, provider, the
#      Resend key, the webhook secret and a fresh tick token.
#   6. pg_net and one pg_cron job, `brigade_gateway_tick`, that POSTs mail-out every minute with the token.
#   7. The project's .brigade.json gains "gateway": {"email": "<inbox>"} (a public value) for the admin to
#      commit, and one tick is run so the roster shows the gateway at once.
#
# Nothing here prints a token, a key or a signing secret; the access token and the Resend key reach curl through
# 0600 header files (`-H @<file>`), never on argv (CLAUDE.md). `--dry-run` runs the reads and prints the plan.
set -eu

ref=''
from=''
inbox=''
public_address=''
team_file='.brigade.json'
dry=''
while [ $# -gt 0 ]; do
  case "$1" in
    --from) from="$2"; shift 2 ;;
    --inbox) inbox="$2"; shift 2 ;;
    --public-address) public_address="$2"; shift 2 ;;
    --team-file) team_file="$2"; shift 2 ;;
    --dry-run) dry=1; shift ;;
    -*) echo "gateway-install: unknown flag $1" >&2; exit 2 ;;
    *) if [ -z "$ref" ]; then ref="$1"; shift; else echo "gateway-install: unexpected argument" >&2; exit 2; fi ;;
  esac
done
if [ -z "$ref" ] || [ -z "$from" ]; then
  echo "usage: scripts/gateway-install.sh <project-ref> --from 'Name <address>' [--inbox <address>] [--public-address <address>] [--dry-run]" >&2
  exit 2
fi
: "${SUPABASE_ACCESS_TOKEN:?set SUPABASE_ACCESS_TOKEN to a personal access token (sbp_...) first; see docs/setup.md}"
: "${RESEND_API_KEY:?set RESEND_API_KEY to the Resend API key (re_...) first}"
supabase_cmd="${SUPABASE:-npx --yes supabase@2.116.0}"
for tool in curl jq od; do
  command -v "$tool" >/dev/null 2>&1 || { echo "gateway-install: $tool is required" >&2; exit 1; }
done
if [ ! -f "$team_file" ]; then
  echo "gateway-install: $team_file not found; run this at the toplevel of the project, after team create" >&2
  exit 1
fi

from_addr=$(printf '%s' "$from" | sed -n 's/.*<\([^>]*\)>.*/\1/p')
[ -n "$from_addr" ] || from_addr="$from"
case "$from_addr" in
  *@*.*) ;;
  *) echo "gateway-install: --from must carry an email address on a domain verified in Resend" >&2; exit 2 ;;
esac

team_ref=$(jq -r '.team_ref // empty' "$team_file")
url=$(jq -r '.url // empty' "$team_file")
key=$(jq -r '.publishable_key // empty' "$team_file")
team_name=$(jq -r '.team_name // "team"' "$team_file")
if [ -z "$team_ref" ] || [ -z "$url" ] || [ -z "$key" ]; then
  echo "gateway-install: $team_file lacks team_ref, url or publishable_key" >&2
  exit 1
fi
case "$url" in
  "https://$ref.supabase.co") ;;
  *) echo "gateway-install: $team_file names $url, not project $ref" >&2; exit 1 ;;
esac
fn_base="$url/functions/v1"

umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
printf 'Authorization: Bearer %s\n' "$SUPABASE_ACCESS_TOKEN" > "$work/sb.h"
printf 'Authorization: Bearer %s\n' "$RESEND_API_KEY" > "$work/re.h"
mapi() { curl -sS -H @"$work/sb.h" -H 'Content-Type: application/json' "$@"; }
resend() { curl -sS -H @"$work/re.h" -H 'Content-Type: application/json' "$@"; }
# sql runs one statement through the Management API as postgres and prints its JSON rows.
sql() {
  jq -n --arg q "$1" '{query: $q}' | mapi -X POST "https://api.supabase.com/v1/projects/$ref/database/query" -d @-
}
# lit quotes a value as an SQL string literal.
lit() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"; }
say() { printf 'gateway-install: %s\n' "$*"; }

# 1. migrations
if [ -n "$dry" ]; then
  $supabase_cmd db push --dry-run --project-ref "$ref"
else
  $supabase_cmd db push --yes --project-ref "$ref"
fi

# 2. functions
if [ -n "$dry" ]; then
  say "would deploy functions mail-in and mail-out to $fn_base (--use-api --no-verify-jwt)"
else
  $supabase_cmd functions deploy mail-in mail-out --project-ref "$ref" --use-api --no-verify-jwt
fi

# 3. Resend: the inbox that receives, and the webhook
if [ -z "$inbox" ]; then
  # An installed gateway keeps its receiving address across re-runs (a changed --from must not mint a new inbox:
  # a mailbox routed to the old address would then be ignored). The Management API returns the secret's value.
  inbox=$(mapi "https://api.supabase.com/v1/projects/$ref/secrets" |
    jq -r 'if type == "array" then (.[] | select(.name == "BRIGADE_MAIL_INBOX") | .value // empty) else empty end' | head -1)
  [ -z "$inbox" ] || say "keeping the receiving address the installed gateway already uses"
fi
if [ -z "$inbox" ]; then
  # The list carries no receiving address; the inbox itself does.
  inbox_id=$(resend "https://api.resend.com/inboxes" |
    jq -r --arg a "$from_addr" '(.data // [])[] | select(.email_address == $a) | .id // empty' | head -1)
  if [ -n "$inbox_id" ]; then
    inbox=$(resend "https://api.resend.com/inboxes/$inbox_id" | jq -r '.receiving_address // empty')
  fi
fi
if [ -z "$inbox" ] && [ -z "$dry" ]; then
  inbox=$(jq -n --arg a "$from_addr" '{email_address: $a, name: "Brigade mail gateway", from_name: "Brigade", forwarding: true}' |
    resend -X POST "https://api.resend.com/inboxes" -d @- | jq -r '.receiving_address // empty')
fi
if [ -z "$inbox" ]; then
  if [ -n "$dry" ]; then
    inbox="<a Resend-managed receiving address, created on the real run>"
  else
    echo "gateway-install: Resend did not provide a receiving address; enable receiving in the Resend dashboard and pass --inbox <address>" >&2
    exit 1
  fi
fi
webhook_secret=''
if [ -z "$dry" ]; then
  resend "https://api.resend.com/webhooks" | jq -r --arg e "$fn_base/mail-in" '(.data // [])[] | select(.endpoint == $e) | .id' |
    while IFS= read -r id; do
      [ -n "$id" ] && resend -X DELETE "https://api.resend.com/webhooks/$id" >/dev/null
    done
  webhook_secret=$(jq -n --arg e "$fn_base/mail-in" '{endpoint: $e, events: ["email.received"]}' |
    resend -X POST "https://api.resend.com/webhooks" -d @- | jq -r '.signing_secret // empty')
  if [ -z "$webhook_secret" ]; then
    echo "gateway-install: Resend did not return a webhook signing secret" >&2
    exit 1
  fi
else
  say "would point a Resend email.received webhook at $fn_base/mail-in"
fi

# 4. the gateway principal and session
existing=$(sql "select user_id::text as user_id from brigade_gateway.gateways where team_id = $(lit "$team_ref") and kind = 'email'" |
  jq -r 'if type == "array" then (.[0].user_id // empty) else empty end')
uid="$existing"
if [ -n "$existing" ]; then
  say "the team already has a gateway member; keeping it"
elif [ -n "$dry" ]; then
  say "would sign up one anonymous principal for the gateway and join it to team $team_name"
else
  uid=$(curl -sS -X POST "$url/auth/v1/signup" -H "apikey: $key" -H 'Content-Type: application/json' \
    -d '{"data":{},"gotrue_meta_security":{}}' | jq -r '.user.id // empty')
  if [ -z "$uid" ]; then
    echo "gateway-install: the anonymous sign-up for the gateway principal failed (anonymous sign-ins must be on; make backend-install sets them)" >&2
    exit 1
  fi
fi
[ -n "$public_address" ] || public_address="$inbox"
description="email gateway: a person writes to $public_address with a first line \"to: <session>\"; a session writes to me with the same line, \"to: <address>\""
if [ -z "$dry" ]; then
  block="do \$\$
declare v_team uuid := $(lit "$team_ref"); v_uid uuid := $(lit "$uid"); v_existing uuid; v_sid uuid; v_rec jsonb; v_live boolean := false;
begin
  select user_id, session_id into v_existing, v_sid from brigade_gateway.gateways where team_id = v_team and kind = 'email';
  if found then v_uid := v_existing; end if;
  insert into brigade.memberships (team_id, user_id, human_label, joined_secret_version)
    select v_team, v_uid, 'mail gateway', t.secret_version from brigade.teams t where t.id = v_team
    on conflict (team_id, user_id) do update set status = 'active', revoked_at = null;
  perform set_config('request.jwt.claim.sub', v_uid::text, true);
  perform set_config('request.jwt.claims', jsonb_build_object('sub', v_uid, 'role', 'authenticated', 'aud', 'authenticated', 'is_anonymous', true)::text, true);
  if v_sid is not null then
    select (closed_at is null and last_seen_at + make_interval(secs => lease_seconds) > now()) into v_live from brigade.sessions where id = v_sid;
    if not found then v_sid := null; end if;
  end if;
  if v_live then
    perform brigade.session_heartbeat(v_sid, 'idle', null, $(lit "$description"), 'accept', 600);
  else
    v_rec := brigade.register_session(v_team, 'mail-gateway', $(lit "$description"), 'idle', 'accept', 'gateway-email', '0.1.0', null, 600, v_sid);
    v_sid := (v_rec->>'session_id')::uuid;
  end if;
  insert into brigade_gateway.gateways (team_id, user_id, session_id, kind) values (v_team, v_uid, v_sid, 'email')
    on conflict (team_id, kind) do update set session_id = excluded.session_id;
  perform set_config('request.jwt.claim.sub', '', true);
end \$\$"
  out=$(sql "$block")
  if ! printf '%s' "$out" | jq -e 'type == "array"' >/dev/null 2>&1; then
    echo "gateway-install: the gateway SQL failed: $(printf '%s' "$out" | jq -r '.message // .error // .' 2>/dev/null | head -c 300)" >&2
    exit 1
  fi
  session=$(sql "select session_id::text as s from brigade_gateway.gateways where team_id = $(lit "$team_ref") and kind = 'email'" | jq -r '.[0].s // empty')
  say "gateway session $session is registered as mail-gateway"
fi

# 5. secrets
tick=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
if [ -z "$dry" ]; then
  {
    printf 'BRIGADE_TEAM_REF=%s\n' "$team_ref"
    printf 'BRIGADE_MAIL_FROM=%s\n' "$from"
    printf 'BRIGADE_MAIL_INBOX=%s\n' "$inbox"
    printf 'BRIGADE_MAIL_PUBLIC_ADDRESS=%s\n' "$public_address"
    printf 'BRIGADE_MAIL_PROVIDER=resend\n'
    printf 'RESEND_API_KEY=%s\n' "$RESEND_API_KEY"
    printf 'RESEND_WEBHOOK_SECRET=%s\n' "$webhook_secret"
    printf 'BRIGADE_TICK_TOKEN=%s\n' "$tick"
  } > "$work/secrets.env"
  $supabase_cmd secrets set --project-ref "$ref" --env-file "$work/secrets.env" >/dev/null
  say "8 function secrets set"
else
  say "would set 8 function secrets: BRIGADE_TEAM_REF, BRIGADE_MAIL_FROM, BRIGADE_MAIL_INBOX, BRIGADE_MAIL_PUBLIC_ADDRESS, BRIGADE_MAIL_PROVIDER, RESEND_API_KEY, RESEND_WEBHOOK_SECRET, BRIGADE_TICK_TOKEN"
fi

# 6. the tick
if [ -z "$dry" ]; then
  sql "create extension if not exists pg_net" >/dev/null
  cron="select cron.schedule('brigade_gateway_tick', '* * * * *', \$job\$select net.http_post(url := '$fn_base/mail-out', headers := '{\"Content-Type\":\"application/json\",\"x-brigade-tick\":\"$tick\"}'::jsonb, body := '{}'::jsonb, timeout_milliseconds := 20000)\$job\$)"
  if ! sql "$cron" | jq -e 'type == "array"' >/dev/null; then
    echo "gateway-install: scheduling the tick failed" >&2
    exit 1
  fi
  say "pg_cron job brigade_gateway_tick scheduled (every minute)"
else
  say "would create pg_net and schedule brigade_gateway_tick to POST $fn_base/mail-out every minute"
fi

# 7. the team file, and one tick now
if [ -z "$dry" ]; then
  jq --arg a "$public_address" '.gateway = ((.gateway // {}) + {email: $a})' "$team_file" > "$work/team.json" && cat "$work/team.json" > "$team_file"
  printf 'x-brigade-tick: %s\n' "$tick" > "$work/tick.h"
  first=$(curl -sS -X POST "$fn_base/mail-out" -H @"$work/tick.h" -H 'Content-Type: application/json' -d '{}' || true)
  say "first tick: $(printf '%s' "$first" | jq -c '{heartbeat, delivered, undeliverable, retry, error}' 2>/dev/null || printf '%s' "$first" | head -c 200)"
fi

echo ''
echo "mail gateway for team \"$team_name\" on $ref"
echo "  people write to:             $public_address"
echo "  receiving address (Resend):  $inbox"
echo "  sends from:                  $from"
echo "  functions:                   $fn_base/mail-in, $fn_base/mail-out"
if [ -z "$dry" ]; then
  echo "next: git add $team_file && git commit && git push (it carries only public values); members update the plugin to learn the gateway"
else
  echo "(dry run: nothing was changed)"
fi
