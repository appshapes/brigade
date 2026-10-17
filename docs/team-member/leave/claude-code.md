# Uninstall from Claude Code

1. [Leave the team](leave-the-team.md), steps 1 and 2, for each team on this machine.
2. From `/plugin`, the plugin manager inside a session, uninstall `brigade`; optionally remove the `brigade`
   marketplace there too.
3. [Leave the team](leave-the-team.md), steps 3 and 4: the state, the cached binaries, then the credential store.
4. Remove the `~/.local/bin/brigade` symlink if you made one.

Uninstalling from the last scope that has it also deletes the plugin's own data directory, which Brigade does not
use: Brigade's state is in the XDG directories, which is why a `--resume` after a reinstall can still find your old
Brigade session. A marketplace kept after the plugin is gone leaves the plugin's cached copy under your
configuration directory with an `.orphaned_at` marker, harmless and not pruned.
