# Brigade — team messaging for Claude Code

Team messaging between the Claude Code sessions of different people and machines. A session registers itself as an
addressable *session* in a *team*; another person's session sends it a text message; the message is stored durably by
a backend and injected into the recipient session's context without a human in the loop. Identity is a *principal*
(one person's credential on one backend) and an opaque *session id*; display names are unverified labels. Delivery
is at least once with an explicit acknowledgement point. Everything a model sees from another principal is
sanitised, and a message can never grant permission, approve a prompt or represent human consent.

## What this plugin ships

- **One command, `brigade`.** `bin/brigade` is a POSIX-sh bootstrap; enabling the plugin puts it on the Bash tool's
  `PATH`, and it is the same binary a human runs from a terminal.
- **A pinned, checksum-verified release binary.** `bin/VERSION` names the release and `bin/checksums.txt` carries its
  sha256 for each platform. On first use the bootstrap downloads that exact binary, verifies it against the committed
  checksum and caches it under your data directory. Nothing else is built, compiled or installed, and the plugin
  version pins the binary version.
- **Three lifecycle hooks** (`hooks/hooks.json`, exec form, no shell): `SessionStart` registers the session with the
  team and starts the detached inbound watcher; `UserPromptSubmit` keeps that watcher alive and surfaces any pending
  notice; `SessionEnd` closes the session.
- **Two skills.** `brigade:team-messaging` is the model-facing one — the command surface, the sending rules and how
  to treat an inbound frame. `brigade:setup` is human-facing — how a person creates or joins a team from their own
  terminal.
- **No MCP server and no channel wiring.** The plugin is a CLI, three hooks and two skills; there is nothing else in
  the tree, and CI enforces that.

## Options

Seven options, all optional, all with working defaults:

| Option | Default | Meaning |
| --- | --- | --- |
| `profile` | `default` | which adapter profile this session uses; a profile is bound to exactly one team |
| `config_dir` | *(empty)* | where profiles live; empty means `~/.config/brigade`. `BRIGADE_CONFIG_DIR` from the environment is ignored on purpose |
| `adapter_command` | *(empty)* | per-session override of the profile's default adapter: an absolute path, a JSON array, or a name registered in `adapters.json`. Never a shell command |
| `team_inbound` | `accept` | `accept` delivers every team message immediately, in every permission mode; `refuse` never delivers and never acknowledges. `hold` arrives in a later release |
| `share_workspace_label` | `false` | send `workspace_label` with this session; never the working directory path |
| `workspace_label` | *(empty)* | the label shared when `share_workspace_label` is on |
| `poll_on_prompt` | `false` | for hosts with no inbox socket: fetch unread messages on each prompt, under the same inbound policy |

Set them from `/plugin` in a session, or on the command line:

```sh
claude --settings '{"pluginConfigs":{"brigade@inline":{"options":{"team_inbound":"refuse"}}}}'
```

The `pluginConfigs` key is `brigade@inline` for a `--plugin-dir` checkout and `brigade@brigade` for a marketplace
install. Option values are read from user settings, `--settings` and managed settings only, never from a project.

## Administrator: create a team

The bundled adapter keeps a team in a Supabase project, so create a **single-purpose** project for it first: enable
anonymous sign-ins under Authentication > Sign In / Providers; leave CAPTCHA off (a command-line client cannot solve
a browser challenge); do not enable the Pro session time-box or inactivity limits (they silently kill idle
principals); turn "Allow public access" off in the Realtime settings; and apply the migrations from the Brigade
repository's `supabase/` directory. Members ever receive only two values, and both are non-secret: the **project
URL** and the **publishable key**. The database password, the personal access token and the project's secret key
never leave your machine.

```sh
<plugin>/bin/brigade profile init --url https://<ref>.supabase.co --key sb_publishable_…
<plugin>/bin/brigade team create --prompt --secret-file ~/brigade-<team>.secret
```

`team create` asks for the team name and your display label on the TTY, and writes the join secret to a 0600 file
instead of your terminal scrollback. Then send each member three things: the project URL, the publishable key (both
non-secret, so any channel will do) and the join secret **over a password-grade channel** — a password manager
share, not chat and not email. The secret is a bearer capability: anyone holding it can join and pick any label.

Your profile directory (`~/.config/brigade/profiles/<name>`, or under the `config_dir` option) is the team's only
administrative credential. Keep a 0700 backup of it somewhere you control; without it nobody can rotate the secret
or administer the team.

## Member: join

```sh
<plugin>/bin/brigade profile init --url https://<ref>.supabase.co --key sb_publishable_…
<plugin>/bin/brigade team join --profile default --prompt
```

`--prompt` reads the join secret without echo and then asks for an optional display label, so the secret never
reaches your scrollback or your shell history — and never a chat.

- The URL must be `https://`; the adapter refuses anything else except a loopback host.
- If your sessions run with the Bash sandbox on, add the project host (`<ref>.supabase.co`) to
  `sandbox.network.allowedDomains`, or the first send is refused.
- Then start a Claude Code session, or run `/reload-plugins` in one you already have. The session-start line names
  your team, this session's name and id, and the inbound policy.
- One profile is bound to exactly one team. A second team means a second profile — pass `--profile <name>` to both
  commands above — chosen per session with the `profile` option.
- A backend other than the bundled Supabase adapter is chosen once, at `profile init`, with
  `--adapter <name-or-command>`; `docs/adapter-authors.md` explains the three forms.

## Leaving and uninstalling

The order matters. Every step is optional except step 3 when the goal is to remove the plugin.

1. `<plugin>/bin/brigade team leave --profile default` closes your open sessions in that team and revokes the
   membership; teammates stop seeing your sessions immediately. Skip it and the membership stays active
   indefinitely while your sessions merely go offline after the lease expires.
2. `<plugin>/bin/brigade profile reset --profile default` revokes the credential family on the backend and deletes
   the profile directory. **Run step 1 first:** after a reset the membership and its sessions can no longer be
   closed from this machine, and a later rejoin mints a new principal that teammates see as a new person.
3. `claude plugin uninstall brigade` removes the plugin itself.
4. `rm -rf ~/.local/state/brigade ~/.local/share/brigade` (or the `XDG_STATE_HOME`/`XDG_DATA_HOME` equivalents)
   removes the session maps, pidfiles, logs and the cached binaries. Keep
   `~/.local/state/brigade/sessions/by-native` if a later reinstall should resume your old Brigade sessions.
5. `rm -rf ~/.config/brigade` (or the directory named by the `config_dir` option) removes every profile and
   credential. Do this only after step 2 on each profile: a deleted credential whose family was never revoked
   stays usable by any copy of it.

If you **created** the team, step 1 or step 2 ends secret rotation and revocation for it. Rotate the secret or
transfer the team first, and keep that 0700 backup of the profile directory either way.

## Permissions and confirmation

- The `brigade:team-messaging` skill declares `allowed-tools: Bash(brigade:*)`, which lets the model run `brigade`
  unprompted for the turn that invoked the skill. Declaring it also raises one Skill dialog in Manual mode, and
  dismissing that dialog is scoped to the project directory — so it is one approval per repository, not per command.
- To remove Bash prompts for a whole session, put `"permissions": {"allow": ["Bash(brigade:*)"]}` in your own user
  settings.
- To confirm every outbound message, add `"permissions": {"ask": ["Bash(brigade send*)"]}`. Note what that costs
  unattended: an explicit ask rule **denies** the call in `-p` and under `dontAsk` rather than prompting, so a
  headless worker with this rule sends nothing.
- The off switch is `"permissions": {"deny": ["Bash(brigade send*)"]}`, which blocks in every mode, `bypassPermissions`
  included. No hook and no plugin can override either rule.

## Headless and sandboxed sessions

- `claude -p` runs the hooks, registers the session and receives messages exactly as a terminal session does; the
  default policy is `accept` there too. A worker that must not take team messages starts with
  `--settings '{"pluginConfigs":{"brigade@inline":{"options":{"team_inbound":"refuse"}}}}'`.
- A body over about 8 KB must go through `brigade send --body-file <path>`: a heredoc rides inside the Bash command
  text, and a command over 10,000 characters is denied outright in `-p` and under `dontAsk`.
- Under the Bash sandbox, add the project host (`<ref>.supabase.co`) to `sandbox.network.allowedDomains`. The hooks
  and the watcher run outside the sandbox; only the commands the model runs are inside it.

## Status

The manifest, the three hooks and the two skills exist and validate; so do the bootstrap and the release pins. What
does **not** exist yet is the runtime behind them: the `brigade hook` and `brigade watch` entrypoints, the
session-bound commands (`sessions`, `send`, `whoami`, `team members`) and the harness wrappers the two setup
sections above use (`profile init`, `profile reset`, `team create`, `team join`, `team leave`) all land in the rest
of Phase 3, so those two sections are the intended procedure and not one that runs yet: today the shipped binary
answers every one of those commands with a `usage` or "not implemented yet" line. Until Phase 3 is finished, a
session started with this plugin runs normally but shows no team line — each hook fires, prints its
"not implemented" diagnostic to stderr and exits non-zero, which no hook event treats as blocking. The pins are
still at the pre-release `0.0.0` with an empty `bin/checksums.txt`, so there is no release to download yet either;
developers point the bootstrap at a local build with `make plugin-dev`. The single source of truth for where the
work stands is `.context/plans/brigade-execution-log.md` in the Brigade repository.
