# Update in Codex CLI

Codex installs the package from the `brigade` marketplace, so an update is a refresh of the marketplace's snapshot
and an install again:

```sh
codex plugin marketplace upgrade brigade
codex plugin add brigade@brigade
```

then start a new session and type one prompt. A running session keeps the version it started with, and nothing a
session's model can run replaces it.

The bootstrap under Codex's plugin cache carries the version in its path, so the path your terminal runs changes
with every update: run `brigade whoami` in a new session and copy its `terminal:` line
([Use brigade in a terminal](../install/terminal.md)). A `~/.local/bin/brigade` symlink from an earlier release
points at the old version; remove it. `brigade version` afterwards confirms the new pin.
