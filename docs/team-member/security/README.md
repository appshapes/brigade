# Security

Three sentences before you put Brigade on a machine: Brigade puts another person's text into your session by
itself, in every permission mode; the join secret is the team's security boundary; there is no end-to-end
encryption, so whoever runs the backend can read every message. [docs/security.md](../../security.md) is the
argued record; this page is the way in.

## If you want to

| If you want to | Go to |
| --- | --- |
| know who and what Brigade trusts, and what a label proves | [Know what is trusted](#know-what-is-trusted) |
| know what a teammate's message can make your session do | [Know what a message can do](#know-what-a-message-can-do) |
| know what the person who runs the backend sees | [Know what the backend sees](#know-what-the-backend-sees) |
| choose what your sessions receive, attended or not | [Choose an inbound policy](#choose-an-inbound-policy) |
| gate what your session sends | [Gate sending](#gate-sending) |
| know where your credential lives and what can read it | [Know where your credential lives](#know-where-your-credential-lives) |
| know what file sync lets a teammate's machine do on yours | [Know what file sync opens](#know-what-file-sync-opens) |
| know what Muse Code adds and what has not been measured there | [Know what Muse Code changes](#know-what-muse-code-changes) |
| know what Codex CLI adds and what has not been measured there | [Know what Codex CLI changes](#know-what-codex-cli-changes) |
| report a problem, or a vulnerability | [Report a problem](#report-a-problem) |

## Know what is trusted

1. A member holds the project URL and publishable key (public), the join secret and their credential directory (the two that matter).
2. Names and labels are free text; `from-principal`, stamped by the server, is the only identity.
3. Six kinds of attacker are held off; a program running as you on your own machine is the accepted seventh.

Details: [docs/security.md, section 1](../../security.md#1-who-and-what-brigade-trusts).

## Know what a message can do

1. What arrives is another person's writing in front of a model with tools; the frame, the sanitiser, the size
   caps and a watcher that does nothing a message asks are the four controls, each with a stated limit.
2. The 26 test messages produced no forbidden action in 155 scored runs; those numbers measure attempts, not effects.

Details: [docs/security.md, section 3](../../security.md#3-a-message-is-untrusted-text).

## Know what the backend sees

1. Message bodies, summaries, names, labels and the roster line, in plain text, for as long as retention keeps them.
2. Never your session id, working directory, hostname, username, transcript or its path.

Details: [docs/security.md, section 2](../../security.md#2-the-person-who-runs-the-backend-can-read-everything).

## Choose an inbound policy

1. `accept` (default, in every mode, `claude -p` included), `hold` (review in your terminal) or `refuse`.
2. Claude Code's own `crossSessionInbound` is a second layer Brigade can only warn about.

Details: [docs/security.md, section 4](../../security.md#4-every-session-receives-including-unattended-ones) and
[section 6](../../security.md#6-claude-codes-own-inbound-setting-is-a-second-layer).

## Gate sending

1. An ask rule prompts in every mode but denies unattended; a deny rule blocks everywhere; neither covers `brigade doing`.
2. The rules match command text; two evasive spellings are an accepted residual risk.

Details: [docs/security.md, section 5](../../security.md#5-sending-what-the-ask-and-deny-rules-stop-and-what-they-miss).

## Know where your credential lives

1. The join secret: nowhere. The refresh and access tokens: `~/.config/brigade/teams/<key>/session.json`, 0600 in 0700.
2. Any program running as you can read it; `brigade team reset` revokes the family at the backend.

Details: [docs/security.md, section 8](../../security.md#8-where-your-credentials-live) and
[section 10](../../security.md#10-your-own-machine).

## Know what file sync opens

1. A teammate's machine can write, overwrite and delete anything in a listed folder on yours, with no prompt.
2. The project decides the folders, `..` included; `sync: off` is the one switch.

Details: [docs/security.md, section 12](../../security.md#12-file-sync).

## Know what Muse Code changes

1. Muse's approval policy decides what a message can make the model do; delivery is a file the model's monitor reads.
2. The injection test set has not been run against Muse's model.

Details: [docs/security.md, section 15](../../security.md#15-muse-code-sessions).

## Know what Codex CLI changes

1. Codex's approval policy decides what a message can make the model do; delivery is a turn Brigade queues into
   the session from outside, and any process of your user account could do the same.
2. The injection test set has not been run against Codex's model.

Details: [docs/security.md, section 16](../../security.md#16-codex-cli-sessions).

## Report a problem

1. An issue at <https://github.com/appshapes/brigade/issues>: what you did, what happened, the versions.
2. A security problem privately, through the repository's "Report a vulnerability" form; never a secret in either.

Details: [docs/security.md, section 13](../../security.md#13-reporting-a-problem).
