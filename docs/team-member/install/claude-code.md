# Install in Claude Code

Brigade is a Claude Code plugin. Two commands inside a session install it; nothing is compiled, no server runs.

## Before you start

- **Log in once per configuration directory.** Claude Code keeps your login in its own configuration directory,
  `~/.claude` or the one `CLAUDE_CONFIG_DIR` names. A directory that has never logged in answers
  `Not logged in · Please run /login` and stops: run `claude auth login` once in it, or `/login` in a session.
  Installing needs no login; starting a session does.

## A project that already uses Brigade

The project's committed `.claude/settings.json` names the `brigade` marketplace, so Claude Code adds it when you
trust the folder. At the prompt:

```
/plugin install brigade@brigade
```

- Choose **user** when it asks where to install. User scope is one install for every folder under this Claude
  account, every repository and every clone, and one update moves them all.
- If it asks nothing and shows the plugin's options, this account already has Brigade: run `/brigade:update`.
- The command copies the plugin into `<configuration directory>/plugins/cache/brigade/brigade/<version>/` and ends
  with `Plugin is now active.` or `Run /reload-plugins to activate.`; do what it says.
- `14 userConfig options not yet set` is information, not a to-do: every option has a working default
  ([Options](../configure/options.md)).

## The first machine of a project

A checkout whose `.claude/settings.json` does not yet name the marketplace adds it first:

```
/plugin marketplace add appshapes/brigade
/plugin install brigade@brigade
```

The first command clones this public repository (SSH, then HTTPS; no GitHub key needed). Answer the scope
question with **user**. Then commit the marketplace entry with auto-update on, so every collaborator after you
installs with the one command and never updates by hand:

```json
{
  "extraKnownMarketplaces": {
    "brigade": { "source": { "source": "github", "repo": "appshapes/brigade" }, "autoUpdate": true }
  }
}
```

Do **not** choose project scope and do not commit `enabledPlugins` for Brigade: project scope is the committed
settings entry, it takes precedence over a user-scope install in that folder, and it gives every checkout a
version of its own that only an update run in that checkout moves. Measured on Claude Code 2.1.270
([E9](../../experiments/E9-plugin-install-update.md)).

## What the first use does

On a cold cache the first session downloads the 8 MB Brigade binary before the hooks can run, so the first
`SessionStart` waits on the download; the line naming your team appears on a later prompt. It needs about
185 kB/s; an attempt gives up after 45 s, your next prompt then shows one line beginning `Brigade: not installed:`,
and `/clear` or a new session tries again. The download is one file, checked against the sha256 that ships inside
the plugin, once per version per machine.

## From a checkout

```sh
claude --plugin-dir ./plugin
```

loads the plugin from a clone of this repository, in place, for that one session. Its options key is
`brigade@inline` instead of `brigade@brigade`.

## Not provided

There is no Homebrew tap and no `.deb` or `.rpm`. A packaged install would put a second, separately versioned
`brigade` on your PATH, which is the shadowing case [Use brigade in a terminal](terminal.md) describes.

Next: [Join in Claude Code](../join/claude-code.md).
