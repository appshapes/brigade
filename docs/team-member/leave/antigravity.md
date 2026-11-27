# Uninstall from Antigravity CLI

1. [Leave the team](leave-the-team.md), steps 1 and 2, for each team on this machine, in a terminal.
2. Run `agy plugin uninstall brigade`. `agy plugin list` then does not show `brigade`.
3. [Leave the team](leave-the-team.md), steps 3 and 4: the state, the cached binaries, then the credential store.
   `~/.config/brigade/antigravity-options.json` goes with the credential store. Do not do these two steps while
   another harness on this machine uses Brigade: all the harnesses share the state and the credential store.

An `agy` session that is open keeps its watcher until the `agy` process stops.
