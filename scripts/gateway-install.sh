#!/bin/sh
# usage: scripts/gateway-install.sh <project-ref> --from 'Name <address>' [--provider resend|postmark|external]
#                                   [--inbox <address>] [--public-address <address>]
#                                   [--send-url <url>] [--secret-file <path>] [--rotate]
#                                   [--team-file <path>] [--dry-run]
#
#   --provider        which mail connector carries the mail (docs/mail-connectors.md). `resend` (default) and
#                     `postmark` are shipped: their connector is deployed into the project beside the core and
#                     needs nothing from the administrator but the provider's token. `external` is a connector
#                     of the team's own, anywhere with HTTPS: --send-url, --inbox and --secret-file are required.
#   --inbox           the address the connector receives at. Default: for resend, the Resend-managed receiving
#                     address; for postmark, the server's InboundAddress; for external, required.
#   --public-address  the address people are told to write to, when the team routes one of its own to the
#                     receiving address (a Google Workspace routing rule, say); the receiving address stays the
#                     Reply-To and the one a mail must be addressed to. Default: the inbox.
#   --team-file       the project's .brigade.json (default: ./.brigade.json). This script runs from a checkout of
#                     Brigade, whose Makefile and migrations it needs; the team's project may be another repository.
#   --send-url        external only: the connector's send endpoint, where the core POSTs mail to send.
#   --secret-file     external only: an absolute path OUTSIDE the repository. The connector secret and the core's
#                     inbound URL are written there (mode 0600) for the administrator to configure in their
#                     connector; on a re-run the secret is read back from it, so the connector keeps working.
#   --rotate          external only: mint a new connector secret even though the file holds one. A shipped
#                     connector gets a fresh secret every run, with the core, and nobody has to know it.
#
#   SUPABASE_ACCESS_TOKEN  a personal access token (sbp_...), exactly as `make backend-install` takes it
#   RESEND_API_KEY         resend: the Resend API key (re_...) of the account that sends for this team
#   POSTMARK_SERVER_TOKEN  postmark: the Server API token of the Postmark server that sends and receives
#   SUPABASE               the CLI command (the Makefile passes its own; default `npx --yes supabase@2.116.0`)
#   Every token comes from the environment, never from argv, and reaches curl through 0600 header files.
#
# Installs the hosted mail gateway (Trello cards 48 and 49; docs/mail-gateway.md) on a team's Supabase project,
# idempotently, in this order:
#
#   1. `db push`: the brigade_gateway migration (supabase/migrations/*_mail_gateway.sql).
#   2. `functions deploy`: the core, mail-in and mail-out, plus the shipped connector for the provider, bundled
#      server-side (--use-api: no Docker on the admin's machine), with the platform's JWT check off: every caller
#      authenticates with a secret of its own (the connector secret, the provider's webhook check, the tick token).
#   3. The provider: resend, the inbox that receives (an existing one for the --from address, else one created in
#      forwarding mode, whose Resend-managed receiving address needs no DNS) and the email.received webhook pointed
#      at the connector; postmark, the server's InboundAddress and its InboundHookUrl pointed at the connector with
#      the webhook password in the URL; external, the secret file written and the inbound URL printed.
#   4. The gateway principal: when the team has none yet, one anonymous GoTrue sign-up through the project's
#      publishable key (what every member's `team join` does), then one SQL block through the Management API as
#      postgres: the membership, the gateway session (registered through brigade.register_session as that
#      member, harness `gateway-email`, lease 600 s) and the brigade_gateway.gateways row that ties them and
#      carries the team's settings (inbox, public address, from, provider, send URL). One project may carry
#      several teams' gateways: run this once per team, from each team's checkout.
#   5. The functions' secrets, through the CLI from a 0600 file: the project-wide ones (connector secret, inbound
#      URL, tick token, the provider's own), and, for the one team the project's BRIGADE_TEAM_REF names (or the
#      first team installed), the BRIGADE_MAIL_* copies of its settings that a row without settings falls back to.
#   6. pg_net and one pg_cron job, `brigade_gateway_tick`, that POSTs mail-out every minute with the token.
#   7. The project's .brigade.json gains "gateway": {"email": "<address>"} (a public value) for the admin to
#      commit, and one tick is run so the roster shows the gateway at once.
#
# Nothing here prints a token, a key or a secret. `--dry-run` runs the reads and prints the plan.
set -eu

ref=''
from=''
provider='resend'
inbox=''
public_address=''
send_url=''
secret_file=''
rotate=''
team_file='.brigade.json'
dry=''
usage="usage: scripts/gateway-install.sh <project-ref> --from 'Name <address>' [--team-file <path>] [--provider resend|postmark|external] [--inbox <address>] [--public-address <address>] [--send-url <url>] [--secret-file <path>] [--rotate] [--dry-run]"
while [ $# -gt 0 ]; do
  case "$1" in
    --from) from="$2"; shift 2 ;;
    --provider) provider="$2"; shift 2 ;;
    --inbox) inbox="$2"; shift 2 ;;
    --public-address) public_address="$2"; shift 2 ;;
    --send-url) send_url="$2"; shift 2 ;;
    --secret-file) secret_file="$2"; shift 2 ;;
    --rotate) rotate=1; shift ;;
    --team-file) team_file="$2"; shift 2 ;;
    --dry-run) dry=1; shift ;;
    -*) echo "gateway-install: unknown flag $1" >&2; exit 2 ;;
    *) if [ -z "$ref" ]; then ref="$1"; shift; else echo "gateway-install: unexpected argument" >&2; exit 2; fi ;;
  esac
done
if [ -z "$ref" ] || [ -z "$from" ]; then
  echo "$usage" >&2
  exit 2
fi
case "$provider" in
  resend|postmark|external) ;;
  *) echo "gateway-install: --provider must be resend, postmark or external (got '$provider')" >&2; exit 2 ;;
esac
: "${SUPABASE_ACCESS_TOKEN:?set SUPABASE_ACCESS_TOKEN to a personal access token (sbp_...) first; see docs/setup.md}"
case "$provider" in
  resend) : "${RESEND_API_KEY:?set RESEND_API_KEY to the Resend API key (re_...) first}" ;;
  postmark) : "${POSTMARK_SERVER_TOKEN:?set POSTMARK_SERVER_TOKEN to the Postmark Server API token first}" ;;
  external)
    [ -n "$send_url" ] || { echo "gateway-install: --provider external needs --send-url <url>, the send endpoint of your connector" >&2; exit 2; }
    [ -n "$inbox" ] || { echo "gateway-install: --provider external needs --inbox <address>, the address your connector receives at" >&2; exit 2; }
    [ -n "$secret_file" ] || { echo "gateway-install: --provider external needs --secret-file <path>, an absolute path outside the repository" >&2; exit 2; }
    case "$secret_file" in
      /*) ;;
      *) echo "gateway-install: --secret-file must be an absolute path" >&2; exit 2 ;;
    esac
    # Outside the team's project repository (the one the team file is in), and outside this Brigade checkout.
    for top in "$(git -C "$(dirname "$team_file")" rev-parse --show-toplevel 2>/dev/null || dirname "$team_file")" "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"; do
      case "$secret_file" in
        "$top"/*) echo "gateway-install: --secret-file must be outside the repository ($top)" >&2; exit 2 ;;
      esac
    done
    ;;
esac
supabase_cmd="${SUPABASE:-npx --yes supabase@2.116.0}"
for tool in curl jq od; do
  command -v "$tool" >/dev/null 2>&1 || { echo "gateway-install: $tool is required" >&2; exit 1; }
done
if [ ! -f "$team_file" ]; then
  echo "gateway-install: $team_file not found; pass --team-file <the project's .brigade.json> (make: team_file=), written by team create" >&2
  exit 1
fi

from_addr=$(printf '%s' "$from" | sed -n 's/.*<\([^>]*\)>.*/\1/p')
[ -n "$from_addr" ] || from_addr="$from"
case "$from_addr" in
  *@*.*) ;;
  *) echo "gateway-install: --from must carry an email address the provider may send from (a verified domain in Resend, a confirmed sender signature or domain in Postmark)" >&2; exit 2 ;;
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
host="${url#https://}"
inbound_url="$fn_base/mail-in"

umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
printf 'Authorization: Bearer %s\n' "$SUPABASE_ACCESS_TOKEN" > "$work/sb.h"
mapi() { curl -sS -H @"$work/sb.h" -H 'Content-Type: application/json' "$@"; }
# sql runs one statement through the Management API as postgres and prints its JSON rows.
sql() {
  jq -n --arg q "$1" '{query: $q}' | mapi -X POST "https://api.supabase.com/v1/projects/$ref/database/query" -d @-
}
# lit quotes a value as an SQL string literal.
lit() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"; }
say() { printf 'gateway-install: %s\n' "$*"; }
mint() { od -An -N24 -tx1 /dev/urandom | tr -d ' \n'; }
sha256() {
  if command -v openssl >/dev/null 2>&1; then printf '%s' "$1" | openssl dgst -sha256 | sed 's/.*= *//'
  elif command -v shasum >/dev/null 2>&1; then printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1
  else printf '%s' "$1" | sha256sum | cut -d' ' -f1
  fi
}
# The Management API lists a project's function secrets with the SHA-256 of each value, never the value
# (measured 2026-10-04). A re-run can therefore recognise what the installed gateway already uses, by hashing
# candidates, but cannot read it back: secret_digest prints the stored digest, secret_is tests one candidate.
secret_digest() {
  mapi "https://api.supabase.com/v1/projects/$ref/secrets" |
    jq -r --arg n "$1" 'if type == "array" then (.[] | select(.name == $n) | .value // empty) else empty end' | head -1
}
secret_is() { [ -n "$2" ] && [ "$(secret_digest "$1")" = "$(sha256 "$2")" ]; }
case "$provider" in
  resend)
    printf 'Authorization: Bearer %s\n' "$RESEND_API_KEY" > "$work/re.h"
    resend() { curl -sS -H @"$work/re.h" -H 'Content-Type: application/json' "$@"; }
    ;;
  postmark)
    printf 'X-Postmark-Server-Token: %s\n' "$POSTMARK_SERVER_TOKEN" > "$work/pm.h"
    postmark() { curl -sS -H @"$work/pm.h" -H 'Accept: application/json' -H 'Content-Type: application/json' "$@"; }
    ;;
esac

# 1. migrations
if [ -n "$dry" ]; then
  $supabase_cmd db push --dry-run --project-ref "$ref"
else
  $supabase_cmd db push --yes --project-ref "$ref"
fi

# 2. functions: the core, and the shipped connector for the provider
functions='mail-in mail-out'
case "$provider" in
  resend) functions="$functions mail-connector-resend" ;;
  postmark) functions="$functions mail-connector-postmark" ;;
esac
if [ -n "$dry" ]; then
  say "would deploy functions $functions to $fn_base (--use-api --no-verify-jwt)"
else
  # shellcheck disable=SC2086  # $functions is a word list on purpose
  $supabase_cmd functions deploy $functions --project-ref "$ref" --use-api --no-verify-jwt
fi

# 3. the connector secret, the inbox, and the provider's webhook
# The connector secret authenticates both directions of docs/mail-connectors.md. A shipped connector lives in
# this project and gets a fresh secret with the core on every run. An external connector holds the secret the
# administrator configured, so it is read back from the secret file and kept; --rotate mints a new one.
connector_secret=''
if [ "$provider" = external ] && [ -z "$rotate" ] && [ -f "$secret_file" ]; then
  connector_secret=$(sed -n 's/^BRIGADE_MAIL_CONNECTOR_SECRET=//p' "$secret_file" | head -1)
  [ -z "$connector_secret" ] || say "keeping the connector secret from $secret_file"
fi
if [ -z "$connector_secret" ]; then
  connector_secret=$(mint)
  say "minted a new connector secret"
fi

# An installed gateway keeps its receiving address across re-runs: a changed --from must not mint a new inbox,
# since a mailbox the team routed to the old address would then be ignored. The team's gateways row says it (from
# migration 20261005090000 on); before that the project's secrets say only the digest of the address, so the
# provider's candidates are hashed against it below.
row=$(sql "select inbox, public_address from brigade_gateway.gateways where team_id = $(lit "$team_ref") and kind = 'email'")
row_inbox=$(printf '%s' "$row" | jq -r 'if type == "array" then (.[0].inbox // empty) else empty end')
row_public=$(printf '%s' "$row" | jq -r 'if type == "array" then (.[0].public_address // empty) else empty end')
if [ -z "$inbox" ] && [ -n "$row_inbox" ]; then
  inbox="$row_inbox"
  say "keeping the receiving address the installed gateway already uses"
fi
# The address people are told is kept too, so a re-run without --public-address does not take it back.
if [ -z "$public_address" ] && [ -n "$row_public" ] && [ "$row_public" != "$row_inbox" ]; then
  public_address="$row_public"
  say "keeping the public address the installed gateway already uses"
fi
# The project's BRIGADE_TEAM_REF and BRIGADE_MAIL_* secrets describe ONE team: the first installed, or the one
# the Slack installer named. They are written for that team only, another team's settings live in its row only,
# and the digest fallback below reads them for that team only: a second team must not inherit the first's inbox.
team_digest=$(secret_digest BRIGADE_TEAM_REF)
if [ -z "$team_digest" ] || [ "$team_digest" = "$(sha256 "$team_ref")" ]; then env_team=1; else env_team=''; fi
inbox_digest=''
if [ -z "$inbox" ] && [ -n "$env_team" ]; then inbox_digest=$(secret_digest BRIGADE_MAIL_INBOX); fi

webhook_secret=''
case "$provider" in
  resend)
    send_url="$fn_base/mail-connector-resend/send"
    hook_url="$fn_base/mail-connector-resend/webhook"
    if [ -z "$inbox" ]; then
      # The list carries no receiving address; each inbox does. The one the installed gateway uses is the one
      # whose receiving address hashes to the stored digest; failing that, the inbox for the --from address.
      inboxes=$(resend "https://api.resend.com/inboxes")
      if [ -n "$inbox_digest" ]; then
        for inbox_id in $(printf '%s' "$inboxes" | jq -r '(.data // [])[] | .id'); do
          candidate=$(resend "https://api.resend.com/inboxes/$inbox_id" | jq -r '.receiving_address // empty')
          if [ -n "$candidate" ] && [ "$(sha256 "$candidate")" = "$inbox_digest" ]; then
            inbox="$candidate"
            say "keeping the receiving address the installed gateway already uses"
            break
          fi
        done
      fi
      if [ -z "$inbox" ]; then
        inbox_id=$(printf '%s' "$inboxes" |
          jq -r --arg a "$from_addr" '(.data // [])[] | select(.email_address == $a) | .id // empty' | head -1)
        if [ -n "$inbox_id" ]; then
          inbox=$(resend "https://api.resend.com/inboxes/$inbox_id" | jq -r '.receiving_address // empty')
        fi
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
    if [ -z "$dry" ]; then
      # Replace any webhook already pointed at the connector, or at the core's own endpoint (where the webhook
      # pointed before connectors): a webhook's signing secret is shown once by Resend, so none can be reused.
      resend "https://api.resend.com/webhooks" |
        jq -r --arg a "$hook_url" --arg b "$inbound_url" '(.data // [])[] | select(.endpoint == $a or .endpoint == $b) | .id' |
        while IFS= read -r id; do
          [ -n "$id" ] && resend -X DELETE "https://api.resend.com/webhooks/$id" >/dev/null
        done
      webhook_secret=$(jq -n --arg e "$hook_url" '{endpoint: $e, events: ["email.received"]}' |
        resend -X POST "https://api.resend.com/webhooks" -d @- | jq -r '.signing_secret // empty')
      if [ -z "$webhook_secret" ]; then
        echo "gateway-install: Resend did not return a webhook signing secret" >&2
        exit 1
      fi
    else
      say "would point a Resend email.received webhook at $hook_url"
    fi
    ;;
  postmark)
    send_url="$fn_base/mail-connector-postmark/send"
    server=$(postmark "https://api.postmarkapp.com/server")
    if ! printf '%s' "$server" | jq -e '.ID' >/dev/null 2>&1; then
      echo "gateway-install: Postmark refused the server token: $(printf '%s' "$server" | jq -r '.Message // .' 2>/dev/null | head -c 200)" >&2
      exit 1
    fi
    server_name=$(printf '%s' "$server" | jq -r '.Name // "server"')
    if [ -z "$inbox" ]; then
      inbox=$(printf '%s' "$server" | jq -r '.InboundAddress // empty')
      [ -n "$inbox" ] || { echo "gateway-install: the Postmark server has no InboundAddress" >&2; exit 1; }
      if [ -n "$inbox_digest" ] && [ "$(sha256 "$inbox")" != "$inbox_digest" ]; then
        echo "gateway-install: the installed gateway receives at an address other than this server's inbound address (a custom inbound domain, or another server); pass --inbox <that address>, or --inbox $inbox to move to it" >&2
        exit 1
      fi
    fi
    # Postmark signs nothing; the password in the webhook URL is the control. Postmark keeps the URL, so a fresh
    # password every run costs nothing and rotates it.
    webhook_secret=$(mint)
    hook_url="https://brigade:$webhook_secret@$host/functions/v1/mail-connector-postmark/webhook"
    if [ -z "$dry" ]; then
      set_hook=$(jq -n --arg u "$hook_url" '{InboundHookUrl: $u}' | postmark -X PUT "https://api.postmarkapp.com/server" -d @-)
      if ! printf '%s' "$set_hook" | jq -e '.InboundHookUrl != null and .InboundHookUrl != ""' >/dev/null 2>&1; then
        echo "gateway-install: Postmark did not accept the inbound webhook URL: $(printf '%s' "$set_hook" | jq -r '.Message // .' 2>/dev/null | head -c 200)" >&2
        exit 1
      fi
      say "Postmark server \"$server_name\" posts inbound mail to the connector"
    else
      say "would set Postmark server \"$server_name\"'s InboundHookUrl to the connector (with a password in the URL)"
    fi
    ;;
  external)
    if [ -n "$inbox_digest" ] && ! secret_is BRIGADE_MAIL_INBOX "$inbox"; then
      say "note: the installed gateway received at a different address until now; mail to the old one is no longer read"
    fi
    if [ -z "$dry" ]; then
      {
        printf '# Brigade mail gateway, team %s, project %s: configure these two values in your connector\n' "$team_name" "$ref"
        printf 'BRIGADE_MAIL_INBOUND_URL=%s\n' "$inbound_url"
        printf 'BRIGADE_MAIL_CONNECTOR_SECRET=%s\n' "$connector_secret"
      } > "$secret_file"
      say "wrote the inbound URL and the connector secret to $secret_file (mode 0600)"
    else
      say "would write the inbound URL and the connector secret to $secret_file"
    fi
    ;;
esac

# Two teams in one project never share an inbox: the core routes a mail to the team whose inbox it reached.
if [ -z "$dry" ]; then
  taken=$(sql "select t.name from brigade_gateway.gateways g join brigade.teams t on t.id = g.team_id where g.kind = 'email' and g.inbox = $(lit "$inbox") and g.team_id <> $(lit "$team_ref")" |
    jq -r 'if type == "array" then (.[0].name // empty) else empty end')
  if [ -n "$taken" ]; then
    echo "gateway-install: the receiving address $inbox already belongs to team \"$taken\"'s gateway in this project; pass --inbox <an address of this team's own>" >&2
    exit 1
  fi
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
  insert into brigade_gateway.gateways (team_id, user_id, session_id, kind, inbox, public_address, from_address, provider, send_url)
    values (v_team, v_uid, v_sid, 'email', $(lit "$inbox"), $(lit "$public_address"), $(lit "$from"), $(lit "$provider"), $(lit "$send_url"))
    on conflict (team_id, kind) do update set session_id = excluded.session_id, inbox = excluded.inbox,
      public_address = excluded.public_address, from_address = excluded.from_address, provider = excluded.provider,
      send_url = excluded.send_url;
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
tick=$(mint)
shared='BRIGADE_MAIL_CONNECTOR_SECRET, BRIGADE_MAIL_INBOUND_URL, BRIGADE_TICK_TOKEN'
team_copies='BRIGADE_TEAM_REF, BRIGADE_MAIL_FROM, BRIGADE_MAIL_INBOX, BRIGADE_MAIL_PUBLIC_ADDRESS, BRIGADE_MAIL_PROVIDER, BRIGADE_MAIL_SEND_URL'
case "$provider" in
  resend) own='RESEND_API_KEY, RESEND_WEBHOOK_SECRET' ;;
  postmark) own='POSTMARK_SERVER_TOKEN, POSTMARK_WEBHOOK_SECRET' ;;
  *) own='' ;;
esac
[ -n "$env_team" ] || say "the project's secrets name another team; this team's settings live in its gateway row only"
if [ -z "$dry" ]; then
  {
    if [ -n "$env_team" ]; then
      printf 'BRIGADE_TEAM_REF=%s\n' "$team_ref"
      printf 'BRIGADE_MAIL_FROM=%s\n' "$from"
      printf 'BRIGADE_MAIL_INBOX=%s\n' "$inbox"
      printf 'BRIGADE_MAIL_PUBLIC_ADDRESS=%s\n' "$public_address"
      printf 'BRIGADE_MAIL_PROVIDER=%s\n' "$provider"
      printf 'BRIGADE_MAIL_SEND_URL=%s\n' "$send_url"
    fi
    printf 'BRIGADE_MAIL_CONNECTOR_SECRET=%s\n' "$connector_secret"
    printf 'BRIGADE_MAIL_INBOUND_URL=%s\n' "$inbound_url"
    printf 'BRIGADE_TICK_TOKEN=%s\n' "$tick"
    case "$provider" in
      resend)
        printf 'RESEND_API_KEY=%s\n' "$RESEND_API_KEY"
        printf 'RESEND_WEBHOOK_SECRET=%s\n' "$webhook_secret"
        ;;
      postmark)
        printf 'POSTMARK_SERVER_TOKEN=%s\n' "$POSTMARK_SERVER_TOKEN"
        printf 'POSTMARK_WEBHOOK_SECRET=%s\n' "$webhook_secret"
        ;;
    esac
  } > "$work/secrets.env"
  $supabase_cmd secrets set --project-ref "$ref" --env-file "$work/secrets.env" >/dev/null
  say "function secrets set: $shared${own:+, $own}${env_team:+, $team_copies}"
else
  say "would set function secrets: $shared${own:+, $own}${env_team:+, $team_copies}"
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
echo "  people write to:      $public_address"
echo "  receiving address:    $inbox"
echo "  sends from:           $from"
echo "  provider:             $provider"
echo "  core:                 $inbound_url (inbound), $fn_base/mail-out (tick)"
echo "  connector send URL:   $send_url"
if [ "$provider" = external ]; then
  echo "  your connector needs: the inbound URL and the connector secret, in $secret_file"
fi
if [ -z "$dry" ]; then
  echo "next: git add $team_file && git commit && git push (it carries only public values)"
else
  echo "(dry run: nothing was changed)"
fi
