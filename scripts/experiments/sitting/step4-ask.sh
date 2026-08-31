#!/bin/sh
SD="$(cd "$(dirname "$0")" && pwd)"
echo "STEP 4 — E0-8(b) ASK rule in bypassPermissions. No frame is posted."
echo "Paste the heredoc command. Watch for a DIALOG (not a denial); answer Yes; note 'don't ask again'."
exec "$SD/_launch.sh" "$SD/frames/A.txt" 99999 bypassPermissions "$SD/settings/ask.json"
