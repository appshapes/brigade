# Configure

The gateways let people without a session take part: one more member each, hosted in your project, that turn a
person's email or Slack message into a Brigade message and back, with nothing of theirs on anyone's machine. The
webhook gateway connects an outside service the same way and is the one you run yourself. The project's settings
themselves are applied by `make backend-install` and can be re-checked at any time.

## If you want to

| If you want to | Go to |
| --- | --- |
| let people write to the team by email | [Install the mail gateway](#install-the-mail-gateway) |
| decide who may write to the mail gateway | [Choose who may write](#choose-who-may-write) |
| let people write to the team from Slack | [Install the Slack gateway](#install-the-slack-gateway) |
| connect an outside service or bot by webhook | [Run the webhook gateway](#run-the-webhook-gateway) |
| check or restore the project's settings | [Review the project's settings](#review-the-projects-settings) |
| know how long the backend keeps messages, sessions and principals | [Know the retention](#know-the-retention) |

## Install the mail gateway

1. A Resend account with a verified sending domain and a full-access key, or a Postmark server with a confirmed sender.
2. `read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN`, then `read -rs RESEND_API_KEY && export RESEND_API_KEY`.
3. `make gateway-install project=<ref> team_file=<path>/.brigade.json from='Brigade <brigade@your-domain.com>'`
   (`provider=postmark` for Postmark); commit the changed `.brigade.json`; give people the `people write to:` address.

Details: [Install the mail gateway](mail-gateway.md).

## Choose who may write

1. Until you list senders, anyone who knows the address can write to any session and is answered with the roster.
2. `make gateway-senders project=<ref> team_file=<path>/.brigade.json add='qa@your-domain.com,@your-domain.com'`.

Details: [Choose who may write](mail-senders.md).

## Install the Slack gateway

1. `make slack-gateway-install project=<ref> team_file=<path>/.brigade.json` prints a Slack app manifest.
2. Create the app from that manifest at api.slack.com, install it, put its bot token and signing secret in a file.
3. `SLACK_SECRETS_FILE=~/brigade-slack.env make slack-gateway-install project=<ref> team_file=<path>/.brigade.json`;
   commit the changed `.brigade.json`.
4. Another team on the same project: run step 3 again with that team's `team_file=`. One app serves them all.

Details: [Install the Slack gateway](slack-gateway.md).

## Run the webhook gateway

1. On an always-on machine of yours: `make build-webhook-gateway`, then `brigade team join --secret-file <join
   secret> --label "<name>"` with `BRIGADE_CONFIG_DIR` set to a directory only that user can read.
2. `(umask 077; openssl rand -hex 32 > ~/.config/brigade-webhook/secret)`; the outside service gets this secret and
   nothing else.
3. `brigade-webhook-gateway --team-file <path>/.brigade.json --session-name <name> --secret-file
   ~/.config/brigade-webhook/secret --target-url <url>`, under systemd or a run script. Nothing is added to the project.

Details: [Run the webhook gateway](webhook-gateway.md).

## Review the project's settings

1. `scripts/backend-settings.sh <ref>` re-checks every setting and changes only what is wrong.
2. Anonymous sign-ins on, CAPTCHA off, no session time-box, Realtime public access off, `brigade` exposed.

Details: [Review the project's settings](project-settings.md).

## Know the retention

1. An unacknowledged message at least 7 days; an acknowledged one may go 24 hours after the acknowledgement; a closed
   session and its messages after 7 days; an anonymous principal that never joined after 7 days.
2. Nothing here needs your hand.

Details: [Know the retention](retention.md).
