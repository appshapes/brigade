# Update in Muse Code

Muse installs the package from a snapshot of this repository's marketplace, so an update is a refresh of the
snapshot and a reinstall:

```sh
muse plugins marketplace update brigade      # a fresh clone; the old snapshot stays if this fails
muse plugins remove brigade
muse plugins install brigade@brigade
muse plugins approve brigade                 # approvals do not survive a reinstall
```

then start a new session and type one prompt. A running session keeps the version it started with.

The reinstall is needed because `muse plugins update brigade` re-reads the source the package was installed from,
and for a git marketplace that is the snapshot of the first add, not the refreshed one (Muse 1.4.10, measured
2026-10-17: after the refresh, `update` kept the old digest; remove, install and approve moved to the new one).
The same four steps run in the plugin panel: `/plugins`, then the Marketplaces, Installed, Discover and Runtime
tabs in that order.

A package installed from a clone (`muse plugins inspect brigade` says provenance `native-local`) follows the clone
instead: `git pull` there, then `muse plugins update brigade`.

The bootstrap under Muse's plugin cache carries the package digest in its path, so the path your terminal runs changes
with every update: run `brigade whoami` in a new session and copy its `terminal:` line
([Use brigade in a terminal](../install/terminal.md)). A `~/.local/bin/brigade` symlink from an earlier release
points at the old version; remove it. `brigade version` afterwards confirms the new pin.
