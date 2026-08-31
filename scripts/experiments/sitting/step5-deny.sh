#!/bin/sh
SD="$(cd "$(dirname "$0")" && pwd)"
echo "STEP 5 — E0-8(b) DENY rule in bypassPermissions. Same command as step 4."
echo "Watch for: a BLOCK with no dialog — deny works even in bypass mode."
exec "$SD/_launch.sh" "$SD/frames/A.txt" 99999 bypassPermissions "$SD/settings/deny.json"
