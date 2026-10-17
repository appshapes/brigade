# Uninstall from Muse Code

1. [Leave the team](leave-the-team.md), steps 1 and 2, for each team on this machine, in a terminal.
2. `muse plugins remove brigade`.
3. Remove the `~/.local/bin/brigade` symlink that pointed at the package's bootstrap.
4. [Leave the team](leave-the-team.md), steps 3 and 4: the state, the cached binaries, then the credential store.
   `~/.config/brigade/muse-options.json` goes with the credential store.

A Muse session that is still open keeps its monitor until it ends; nothing arrives through it once the watcher
has closed the session.
