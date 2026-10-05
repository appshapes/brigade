#!/bin/sh
# usage: scripts/slack-gateway-install.sh <project-ref> [--team-file <path>] [--dry-run]
#
#   SUPABASE_ACCESS_TOKEN  a personal access token (sbp_...), exactly as `make backend-install` takes it
#   SLACK_BOT_TOKEN        the app's Bot User OAuth Token (xoxb-...), for the second run
#   SLACK_SIGNING_SECRET   the app's Signing Secret, for the second run
#   SLACK_SECRETS_FILE     instead of the two above: a 0600 file outside the repository with one NAME=value per line
#   SUPABASE               the CLI command (the Makefile passes its own; default `npx --yes supabase@2.116.0`)
#
# Installs the hosted Slack gateway (Trello card 48; docs/slack-gateway.md) on a team's Supabase project, in two
# runs, because Slack verifies an app's request URL against a running endpoint before it hands out a token:
#
#   First run, no Slack secrets in the environment: pushes the migrations, deploys slack-in and slack-out, and
#   prints the app manifest with this project's URLs filled in. The administrator pastes it at
#   https://api.slack.com/apps -> Create New App -> From a manifest, installs the app to the workspace, and copies
#   the Bot User OAuth Token and the Signing Secret.
#
#   Second run, with SLACK_BOT_TOKEN and SLACK_SIGNING_SECRET set (or SLACK_SECRETS_FILE): pushes and deploys
#   again (idempotent), asks Slack who the bot is (auth.test, users.info), creates the gateway principal when the
#   team has none (one anonymous GoTrue sign-up, then one SQL block through the Management API: membership,
#   session, gateways row of kind slack), sets the functions' secrets from a 0600 file, schedules the per-minute
#   tick (pg_net + pg_cron), writes `gateway.slack` into .brigade.json (workspace and bot handle, public text)
#   and runs one tick so the roster shows the gateway at once.
#
# Nothing here prints a token or a secret; every bearer credential reaches curl through a 0600 header file.
set -eu

ref=''
team_file='.brigade.json'
dry=''
while [ $# -gt 0 ]; do
  case "$1" in
    --team-file) team_file="$2"; shift 2 ;;
    --dry-run) dry=1; shift ;;
    -*) echo "slack-gateway-install: unknown flag $1" >&2; exit 2 ;;
    *) if [ -z "$ref" ]; then ref="$1"; shift; else echo "slack-gateway-install: unexpected argument" >&2; exit 2; fi ;;
  esac
done
if [ -z "$ref" ]; then
  echo "usage: scripts/slack-gateway-install.sh <project-ref> [--team-file <path>] [--dry-run]" >&2
  exit 2
fi
: "${SUPABASE_ACCESS_TOKEN:?set SUPABASE_ACCESS_TOKEN to a personal access token (sbp_...) first; see docs/setup.md}"
supabase_cmd="${SUPABASE:-npx --yes supabase@2.116.0}"
for tool in curl jq od; do
  command -v "$tool" >/dev/null 2>&1 || { echo "slack-gateway-install: $tool is required" >&2; exit 1; }
done
if [ ! -f "$team_file" ]; then
  echo "slack-gateway-install: $team_file not found; pass --team-file <the project's .brigade.json> (make: team_file=), written by team create" >&2
  exit 1
fi
if [ -n "${SLACK_SECRETS_FILE:-}" ]; then
  # one NAME=value per line, 0600, outside the repository; only the two names are read
  SLACK_BOT_TOKEN=$(sed -n 's/^SLACK_BOT_TOKEN=//p' "$SLACK_SECRETS_FILE" | head -1)
  SLACK_SIGNING_SECRET=$(sed -n 's/^SLACK_SIGNING_SECRET=//p' "$SLACK_SECRETS_FILE" | head -1)
fi
bot_token="${SLACK_BOT_TOKEN:-}"
signing_secret="${SLACK_SIGNING_SECRET:-}"

team_ref=$(jq -r '.team_ref // empty' "$team_file")
url=$(jq -r '.url // empty' "$team_file")
key=$(jq -r '.publishable_key // empty' "$team_file")
team_name=$(jq -r '.team_name // "team"' "$team_file")
if [ -z "$team_ref" ] || [ -z "$url" ] || [ -z "$key" ]; then
  echo "slack-gateway-install: $team_file lacks team_ref, url or publishable_key" >&2
  exit 1
fi
case "$url" in
  "https://$ref.supabase.co") ;;
  *) echo "slack-gateway-install: $team_file names $url, not project $ref" >&2; exit 1 ;;
esac
fn_base="$url/functions/v1"

umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
printf 'Authorization: Bearer %s\n' "$SUPABASE_ACCESS_TOKEN" > "$work/sb.h"
mapi() { curl -sS -H @"$work/sb.h" -H 'Content-Type: application/json' "$@"; }
sql() {
  jq -n --arg q "$1" '{query: $q}' | mapi -X POST "https://api.supabase.com/v1/projects/$ref/database/query" -d @-
}
lit() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"; }
say() { printf 'slack-gateway-install: %s\n' "$*"; }

# migrations and functions, on both runs
if [ -n "$dry" ]; then
  $supabase_cmd db push --dry-run --project-ref "$ref"
  say "would deploy functions slack-in and slack-out to $fn_base (--use-api --no-verify-jwt)"
else
  $supabase_cmd db push --yes --project-ref "$ref"
  $supabase_cmd functions deploy slack-in slack-out --project-ref "$ref" --use-api --no-verify-jwt
fi

if [ -z "$bot_token" ] || [ -z "$signing_secret" ]; then
  # First run: the manifest, and what to do with it.
  cat <<MANIFEST

The functions are deployed. Now create the Slack app from this manifest, at https://api.slack.com/apps
(Create New App -> From a manifest -> pick the workspace -> paste -> Create), then Install to Workspace.

---------------------------------------------------------------------------------------------------------
display_information:
  name: Brigade
  description: Messages between this workspace and the team's Claude Code sessions
  background_color: "#1f2a44"
features:
  app_home:
    messages_tab_enabled: true
    messages_tab_read_only_enabled: false
  bot_user:
    display_name: brigade
    always_online: false
  slash_commands:
    - command: /brigade
      url: $fn_base/slack-in
      description: Brigade sessions and messages
      usage_hint: sessions | send <session> <text> | help
      should_escape: false
oauth_config:
  scopes:
    bot:
      - app_mentions:read
      - channels:join
      - channels:read
      - chat:write
      - commands
      - groups:read
      - im:history
      - im:read
      - im:write
      - reactions:write
      - users:read
      - users:read.email
settings:
  event_subscriptions:
    request_url: $fn_base/slack-in
    bot_events:
      - app_mention
      - message.im
  interactivity:
    is_enabled: false
  org_deploy_enabled: false
  socket_mode_enabled: false
  token_rotation_enabled: false
---------------------------------------------------------------------------------------------------------

Then, from the app's pages, copy two values: Basic Information -> Signing Secret, and OAuth & Permissions ->
Bot User OAuth Token (xoxb-...). Put them in a 0600 file OUTSIDE the repository, one per line:

  SLACK_BOT_TOKEN=xoxb-...
  SLACK_SIGNING_SECRET=...

and run this command again with SLACK_SECRETS_FILE=<that path>. Nothing else is needed from Slack.
MANIFEST
  exit 0
fi

# Second run: who the bot is
printf 'Authorization: Bearer %s\n' "$bot_token" > "$work/slack.h"
auth=$(curl -sS -H @"$work/slack.h" -X POST https://slack.com/api/auth.test)
if ! printf '%s' "$auth" | jq -e '.ok == true' >/dev/null 2>&1; then
  echo "slack-gateway-install: the bot token was refused by Slack ($(printf '%s' "$auth" | jq -r '.error // "no answer"'))" >&2
  exit 1
fi
bot_user=$(printf '%s' "$auth" | jq -r '.user_id')
workspace=$(printf '%s' "$auth" | jq -r '.url' | sed 's#^https\{0,1\}://##; s#/$##')
bot_handle=$(curl -sS -H @"$work/slack.h" "https://slack.com/api/users.info?user=$bot_user" | jq -r '.user.name // "brigade"')
say "the app is @$bot_handle in $workspace"

# the gateway principal and session
existing=$(sql "select user_id::text as user_id from brigade_gateway.gateways where team_id = $(lit "$team_ref") and kind = 'slack'" |
  jq -r 'if type == "array" then (.[0].user_id // empty) else empty end')
uid="$existing"
if [ -n "$existing" ]; then
  say "the team already has a slack gateway member; keeping it"
elif [ -n "$dry" ]; then
  say "would sign up one anonymous principal for the slack gateway and join it to team $team_name"
else
  uid=$(curl -sS -X POST "$url/auth/v1/signup" -H "apikey: $key" -H 'Content-Type: application/json' \
    -d '{"data":{},"gotrue_meta_security":{}}' | jq -r '.user.id // empty')
  if [ -z "$uid" ]; then
    echo "slack-gateway-install: the anonymous sign-up for the gateway principal failed (anonymous sign-ins must be on; make backend-install sets them)" >&2
    exit 1
  fi
fi
description="slack gateway for $workspace: a person DMs @$bot_handle or mentions it with a first line \"to: <session>\"; a session writes to me with \"to: @name\", \"to: #channel\" or \"to: <email>\""
if [ -z "$dry" ]; then
  block="do \$\$
declare v_team uuid := $(lit "$team_ref"); v_uid uuid := $(lit "$uid"); v_existing uuid; v_sid uuid; v_rec jsonb; v_live boolean := false;
begin
  select user_id, session_id into v_existing, v_sid from brigade_gateway.gateways where team_id = v_team and kind = 'slack';
  if found then v_uid := v_existing; end if;
  insert into brigade.memberships (team_id, user_id, human_label, joined_secret_version)
    select v_team, v_uid, 'slack gateway', t.secret_version from brigade.teams t where t.id = v_team
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
    v_rec := brigade.register_session(v_team, 'slack-gateway', $(lit "$description"), 'idle', 'accept', 'gateway-slack', '0.1.0', null, 600, v_sid);
    v_sid := (v_rec->>'session_id')::uuid;
  end if;
  insert into brigade_gateway.gateways (team_id, user_id, session_id, kind) values (v_team, v_uid, v_sid, 'slack')
    on conflict (team_id, kind) do update set session_id = excluded.session_id;
  perform set_config('request.jwt.claim.sub', '', true);
end \$\$"
  out=$(sql "$block")
  if ! printf '%s' "$out" | jq -e 'type == "array"' >/dev/null 2>&1; then
    echo "slack-gateway-install: the gateway SQL failed: $(printf '%s' "$out" | jq -r '.message // .error // .' 2>/dev/null | head -c 300)" >&2
    exit 1
  fi
  session=$(sql "select session_id::text as s from brigade_gateway.gateways where team_id = $(lit "$team_ref") and kind = 'slack'" | jq -r '.[0].s // empty')
  say "gateway session $session is registered as slack-gateway"
fi

# secrets
tick=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
if [ -z "$dry" ]; then
  {
    printf 'BRIGADE_TEAM_REF=%s\n' "$team_ref"
    printf 'BRIGADE_SLACK_BOT_TOKEN=%s\n' "$bot_token"
    printf 'BRIGADE_SLACK_SIGNING_SECRET=%s\n' "$signing_secret"
    printf 'BRIGADE_SLACK_TICK_TOKEN=%s\n' "$tick"
    printf 'BRIGADE_SLACK_BOT_HANDLE=%s\n' "$bot_handle"
    printf 'BRIGADE_SLACK_WORKSPACE=%s\n' "$workspace"
  } > "$work/secrets.env"
  $supabase_cmd secrets set --project-ref "$ref" --env-file "$work/secrets.env" >/dev/null
  say "6 function secrets set"
else
  say "would set 6 function secrets: BRIGADE_TEAM_REF, BRIGADE_SLACK_BOT_TOKEN, BRIGADE_SLACK_SIGNING_SECRET, BRIGADE_SLACK_TICK_TOKEN, BRIGADE_SLACK_BOT_HANDLE, BRIGADE_SLACK_WORKSPACE"
fi

# the tick
if [ -z "$dry" ]; then
  sql "create extension if not exists pg_net" >/dev/null
  cron="select cron.schedule('brigade_slack_gateway_tick', '* * * * *', \$job\$select net.http_post(url := '$fn_base/slack-out', headers := '{\"Content-Type\":\"application/json\",\"x-brigade-tick\":\"$tick\"}'::jsonb, body := '{}'::jsonb, timeout_milliseconds := 20000)\$job\$)"
  if ! sql "$cron" | jq -e 'type == "array"' >/dev/null; then
    echo "slack-gateway-install: scheduling the tick failed" >&2
    exit 1
  fi
  say "pg_cron job brigade_slack_gateway_tick scheduled (every minute)"
else
  say "would create pg_net and schedule brigade_slack_gateway_tick to POST $fn_base/slack-out every minute"
fi

# the team file, and one tick now
slack_line="$workspace: @$bot_handle"
if [ -z "$dry" ]; then
  jq --arg s "$slack_line" '.gateway = ((.gateway // {}) + {slack: $s})' "$team_file" > "$work/team.json" && cat "$work/team.json" > "$team_file"
  printf 'x-brigade-tick: %s\n' "$tick" > "$work/tick.h"
  first=$(curl -sS -X POST "$fn_base/slack-out" -H @"$work/tick.h" -H 'Content-Type: application/json' -d '{}' || true)
  say "first tick: $(printf '%s' "$first" | jq -c '{heartbeat, delivered, undeliverable, retry, error}' 2>/dev/null || printf '%s' "$first" | head -c 200)"
fi

echo ''
echo "slack gateway for team \"$team_name\" on $ref"
echo "  workspace and bot:  $slack_line"
echo "  people:             DM @$bot_handle, or mention it in a channel, with a first line \"to: <session>\"; /brigade sessions lists them"
echo "  sessions:           brigade send <slack-gateway> with a first line \"to: @name\", \"to: #channel\" or \"to: <email>\""
echo "  functions:          $fn_base/slack-in, $fn_base/slack-out"
if [ -z "$dry" ]; then
  echo "next: git add $team_file && git commit && git push (it carries only public values)"
else
  echo "(dry run: nothing was changed)"
fi
