# Configure

Every option has a working default. Options are yours alone: a project's `.brigade.json` cannot set them, and a
teammate's message cannot change them.

## If you want to

| If you want to | Go to |
| --- | --- |
| see every option, its default and its meaning | [Know the options](#know-the-options) |
| set an option in Claude Code | [Configure in Claude Code](#configure-in-claude-code) |
| set an option in Muse Code | [Configure in Muse Code](#configure-in-muse-code) |
| set an option in Codex CLI | [Configure in Codex CLI](#configure-in-codex-cli) |
| change the sentence your sessions receive with each message | [Choose the frame text](#choose-the-frame-text) |
| hold messages for your review before your session sees them | [Hold messages for review](#hold-messages-for-review) |
| hear a sound, or see a desktop notification, when a message arrives | [Be told when a message arrives](#be-told-when-a-message-arrives) |
| switch file sync off for your sessions | [Switch file sync off](#switch-file-sync-off) |
| confirm or block what your session may send | [Gate what your session sends](#gate-what-your-session-sends) |
| run `claude -p` workers or sandboxed sessions | [Run headless or sandboxed sessions](#run-headless-or-sandboxed-sessions) |
| inspect experimental Grok Build configure status | [Grok Build (experimental)](#grok-build-experimental) |

## Know the options

1. Fifteen options, all with working defaults: where the credential lives, your label, the inbound policy, what the
   roster shows, file sync, the frame text, sounds and notifications.
2. The same names and meanings in every harness; only the way to set them differs.

Details: [Know the options](options.md).

## Configure in Claude Code

1. From `/plugin` in a session, or ask the session: `Set message_sound to on.`
2. Or on the command line: `claude --settings '{"pluginConfigs":{"brigade@brigade":{"options":{"frame":"guarded"}}}}'`.
3. Start a new session for the change to take effect.

Details: [Configure in Claude Code](claude-code.md).

## Configure in Muse Code

1. Write `~/.config/brigade/muse-options.json`: a JSON object whose keys are the option names.
2. Start a new session; a missing file is the defaults.

Details: [Configure in Muse Code](muse-code.md).

## Configure in Codex CLI

1. Write `~/.config/brigade/codex-options.json`: a JSON object whose keys are the option names.
2. Start a new session; a missing file is the defaults.

Details: [Configure in Codex CLI](codex.md).

## Choose the frame text

1. Set `frame` to `open` (default, adds nothing), `guarded` or `strict`.
2. Or set `frame_file` to a text file of your own sentence or two; the file wins.

Details: [Choose the frame text](frame-text.md).

## Hold messages for review

1. Set `team_inbound` to `hold`. Arriving messages are recorded, not delivered, not acknowledged.
2. In your own terminal: `brigade inbox` to read, `brigade inbox release --all` (or `<message_id>…`) to deliver.

Details: [Hold messages for review](hold-messages.md).

## Be told when a message arrives

1. Set `message_sound` to `on`, `message_notification` to `on`, or both.
2. `message_interval` (default 30 s, 5 to 3600) is the least time between two sounds or two notifications.

Details: [Be told when a message arrives](notifications.md).

## Switch file sync off

1. Set `sync` to `off`. From the next session start that session syncs nothing and starts no Syncthing.

Details: [Switch file sync off](sync-off.md).

## Gate what your session sends

1. `"permissions": {"allow": ["Bash(brigade:*)"]}` removes the prompts in Manual mode.
2. `"ask": ["Bash(brigade send*)"]` prompts before each send, in bypass mode too, but **denies** in `claude -p`.
3. `"deny": ["Bash(brigade send*)"]` blocks sending in every mode.

Details: [Gate what your session sends](permissions.md).

## Run headless or sandboxed sessions

1. `claude -p` receives team messages like any session; a worker that must not: `team_inbound` `refuse` through `--settings`.
2. With the Bash sandbox on, add `<ref>.supabase.co` to `sandbox.network.allowedDomains`.
3. A body over about 8 KB goes through `brigade send --body-file <path>`.

Details: [Run headless or sandboxed sessions](headless-and-sandbox.md).

## Grok Build (experimental)

Participation remains gated. Read the current source-package limits before an owned packaging experiment.

Details: [Grok Build (experimental)](grok.md).
