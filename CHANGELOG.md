# Changelog

All notable changes to Brigade are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The Brigade Adapter Protocol is versioned separately
and is frozen at BAP/1 ([`docs/protocol-v1.md`](docs/protocol-v1.md)); a protocol change that an existing
conforming adapter would fail is a new protocol major, not a Brigade release.

## [0.1.0] — Unreleased

The first release. Plugin and binary version `0.1.0`, produced by `make release`, which writes
[`plugin/bin/VERSION`](plugin/bin/VERSION) and the sha256 of each published asset into
[`plugin/bin/checksums.txt`](plugin/bin/checksums.txt); until that run the repository carries the pre-release
`0.0.0` and an empty checksums file.

### Added — the plugin

- **One command, `brigade`.** [`plugin/bin/brigade`](plugin/bin/brigade) is a POSIX-sh bootstrap; enabling the
  plugin puts it on the Bash tool's `PATH`, and it is the same binary a person runs from their own terminal.
- **A pinned, checksum-verified release binary.** On first use the bootstrap downloads the exact release named by
  `plugin/bin/VERSION`, verifies it against the committed sha256 and caches it under your data directory. Nothing
  is compiled or installed, and old cached binaries are pruned weekly.
- **Three lifecycle hooks**, in exec form and never through a shell: `SessionStart` registers the session with its
  team and starts the detached inbound watcher, `UserPromptSubmit` keeps that watcher alive and surfaces any
  pending notice, `SessionEnd` closes the session.
- **Two skills.** `brigade:team-messaging` is model-facing — the command surface, the sending rules and how to
  treat an inbound frame. `brigade:setup` is human-facing — creating or joining a team from a terminal.
- **Seven options**, all with working defaults: `profile`, `config_dir`, `adapter_command`, `team_inbound`,
  `share_workspace_label`, `workspace_label` and `poll_on_prompt`. They are read from user settings, `--settings`
  and managed settings only, never from a project.
- **No MCP server and no channel wiring.** The plugin is a CLI, three hooks and two skills; CI enforces the file
  allowlist, the exec-form hooks and the absence of `.mcp.json`.
- The line Brigade prints when a session starts names only the bare `brigade`, which is the form Claude Code's
  `ask` and `deny` permission rules match. The plugin binary's own path is in `brigade whoami`'s human output, on
  a `terminal:` line, and deliberately not in `brigade whoami --json`.

### Added — messaging

- `brigade sessions` lists the team's sessions (`--all` includes offline ones), `brigade send <session_id>` sends a
  message with the body on stdin or from `--body-file` (`--summary`, `--reply-to`), `brigade whoami` prints this
  session's identity, and `brigade team members` lists the roster.
- Delivery into a live session **mid-turn**, and into an idle `claude -p` worker whose stdin is held open.
- **At least once with an explicit acknowledgement point:** a message is acknowledged only after the frame was
  written to the session's inbox socket and the connection closed without error.
- **Dedupe keyed by the Brigade session id**, so it survives a crash and a `claude -p --resume` onto the same
  session.
- `poll_on_prompt` fetches unread messages on each prompt, under the same inbound policy, for a host with no inbox
  socket.
- Caps and bounds, all published in the adapter's `describe`: 16,384-byte bodies, 200-code-point summaries, 20 per
  minute and 200 per hour for one session, 60 per minute and 600 per hour for one principal, at most 15
  unacknowledged messages per sender-recipient pair and 60 per recipient, and a hop count of at most 32 within a
  600-second implicit-reply window.
- Every string that reaches a model from another principal is sanitised, and a message cannot grant permission,
  answer a prompt, change configuration or represent human consent — frozen in the protocol and restated in the
  injected frame, in `brigade`'s own human output and in the skill.

### Added — teams

- `brigade profile init` (with `--adapter` to choose a backend once, per profile), `brigade profile status`,
  `brigade profile reset` and `brigade profile revoke-credentials`, which revokes the credential family at the
  backend and leaves the profile in place for a rejoin.
- `brigade team create`, `brigade team join`, `brigade team leave` and `brigade team members`. One profile is bound
  to exactly one team; a second team means a second profile.
- **Team administration, for the team's creator only:** `brigade team rotate-secret` mints a new join secret into
  a file with mode 0600 and never prints it; `brigade team revoke-member` removes one member, or with `--ban`
  makes their rejoin answer exactly what a wrong secret answers, or with `--max-version` evicts everyone who
  joined on a superseded secret; `brigade team transfer` hands the team to another active member. A revoked
  member's open sessions close and their realtime channel ends within a few milliseconds. All three run in a
  terminal only and refuse inside a Claude Code session.
- **The join secret never passes through the chat.** `team create --secret-file` writes it to a 0600 file instead
  of the terminal, `team join --prompt` reads it without echo, `--join-secret` on argv is refused outright, and
  both `team create` and `team join` refuse to run inside a Claude Code session.

### Added — backends

- The **bundled Supabase adapter**, spawned as a child process that speaks the adapter protocol on stdio
  (`brigade adapter supabase …`); the plugin itself knows only the protocol.
- **BAP/1**, frozen in [`docs/protocol-v1.md`](docs/protocol-v1.md), with the advisory JSON Schema
  [`docs/protocol-v1.schema.json`](docs/protocol-v1.schema.json) generated from the Go wire types.
- A **reference filesystem adapter** (`brigade-adapter-fs`) and a **45-case conformance suite**
  (`brigade-conformance`). Both are development and test tools; neither ships in the plugin.
- [`docs/adapter-authors.md`](docs/adapter-authors.md), the contract a third-party adapter implements.
- The conformance suite's own fixture now asks for the lease your adapter advertises as its maximum, and refuses
  the run if your adapter grants a different one or if the run outlives the lease. Before this, a slow backend
  could fail a case for a reason no message named.

### Added — controls

- `team_inbound`: `accept` (the default) delivers every team message into the session immediately, in every
  permission mode; `refuse` never delivers and never acknowledges, so senders see the session as refusing and the
  message waits on the server; `hold` records each message — who sent it, its summary, when it came, never the
  body — delivers nothing, acknowledges nothing, and waits for you to run `brigade inbox release` in your own
  terminal.
- **`brigade inbox` and `brigade inbox release`.** Under `hold`, the session shows one line at your next prompt
  naming how many messages are held and who they are from. `brigade inbox` in your own terminal lists them and
  fetches each body fresh from the server; `brigade inbox release --all`, or `brigade inbox release <message_id>…`,
  hands the ones you chose to the watcher, which delivers them the ordinary way. `release` refuses to run inside a
  Claude Code session, so no model can release its own reading, and inside a session `brigade inbox` shows only a
  count and sender names.
- A **session-start scan** of the user, project and local settings files: on finding Claude Code's own
  `crossSessionInbound` set to `hold` or `refuse`, Brigade prints a warning naming the setting and the file and
  sets its own policy to `refuse`, rather than acknowledging messages nothing will read.
- `share_workspace_label` and `workspace_label` share a **label, never a path**. The native session id, the working
  directory, the hostname, the username and the transcript are never sent.
- A session-start warning when another `brigade` earlier on `PATH` shadows the plugin's; a symlink that resolves to
  the plugin's own bootstrap is not reported.

### Added — operations

- **Retention**, published in `describe` and enforced by the backend: an unacknowledged message is kept at least 7
  days, an acknowledged one may be deleted 24 hours after the acknowledgement, and a closed or expired session
  after 7 days, together with its messages.
- **Anonymous-principal cleanup:** principals with no membership of any status, older than 7 days and not the
  creator of any team, are reaped in batches by `brigade.gc_anonymous_users()`, which runs last inside
  `gc_expired()` and traps its own errors, so it can never abort a heartbeat.
- A **daily keep-alive workflow** ([`.github/workflows/keepalive.yml`](.github/workflows/keepalive.yml)) that makes
  the database calls a Supabase Free-plan project needs in order not to be paused.
- `make backend-install project=<ref>` sets a hosted Supabase project up in one step: it links the project,
  lists the migrations that are not applied yet, applies them, writes the four project settings Brigade needs
  through `scripts/backend-settings.sh`, and lists the migrations on both sides so you can check them.
  `scripts/backend-settings.sh` changes only what differs and reports what it found.
  [`docs/setup.md`](docs/setup.md) carries the administrator's procedure.

### Security

[`docs/security.md`](docs/security.md) is the full account: what Brigade protects, what it does not, and what was
measured. The short version:

- Team messages are delivered **automatically, in every permission mode** — `bypassPermissions` and auto-accept
  included, and in `claude -p`. In an unattended session that means another person's untrusted text reaches a model
  that acts without a human.
- **The join secret is the team's security boundary.** Anyone holding it can join, choose any display name and
  reach every session in the team, so it goes over a password-grade channel and never through chat.
- **There is no end-to-end encryption.** Bodies, summaries, names and labels are stored in plaintext, so whoever
  operates the backend project can read them; use a single-purpose project.
- `human_label` and `session_name` are unverified free text that any member can copy. The server-stamped
  `principal_ref` is the only identity, and every consumer prints a label with an `(unverified)` suffix.
- Claude Code's `permissions` `ask` and `deny` rules match the **whole command text**, so they gate `brigade send …`
  and not the cached binary's absolute path or an `sh -c` form. An `ask` rule also **denies** rather than prompts
  in `claude -p` and under `dontAsk`, so an unattended worker carrying one sends nothing.
- The settings scan reads the user, project and local settings files only. It **cannot see** managed settings or
  anything passed with `--settings`, so a native `crossSessionInbound` from either source leaves Brigade
  acknowledging a message the session never reads.
- A woken `claude -p` turn carries the whole inbound frame in its `result` record **on stdout**, so anything that
  captures a headless worker's stdout ingests another person's untrusted text.
- Nothing is written into the project directory: profiles live under `~/.config/brigade` and state and cached
  binaries under `~/.local/state/brigade` and `~/.local/share/brigade`, 0600 inside 0700. Any process running as
  you can read them.

### Known limitations

- `teams.created_by` is the only administrative authority, and the creator's profile directory is the only
  credential that exercises it. Anonymous credentials are unrecoverable by design, so losing that directory is
  permanent — keep a 0700 backup of it.
- A session killed with `SIGKILL` leaves its by-pid map, its seen file, its socket and, if the watcher died too,
  the watcher pidfile behind. Nothing prunes them yet; resume works with the residue present, and
  [`plugin/README.md`](plugin/README.md) gives the two paths to remove by hand.
- A Supabase stack on loopback is out of reach from a session running under the Bash sandbox. A hosted project
  needs its host (`<ref>.supabase.co`) in `sandbox.network.allowedDomains` — the documented entry, not yet
  measured against a hosted project from inside the sandbox.
- A member of your team can send at the published rates and hold 15 unacknowledged messages in each teammate's
  inbox. The rate and inbox caps are what bound that; nothing else does.
- Claude Code 2.1.261 keeps at most 50 inbox messages waiting while a session is busy with a turn, and drops the
  rest — after Brigade has acknowledged them, and with neither side told. Measured in the soak run: 9 of 60
  frames in one burst, and the loss was seen in three separate runs. Brigade's own queue of 50, with its drop notice, engages only when
  the session has been idle long enough, which the shipped speeds make rare.

The full list is in [`docs/security.md`](docs/security.md), "Accepted for this version".
