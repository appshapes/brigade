#!/bin/sh
# Runs Brigade's own Codex name reader on the operator's real Codex home ($HOME/.codex, as the rest of the
# driver) for one thread id and prints the name the roster would carry. The probe test is laid over the package
# with `go test -overlay`: nothing is written into the checkout. Exits 1 when the probe did not run.
# Usage: reader-probe.sh <repository checkout> <thread id>
REPO=${1:?usage: reader-probe.sh <repository checkout> <thread id>}
ID=${2:?usage: reader-probe.sh <repository checkout> <thread id>}
case "$ID" in
  ????????-????-????-????-????????????) ;;
  *) echo "not a thread id (a 36-character UUID): $ID" >&2; exit 2 ;;
esac
HERE=$(cd "$(dirname "$0")" && pwd -P) || exit 2
REPO=$(cd "$REPO" && pwd -P) || exit 2
OVERLAY=$(mktemp "${TMPDIR:-/tmp}/card190-overlay.XXXXXX") || exit 2
trap 'rm -f "$OVERLAY"' EXIT
python3 -I -c 'import json,sys; print(json.dumps({"Replace":{sys.argv[1]:sys.argv[2]}}))' \
  "$REPO/internal/harness/watch/zz_card190_probe_test.go" "$HERE/reader-probe.go.txt" > "$OVERLAY" || exit 2
cd "$REPO" || exit 2
OUT=$(CARD190_CODEX_HOME="$HOME/.codex" CARD190_THREAD_ID=$ID \
  go test -overlay "$OVERLAY" -count=1 -run '^TestCard190ReaderProbe$' -v ./internal/harness/watch 2>&1)
LINE=$(printf '%s\n' "$OUT" | grep 'roster name from the reader')
if [ -z "$LINE" ]; then
  printf '%s\n' "$OUT" | tail -5 >&2
  echo "the probe did not run" >&2
  exit 1
fi
printf '%s\n' "$LINE" | sed -e 's/^[[:space:]]*//'
