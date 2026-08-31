#!/bin/sh
SD="$(cd "$(dirname "$0")" && pwd)"
echo "STEP 2 — E0-3(b) variant A. Frame arrives ~8s after the session is up."
echo "Watch for: the ONE-LINE PREVIEW. Expect the raw <brigade-message team=...> tag line."
exec "$SD/_launch.sh" "$SD/frames/A.txt" 8 bypassPermissions 
