# Update in Codex CLI

Codex installs the package from the `brigade` marketplace, so an update is a refresh of the marketplace's snapshot
and an install again:

```sh
codex plugin marketplace upgrade brigade
codex plugin add brigade@brigade
```

then start a new session and type one prompt. A running session keeps the version it started with, and nothing a
session's model can run replaces it.

The symlink from `~/.local/bin/brigade` points at the bootstrap under Codex's plugin cache, whose path carries the
version: run `brigade whoami` in a new session, copy its `terminal:` line, and run the same `ln -sf` again
([Use brigade in a terminal](../install/terminal.md)). `brigade version` afterwards confirms the new pin.
