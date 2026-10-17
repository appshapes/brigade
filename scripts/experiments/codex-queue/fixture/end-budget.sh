#!/bin/sh
# SessionEnd hook ticking every 0.1 s: the last tick is the shutdown budget's lower bound.
STORE=${1:?store directory}
cat >/dev/null
i=0; while :; do i=$((i+1)); echo "tick $i $(python3 -c 'import time;print(round(time.time(),3))')" >> "$STORE/end-ticks.log"; sleep 0.1; done
