#!/bin/sh
SD="$(cd "$(dirname "$0")" && pwd)"
echo "STEP 3 — E0-3(b) variant C. Same frame, nested in the native wrapper."
echo "Watch for: does the preview / attribution read 'Message from @payments-api'?"
exec "$SD/_launch.sh" "$SD/frames/C.txt" 8 bypassPermissions 
