#!/bin/sh
# usage: scripts/gateway-senders.sh <project-ref> [--team-file <path>] [--add <entries>] [--remove <entries>] [--clear]
#
# Who may write to a team's mail gateway (Trello card 55; docs/mail-gateway.md "Who may write"). With no change flag
# it prints the list. <entries> is addresses and domains separated by commas or spaces: `qa@example.com` admits one
# address, `@example.com` admits every address on exactly that domain (a bare `example.com` is read as
# `@example.com`). mail-in matches a mail's From against the list case-insensitively and drops a mail from anyone
# else before it looks anything up or answers. An empty list leaves the gateway open to anyone who knows its
# address, as every gateway was before card 55.
#
#   SUPABASE_ACCESS_TOKEN  a personal access token (sbp_...), as for make gateway-install. Nothing else: the list is
#                          a column of the team's brigade_gateway.gateways row, which mail-in reads on every mail, so
#                          a change takes effect on the next mail with no redeploy and no provider key.
#
# The column comes with migration 20261015120000. A gateway installed before it needs `make gateway-install` run
# again first, which pushes the migration and deploys the mail-in that reads the list; this script says so and
# stops. Nothing here prints the token. Run from a checkout of Brigade; the team's project may be another repository.
set -eu

ref=''
team_file='.brigade.json'
add=''
remove=''
clear=''
usage="usage: scripts/gateway-senders.sh <project-ref> [--team-file <path>] [--add <entries>] [--remove <entries>] [--clear]"
while [ $# -gt 0 ]; do
  case "$1" in
    --team-file) team_file="$2"; shift 2 ;;
    --add) add="$2"; shift 2 ;;
    --remove) remove="$2"; shift 2 ;;
    --clear) clear=1; shift ;;
    -*) echo "gateway-senders: unknown flag $1" >&2; exit 2 ;;
    *) if [ -z "$ref" ]; then ref="$1"; shift; else echo "gateway-senders: unexpected argument" >&2; exit 2; fi ;;
  esac
done
if [ -z "$ref" ]; then
  echo "$usage" >&2
  exit 2
fi
if [ -n "$clear" ] && { [ -n "$add" ] || [ -n "$remove" ]; }; then
  echo "gateway-senders: --clear takes no --add or --remove" >&2
  exit 2
fi
# zsh does not expand ~ after `team_file=` in a make argument; do it here (the quoted tilde is the literal we
# look for, hence the shellcheck directive).
# shellcheck disable=SC2088
case $team_file in '~/'*) team_file=$HOME/${team_file#'~/'} ;; esac
: "${SUPABASE_ACCESS_TOKEN:?set SUPABASE_ACCESS_TOKEN to a personal access token (sbp_...) first; see docs/setup.md}"
for tool in curl jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "gateway-senders: $tool is required" >&2; exit 1; }
done
if [ ! -f "$team_file" ]; then
  echo "gateway-senders: $team_file not found; pass --team-file <the project's .brigade.json> (make: team_file=)" >&2
  exit 1
fi
team_ref=$(jq -r '.team_ref // empty' "$team_file")
url=$(jq -r '.url // empty' "$team_file")
team_name=$(jq -r '.team_name // "team"' "$team_file")
if [ -z "$team_ref" ] || [ -z "$url" ]; then
  echo "gateway-senders: $team_file lacks team_ref or url" >&2
  exit 1
fi
case "$url" in
  "https://$ref.supabase.co") ;;
  *) echo "gateway-senders: $team_file names $url, not project $ref" >&2; exit 1 ;;
esac

umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
printf 'Authorization: Bearer %s\n' "$SUPABASE_ACCESS_TOKEN" > "$work/sb.h"
# sql runs one statement through the Management API as postgres and prints its JSON rows; anything else is fatal.
sql() {
  out=$(jq -n --arg q "$1" '{query: $q}' |
    curl -sS -H @"$work/sb.h" -H 'Content-Type: application/json' -X POST \
      "https://api.supabase.com/v1/projects/$ref/database/query" -d @-)
  if ! printf '%s' "$out" | jq -e 'type == "array"' >/dev/null 2>&1; then
    echo "gateway-senders: the query failed: $(printf '%s' "$out" | jq -r '.message // .error // .' 2>/dev/null | head -c 300)" >&2
    exit 1
  fi
  printf '%s' "$out"
}
# lit quotes a value as an SQL string literal.
lit() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"; }
# entries prints the canonical form of each entry in a comma- or space-separated list, one per line: lowercase,
# a bare domain prefixed with @. Anything that is neither an address nor a domain stops the script.
entries() {
  list=$(printf '%s' "$1" | tr ',' ' ')
  # The split on the commas and spaces is the point, hence the shellcheck directive.
  # shellcheck disable=SC2086
  for e in $list; do
    e=$(printf '%s' "$e" | tr '[:upper:]' '[:lower:]')
    case "$e" in
      *[\'\"\\]*|*@*@*) echo "gateway-senders: \"$e\" is not an address (qa@example.com) or a domain (@example.com)" >&2; exit 2 ;;
      @?*.?*) printf '%s\n' "$e" ;;
      ?*@?*.?*) printf '%s\n' "$e" ;;
      ?*.?*) printf '@%s\n' "$e" ;;
      *) echo "gateway-senders: \"$e\" is not an address (qa@example.com) or a domain (@example.com)" >&2; exit 2 ;;
    esac
  done
}
# array renders the lines on stdin as an SQL text[] literal.
array() {
  items=''
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    items="${items:+$items, }$(lit "$line")"
  done
  printf 'array[%s]::text[]' "$items"
}

columns=$(sql "select 1 as ok from information_schema.columns where table_schema = 'brigade_gateway' and table_name = 'gateways' and column_name = 'allowed_senders'")
if [ "$(printf '%s' "$columns" | jq 'length')" = 0 ]; then
  echo "gateway-senders: the gateway on project $ref predates the allowed-senders list (migration 20261015120000): run make gateway-install for team $team_name again first" >&2
  exit 1
fi
where="where team_id = $(lit "$team_ref") and kind = 'email'"
shown="coalesce(to_jsonb(allowed_senders), '[]'::jsonb) as allowed_senders"
if [ -n "$clear" ]; then
  q="with u as (update brigade_gateway.gateways set allowed_senders = null $where returning allowed_senders) select $shown from u"
elif [ -n "$add" ] || [ -n "$remove" ]; then
  plus_lines=$(entries "$add") || exit 2
  minus_lines=$(entries "$remove") || exit 2
  plus=$(printf '%s\n' "$plus_lines" | array)
  minus=$(printf '%s\n' "$minus_lines" | array)
  # The row's list plus the additions, deduplicated and sorted, minus the removals; an empty result is stored as null.
  q="with u as (update brigade_gateway.gateways set allowed_senders = nullif((select coalesce(array_agg(e order by e), '{}'::text[]) from (select distinct e from unnest(coalesce(allowed_senders, '{}'::text[]) || $plus) as e where e <> all ($minus)) s), '{}'::text[]) $where returning allowed_senders) select $shown from u"
else
  q="select $shown from brigade_gateway.gateways $where"
fi
rows=$(sql "$q")
if [ "$(printf '%s' "$rows" | jq 'length')" = 0 ]; then
  echo "gateway-senders: team $team_name has no mail gateway on project $ref: run make gateway-install first" >&2
  exit 1
fi
list=$(printf '%s' "$rows" | jq -r '.[0].allowed_senders | join(", ")')
echo "mail gateway for team \"$team_name\" on $ref"
if [ -n "$list" ]; then
  echo "  allowed senders:      $list"
else
  echo "  allowed senders:      (none: anyone who knows the address may write, and is sent the session list)"
fi
