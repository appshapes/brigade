#!/bin/sh
# usage: scripts/ci/supabase-start.sh reserve
#        scripts/ci/supabase-start.sh start <command> [<argument>…]
#
# The start of the local Supabase stack in ci.yml's `supabase` and `e2e` jobs, made to stand up to a port of
# the stack that something else on the runner holds (work item 437).
#
# What was seen: four times in about 200 starts of the two jobs, 2026-11-22 to 2026-11-24, `supabase start` ended
# about a second in, before any test, with
#
#   failed to start docker container "supabase_db_brigade": Error response from daemon: failed to set up
#   container networking: driver failed programming external connectivity on endpoint supabase_db_brigade
#   (…): failed to bind host port for 0.0.0.0:54322:172.18.0.2:5432/tcp: address already in use
#
# each time on a runner started for that job alone, on four different hosts, and each time the job passed when
# it was run again. The port is the database's, the first the stack asks for.
#
# Why a fresh runner has it taken: the stack's ports (54320 to 54329 in supabase/config.toml) lie inside the
# range Linux gives to outgoing connections (net.ipv4.ip_local_port_range, 32768 to 60999). Every connection
# the runner makes, for the actions it downloads, the checkout, a cache it restores, its own agents, takes a
# port of that range, and keeps it for as long as it is open and for a minute after it closed (TIME-WAIT: 60
# seconds, and up to five more before the kernel's timer for it fires). While one of them sits on 54322,
# Docker's bind of 0.0.0.0:54322 is refused with exactly that message. Shown on Docker 28.0.4, the runner's
# version, on 2026-11-24: an open connection and one closed 55 seconds before both give the line above; 62
# seconds after the close the port was free in one run and still held in another. The four logs name the port
# and not who held it, so that this is what happened on the runner is inferred; `start` prints who holds the
# port when it happens again.
#
#   reserve   Takes the stack's ports out of that range (net.ipv4.ip_local_reserved_ports, which Linux has for
#             this), as early in the job as the checkout allows, so that no later connection lands on one. It
#             prints the range, how many of its ports are held at that moment, and what it reserved. Where it
#             cannot (not Linux, no sysctl, a refusal) it says so in a `::warning::` and exits 0: `start`
#             still stands up to the fault without it.
#   start     Runs the command. When it fails and a line of its output has `address already in use` and names
#             a port of the stack (`:54322`), that is said in a `::warning::` with Docker's own words and with
#             the sockets that hold that port (state, peer and process, from `ss`), and the command is run
#             again after SUPABASE_START_WAIT_SECONDS (35): at most SUPABASE_START_ATTEMPTS times (3) in all,
#             so the last attempt begins 70 seconds after the first failed, when a connection that was closed
#             by then has given its port back. When the last one fails too, an `::error::` and exit 1. Any
#             other failure of the command is not tried again: it ends the script at once with the command's
#             own exit status. The Supabase CLI removes the containers and the database's volume of a start
#             that failed, so nothing is cleaned between two attempts and the next one starts from nothing.
#
# A start of infrastructure is not a test's assertion: no test is run again here, and no test's timing changes
# (the owner's rule for a test that fails some of the time is untouched). So one start of the `supabase` job
# is outside `start`: the adapter's fault test stops the stack and starts it again with `make supabase-start`
# (internal/adapters/supabase/fault_integration_test.go), inside `make test-integration-adapter`. `reserve`
# alone keeps a connection off the ports for that one.
#
# What the worst case that still reaches the tests costs: two refused attempts and two waits before the one
# that starts the stack. An attempt refused at the database's port ends in a second or two; one refused at the
# API's port, 54321, comes after the database is up, 13 seconds in on run 45383370769, and is counted as 20.
# That is 1.9 minutes, counted into the sums beside the `supabase` job's `timeout-minutes` and its gateway
# step in ci.yml; `e2e` runs no `go test` and has no sum.
# It also takes a job past the five minutes ci.yml's shape asks of each (gates.md), on that path alone, where
# the job was red before.
#
# Output: every line goes to stdout, the annotations included, so that GitHub renders them on the run page.
set -eu

say()  { printf 'supabase-start: %s\n' "$1"; }
warn() { printf '::warning::supabase-start: %s\n' "$1"; }
fail() { printf '::error::supabase-start: %s\n' "$1"; }

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
config=$root/supabase/config.toml

# stack_ports prints every port supabase/config.toml names, one a line, in order. Commented lines are not read.
stack_ports() {
  sed -n 's/^[a-z_]*port = \([0-9][0-9]*\).*$/\1/p' "$config" | sort -un
}

# sockets prints the TCP sockets of this machine, open and closing, one a line as `ss` prints them: state, two
# queue lengths, local address:port, peer address:port and, with root, the process. It fails where `ss` is not
# or does not answer: a caller then says that it cannot look, and never that nothing is held.
sockets() {
  command -v ss >/dev/null 2>&1 || return 1
  sudo -n ss -H -tanp 2>/dev/null || ss -H -tan 2>/dev/null
}

# held_in_range <sockets> <low> <high> prints how many ports from low to high are the local port of a socket.
held_in_range() {
  printf '%s\n' "$1" | awk -v low="$2" -v high="$3" '
    { n = split($4, part, ":"); port = part[n] + 0; if (port >= low && port <= high) seen[port] = 1 }
    END { count = 0; for (port in seen) count++; print count }'
}

# holders <sockets> <ports> prints the sockets whose local port is one of the ports, each on a line of its own.
holders() {
  printf '%s\n' "$1" | awk -v ports="$2" '
    BEGIN { n = split(ports, list, " "); for (i = 1; i <= n; i++) want[list[i]] = 1 }
    { n = split($4, part, ":"); if (part[n] in want) print }'
}

# kernel_range prints the range Linux gives to outgoing connections as `<low> <high>`, or nothing.
kernel_range() {
  command -v sysctl >/dev/null 2>&1 || return 0
  sysctl -n net.ipv4.ip_local_port_range 2>/dev/null | awk 'NF == 2 { print $1, $2 }' || true
}

# exposure says how many ports of the kernel's range are held now, and by what any of the stack's is.
exposure() {
  range=$(kernel_range)
  if [ -z "$range" ]; then
    say 'no net.ipv4.ip_local_port_range here: nothing to say about held ports'
    return 0
  fi
  low=${range% *}
  high=${range#* }
  if ! all=$(sockets); then
    say "the kernel gives outgoing connections the ports $low to $high; there is no ss here that answers, so what is held now cannot be seen"
    return 0
  fi
  say "the kernel gives outgoing connections the ports $low to $high; held now, by a connection that is open or was closed within the last minute: $(held_in_range "$all" "$low" "$high")"
  held=$(holders "$all" "$(stack_ports | tr '\n' ' ')")
  if [ -n "$held" ]; then
    say "held now among the stack's ports ($(stack_ports | tr '\n' ' ' | sed 's/ $//')):"
    printf '%s\n' "$held" | sed 's/^/supabase-start:   /'
  else
    say "none of the stack's ports ($(stack_ports | tr '\n' ' ' | sed 's/ $//')) is held now"
  fi
}

reserve() {
  ports=$(stack_ports | tr '\n' ',' | sed 's/,$//')
  if [ -z "$ports" ]; then
    warn "found no port in $config: nothing is reserved"
    return 0
  fi
  if [ "$(uname -s)" != Linux ] || ! command -v sysctl >/dev/null 2>&1; then
    warn "this is not Linux with sysctl: the stack's ports $ports are not reserved"
    return 0
  fi
  exposure
  already=$(sysctl -n net.ipv4.ip_local_reserved_ports 2>/dev/null || true)
  if [ -n "$already" ]; then
    ports=$already,$ports
  fi
  if ! sudo -n sysctl -w "net.ipv4.ip_local_reserved_ports=$ports"; then
    warn "could not set net.ipv4.ip_local_reserved_ports=$ports: the stack's ports are not reserved, and a connection of the runner's can still land on one"
    return 0
  fi
  say "reserved: no connection made from here on takes one of $(sysctl -n net.ipv4.ip_local_reserved_ports 2>/dev/null || printf '%s' "$ports")"
}

start() {
  attempts=${SUPABASE_START_ATTEMPTS:-3}
  wait_seconds=${SUPABASE_START_WAIT_SECONDS:-35}
  case $attempts in ''|*[!0-9]*) fail "SUPABASE_START_ATTEMPTS is '$attempts', not a number from 1 up"; exit 1 ;; esac
  [ "$attempts" -ge 1 ] || { fail "SUPABASE_START_ATTEMPTS is '$attempts', not a number from 1 up"; exit 1; }
  case $wait_seconds in ''|*[!0-9]*) fail "SUPABASE_START_WAIT_SECONDS is '$wait_seconds', not a number"; exit 1 ;; esac
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  exposure
  attempt=1
  while :; do
    say "attempt $attempt of $attempts: $*"
    # The command's output goes to the log as it is written and to a file this script reads afterwards; its
    # exit status travels in a file of its own, because a pipeline's status is its last command's.
    { set +e; "$@" 2>&1; printf '%s\n' "$?" >"$work/status"; } | tee "$work/output"
    status=$(cat "$work/status")
    if [ "$status" -eq 0 ]; then
      if [ "$attempt" -gt 1 ]; then
        warn "the stack started at attempt $attempt of $attempts, after a port of it was held at every attempt before (work item 437)"
      fi
      return 0
    fi
    # The refusal: a line that says the address is in use and names a port of the stack, as Docker's does
    # (`0.0.0.0:54322:172.18.0.2:5432/tcp`). The same words about anything else are another failure.
    refused=
    port=
    for candidate in $(stack_ports); do
      refused=$(grep -F 'address already in use' "$work/output" | grep -E ":$candidate([^0-9]|\$)" | head -n 1 || true)
      if [ -n "$refused" ]; then
        port=$candidate
        break
      fi
    done
    if [ -z "$refused" ]; then
      fail "attempt $attempt of $attempts failed with exit status $status and not on a port of the stack that is held: not tried again"
      exit "$status"
    fi
    refused=$(printf '%s\n' "$refused" | sed 's/^.*\(failed to bind host port\)/\1/')
    warn "attempt $attempt of $attempts: port $port of the stack is held by something else on this machine (work item 437): $refused"
    if ! all=$(sockets); then
      say "there is no ss here that answers, so what holds port $port cannot be seen"
    else
      held=$(holders "$all" "$port")
      if [ -n "$held" ]; then
        say "what holds port $port now (state, queues, local, peer, process):"
        printf '%s\n' "$held" | sed 's/^/supabase-start:   /'
      else
        say "nothing holds port $port now: its holder is gone"
      fi
    fi
    if [ "$attempt" -ge "$attempts" ]; then
      fail "the stack did not start in $attempts attempt(s), each refused a port that something else held: see the lines above for what held it"
      exit 1
    fi
    say "waiting $wait_seconds seconds: a connection that has closed gives its port back a minute after its close"
    sleep "$wait_seconds"
    attempt=$((attempt + 1))
  done
}

case ${1:-} in
  reserve)
    [ $# -eq 1 ] || { fail 'usage: scripts/ci/supabase-start.sh reserve'; exit 1; }
    reserve
    ;;
  start)
    shift
    [ $# -ge 1 ] || { fail 'usage: scripts/ci/supabase-start.sh start <command> [<argument>…]'; exit 1; }
    start "$@"
    ;;
  *)
    fail 'usage: scripts/ci/supabase-start.sh reserve | start <command> [<argument>…]'
    exit 1
    ;;
esac
