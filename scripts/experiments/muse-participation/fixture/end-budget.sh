#!/bin/sh
# SessionEnd budget probe: ticks every 0.1 s for up to 8 s; the last tick written is the budget's lower bound.
OUT=${1:?store directory}/ticks-$$.log
cat >/dev/null
i=0; while [ $i -lt 80 ]; do python3 -c 'import time;print(f"{time.time():.3f}")' >> "$OUT"; i=$((i+1)); /bin/sleep 0.1; done
echo "completed" >> "$OUT"
