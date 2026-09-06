#!/bin/sh
# usage: bin/brigade-conformance --env SUPABASE_URL=… --env SUPABASE_PUBLISHABLE_KEY=… \
#          --setup scripts/ci/conformance-setup-supabase.sh --adapter bin/brigade -- adapter supabase
#
# The conformance suite's --setup hook for the bundled Supabase adapter (plan 9.2, P2-6): the documented
# OUT-OF-BAND alternative. `make test-integration` no longer passes it — with the BRIGADE_SUPABASE_URL /
# BRIGADE_SUPABASE_PUBLISHABLE_KEY pair in the environment, `team create`/`team join` bind an empty profile
# themselves and the suite provisions its own teams, learns the join secret and runs C-28/C-40 instead of
# skipping them. Use this hook when principals must be provisioned outside the suite. The suite runs it
# ONCE PER FIXTURE PRINCIPAL — a, b, then c — in that principal's from-scratch environment (HOME,
# BRIGADE_CONFIG_DIR, BRIGADE_STATE_DIR, BRIGADE_PROFILE=default, BRIGADE_LOG_LEVEL=debug, the --env pairs,
# PATH and TMPDIR), with no arguments and no stdin, and expects the three profiles to come out JOINED: a and
# b in one team, c in another (internal/conformance/fixture.go). What it does, per principal:
#
#   1. `profile init --url $SUPABASE_URL --key $SUPABASE_PUBLISHABLE_KEY` — the backend pair of 5.2 written
#      to team.json (under a live session no BRIGADE_<ADAPTER>_* variable ever arrives, 4.1);
#   2. a: `team create` with --secret-file into a scratch directory OUTSIDE the run directory (the suite
#      scans the whole run directory for join-secret shapes at the end and reports a hit as C-05);
#      b: `team join` with that secret on stdin, then the secret file is removed;
#      c: `team create` a second team, then the scratch directory is removed.
#
# The principal's name is the parent of its config directory (<run>/principals/<name>/config) and the run
# directory names the scratch directory, so two concurrent runs never share a secret. The adapter binary is
# $BRIGADE_BIN when set, else bin/brigade beside this script's repository root. Nothing here echoes the secret:
# the file is written by the adapter (0600) and read back by the adapter on stdin.
set -eu

die() { printf 'conformance-setup-supabase: %s\n' "$1" >&2; exit 1; }

[ -n "${SUPABASE_URL:-}" ] || die "SUPABASE_URL is not set; pass it with --env SUPABASE_URL=…"
[ -n "${SUPABASE_PUBLISHABLE_KEY:-}" ] || die "SUPABASE_PUBLISHABLE_KEY is not set; pass it with --env SUPABASE_PUBLISHABLE_KEY=…"
[ -n "${BRIGADE_CONFIG_DIR:-}" ] || die "BRIGADE_CONFIG_DIR is not set; this script is the suite's --setup hook"

case ${BRIGADE_CONFIG_DIR} in /*) ;; *) die "BRIGADE_CONFIG_DIR must be absolute" ;; esac

# ---- the binary --------------------------------------------------------------------------------------------
if [ -n "${BRIGADE_BIN:-}" ]; then
  brigade=$BRIGADE_BIN
else
  unset CDPATH
  here=$(cd -- "$(dirname -- "$0")" && pwd -P) || die "cannot resolve the script directory"
  brigade=$here/../../bin/brigade
fi
[ -x "$brigade" ] || die "adapter binary not found or not executable: $brigade (run make build, or set BRIGADE_BIN)"

# ---- which principal, which run ----------------------------------------------------------------------------
principal_dir=$(dirname -- "$BRIGADE_CONFIG_DIR")
principal=$(basename -- "$principal_dir")
run_dir=$(dirname -- "$(dirname -- "$principal_dir")")
run_id=$(basename -- "$run_dir")
case $run_id in ''|*[!0-9A-Za-z._-]*) die "unexpected run directory name" ;; esac

# The scratch directory is under TMPDIR, never under the run directory (C-05 scans it), keyed by the run.
scratch=${TMPDIR:-/tmp}/brigade-conformance-setup-$run_id
secret_file=$scratch/t1.secret

adapter() { "$brigade" adapter supabase "$@"; }

# ---- 1. the backend pair -------------------------------------------------------------------------------------
adapter profile init --url "$SUPABASE_URL" --key "$SUPABASE_PUBLISHABLE_KEY" >/dev/null

# ---- 2. the team binding -------------------------------------------------------------------------------------
case $principal in
  a)
    umask 077
    mkdir -p "$scratch"
    printf '{"team_name":"t1-%s","human_label":"alice@example.com"}\n' "$run_id" \
      | adapter team create --secret-file "$secret_file" >/dev/null
    ;;
  b)
    [ -r "$secret_file" ] || die "no join secret from principal a at $secret_file; was --setup run for a first?"
    { printf '{"join_secret":"'; tr -d '\n' < "$secret_file"; printf '","human_label":"bob@example.com"}\n'; } \
      | adapter team join >/dev/null
    rm -f "$secret_file"
    ;;
  c)
    printf '{"team_name":"t2-%s","human_label":"carol@example.com"}\n' "$run_id" \
      | adapter team create --secret-file "$scratch/t2.secret" >/dev/null
    rm -f "$scratch/t2.secret" "$secret_file"
    rmdir "$scratch" 2>/dev/null || true
    ;;
  *)
    die "unexpected principal name '$principal' (expected a, b or c under <run>/principals/)"
    ;;
esac
