#!/bin/sh
# usage: scripts/ci/bootstrap-alpine.sh        (LOCAL ONLY -- not run by CI; needs a working docker)
#
# The third leg of the bootstrap test matrix (plan 6.2, P1-8): plugin/bin/brigade under busybox `ash`, with
# busybox `wget` and busybox `sha256sum` and no curl at all, inside `docker run --rm alpine:3.20`. The Go test
# in internal/harness/bootstrap covers the host's /bin/sh (and dash where present) with curl and shasum; this
# covers the toolset those two can never exercise on a developer's macOS box.
#
# The repository's plugin/bin/brigade is mounted READ-ONLY; every fixture (VERSION, checksums.txt, the fake
# release asset, HOME and the XDG directories) is built inside the container, and the asset is served over
# loopback by busybox httpd (from busybox-extras, which apk installs inside the container), so nothing here touches the network, the real release, or anything on the host.
#
# Cases, one PASS/FAIL line each: first run (cold cache: downloads, verifies, installs 0755, execs, passes argv
# and stdin through), second run (the served asset is DELETED first, so a warm-cache run that still succeeds
# proves it made no request), the P5-18 hooks on a cold cache (`hook prompt` and `hook session-end` exit 0 in
# silence with the asset not even served; a `hook session-start` whose detached download fails leaves the
# failure marker, which the next `hook prompt` reports once with exit 9), wrong checksum (exit 11, nothing
# installed). Exits non-zero if any case FAILs.
set -eu

image=alpine:3.20

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd -P)
[ -f "$root/plugin/bin/brigade" ] || { printf 'bootstrap-alpine: no %s/plugin/bin/brigade\n' "$root" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { printf 'bootstrap-alpine: docker is not installed\n' >&2; exit 1; }

printf 'bootstrap-alpine: %s, bootstrap mounted read-only from %s\n' "$image" "$root/plugin/bin/brigade"

docker run --rm -i \
  -v "$root/plugin/bin/brigade:/mnt/brigade:ro" \
  "$image" sh -s <<'INNER'
set -u

version=9.9.9
port=8080
fails=0
passes=0

say() { printf '%s\n' "$*"; }
pass() { printf 'PASS  %s\n' "$*"; passes=$((passes + 1)); }
fail() { printf 'FAIL  %s\n' "$*"; fails=$((fails + 1)); }

say "busybox: $(busybox | head -n 1)"
say "shell:   $(readlink -f /bin/sh)"
# alpine:3.20's base busybox is compiled WITHOUT the httpd applet (`busybox httpd` -> "applet not found",
# and it is absent from `busybox --list`). busybox-extras is busybox's own httpd, packaged separately by
# Alpine, so this is still the busybox web server the plan asks for -- it just has to be added.
apk add --no-cache busybox-extras >/dev/null 2>&1 || { say "cannot apk add busybox-extras (needed for busybox httpd)"; exit 1; }
say "httpd:   $(command -v httpd) (busybox-extras)"
for t in wget sha256sum httpd mktemp awk uname; do
  command -v "$t" >/dev/null 2>&1 || { say "missing required busybox applet: $t"; exit 1; }
done
command -v curl >/dev/null 2>&1 && say "note: curl IS present, so the wget branch would not be exercised" || say "curl: absent (the wget branch is the one under test)"

arch=$(uname -m)
case $arch in aarch64|arm64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) say "unsupported test arch $arch"; exit 1 ;; esac
asset=brigade_${version}_linux_${arch}

# ---- fixture: the plugin tree, the fake release asset, the served directory ---------------------------------
mkdir -p /work/plugin/bin /srv/v$version
cp /mnt/brigade /work/plugin/bin/brigade
chmod 0755 /work/plugin/bin/brigade
printf '%s\n' "$version" > /work/plugin/bin/VERSION

# The "release binary": a script that prints its argv on one line and then copies stdin to stdout.
cat > "/srv/v$version/$asset" <<'FAKE'
#!/bin/sh
printf 'ARGV:'
for a in "$@"; do printf ' %s' "$a"; done
printf '\n'
cat
FAKE
sum=$(sha256sum "/srv/v$version/$asset" | awk '{print $1}')
printf '%s  %s\n' "$sum" "$asset" > /work/plugin/bin/checksums.txt
printf 'line one\nline two\n' > /work/stdin.txt

httpd -p "$port" -h /srv
sleep 1
wget -q -O - "http://127.0.0.1:$port/v$version/$asset" >/dev/null || { say "the local httpd is not serving the asset"; exit 1; }

run_bootstrap() {   # run_bootstrap <homeroot> [args...]
  h=$1; shift
  mkdir -p "$h"
  HOME=$h XDG_CONFIG_HOME=$h/config XDG_DATA_HOME=$h/data \
    BRIGADE_RELEASE_BASE_URL="http://127.0.0.1:$port" \
    PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    /work/plugin/bin/brigade "$@" < /work/stdin.txt > /work/out.txt 2> /work/err.txt
}

# ---- case 1: first run on a cold cache --------------------------------------------------------------------
status=0
run_bootstrap /work/h1 hook status --verbose || status=$?
cache=/work/h1/data/brigade/bin/brigade-$version-linux-$arch
if [ "$status" != 0 ]; then
  fail "first run: exit $status (expected 0)"; sed 's/^/      err| /' /work/err.txt
elif [ "$(head -n 1 /work/out.txt)" != "ARGV: hook status --verbose" ]; then
  fail "first run: argv line was '$(head -n 1 /work/out.txt)'"
elif [ "$(sed -n '2,3p' /work/out.txt)" != "$(printf 'line one\nline two')" ]; then
  fail "first run: stdin did not pass through"
elif [ ! -x "$cache" ]; then
  fail "first run: no cached binary at $cache"
elif [ "$(stat -c %a "$cache")" != 755 ]; then
  fail "first run: cached binary mode is $(stat -c %a "$cache"), expected 755"
elif [ -n "$(find /work/h1/data/brigade/bin -name '.brigade-*' -print -quit)" ]; then
  fail "first run: a .brigade-* temp file was left behind"
else
  pass "first run: downloaded over busybox wget, verified with busybox sha256sum, installed 0755, argv and stdin passed through"
fi

# ---- case 2: second run makes no request (the asset is deleted first) --------------------------------------
rm -f "/srv/v$version/$asset"
status=0
run_bootstrap /work/h1 whoami || status=$?
if [ "$status" != 0 ]; then
  fail "second run: exit $status (expected 0)"; sed 's/^/      err| /' /work/err.txt
elif [ "$(head -n 1 /work/out.txt)" != "ARGV: whoami" ]; then
  fail "second run: argv line was '$(head -n 1 /work/out.txt)'"
elif grep -q 'first use' /work/err.txt; then
  fail "second run: the script announced a download"
else
  pass "second run: served asset deleted, cache hit, zero requests"
fi
cat > "/srv/v$version/$asset" <<'FAKE'
#!/bin/sh
printf 'ARGV:'
for a in "$@"; do printf ' %s' "$a"; done
printf '\n'
cat
FAKE

# ---- case 4 (P5-18): a cold-cache `hook prompt` / `hook session-end` never downloads --------------------------
# The served asset is deleted first: a hook that took the download path would exit 9 with a `download failed`
# line, not the exit 0 and silence required here.
rm -f "/srv/v$version/$asset"
for sub in prompt session-end; do
  status=0
  run_bootstrap /work/h4 hook "$sub" || status=$?
  if [ "$status" != 0 ]; then
    fail "cold-cache hook $sub: exit $status (expected 0)"; sed 's/^/      err| /' /work/err.txt
  elif [ -s /work/out.txt ] || [ -s /work/err.txt ]; then
    fail "cold-cache hook $sub: printed something: out='$(cat /work/out.txt)' err='$(cat /work/err.txt)'"
  elif [ -e "/work/h4/data/brigade/bin/brigade-$version-linux-$arch" ]; then
    fail "cold-cache hook $sub: installed a binary"
  else
    pass "cold-cache hook $sub: exit 0, silent, nothing downloaded (the asset was not even served)"
  fi
done

# ---- case 5 (P5-18): a failed detached install is reported once, by the next `hook prompt`, with exit 9 -------
marker=/work/h4/data/brigade/bin/brigade-$version-linux-$arch.failed
status=0
run_bootstrap /work/h4 hook session-start || status=$?
i=0
while [ ! -s "$marker" ] && [ "$i" -lt 100 ]; do sleep 0.1; i=$((i + 1)); done
if [ "$status" != 0 ]; then
  fail "hook session-start: exit $status (expected 0)"; sed 's/^/      err| /' /work/err.txt
elif ! grep -q 'installing the brigade binary in the background' /work/out.txt; then
  fail "hook session-start: no context line: '$(cat /work/out.txt)'"
elif [ ! -s "$marker" ]; then
  fail "hook session-start: the detached worker left no failure marker at $marker"
elif ! grep -q '^9 download failed: ' "$marker"; then
  fail "failure marker content: '$(cat "$marker")'"
elif [ -n "$(find /work/h4/data/brigade/bin -name '.brigade-*' -print -quit)" ]; then
  fail "the failed worker left a .brigade-* temp file behind"
else
  status=0
  run_bootstrap /work/h4 hook prompt || status=$?
  if [ "$status" != 9 ]; then
    fail "hook prompt after a failed install: exit $status (expected 9)"; sed 's/^/      err| /' /work/err.txt
  elif ! grep -q '^Brigade: not installed: download failed: ' /work/err.txt; then
    fail "hook prompt after a failed install: stderr is '$(cat /work/err.txt)'"
  elif [ -s /work/out.txt ]; then
    fail "hook prompt after a failed install: stdout is '$(cat /work/out.txt)'"
  elif [ -e "$marker" ]; then
    fail "hook prompt after a failed install: the marker survived the report"
  else
    status=0
    run_bootstrap /work/h4 hook prompt || status=$?
    if [ "$status" != 0 ] || [ -s /work/out.txt ] || [ -s /work/err.txt ]; then
      fail "second hook prompt: exit $status out='$(cat /work/out.txt)' err='$(cat /work/err.txt)' (expected 0 and silence)"
    else
      pass "failed detached install: session-start returned at once, the marker was written, hook prompt reported it once with exit 9, then silence"
    fi
  fi
fi
cat > "/srv/v$version/$asset" <<'FAKE'
#!/bin/sh
printf 'ARGV:'
for a in "$@"; do printf ' %s' "$a"; done
printf '\n'
cat
FAKE

# ---- case 3: a wrong checksum installs nothing -------------------------------------------------------------
printf '%s  %s\n' 0000000000000000000000000000000000000000000000000000000000000000 "$asset" > /work/plugin/bin/checksums.txt
status=0
run_bootstrap /work/h3 whoami || status=$?
if [ "$status" != 11 ]; then
  fail "wrong checksum: exit $status (expected 11)"; sed 's/^/      err| /' /work/err.txt
elif ! grep -q 'checksum mismatch' /work/err.txt; then
  fail "wrong checksum: stderr does not name a checksum mismatch: $(cat /work/err.txt)"
elif [ -n "$(find /work/h3/data/brigade/bin -type f -print -quit 2>/dev/null)" ]; then
  fail "wrong checksum: something was installed under /work/h3/data/brigade/bin"
else
  pass "wrong checksum: exit 11, nothing installed, no temp file"
fi

say ""
if [ "$fails" = 0 ]; then
  say "bootstrap-alpine: $passes/$passes cases PASS"
  exit 0
fi
say "bootstrap-alpine: $fails of $((passes + fails)) case(s) FAILED"
exit 1
INNER
