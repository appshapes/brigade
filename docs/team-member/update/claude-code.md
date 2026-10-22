# Update in Claude Code

## Automatic

The project's committed marketplace entry carries `"autoUpdate": true`, so Claude Code refreshes the marketplace
and updates the plugin in the background after a session starts, up to ten minutes later, and asks you to run
`/reload-plugins`; the new version also loads at the next launch. That is Claude Code's own mechanism, on by
default for Anthropic's marketplaces and off for every other, and the committed flag turns it on for each member
the moment they trust the folder (measured on Claude Code 2.1.270,
[E9](../../experiments/E9-plugin-install-update.md)). Without the flag, members update by hand.

## Right now

`/brigade:update` refreshes the marketplace, updates the user-scope install and then any project-scope install the
folder has, and ends by asking for `/reload-plugins`. A running session keeps the version it started with until
then; its background watcher is replaced by the new version at the next prompt after the reload.

One install per Claude account covers every repository and every clone under it, so an account updates once. A
machine with several accounts (`CLAUDE_CONFIG_DIR`) updates once per account.

## After an update

Your terminal runs the path `brigade whoami` prints on its `terminal:` line, which now names the new version
([Use brigade in a terminal](../install/terminal.md)). A `~/.local/bin/brigade` symlink from an earlier release
points at the old version; remove it.
