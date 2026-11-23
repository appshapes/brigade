# Update in Codex CLI

Codex installs the package from the `brigade` marketplace, so an update is a refresh of the marketplace's snapshot
and an install again:

```sh
codex plugin marketplace upgrade brigade
codex plugin add brigade@brigade
```

then start a new session and type one prompt. A running session keeps the version it started with, and nothing a
session's model can run replaces it.

Codex asks you to review Brigade's hooks again once the package has changed: choose **Trust all and continue**
([Trust the hooks](../install/codex.md#trust-the-hooks)). Until you do, nothing of Brigade runs. A start that
fetched the changed package itself may not ask and show `Hook failed: plugin contents changed; hook approval
required` at your prompt instead (measured 2026-11-23): quit and start Codex again, and it asks.

The bootstrap under Codex's plugin cache carries the version in its path, so the path your terminal runs changes
with every update: run `brigade whoami` in a new session and copy its `terminal:` line
([Use brigade in a terminal](../install/terminal.md)). A `~/.local/bin/brigade` symlink from an earlier release
points at the old version; remove it. `brigade version` afterwards confirms the new pin.
