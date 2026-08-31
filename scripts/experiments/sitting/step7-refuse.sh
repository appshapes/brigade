#!/bin/sh
SD="$(cd "$(dirname "$0")" && pwd)"
echo "STEP 7 — E0-9 refuse. A frame is posted ~8s in."
echo "Watch for: NOTHING. It should drop silently — no notice, no dialog, no message."
exec "$SD/_launch.sh" "$SD/frames/A.txt" 8 bypassPermissions "$SD/settings/refuse.json"
