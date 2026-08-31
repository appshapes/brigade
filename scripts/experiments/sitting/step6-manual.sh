#!/bin/sh
SD="$(cd "$(dirname "$0")" && pwd)"
echo "STEP 6 — E0-8(b) Manual mode, no rules. Type: run this in Bash: brigade sessions"
echo "Watch for: whether it prompts."
exec "$SD/_launch.sh" "$SD/frames/A.txt" 99999 default 
