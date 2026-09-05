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

**Administering the team.** Three more terminal-only commands, for the creator only (they refuse inside a session):

- `<plugin>/bin/brigade team rotate-secret --secret-file <path>` mints a new join secret into a 0600 file (never
  to the terminal); the old secret stops working, existing members are untouched.
- `<plugin>/bin/brigade team revoke-member --principal <ref> [--ban]` removes one member at once, or
  `--max-version <n>` evicts everyone who joined with a superseded secret (never you).
- `<plugin>/bin/brigade team transfer --principal <ref>` hands the team to another active member.

The procedure, the leaked-secret playbook and what each command does to a running member are in
[docs/setup.md](../docs/setup.md), "Team administration".

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

If you **created** the team, step 2 ends secret rotation, revocation and transfer for it, permanently — run
`<plugin>/bin/brigade team transfer --principal <ref>` to another active member *before* step 1. Step 1 alone
(`team leave`) is recoverable: `created_by` survives it, and a rejoin with the current secret restores
administration; step 2 (`profile reset`) is not. Keep that 0700 backup of the profile directory either way.

## Permissions and confirmation

- The `brigade:team-messaging` skill declares `allowed-tools: Bash(brigade:*)`, which lets the model run `brigade`
  unprompted for the turn that invoked the skill, and only that turn — the next message prompts again. Declaring it
  also raises one Skill dialog in Manual mode, and dismissing that dialog with its second option is scoped to the
  project directory it names — so it is one approval per repository, not per command. Measured on Claude Code
  2.1.252 and re-measured on 2.1.259 in real interactive sessions; both runs and their controls are in
  `docs/experiments/E3-interactive.md`.
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

The manifest, the three hooks and the two skills exist and validate; so do the bootstrap and the release pins. The
plugin now works end to end with a local build: `SessionStart` registers the session with its team, writes the
session map and starts the detached watcher; the watcher injects each teammate's message into the session's inbox
and acknowledges only what it injected; `UserPromptSubmit` keeps the watcher alive and surfaces its notice;
`SessionEnd` closes the session; and the session-bound commands (`sessions`, `send`, `whoami`, `team members`)
resolve their session from that map, while the terminal commands the two setup sections above use (`profile init`
with `--adapter`, `profile status|reset|revoke-credentials`, `team create|join|leave`,
`team rotate-secret|revoke-member|transfer`) pass their terminal straight through to the adapter — `team create`,
`team join` and the three administrative verbs refuse to run from inside a session.

The pins are still at the pre-release `0.0.0` with an empty `bin/checksums.txt`, so there is no release to download
yet. **Developers** point the bootstrap at a local build instead, with the dev-binary pointer `make plugin-dev`
writes, and drive the whole chain against the filesystem test adapter. Once, in your own terminal (never from
inside a session — `team create` and `team join` refuse there):

```sh
make build
bin/brigade profile init --adapter '["'"$PWD"'/bin/brigade-adapter-fs"]'
bin/brigade team create --name ops --label dev --secret-file ~/brigade-ops.secret
```

`profile init --adapter` writes the profile's sidecar, so from then on `make plugin-dev` alone starts a session
already bound to that adapter; `make plugin-dev adapter=fs` passes the same command again as a per-session
override, and `make plugin-dev profile=<name>` picks a second profile on the same machine (`make plugin-dev-off`
removes the pointer). `internal/adapters/fs/README.md` has the second-profile join and the two-store variant;
`docs/experiments/E3-wiring.md` is the measured record of both. One thing to expect while developing: symlinking
your **own build** onto `PATH` (`ln -s <repo>/bin/brigade ~/.local/bin/brigade`) makes every session start with the
extra line ``Brigade: another `brigade` at … shadows the plugin's``, because that path does not resolve to this
plugin's `bin/brigade`. That is the check doing its job — the symlink the setup skill suggests, which points at
`${CLAUDE_PLUGIN_ROOT}/bin/brigade`, is silent (both measured). The single source of truth for where the work
stands is `.context/plans/brigade-execution-log.md` in the Brigade repository.
