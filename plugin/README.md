# Brigade for Claude Code

This directory is the Claude Code package of Brigade: team messaging between the coding sessions of different
people and machines. A session registers itself as an addressable *session* in a *team*; another person's session
sends it a text message; the message is stored durably by a backend and injected into the recipient session's
context without a human in the loop. Identity is a *principal* (one person's credential on one backend) and an
opaque *session id*; display names are unverified labels. Delivery is at least once with an explicit
acknowledgement point. Everything a model sees from another principal is sanitised, and a message can never grant
permission, approve a prompt or represent human consent.

The procedures are in the documentation tree: [install](../docs/team-member/install/claude-code.md),
[join](../docs/team-member/join/claude-code.md), [update](../docs/team-member/update/claude-code.md),
[configure](../docs/team-member/configure/claude-code.md), [leave](../docs/team-member/leave/claude-code.md); the
whole [Team Member](../docs/team-member/README.md) tree; what the model reads,
[AI Agent › Claude Code](../docs/agent/in-a-team/claude-code.md).

## What this plugin ships

- **One command, `brigade`.** `bin/brigade` is a POSIX-sh bootstrap; enabling the plugin puts it on the Bash tool's
  `PATH`, and it is the same binary a human runs from a terminal.
- **A pinned, checksum-verified release binary.** `bin/VERSION` names the release and `bin/checksums.txt` carries
  its sha256 for each platform. On first use the bootstrap downloads that exact binary, verifies it against the
  committed checksum and caches it under your data directory. Nothing else is built, compiled or installed, and the
  plugin version pins the binary version.
- **Three lifecycle hooks** (`hooks/hooks.json`, exec form, no shell): `SessionStart` registers the session with
  the team and starts the detached inbound watcher; `UserPromptSubmit` keeps that watcher alive, surfaces any
  pending notice and, where your permission settings allow it, reminds the model to keep its one-sentence roster
  line current; `SessionEnd` closes the session. A session the VS Code extension started also closes itself after
  the team's `idle_close_hours` without activity, because the extension keeps the `claude` process after a
  conversation is closed and fires no hook; its next prompt re-opens it. Before the binary is installed, the
  prompt and session-end hooks return at once and print nothing, and an install that fails for good is reported
  once, on the next prompt, as a hook error beginning `Brigade: not installed:`.
- **Eight skills** under `skills/`: `team-messaging` (model-facing: the command surface, the sending rules, the
  inbound frame, the gateways, the roster line), `setup` (human-facing: create, join, leave, uninstall), `join`
  (`/brigade:join <path>`), `update` (`/brigade:update`), `sessions` (`/brigade:sessions`), `email`
  (`/brigade:email <address> <text>`), `slack` (`/brigade:slack <to> <text>`) and `quiet` (`/brigade:quiet`, which
  makes the one session it is typed in hold its team messages and say so on the roster; `/brigade:quiet off`
  returns it).
- **No MCP server and no channel wiring.** The plugin is a CLI, three hooks and eight skills; there is nothing else
  in the tree, and `make plugin-check` enforces it.

## What leaves your machine, and where it goes

- **Fetched:** on first use, the release binary that `bin/VERSION` names, from this repository's GitHub Releases,
  checked against the sha256 in `bin/checksums.txt`. Nothing else is downloaded.
- **Sent, and to where:** everything goes to the backend named in the project's `.brigade.json`: a Supabase project
  your team's Project Owner owns, or, for a team over Syncthing (`"adapter": "syncthing"`), the machines of the
  team's other members, through Syncthing's own device-to-device TLS, where every member's machine then holds
  every message of the team. Nothing goes to AppShapes or to Anthropic. What is sent: the messages you send
  and the replies, as plain text; and with each session's registration and heartbeat, the session name, the
  repository name, the session state, the one-sentence roster line the model writes with `brigade doing` (the
  option `share_doing` stops it), the model identity and the context occupancy in tokens (two values read from the
  session's transcript file on this machine, and only those two), and the plugin version. Your label is sent once,
  when you join: by default the email address of the Claude account this install is signed in to, read from Claude
  Code's own configuration; the option `label` changes that.
- **Never sent:** your Claude Code session id, your working directory, your hostname, your username, your
  transcript or the path to it.
- **Read on this machine:** Claude Code's configuration file (the account email, read-only), its session registry
  (best effort, never written), your `settings.json` (the `crossSessionInbound` setting and the `permissions`
  arrays, for two fixed answers), and the session's transcript file (the two values above).
- **Written on this machine, under Claude Code's directory:** one line, `"crossSessionInbound": "accept"`, in your
  user `settings.json`, in a session that bypasses permission prompts when the file has none; the option
  `claude_inbound_setting` set to `false` stops it.
- **Run:** the hooks run `bin/brigade` and nothing else. Two optional programs are started only by an option that
  is off by default (the system's sound player for `message_sound`, its notification program for
  `message_notification`), and `syncthing` only for a project whose `.brigade.json` lists folders to sync or names
  the syncthing adapter.
- **Kept for how long:** the backend keeps an unacknowledged message for at least 7 days, may delete an
  acknowledged one 24 hours after the acknowledgement, and removes a closed session and its messages after 7 days.

Who can read what, and what was measured, is [docs/security.md](../docs/security.md).

## Options

Fifteen options, all optional, all with working defaults, set from `/plugin` in a session, by asking the session,
or with `--settings`; the table and each option's meaning is [Know the options](../docs/team-member/configure/options.md).

## For developers

`make plugin-dev` runs this plugin against your own build with the dev-binary pointer, `make plugin-check` holds the
tree's invariants, and `make release` writes `bin/VERSION` and `bin/checksums.txt`: [Developer](../docs/developer/README.md).
The single source of truth for where the work stands is `.context/plans/brigade-execution-log.md`.
