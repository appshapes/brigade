#!/bin/sh
SD="$(cd "$(dirname "$0")" && pwd)"
echo "STEP 1 — E0-9 hold. LEAVE THIS WINDOW OPEN; we return to it at the end."
echo "Watch for: a NOTICE that a message was held. No dialog, message NOT delivered."
exec "$SD/_launch.sh" "$SD/frames/A.txt" 8 bypassPermissions "$SD/settings/hold.json"
