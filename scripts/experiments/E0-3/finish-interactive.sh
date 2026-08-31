#!/bin/sh
# E0-3 (a): complete the interactive batch the first pass could not finish.
# Plan requires 5 interactive runs each for A and C. Present: A=4, C=0.
set -u
for tag in a-A-int-05 a-C-int-01 a-C-int-02 a-C-int-03 a-C-int-04 a-C-int-05; do
  [ -d "results/$tag" ] && { echo "SKIP $tag (exists)"; continue; }
  case "$tag" in a-A-*) v=A ;; *) v=C ;; esac
  echo "=== $(date +%H:%M:%S) running $tag (variant $v, interactive) ==="
  ./run.sh --variant "$v" --mode interactive --delay 6 --timeout 70 --tag "$tag" >"results/$tag.log" 2>&1
  echo "    exit=$? "
done
echo "BATCH DONE"
