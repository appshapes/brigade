# Uninstall from Codex CLI

1. [Leave the team](leave-the-team.md), steps 1 and 2, for each team on this machine, in a terminal.
2. `codex plugin remove brigade@brigade`; `codex plugin marketplace remove brigade` removes the marketplace too.
3. Remove a `~/.local/bin/brigade` symlink if you made one (earlier releases prescribed it).
4. [Leave the team](leave-the-team.md), steps 3 and 4: the state, the cached binaries, then the credential store.
   `~/.config/brigade/codex-options.json` goes with the credential store.

A Codex session that is still open keeps its watcher until the session ends; nothing arrives once the watcher has
closed the session.
