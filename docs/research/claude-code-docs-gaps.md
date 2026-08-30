# Claude Code documentation gaps for Brigade (research digest)

Date: 2026-08-30. Target: Claude Code v2.1.251.
Method: every page below was fetched today as raw Markdown from `https://code.claude.com/docs/en/<page>.md` (the `.md` suffix returns the source; the `/docs/llms.txt` index lists all pages). Quotes are verbatim. Where a fact is inferred rather than quoted, it is labelled as such. Claims are cross-checked against `verified-facts.md` (live probing, same day); conflicts are called out explicitly.

Sources fetched (all under `https://code.claude.com/docs/en/`): `settings-reference`, `settings`, `env-vars`, `sessions`, `sandboxing`, `channels`, `channels-reference`, `agent-view`, `plugins`, `plugins-reference`, `mcp`, `permissions`, `permission-modes`, `hooks`, `tools-reference`, `cross-session-messaging`, `headless`, `cli-reference`, `desktop`, `errors`, `changelog`, `whats-new/2026-w34`, `whats-new/2026-w33`, `whats-new/2026-w32`, `whats-new/2026-w30`, `whats-new/index`, `agent-sdk/sessions`, `agent-sdk/typescript`, `agent-sdk/streaming-vs-single-mode`.

`whats-new/2026-w31` returns HTTP 404 and the index at `whats-new/index.md` jumps from Week 30 (v2.1.214–v2.1.219) to Week 32 (v2.1.220–v2.1.224); no Week 31 digest exists. The what's-new digests stop at v2.1.239 (Week 34), so the changelog was read directly for v2.1.240–v2.1.251 (section 11).

---

## 0. Headline answers

1. **Framing of socket-delivered messages cannot be changed by the poster.** The documented socket payload is exactly two frames (`auth`, then `user`). Everything posted on the socket "go[es] through the session's inbound controls" and is treated as a peer message. The only documented senders that can attach peer-origin metadata (`from`, `fromMode`, `name`, `fromSession`) are Claude Code's own `SendMessage` and "a host that relays a peer message between your sessions" through the Agent SDK / `--input-format stream-json` stdin path. No hook, monitor, or socket-poster field is documented for altering the preamble or declaring a host application. Details: section 10.
2. **Channels remain a research preview** with an Anthropic-curated allowlist; custom channels need `--dangerously-load-development-channels`, and "the `--channels` flag syntax and protocol contract may change." Team/Enterprise orgs must enable `channelsEnabled`. Not available on Bedrock/Vertex/Foundry. The plan's decision not to build on Channels is still justified. Section 5.
3. **`crossSessionInbound` precedence is not ordinary settings precedence.** Trusted sources are read managed → `--settings` → user, first value wins; project/local values apply only when stricter. Invalid values cause a hold (user files) or refuse (managed). Section 1.
4. **Sandbox and the inbox socket:** on macOS the sandbox "blocks every Unix socket" by default; `sandbox.network.allowUnixSockets` (macOS) or `allowAllUnixSockets` (Linux seccomp) is required for a sandboxed Bash child to reach `CLAUDE_CODE_MESSAGING_SOCKET`. Hooks, MCP servers and plugin monitors are not sandboxed. `sandbox.enabled` defaults to `false`. Section 4.
5. **Session identity:** `CLAUDE_CODE_SESSION_ID` "is updated on `/clear`"; `--resume <id>` keeps the ID; `--fork-session`, `/branch`, and `/fork` mint new IDs; compaction keeps the ID (SessionStart `source: "compact"`). An MCP server subprocess "retains the ID it was spawned with", so a Brigade MCP shim must not treat its own env as the live session ID after `/clear`. Section 3.
6. **Session names:** set by `--name`/`-n`, `/rename`, Ctrl+R in the picker, plan acceptance, claude.ai/desktop renames, or a SessionStart hook's `sessionTitle` output. Renaming updates "the shared record your other sessions use to look up the session's name" (undocumented format; `verified-facts.md` observed `$CLAUDE_CONFIG_DIR/sessions/<pid>.json`). Section 3.
7. **Plugin monitors** are the documented alternative framing: each stdout line is "delivered to Claude as a notification", not a peer message. They are experimental, interactive-CLI-only, unsandboxed, and off on Bedrock/Vertex/Foundry and when `DISABLE_TELEMETRY` or `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is set. Section 7.

---

## 1. Settings reference (`/docs/en/settings-reference`)

### 1.1 `crossSessionInbound`

> "Choose what this session does with messages arriving from your other Claude Code sessions. When no value applies, Claude Code decides per message from the two sessions' permission-mode classes. Requires Claude Code v2.1.224 or later."

> "**Scope**: Any file. A project or local value applies only when it's stricter than the value managed settings, the `--settings` flag, or user settings give."

Values: `"accept"` ("delivers the message to Claude"), `"hold"` ("shows a notice for the message without delivering it"), `"refuse"` ("drops the message"). Default: "unset, so Claude Code decides per message".

Precedence (verbatim):

> "Claude Code reads managed settings first, then the `--settings` flag, then user settings, and applies the first value found. `refuse` is stricter than `hold`, and `hold` is stricter than `accept`. When none of the trusted sources sets a value, a project or local `hold` or `refuse` still applies, replacing the per-message default. In sessions with cross-session messaging, this key appears in `/config` as **Messages from your other sessions**, which writes it to user settings; the row requires Claude Code v2.1.232 or later, and Claude Code hides it while the `--settings` flag or managed settings set the key."

Invalid values:

> "Claude Code warns when you set a value it doesn't recognize. While that value is present in a user, project, local, or `--settings` file, Claude Code holds inbound messages, even when a source that takes precedence sets `accept`. A `refuse` that another source sets still applies. Fix or remove the value to clear the hold."
> "When the unrecognized value is in managed settings, Claude Code instead treats it as `refuse` until an administrator fixes it. Before v2.1.248, Claude Code ignored an unrecognized value without warning."

Note (cross-session page): "Claude Code rejects the `/config crossSessionInbound=value` shorthand for this key."

### 1.2 `dialogExpiry`

> "Set the deadline for dialogs Claude Code forwards to a remote client, such as a Remote Control or SDK host, for the approval dialog for a held cross-session message, and for the mid-session Fable 5 usage-credits consent prompt in a session that may have nobody at the terminal. When no answer arrives before the deadline, Claude Code cancels the dialog and continues with its no-action default. Requires Claude Code v2.1.224 or later."

- Scope: **User or managed** (not project/local).
- Type: string, one of `"60s"`, `"5m"`, `"10m"`, or `"never"` ("which disables the deadline").
- Default: `"5m"`.
- Per-session override: `CLAUDE_CODE_USER_DIALOG_TIMEOUT_MS` "takes precedence over this key for one session".
- "Permission prompts and `AskUserQuestion` questions use their own flows and aren't governed by this deadline."

### 1.3 `isolatePeerMachines`

> "Require your explicit approval before Claude's `SendMessage` reaches one of your sessions beyond this machine ... The approval prompt appears even in `bypassPermissions` mode."
> "**Scope**: Any file. A `true` from any scope applies, so a checked-in project file can turn the requirement on but not off."
> Default: "unset, so cross-machine messages don't prompt". "The cross-machine `SendMessage` approval requires Claude Code v2.1.224 or later."

Relevance: this gate applies only to Claude Code's native cross-machine path (Remote Control). Brigade's adapter path is not a "session beyond this machine" in Claude Code's sense, so this setting does not gate `TeamSendMessage`.

### 1.4 Sandbox Unix-socket settings

`sandbox.network.allowUnixSockets`:
> "List the Unix socket paths sandboxed commands can connect to on macOS. Claude Code ignores this list on Linux and WSL2, where the seccomp filter can't inspect socket paths; use `allowAllUnixSockets` there instead."
> "**Default**: unset, so the macOS sandbox blocks every Unix socket"
> "A socket path can grant broad access: allowing `/var/run/docker.sock`, for example, lets a sandboxed command control the Docker daemon."

`sandbox.network.allowAllUnixSockets`:
> "Let sandboxed commands connect to every Unix socket. On Linux and WSL2, the sandbox's seccomp filter blocks `socket(AF_UNIX, ...)` calls, so this is the only way to permit Unix sockets there. When the filter is missing, which `/sandbox` reports on its Dependencies tab, the sandbox doesn't block Unix-socket calls."
> Default `false`: "the sandbox blocks Unix-socket connections: on macOS except the paths in `allowUnixSockets`, and on Linux and WSL2 through the seccomp filter when it's present"

`sandbox.network` in general: "Claude Code merges the array sub-keys across settings scopes and deduplicates them, so a project can add domains to your user list." (Inference: `allowUnixSockets` is an array sub-key, so a plugin cannot set it, but a project `.claude/settings.json` can add the socket path.)

`sandbox.enabled`: "**Default**: `false`". `sandbox.excludedCommands`: "Name commands that Claude Code always runs outside the sandbox ... Excluded commands still go through the regular permission flow. Exclusion is a convenience, not a security boundary".

### 1.5 Plugin-related settings

`enabledPlugins`:
> "Turn individual plugins on or off, keyed by `plugin-name@marketplace-name`. A plugin with no entry at any scope falls back to its `defaultEnabled` value."
> "Project settings take precedence over user settings, so setting a plugin to `false` in `~/.claude/settings.json` doesn't disable a plugin that the project's `.claude/settings.json` enables. To opt out of a project-enabled plugin on your machine, set it to `false` in `.claude/settings.local.json` instead."
> "Enabling a plugin from an external source such as a GitHub repository or npm package in a project's `.claude/settings.json` doesn't install it for other people. On every path that loads plugins, Claude Code reports the plugin as not installed until each user installs it themselves."

`pluginConfigs` (where `userConfig` answers are stored):
> "**Scope**: User or managed" ... "Sensitive options go to the macOS Keychain instead, or to `~/.claude/.credentials.json` on platforms without a supported keychain."
> "Claude Code ignores project and local entries because it substitutes these values into plugin hook, MCP, and LSP configurations, and a cloned repository must not be able to supply them. Before v2.1.207, project and local settings were also read."

`disableSideloadFlags` (managed): "Reject the `--plugin-dir`, `--plugin-url`, `--agents`, and `--mcp-config` CLI flags at startup ... Requires Claude Code v2.1.193 or later." Plugin settings.json in the plugin root: "Currently, only the `agent` and `subagentStatusLine` keys are supported" (plugins page). So a plugin cannot ship `crossSessionInbound`, `sandbox.*`, or `permissions.*` defaults.

Other plugin keys present: `extraKnownMarketplaces` (Any file; aliases `additionalMarketplaces`), `strictKnownMarketplaces` (Managed; alias `allowedMarketplaces`), `blockedMarketplaces`, `pluginSuggestionMarketplaces`, `pluginTrustMessage`, `disableCommandPluginSources`, `strictPluginOnlyCustomization.{skills,agents,hooks,mcp}` (managed lock to plugin/managed sources).

### 1.6 Permission rules for MCP tools

`permissions.allow`:
> "In an MCP rule, `*` can appear only in the tool name after the `mcp__<server>__` prefix, such as `mcp__github__get_*`; it can't appear in the server name."
> "Claude Code evaluates `deny` rules first, then `ask`, then `allow`, and the first match decides regardless of how specific each rule is"
> "Claude Code applies `allow` rules from a project's `.claude/settings.json` only after you accept the workspace trust dialog for that folder."

`permissions.deny`: "Tool names accept glob patterns, so `"*"` denies every tool and `"mcp__*"` denies every MCP tool."

Permissions page (`/docs/en/permissions#mcp`):
> "`mcp__puppeteer` matches any tool provided by the `puppeteer` server"
> "`mcp__puppeteer__*` uses wildcard syntax and also matches all tools from the `puppeteer` server"
> "`mcp__puppeteer__puppeteer_navigate` matches the `puppeteer_navigate` tool provided by the `puppeteer` server"
> "When Claude Code loads a settings file, it skips any `mcp__` rule that has parentheses."
> "Allow rules accept tool-name globs only after a literal `mcp__<server>__` prefix. The server segment must be glob-free"

Plugin-bundled server tool names (`/docs/en/mcp#plugin-provided-mcp-servers`):
> "The full form is `mcp__plugin_<plugin-name>_<server-name>__<tool-name>`, where any character outside `A-Z`, `a-z`, `0-9`, `_`, and `-` is replaced with `_`."
> "Use this full name when referencing the tool in permission rules, a skill's `allowed-tools` list, a subagent's `tools` field, or a hook matcher. A hook matcher written against the bare server key, such as `mcp__database-tools__.*`, never fires for a plugin-bundled server."
> "The server itself registers under the scoped name `plugin:<plugin-name>:<server-name>`"

For Brigade (plugin `brigade`, server `brigade`): tools would be `mcp__plugin_brigade_brigade__TeamListSessions` and `mcp__plugin_brigade_brigade__TeamSendMessage`; an allow rule `mcp__plugin_brigade_brigade__*` is valid.

Turning off native messaging (cross-session page): "add permission deny rules naming `SendMessage` and `ListAgents`. Both take the bare tool name with no specifier." ... "Denying `SendMessage` also removes messaging to subagents and agent-team teammates, since the same tool serves both."

### 1.7 General settings precedence (`/docs/en/settings#settings-precedence`)

> "In order, highest precedence first: 1. Managed settings ... 2. Command line arguments ... `--settings <file-or-json>` ... 3. Project local settings (`.claude/settings.local.json`) ... 4. Shared project settings (`.claude/settings.json`) ... 5. User settings (`~/.claude/settings.json`)"
> "When you set the same list key, such as `permissions.allow`, in more than one file, Claude Code combines the lists instead of picking one"
> "Environment variables aren't a level in this stack."

Note: `crossSessionInbound` (1.1) and `isolatePeerMachines` (1.3) deliberately deviate from this ordering.

---

## 2. Environment variables (`/docs/en/env-vars`)

### 2.1 Messaging variables

`CLAUDE_CODE_MESSAGING_SOCKET`:
> "Set by Claude Code, not by you: in sessions that bind an inbox socket, Claude Code exports that socket's path to hooks and Bash commands when it binds the socket. In a session that starts with messaging on, Claude Code binds the socket before any hook runs. Other sessions on the machine deliver messages to this path. Each session exports its own socket rather than one inherited from a parent, and messages arriving on it go through the session's inbound controls. Settings `env` blocks can't set it. Requires Claude Code v2.1.224 or later"

`CLAUDE_CODE_MESSAGING_TOKEN`:
> "Set by Claude Code, not by you: in sessions that bind an inbox socket, Claude Code exports this per-session token to hooks and Bash commands alongside `CLAUDE_CODE_MESSAGING_SOCKET`. A script posting to the socket can send `{"type":"auth","token":"<token>"}` as its first line to prove it belongs to the session. On native Windows, Claude Code requires this line and closes any connection that doesn't open with a valid one. The own-child rules say when Claude Code consults the token. Each session exports its own token, never one inherited from a parent session. Settings `env` blocks can't set it. Requires Claude Code v2.1.228 or later"

Gap: the docs say these are exported "to hooks and Bash commands". They do not say they are exported to stdio MCP server subprocesses (compare `CLAUDE_CODE_SESSION_ID`, which explicitly lists MCP servers). `verified-facts.md` observed them in hooks and Bash children only. Treat availability inside the MCP shim as unverified; the shim can receive them via the plugin `.mcp.json` `env` block only if `${CLAUDE_CODE_MESSAGING_SOCKET}` expansion resolves at server launch, which is undocumented (see 8.2). Safer: have the SessionStart hook write socket path and token into a per-session file under `${CLAUDE_PLUGIN_DATA}` keyed by `session_id`, mode 0600, and have the watcher read that.

### 2.2 Session and process identity

`CLAUDE_CODE_SESSION_ID`:
> "Set automatically to the current session ID in Bash and PowerShell tool subprocesses, hook command subprocesses, and stdio MCP server subprocesses. For Bash, PowerShell, and hooks this matches the `session_id` field in the hook JSON input and is updated on `/clear`. An MCP server subprocess retains the ID it was spawned with. On `--resume <session-id>` it receives the resumed ID, matching hooks and Bash. On `--continue` or `--resume` without an explicit ID it may receive the initial startup ID instead."

`CLAUDE_PID`: "Claude Code sets this to its own process ID in the subprocesses it spawns: Bash and PowerShell tool commands and hook commands ... Requires Claude Code v2.1.214 or later". (Not listed for MCP subprocesses.)

`CLAUDE_CODE_BRIDGE_SESSION_ID`: "Set automatically in Bash tool and hook command subprocesses while the session has an active Remote Control connection, and removed when the connection ends. The value is the session's ID in `session_` form ... Requires Claude Code v2.1.199 or later."

`CLAUDE_CODE_USER_DIALOG_TIMEOUT_MS`: "Deadline in milliseconds for dialogs Claude Code forwards to a remote client ... and for the approval dialog for a held cross-session message ... Overrides the `dialogExpiry` setting; `0` or a negative value disables the deadline"

### 2.3 Config directory and temp

`CLAUDE_CONFIG_DIR`: "Override the configuration directory (default: `~/.claude`). All settings, session history, and plugins are stored under this path, as are credentials on Linux and Windows; on macOS, credentials are in the system Keychain."

`CLAUDE_CODE_PROJECT_DIR_NAME`: "Set together with `CLAUDE_CONFIG_DIR` to choose the `projects/` directory name ... Claude Code ignores this variable when `CLAUDE_CONFIG_DIR` is unset ... Requires Claude Code v2.1.234 or later"

`CLAUDE_CODE_TMPDIR`: "Override the temp directory used for internal temp files. Claude Code appends `/claude-{uid}/` on Unix or `/claude/` on Windows to this path. Default: `/tmp` on macOS, `os.tmpdir()` on Linux and Windows. As of v2.1.161, on macOS and Linux, sandboxed Bash subprocesses receive a short fallback `$TMPDIR` under the system default when your override is a long path ... Unsandboxed Bash commands inherit your shell's `$TMPDIR` unchanged."

Note: the inbox socket directory (`/tmp/cc-socks/<pid>.sock`, fallback `/tmp/cc-socks-<uid>`) is not documented as governed by `CLAUDE_CODE_TMPDIR`; the cross-session page names `/tmp/cc-socks-<uid>` literally. Do not derive the socket path; always read `CLAUDE_CODE_MESSAGING_SOCKET`.

### 2.4 Plugin-related variables

Path placeholders (`/docs/en/plugins-reference#environment-variables`):
> "`${CLAUDE_PLUGIN_ROOT}` Absolute path to the plugin's installation directory"
> "`${CLAUDE_PLUGIN_DATA}` Persistent directory that survives plugin updates, created on first reference"
> "`${CLAUDE_PROJECT_DIR}` The project root"
> "All three are exported as environment variables to hook processes and to MCP and LSP server subprocesses."
> "MCP `stdio` servers: `command`, `args`, `env`" are the fields where placeholders resolve.
> "`${CLAUDE_PLUGIN_ROOT}` changes when the plugin updates ... treat it as ephemeral and don't write state there."
> "The `${CLAUDE_PLUGIN_DATA}` directory resolves to `~/.claude/plugins/data/{id}/`, where `{id}` is the plugin identifier with characters outside `a-z`, `A-Z`, `0-9`, `_`, and `-` replaced by `-`."
> "The data directory is deleted automatically when you uninstall the plugin from the last scope where it is installed."

`userConfig` values: "All values are exported to hook processes as `CLAUDE_PLUGIN_OPTION_<KEY>` environment variables, where `<KEY>` is the option key uppercased." Monitors: "Monitor processes don't receive `CLAUDE_PLUGIN_OPTION_<KEY>` environment variables". MCP servers: `${user_config.KEY}` "is available for substitution ... in MCP and LSP server configs and hook commands".

Other: `CLAUDE_CODE_PLUGIN_CACHE_DIR` ("Override the plugins root directory ... Defaults to `~/.claude/plugins`"), `CLAUDE_CODE_SYNC_PLUGIN_INSTALL` ("Set to `1` in non-interactive mode (the `-p` flag) to wait for plugin installation to complete before the first query"), `CLAUDE_CODE_PLUGIN_PREFER_HTTPS`, `CLAUDE_CODE_PLUGIN_SEED_DIR`, `CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS` ("By default the budget is 1.5 seconds ... Timeouts on plugin-provided hooks do not raise the budget").

### 2.5 Variables that disable things Brigade may rely on

`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`: disables the Monitor tool and plugin monitors (tools-reference: "It is also not available when `DISABLE_TELEMETRY` or `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is set"), and "stops the background runs of plugin `command` sources". Same-machine cross-session messaging still works with flag fetching off (v2.1.248+).

`CLAUDE_CODE_SIMPLE` / `--bare`: "Disables auto-discovery of hooks, skills, custom commands, subagents, plugins, MCP servers, auto memory, and CLAUDE.md." Cross-session page: "When you start a session in bare mode, Claude Code doesn't bind the socket, so that session can't receive messages and doesn't appear in the agent list."

Precedence: "When the same variable is set in both your shell and a settings file `env` block, the settings file value applies." "Settings `env` blocks can't set" the messaging variables.

---

## 3. Sessions (`/docs/en/sessions`, plus hooks/env-vars/SDK)

### 3.1 Naming

> "At startup: `claude -n auth-refactor`"; "During a session: `/rename auth-refactor`. The name also appears on the prompt bar"; "From the session picker: Highlight a session and press `Ctrl+R`"; "On plan accept: Accepting a plan in plan mode gives the session a generated title based on the plan unless you've already named it"; "From claude.ai or the Claude app: Rename a Remote Control session; Claude Code applies the same name in the CLI. Requires Claude Code v2.1.221 or later"; "From the desktop app: Rename a session in the desktop app".

CLI reference `--name`, `-n`: "Set a display name for the session, shown in `/resume` and the terminal title. You can resume a named session with `claude --resume <name>`. In an interactive session, if another live session on this machine already uses the name, Claude Code applies a variant of it instead."

Uniqueness:
> "When you start or resume an interactive session with a name that another live session on this machine already uses, or rename a session into such a name, Claude Code leaves the name with the session that already has it, renames yours to a variant with a two-word suffix, such as `auth-refactor-graceful-unicorn`, and tells you ... Before v2.1.232, both sessions kept the name."
> "In three cases Claude Code doesn't rename the duplicate ... It doesn't check AI-generated titles or default display names. It doesn't check the `--name` of a background or `-p` session at startup. It can't rename a session on an earlier version of Claude Code."

Unnamed sessions:
> "Default display name: interactive sessions you never name still get a default display name when they start. Requires Claude Code v2.1.196 or later. The default combines the working directory's name with a two-character suffix, for example `my-app-3f`, and identifies the session in listings of running sessions, such as agent view and `claude agents --json` output. The default isn't a resume handle."
> "Generated title: if you don't name a session, Claude Code generates a session title for it. The title is a short summary of your first prompt, written by a background request to the small/fast model ... You see the first-prompt title in the session picker and in the statusline `session_name` field when no name is set."

Where the name lives:
- Cross-session page: "When you rename a session, Claude Code also updates the shared record your other sessions use to look up the session's name. If it can't update that record, it warns you in the `/rename` output that other sessions may still show the old name." (Changelog 2.1.247: "Fixed `/rename` silently confirming when the session registry could not be updated".) The file format is not documented; `verified-facts.md` observed `$CLAUDE_CONFIG_DIR/sessions/<pid>.json` with `name`, `nameSource`, `status`.
- Transcript: SDK `renameSession()` "Renames a session by appending a custom-title entry. Repeated calls are safe; the most recent title wins." `SDKSessionInfo.customTitle`: "User-set session title (via `/rename`)". Transcript format is "internal to Claude Code and changes between versions".
- Hooks: SessionStart input `session_title` = "The current session title if one is already set, for example via `--name` or `/rename`." SessionStart output `sessionTitle`: "Sets the session title, with the same effect as `/rename`. Use to name sessions automatically from the launch folder, git branch, or worktree name. Applies when `source` is `"startup"`, `"resume"`, or `"fork"`; ignored on `"clear"` and `"compact"`". So a Brigade SessionStart hook can both read and set the name, and "A hook that emits `sessionTitle` can check `session_title` first to avoid overwriting a title the user set explicitly".

Under Remote Control, `/list-agents` "leaves out any session name it can't attribute to a person, so a row left with no name reads `(unnamed session)`", and omits the own-name line "unless you typed that name at this terminal, with `--name` or with `/rename`". Brigade's display of native names should treat name provenance as untrusted display data.

### 3.2 Resume, continue, fork, branch, clear, compact: effect on the session ID

- `--resume <session-id>`: "On `--resume <session-id>` [CLAUDE_CODE_SESSION_ID] receives the resumed ID, matching hooks and Bash." SessionStart `source: "resume"` fires for "`--resume`, `--continue`, or `/resume`".
- `--continue` / `--resume` without ID: CLAUDE_CODE_SESSION_ID "may receive the initial startup ID instead" (i.e. the env value in Bash/hooks may be stale; rely on hook JSON `session_id`).
- Fork: CLI `--fork-session`: "When resuming, create a new session ID instead of reusing the original (use with `--resume` or `--continue`)". SDK: "The fork gets its own session ID; the original's ID and history stay unchanged." SessionStart `source: "fork"`: "A new session forked from an existing one: `--fork-session` with `--resume` or `--continue`, the `/fork` background copy, or `/branch`" ("Before v2.1.214, forked sessions reported source `"resume"`").
- `/branch`: "Sessions created with `/branch` or `--fork-session` get their own session IDs and appear as separate rows." "`/branch` copies the transcript and switches the running Claude Code process to write to it." Background Bash/subagents "Keep running. Their output appears in the new branch". Remote Control "Stays connected".
- `/clear`: SessionStart `source: "clear"`; CLAUDE_CODE_SESSION_ID "is updated on `/clear`"; SessionEnd fires with `reason: "clear"` first (SessionEnd reasons: `clear`, `resume`, `logout`, `prompt_input_exit`, `other`). Name: "With no argument, the new conversation keeps a name you set with `--name` or `/rename`, but not an AI-generated session title." SessionStart `sessionTitle` output is "ignored on `"clear"`".
- Compact: SessionStart `source: "compact"` ("Auto or manual compaction"); no new ID is documented; `sessionTitle` ignored on compact. PreCompact/PostCompact hooks exist.
- Two terminals: "If you resume the same session in two terminals without forking, messages from both interleave into one transcript."
- Not restored on resume: "If the session depended on `--mcp-config`, `--settings`, `--plugin-dir`, `--fallback-model`, or directories added with `--add-dir`, pass them again when you resume". Settings files are re-read. Background Bash and monitor tasks are not restored.
- `claude -p` and SDK sessions are "out of the session picker and out of `claude --continue`" but resumable by ID.
- v2.1.251: SessionStart on `resume`/`fork` also receives `seconds_since_last_response`, `context_tokens`, `prompt_cache_likely_expired`, `estimated_cache_write_usd`.

Implication for Brigade's identity model: the native `session_id` is stable across `/compact` and `--resume <id>`, changes on `/clear`, `/branch`, `/fork`, `--fork-session`. A Brigade SessionStart hook should treat `source` of `startup|resume` as "register or re-register this native ID", `clear|fork` as "new native ID" (re-register; optionally inherit the Brigade name because Claude Code keeps a user-set name on `/clear`), and `compact` as a no-op. SessionEnd `reason: "clear"`/`"resume"` means the process continues with a different session; do not treat those as process exit.

### 3.3 Transcript storage

> "By default, Claude Code stores transcripts as JSONL at `~/.claude/projects/<project>/<session-id>.jsonl`, where `<project>` is your working directory path with non-alphanumeric characters replaced by `-`."
> "The entry format is internal to Claude Code and changes between versions, so scripts that parse these files directly can break on any release."

SDK functions that read/modify session metadata without parsing JSONL: `listSessions()`, `getSessionInfo()`, `getSessionMessages()`, `renameSession()`, `tagSession()` (`@anthropic-ai/claude-agent-sdk`). `SDKSessionInfo` fields: `sessionId`, `summary`, `lastModified`, `fileSize`, `customTitle`, `firstPrompt`, `gitBranch`, `cwd`, `tag`, `createdAt`.

---

## 4. Sandboxing (`/docs/en/sandboxing`)

Scope:
> "The sandbox isolates Bash subprocesses."
> "Sandboxing provides OS-level enforcement that restricts what Bash commands can access at the filesystem and network level. It applies only to Bash commands and their child processes."
> "Comprehensive coverage: restrictions apply to all scripts, programs, and subprocesses spawned by commands"
> Protected paths rationale: "A command that could edit those files could grant itself permissions, or add a hook or MCP server that Claude Code runs outside the sandbox."

So: hooks and MCP servers run outside the sandbox. Plugin monitors "run unsandboxed at the same trust level as hooks" (plugins-reference). Subagents "use the same sandbox configuration".

Unix sockets (macOS Seatbelt; Linux bubblewrap + seccomp): see 1.4. Cross-session page:
> "**Sandboxed sessions**: control whether a Bash command can reach the socket from inside the sandbox with the sandbox's Unix-socket settings, `sandbox.network.allowAllUnixSockets` and `sandbox.network.allowUnixSockets`."
> "Read this section when ... a sandboxed command can't reach the socket."

Security note: "Privilege escalation via Unix sockets: the `allowUnixSockets` configuration can inadvertently grant access to system services that could lead to sandbox bypasses."

WSL2: "WSL hands a launch of a Windows binary ... to the Windows host over a Unix socket, so whether a sandboxed command can launch one follows the sandbox's Unix-socket settings".

Temp dirs: "Unless you disable filesystem isolation, Claude Code sets `$TMPDIR` to this directory for sandboxed commands ... sandboxed and unsandboxed commands resolve `$TMPDIR` to different directories."

Environment: "sandboxed Bash commands inherit the parent process environment by default, including any credentials set there." (So `CLAUDE_CODE_MESSAGING_TOKEN` is visible to sandboxed Bash; only the socket connect is blocked.)

Defaults: sandbox off (`sandbox.enabled` default `false`); Linux needs `bubblewrap` and `socat`; "WSL1 and native Windows are not supported."

Brigade consequence: the inbound watcher should not be a Bash child of the model's tool call; it should be a hook child (unsandboxed) or a plugin monitor / MCP server process. If a user enables the sandbox and Brigade's poster is a Bash child, the plugin must document `sandbox.network.allowUnixSockets: ["/tmp/cc-socks/*"]`-style configuration; whether globs are accepted in that list is not documented.

---

## 5. Channels (`/docs/en/channels`, `/docs/en/channels-reference`)

Status:
> "Channels are in research preview. They require Anthropic authentication through claude.ai or a Console API key, and are not available on Amazon Bedrock, Google Cloud's Agent Platform, or Microsoft Foundry. Team and Enterprise organizations must explicitly enable them."
> "Channels are a research preview feature. Availability is rolling out gradually, and the `--channels` flag syntax and protocol contract may change based on feedback."
> "Neither `--channels` nor `--dangerously-load-development-channels` appears in `claude --help` while the feature is in preview. The flags work even though they aren't listed."
> "During the preview, `--channels` only accepts plugins from an Anthropic-maintained allowlist, or from your organization's allowlist if an admin has set `allowedChannelPlugins`."
> Reference: "During the research preview, custom channels aren't on the approved allowlist. Use `--dangerously-load-development-channels` to test locally."
> "This flag skips the allowlist only. The `channelsEnabled` organization policy still applies."

Enterprise: "claude.ai Team and Enterprise: channels are blocked until an Owner enables them." "Pro and Max users without an organization skip these checks entirely: channels are available and users opt in per session with `--channels`." `channelsEnabled` is a Managed-scope key.

Mechanics (for comparison): "A channel is an MCP server that runs on the same machine as Claude Code. Claude Code spawns it as a subprocess and communicates over stdio." Server must "Declare the `claude/channel` capability" and "Emit `notifications/claude/channel` events". Delivery framing: "The event arrives in Claude's context wrapped in a `<channel>` tag. The `source` attribute is set automatically from your server's configured name"; `meta` keys become attributes. "Claude Code doesn't acknowledge notifications ... If the session hasn't loaded your server as a channel, or the organization policy blocks it, Claude Code drops the events silently and returns no error to your server." "Events queue into the session and are processed in order. If several notifications arrive while Claude is busy, they're delivered together on the next turn". Channel plugins declare `channels: [{ server, userConfig }]` in `plugin.json`. Runtime: "Bun, Node, and Deno all work."

Also: `-p` runs with channels disable interactive tools; permission relay is a separate opt-in capability.

Assessment: unchanged since the logical plan. Channels would give Brigade a distinct, host-provided framing (`<channel source="brigade" ...>`) and idle-wake, but only behind a dangerous dev flag, an org toggle, and an allowlist Brigade cannot join without Anthropic curation. Not GA; do not build on it. Revisit if `--channels` leaves preview or the allowlist opens to community plugins.

---

## 6. Agent view and background sessions (`/docs/en/agent-view`)

> "Agent view, opened with `claude agents`, is one screen for all your background sessions ... Each background session is a full Claude Code conversation that keeps running without a terminal attached"
> "Agent view is in research preview"
> "Background sessions are hosted by a per-user supervisor process, separate from your terminal and from agent view."
> "Each background session is its own Claude Code process, managed by the supervisor rather than tied to your terminal."
> "Once a session finishes and sits unattached for about an hour, the supervisor stops its process to free resources. A session you have pinned with `Ctrl+T` is exempt ... the next time you attach or reply to a stopped session, the supervisor starts a fresh process from where it left off."
> "Work whose state lives only inside the process itself stops with it instead of being handed off. That's shell commands a subagent started ... and running monitors, whose event stream can't be moved to another process."
> "If you set `CLAUDE_CONFIG_DIR`, the supervisor uses that directory instead of `~/.claude` and runs as a separate instance with its own sessions."
> State: `~/.claude/daemon.log`, `~/.claude/daemon/roster.json`, `~/.claude/jobs/<id>/state.json`, `~/.claude/jobs/<id>/tmp/`; `CLAUDE_JOB_DIR` env var in each background session.
> "`claude agents --json` prints active sessions as a JSON array and exits" with fields including `id` and `state` (`working`, `blocked`, `done`, `failed`, `stopped`).
> "Claude Code rejects `--bg` combined with `-p` or `--print`".
> `/status` "Session kind" row: "`background job · attached` or `background job · unattended` in a background session ... and `interactive` in any other session."

How they message: humans reply through agent view's peek panel ("Type a reply in the peek panel and press `Enter` to send it to that session ... A reply that can't be delivered ... is saved and sent to the session as its next prompt when its process starts again"). Between sessions, the normal cross-session mechanism applies: "After the fork, the two conversations are independent ... though in sessions where cross-session messaging is enabled, either session's Claude can explicitly message the other." Cross-session page: local sessions listed "including background sessions. A session appears only when it binds an inbox socket. The worker process that the supervisor process keeps ready for your next background session appears once you dispatch work to it."

Held-message dialog in a background session: "While no terminal is attached to a background session, Claude Code leaves the dialog open past the deadline. After you attach, Claude Code closes the dialog and drops the message only if it stays unanswered for a full deadline period."

Brigade consequences:
- A background session's process (and its inbox socket) may be stopped after about an hour idle; a Brigade lease should expire, and the adapter must not assume an idle-but-registered session is reachable. A restarted process re-runs SessionStart (inference: `source` would be `resume`; not documented explicitly) so the hook re-registers.
- Plugin monitors do not survive a supervisor restart of the process; the hook-based watcher pattern is more robust for background sessions.
- `claude agents --json` can enrich `session list` state (`working`/`blocked`/`done`) for background sessions.

---

## 7. Plugins (`/docs/en/plugins`, `/docs/en/plugins-reference`)

### 7.1 Local development and testing

> "Use the `--plugin-dir` flag to test plugins during development. This loads your plugin directly without requiring installation." `claude --plugin-dir ./my-plugin`; also `.zip`; repeat the flag for multiple.
> "When a `--plugin-dir` plugin has the same name as an installed marketplace plugin, the local copy takes precedence for that session ... The exception is plugins that managed settings force-enable or force-disable: `--plugin-dir` cannot override those."
> "As you make changes to your plugin, run `/reload-plugins` to pick up the updates without restarting. This reloads plugins, skills, agents, hooks, plugin MCP servers, and plugin LSP servers."
> `--plugin-url`: "Claude Code fetches the archive at startup and loads it for that session only."
> "Run `claude plugin validate ./your-plugin` locally before you submit ... add `--strict` to treat them as errors."
> `claude plugin init my-tool`: "This creates `~/.claude/skills/my-tool/` with a `.claude-plugin/plugin.json` manifest and a starter `SKILL.md`. On the next session it loads as `my-tool@skills-dir` with no marketplace or install step."
> Skills-dir plugin at project scope: "Background monitors do not load"; "MCP servers it declares go through the same per-server approval as a project `.mcp.json`".
> "Changes to the plugin's other components, such as `hooks/`, `.mcp.json`, `agents/`, and `output-styles/`, do not [take effect immediately]. Run `/reload-plugins` or restart".
> `--plugin-dir` plugins use the identity `<name>@inline` (mentioned under synced plugins). Sessions page: `--plugin-dir` is not restored on `--resume`.

### 7.2 Marketplace and distribution

> "The `--plugin-dir` flag is useful for development and testing. When you're ready to share your plugin with others, see Create and distribute a plugin marketplace."
> "To keep a plugin internal to your team, host the marketplace in a private repository."
> Two public marketplaces: `claude-plugins-official` (curated, "There is no application process") and `claude-community` (submissions reviewed; "Approved plugins are pinned to a specific commit SHA").
> Week 33: "Plugin marketplaces accept command sources"; Week 32: "Marketplaces can distribute a plugin as a zip archive with the new archive source, downloaded over HTTPS with an optional SHA-256 pin, so installs work without git or npm"; GitLab marketplaces supported (v2.1.232).
> Install scopes: `user` (`~/.claude/settings.json`, default), `project`, `local`, `managed`.
> `defaultEnabled: false` "to ship a plugin that installs disabled ... Use this for plugins that add cost or scope a user should opt into, such as one that connects to an external service."

### 7.3 Node dependencies

> "The install runs only when the plugin's root directory contains both a `package.json` and a supported lockfile": `bun.lock`/`bun.lockb` → `bun install --frozen-lockfile --ignore-scripts`; `npm-shrinkwrap.json`/`package-lock.json` → `npm ci --ignore-scripts`. "Claude Code skips `yarn.lock` and `pnpm-lock.yaml`".
> "No lifecycle scripts ... 60-second timeout ... A failed or skipped install never blocks the plugin." "You can't turn the automatic install off".
> For anything else, "install them from a hook into the persistent data directory" (SessionStart hook pattern with `${CLAUDE_PLUGIN_DATA}` and `NODE_PATH`).

### 7.4 Monitors

> "Each monitor runs a shell command for the lifetime of the session and delivers every stdout line to Claude as a notification"
> "They run only in interactive CLI sessions, run unsandboxed at the same trust level as hooks, and are skipped on hosts where the Monitor tool is unavailable."
> Fields: `name`, `command`, `description`, optional `when` (`"always"` default, or `"on-skill-invoke:<skill-name>"`).
> "A monitor `command` can't reference `${user_config.*}` values ... Monitor processes don't receive `CLAUDE_PLUGIN_OPTION_<KEY>` environment variables, so have the monitor script read the value from a config file it owns."
> "If you disable a plugin mid-session, Claude Code doesn't stop monitors that are already running; they stop when the session ends."
> "monitors require a session restart" after a plugin update.
> Declared as `experimental.monitors` (top-level still works with a validate warning).
> Monitor tool availability: "not available on Amazon Bedrock, Google Cloud's Agent Platform, or Microsoft Foundry. It is also not available when `DISABLE_TELEMETRY` or `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is set." Monitor commands use Bash permission rules; in auto mode the classifier reviews them.

Whether a plugin monitor's environment includes `CLAUDE_CODE_MESSAGING_SOCKET`/`TOKEN` is not documented (the env-vars page names hooks and Bash commands only).

### 7.5 Hooks in plugins

`hooks/hooks.json` or inline. Exec form: "A command hook runs as exec form when `args` is set ... There is no shell, so each `args` element is one argument exactly as written, and path placeholders like `${CLAUDE_PLUGIN_ROOT}` are substituted into `command` and into each `args` element as plain strings." "Plugin hooks additionally substitute `${user_config.*}` values, in exec form only". Windows exec form needs a real `.exe` (so `node` is fine, `.cmd` shims are not).

Async: "`async`: If `true`, runs in the background without blocking"; "Once an async hook is running in the background, Claude Code doesn't enforce `timeout` on it"; "In non-interactive mode with the `-p` flag, Claude Code kills any async hook still running at teardown"; "If your hook's work must outlive a `claude -p` session, start a fully detached process from it". "Hook output is delivered on the next conversation turn. If the session is idle, the response waits until the next user interaction. Exception: an `asyncRewake` hook that exits with code 2 wakes Claude immediately even when the session is idle." (`asyncRewake`: "The hook's stderr, or stdout if stderr is empty, is shown to Claude as a system reminder".) SessionStart supports only `type: "command"` and `type: "mcp_tool"`.

Hook environment: "A hook process inherits the parent environment"; `CLAUDE_ENV_FILE` available to SessionStart/Setup/CwdChanged/FileChanged to persist exports for later Bash commands.

### 7.6 Plugin `.mcp.json`

Placeholder substitution in `command`, `args`, `env` for stdio servers; `${user_config.KEY}` substitution in MCP server configs (so sensitive `userConfig` values can be passed to the MCP shim's env without a shell). "`/reload-plugins` ... keeps the live connections of plugin servers whose configuration is unchanged". `/cd` (v2.1.246+) connects/disconnects plugin servers per the new directory's settings.

---

## 8. MCP (`/docs/en/mcp`)

### 8.1 Server config and environment

Stdio entry: `command`, `args`, `env`, optional `timeout` (ms; "Values below 1000 are ignored"). `.mcp.json` env expansion: "`${VAR}`: expands to the value of environment variable `VAR`", "`${VAR:-default}`"; locations: `command`, `args`, `env`, `url`, `headers`. "If a referenced environment variable isn't set and has no default value, the config still loads: Claude Code reports a missing-variable warning ... and uses the unexpanded `${VAR}` text as-is."

"Claude Code sets `CLAUDE_PROJECT_DIR` in the spawned server's environment to the project root" and answers `roots/list` with the launch dir plus additional dirs, sending `notifications/roots/list_changed` (v2.1.203+). `CLAUDE_CODE_SESSION_ID` is set in stdio MCP subprocesses (see 2.2). "Stdio servers are local processes, and Claude Code doesn't reconnect them automatically."

### 8.2 Gap: expansion timing of session-scoped variables

The docs do not state whether `${CLAUDE_CODE_MESSAGING_SOCKET}` or `${CLAUDE_CODE_SESSION_ID}` in a plugin `.mcp.json` `env` block expands from the session's process environment at server launch. `CLAUDE_CODE_SESSION_ID` is documented as set directly in the MCP subprocess env, which suffices for correlation at spawn time. Confidence: uncertain; verify in the integration experiment.

### 8.3 Timeouts and output

`MCP_TIMEOUT` (startup, ms); `MCP_TOOL_TIMEOUT` (default "about 28 hours"); idle timeout "30 minutes for stdio servers" (`CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT`); automatic backgrounding of long tool calls (`CLAUDE_CODE_MCP_AUTO_BACKGROUND_MS`); output "limits output to 25,000 tokens by default" (`MAX_MCP_OUTPUT_TOKENS`; per-tool `anthropic/maxResultSizeChars`).

### 8.4 `anthropic/requiresUserInteraction`

> "If you're building an MCP server, you can mark a tool as requiring explicit approval on every call by setting `_meta["anthropic/requiresUserInteraction"]` to `true` in the tool's `tools/list` response entry. The value must be the JSON boolean `true`; any other value is ignored."
> "Claude Code shows that tool's permission prompt on every call, even in `acceptEdits`, `auto`, and `bypassPermissions` permission modes, and doesn't offer a "don't ask again" option for it. Allow rules that match the tool don't skip the prompt either. In `dontAsk` mode, which never prompts, Claude Code denies the call instead."
> "In non-interactive mode with `--permission-prompt-tool`, an `allow` result from the prompt tool for a flagged tool is converted to a deny with the message `MCP tool requires user interaction; not supported via --permission-prompt-tool`."
> "Requires Claude Code v2.1.199 or later." (2.1.246 fixed the stray "don't ask again" option.)

Use for Brigade: candidate for a future `TeamJoin`/secret-handling tool if one is ever model-visible; not for `TeamSendMessage`, which should be low-friction.

### 8.5 Server instructions

> "Only tool names and server instructions load at session start, so adding more MCP servers has minimal impact on your context window." (tool search)
> "Server instructions help Claude understand when to search for your tools, similar to how skills work."
> "Claude Code truncates tool descriptions and server instructions at 2KB each. Keep them concise to avoid truncation, and put critical details near the start."

Brigade's shim should put "reply with TeamSendMessage to <session_id>, not SendMessage" near the start of both the server instructions and the `TeamSendMessage` description, and repeat it in the plugin skill.

### 8.6 Project `.mcp.json` approvals

"In `claude -p` runs, Agent SDK sessions, and cloud sessions, Claude Code can't show that prompt: it loads project-scoped servers without asking." Plugin servers start automatically when the plugin is enabled; `disabledMcpjsonServers`/`enabledMcpjsonServers` control project files.

---

## 9. Cross-session messaging page facts not in the plan (`/docs/en/cross-session-messaging`)

- Requirements: "v2.1.224 or later on macOS and Linux ... On native Windows, it requires Claude Code v2.1.234 or later. When a session meets the requirements, messaging is on with nothing to enable."
- Same-machine messaging "available on every provider, including Amazon Bedrock ... and in sessions that run with feature-flag fetching off. On those providers, and with flag fetching off, same-machine messaging requires Claude Code v2.1.248 or later."
- Discovery: "Each session registers itself in files on disk. When Claude lists or messages your local sessions, Claude Code reads those files to find the sessions, so two sessions can reach each other only when they can see the same files." Containers and WSL2-vs-native-Windows are isolated.
- Socket dir: "uses a private per-user directory, `/tmp/cc-socks-<uid>`, instead. When it can't accept any directory, the session runs without an inbox: Claude Code shows a notice, `/status` shows `unavailable`".
- Connection rule: "Claude Code closes a connection that hasn't sent a complete line within 30 seconds, so capture a slow command's output first and then open the connection to send it." (Changelog 2.1.243.)
- Own-child verification: "On Linux ... Claude Code can verify by process evidence even for a child that has already exited. On macOS it can verify that way only while the posting process is still running, and in a container where Claude Code runs as process ID 1 it has no process evidence at all. On native Windows it also has none." Fallback is the token in the auth line. "When Claude Code can verify neither way, it treats the message like any other that asserts no permission class, so a session that bypasses permission prompts holds it for your approval."
- `-p` sessions: bind a socket; held messages expire after `dialogExpiry` unless `"never"`; "To let a `-p` worker take messages unattended, start it with `crossSessionInbound` set to `accept` in its `--settings` value."
- Default class rule: "It groups sessions that bypass permission prompts into one class, and every other session into the other. Plan mode counts as bypassing in sessions with bypass permissions available, and auto, `acceptEdits`, and `dontAsk` count as prompting". Receiving prompting session: delivers unless sender says bypass. Receiving bypass session: holds unless sender says bypass.
- Send-side safety checks (errors page): symlink at target socket, endpoint not the expected process, not owned by this user, pid reuse — "Before v2.1.248, Claude Code didn't check the endpoint's owning user or process start time".
- Limits: ~1,048,576 serialized chars; sender-side burst refusal ("30 were sent recently" example); receiver rate limit, duplicate suppression, queue of 50, hold of 100.
- Preview: since v2.1.247 a one-line `› Message from @api-worker: ... (ctrl+o to expand)` preview; `--verbose` shows full text.
- `SendMessage` `summary` input: "Claude can include an optional `summary` input, typically 5-10 words, that Claude Code shows as a one-line preview ... Claude Code truncates a summary longer than 200 characters" (tools-reference).
- `notify_when_idle` (v2.1.236+): one-shot idle notice, same-machine only, 12-hour expiry; "Only the Claude in your main conversation can subscribe".
- `@` mention of sessions in prompts (v2.1.232+).
- Subagent messages: "A message that a subagent wrote arrives under the sending session's name, with the subagent identified in the message text. A reply to it reaches that session's main conversation, not the subagent."
- Receiver instruction set (documented): "It can't approve anything", "It can't change configuration", "Commands don't run", "Permission prompts still fire".

---

## 10. Can a poster change the harness framing or claim to be a host application?

### 10.1 What is documented for the socket

The only documented socket protocol is: optional/required auth line `{"type":"auth","token":"<token>"}` followed by message frames; the env-vars page and cross-session page describe "a script or hook to post into a session" and nothing else. The docs describe no `from`, `from-name`, `origin`, or "host" field on the socket. Everything arriving on the socket is subject to the inbound controls "as any other peer message". `verified-facts.md` (live) shows the model receives the fixed peer preamble ("reply via SendMessage to the `from=` address") whether or not the content carries a `<cross-session-message>` wrapper, and that a distinct host-injected preamble exists only for stdin (`--input-format stream-json`) hosts.

### 10.2 What is documented for SDK / stream-json hosts

`SDKUserMessage` (input type) includes `origin?: SDKMessageOrigin` and `isSynthetic?: boolean`. `SDKMessageOrigin` includes:
```
| { kind: "peer"; from: string; fromMode?: "bypass" | "prompting"; name?: string; fromSession?: string; senderTaskId?: string; body?: string; verifiedPeerPid?: number }
```
Documented host semantics:
> "`human` ... If your application forwards what the user typed as a user message, set its `origin` to `{ kind: "human" }` explicitly: Claude Code treats a user message with no `origin` as unattributed"
> "`fromMode`: the sending session's permission class, `bypass` or `prompting`, declared by a host that relays a peer message between your sessions, such as the desktop app. Claude Code reads it in the receiving session when it applies the inbound controls. Requires Agent SDK v0.3.234 or later."
> "`fromSession`: the sender's host-openable session ID, set by the sender's host so your UI can link back to the sending session. Like `from`, it is sender-asserted"
> "`verifiedPeerPid` ... The field is absent when Claude Code can't verify it, such as on Windows or non-socket ingress"
> "`unclassified` ... When Claude Code receives an `SDKUserMessage` with `isSynthetic: true` and can't classify it as any other `kind`, it sets this kind as the message arrives and frames the turn to the model as a non-user source rather than treating it as human input. Your application shouldn't set this value."

So the only documented actor that can declare peer metadata (and therefore trigger the "delivered by your host application" framing observed in the binary) is the process that owns Claude Code's stdin under `--input-format stream-json` or the SDK. The desktop app is the named example of such a relay ("Claude Code quotes each incoming message and attributes it to the session that sent it"). A hook, monitor, or socket poster is not that actor. Whether the CLI honors an `origin` object on a raw stream-json stdin frame is implied by the type but not explicitly documented for the CLI; mark as likely but unverified.

### 10.3 Other documented framings available to a plugin

| Path | Framing to the model | Wakes idle session | Availability |
|---|---|---|---|
| Inbox socket (hook/Bash child, token auth) | Peer message with fixed "another Claude session ... reply via SendMessage" preamble | Yes ("starts a new turn with the message") | v2.1.224+ (Windows 2.1.234+), any provider (2.1.248+) |
| Plugin monitor stdout line | "notification" (Monitor-style), not a peer message | Yes (monitor events interject) | Interactive CLI only; experimental; off on Bedrock/Vertex/Foundry and with telemetry/nonessential-traffic disabled |
| `asyncRewake` hook exiting 2 | "system reminder" containing stderr/stdout | Yes ("wakes Claude immediately even when the session is idle") | One-shot per hook process; fires only from a hook event |
| Async hook `additionalContext`/`systemMessage` | Hook context | No ("waits until the next user interaction") | Any |
| SessionStart/UserPromptSubmit stdout | Context on that turn | No | Any |
| Channel `notifications/claude/channel` | `<channel source="...">` tag with meta attributes | Yes | Research preview; allowlist; dev flag |
| SDK / stream-json host with `origin` | Host-declared peer or human origin | Yes | Only when Brigade owns the Claude Code process (not applicable to a plugin) |

Changelog 2.1.251 confirms framing is chosen by Claude Code from sender classification, not payload: "Improved framing of messages from your own subagents: Claude is told the sender is a worker inside this session, not an unrelated Claude session".

### 10.4 Recommendation

Accept the fixed peer preamble. Put the reply instruction inside the delivered body (first line), e.g. `[Brigade team message from alice@example.com / payments-api (session brg_…)] Reply with TeamSendMessage to session_id brg_…, not SendMessage.` Make `TeamSendMessage`'s description and the MCP server instructions say the same. Do not rely on the `<cross-session-message from="did:brigade:…">` wrapper being parsed to Brigade's advantage: `from` charset allows `did:` but the model is still told to reply via `SendMessage`, which cannot reach a `did:` address, and native `SendMessage` to an unknown name errors. Consider a plugin monitor as the secondary path for users on interactive CLI who prefer notification framing; do not make it the primary path because of its availability holes and non-survival across background-session restarts.

---

## 11. What changed recently (what's-new w30–w34 and changelog v2.1.240–v2.1.251)

Week 34 (v2.1.234–v2.1.239): `notify_when_idle` on `SendMessage`; "On native Windows, your Claude Code sessions can now message each other"; Remote Control "out of research preview"; `/design`; Concise output style.
Week 33 (v2.1.225–v2.1.233): `@` mention of sessions; unique interactive session names; marketplace `command` sources; `additionalMarketplaces`/`allowedMarketplaces` aliases; fork subagents on by default.
Week 32 (v2.1.220–v2.1.224): cross-session messaging shipped ("Requires v2.1.224 or later"); auto mode default from August 14; zip `archive` marketplace source; plugins activate in-session after `/plugin install`; "PreToolUse auto-allow hooks no longer bypass tool restrictions in Claude Code's internal side tasks".
Week 31: no digest (404).
Week 30 (v2.1.214–v2.1.219): Opus 5 default; `sandbox.filesystem.disabled`; `CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS`; "Long-running tool calls emit a periodic progress heartbeat".

Changelog entries after the last digest:
- 2.1.251 (Aug 28): "`SessionStart` resume hooks now receive session staleness and the estimated re-cache cost"; "Fixed replying to a message Claude Desktop delivered from another session: `SendMessage` to that session id now delivers through Claude Desktop"; "Fixed `--input-format stream-json`: client-injected assistant tool calls sent without a message id were merged"; "Improved framing of messages from your own subagents"; "Changed how Bash command output files are created and read back when commands run in the sandbox".
- 2.1.248 (Aug 27): "Added cross-session messaging (`SendMessage` / `ListAgents`) between sessions on the same machine on Bedrock, Vertex, and Foundry, and when telemetry is disabled"; "Fixed an invalid `crossSessionInbound` value being silently ignored: it now warns and holds cross-session messages (user settings) or refuses them (managed settings) until fixed"; "Improved cross-session messaging: falls back to a private per-user `/tmp` directory when the default one can't be used"; "Changed cross-session messaging in Linux user namespaces: root-equivalent trust for unmapped owners is limited to canonical system directories"; "Changed `SendMessage` from a subagent to another session: the result now notes that any reply is delivered to the parent session's conversation"; "Fixed hooks silently treating a stdout `{…}` object that isn't valid JSON as plain text; it's now reported as a hook error".
- 2.1.247 (Aug 26): "Changed cross-session peer messages to collapse by default to a one-line `Message from @<sender>: <first line>` preview"; "Fixed `/rename` silently confirming when the session registry could not be updated"; `SendFeedback` tool.
- 2.1.246 (Aug 25): "Fixed MCP tools marked `requiresUserInteraction` still offering "Yes, and don't ask again""; "Improved `/cd`: the new directory's project settings, hooks, `.mcp.json` servers ... now take effect right after the move"; "Fixed hook error messages showing a literal `${CLAUDE_PLUGIN_ROOT}`"; "Fixed `/reload-plugins` reporting 0 skills for plugins that define skills under `skills/*/SKILL.md`"; "Fixed `/fork` from an already-forked or backgrounded session starting the new session with an empty conversation".
- 2.1.243 (Aug 25): "Fixed cross-session messaging silently turning off inside user namespaces and rootless containers after the 2.1.232 socket-directory hardening"; "Changed the cross-session messaging inbox socket to close connections that send no complete line within 30 seconds; scripts posting to it should connect once their data is ready"; "Fixed `/clear` removing the `/rename` session name from the prompt bar even though the name was kept for the new session"; "Fixed plugin dependencies declared with a `marketplace` field never resolving when both plugins are loaded together via `--plugin-dir`"; "Fixed remote MCP servers in non-interactive (`-p`) and SDK sessions never recovering after a dropped connection".

Nothing in v2.1.240–v2.1.251 adds a documented field for socket posters, changes the socket protocol beyond the 30-second rule, or moves Channels out of preview.

---

## 12. Conflicts and confirmations against `verified-facts.md`

- Socket protocol, token, per-uid fallback dir, 30 s close, own-child delivery in bypass mode: docs agree with live observation.
- The `<cross-session-message ...>` wrapper attribute grammar, hop-chain, and preamble text are from the binary (live), not from docs; docs only describe the SDK-side decoded fields (`name`, `body`, `from`, `fromSession`, `fromMode`, `verifiedPeerPid`), which match the wrapper's attributes.
- Session registry file (`$CLAUDE_CONFIG_DIR/sessions/<pid>.json`) is confirmed to exist conceptually ("shared record", "session registry") but its format is undocumented.
- Monitors: docs add that project-scope skills-dir plugins do not load monitors, monitors survive plugin disable but not process restarts, and monitors do not get `CLAUDE_PLUGIN_OPTION_*`.
- `dialogExpiry` scope is User or managed (not project); the plan should not suggest setting it in a repo.
