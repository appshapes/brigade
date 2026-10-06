#!/bin/sh
# usage: scripts/mail-connector-check.sh <send-url> (--secret-file <path> | with BRIGADE_MAIL_CONNECTOR_SECRET set)
#                                        [--send-to <address> --from 'Name <address>']
#
# Checks a mail connector's send endpoint against docs/mail-connectors.md, from the outside, the way the gateway
# core calls it: a request without the secret is refused with 401, a request with the secret and a malformed body
# with 400, and, when --send-to is given, one real mail goes out and the answer carries an id. The inbound half
# is checked by sending a real mail to the connector's address and watching it reach a session, which only the
# team can do; this script does not post to the core.
#
# The secret comes from --secret-file (the file `make gateway-install provider=external` wrote, with a
# `BRIGADE_MAIL_CONNECTOR_SECRET=` line) or from the environment; never from argv.
set -eu

send_url=''
secret_file=''
send_to=''
from=''
while [ $# -gt 0 ]; do
  case "$1" in
    --secret-file) secret_file="$2"; shift 2 ;;
    --send-to) send_to="$2"; shift 2 ;;
    --from) from="$2"; shift 2 ;;
    -*) echo "mail-connector-check: unknown flag $1" >&2; exit 2 ;;
    *) if [ -z "$send_url" ]; then send_url="$1"; shift; else echo "mail-connector-check: unexpected argument" >&2; exit 2; fi ;;
  esac
done
[ -n "$send_url" ] || { echo "usage: scripts/mail-connector-check.sh <send-url> (--secret-file <path> | BRIGADE_MAIL_CONNECTOR_SECRET set) [--send-to <address> --from 'Name <address>']" >&2; exit 2; }
if [ -n "$secret_file" ]; then
  # shellcheck disable=SC2088
  case $secret_file in '~/'*) secret_file=$HOME/${secret_file#'~/'} ;; esac
  BRIGADE_MAIL_CONNECTOR_SECRET=$(sed -n 's/^BRIGADE_MAIL_CONNECTOR_SECRET=//p' "$secret_file" | head -1)
fi
: "${BRIGADE_MAIL_CONNECTOR_SECRET:?set BRIGADE_MAIL_CONNECTOR_SECRET, or pass --secret-file <path>}"
if [ -n "$send_to" ] && [ -z "$from" ]; then
  echo "mail-connector-check: --send-to needs --from 'Name <address>', an address the provider may send from" >&2
  exit 2
fi
command -v curl >/dev/null 2>&1 || { echo "mail-connector-check: curl is required" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "mail-connector-check: jq is required" >&2; exit 1; }

umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
printf 'Authorization: Bearer %s\n' "$BRIGADE_MAIL_CONNECTOR_SECRET" > "$work/auth.h"
fails=0
# post <name> <expected status> <body> [auth]: one request, one PASS/FAIL line.
post() {
  name=$1; want=$2; body=$3; auth=${4:-}
  if [ -n "$auth" ]; then
    got=$(curl -sS -o "$work/out" -w '%{http_code}' -X POST "$send_url" -H @"$work/auth.h" -H 'Content-Type: application/json' -d "$body" || echo 000)
  else
    got=$(curl -sS -o "$work/out" -w '%{http_code}' -X POST "$send_url" -H 'Content-Type: application/json' -d "$body" || echo 000)
  fi
  if [ "$got" = "$want" ]; then
    printf 'PASS  %-44s %s\n' "$name" "$got"
  else
    printf 'FAIL  %-44s wanted %s, got %s: %s\n' "$name" "$want" "$got" "$(head -c 200 "$work/out" | tr '\n' ' ')"
    fails=$((fails + 1))
  fi
}

post "no secret is refused" 401 '{"brigade_mail":1}'
post "a body that is not JSON is refused" 400 'not json' auth
post "a body missing the recipient is refused" 400 '{"brigade_mail":1,"from":"a@example.com","subject":"x","text":"x"}' auth
post "an unknown contract version is refused" 400 '{"brigade_mail":2,"from":"a@example.com","to":"b@example.com","subject":"x","text":"x","reply_to":null,"headers":{}}' auth
if [ -n "$send_to" ]; then
  body=$(jq -n --arg f "$from" --arg t "$send_to" '{brigade_mail: 1, from: $f, to: $t, reply_to: null, subject: "[brigade] connector check", text: "This mail was sent by scripts/mail-connector-check.sh to check a Brigade mail connector.\n", headers: {"Auto-Submitted": "auto-generated"}}')
  post "a real mail is accepted" 200 "$body" auth
  if jq -e '.id | strings | length > 0' "$work/out" >/dev/null 2>&1; then
    printf 'PASS  %-44s %s\n' "the answer carries an id" "$(jq -r '.id' "$work/out" | head -c 60)"
  else
    printf 'FAIL  %-44s %s\n' "the answer carries an id" "$(head -c 200 "$work/out" | tr '\n' ' ')"
    fails=$((fails + 1))
  fi
fi
if [ "$fails" -eq 0 ]; then
  echo "mail-connector-check: all checks passed"
else
  echo "mail-connector-check: $fails check(s) failed" >&2
  exit 1
fi
