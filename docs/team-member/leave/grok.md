# Leave and uninstall from Grok Build

Use the [common leaving procedure](leave-the-team.md) before deleting credentials. A membership and the plugin
are separate; leaving closes that member's sessions, while uninstalling alone leaves membership intact.

```sh
grok plugin uninstall brigade
```

Follow its normal confirmation. `grok plugin marketplace remove brigade` removes the marketplace and uninstalls
its plugins in one command. Run it only where you added the marketplace to Grok Build yourself and no other
plugin needs it. Where Claude Code has the `brigade` marketplace on the machine, Grok Build lists that one, and
in Grok Build 1.0.92 the command takes it out of Claude Code's own list as well
(`plugins/known_marketplaces.json` in Claude Code's configuration directory): Claude Code then stops updating
Brigade until `/plugin marketplace add appshapes/brigade` is run there again.

Grok Build leaves the plugin's name in its configuration after either command: the `[plugins]` table of
`~/.grok/config.toml` (or of `config.toml` under `GROK_HOME`) still reads `enabled = ["brigade"]`, or
`disabled = ["brigade"]` where the plugin was disabled first, and `grok plugin disable brigade` no longer
knows the name (measured in Grok Build 1.0.92). Take the name out of that list in an editor.

Then [Leave the team](leave-the-team.md), steps 3 and 4: the state, the cached binaries, then the credential
store. `~/.config/brigade/grok-options.json` goes with the credential store. Leave both steps out while a
Claude Code, Muse Code or Codex CLI package on this machine still uses Brigade: the state and the credential
store are shared by the four.

Normal SessionEnd signals only this receiver's watcher, which closes its backend session within a bounded
budget. If the TUI crashes or the hook is missed, the watcher independently observes the actual TUI's death or
PID reuse. The bound follower stops when registration/incarnation changes or the consumer closes. No shared
leader or TUI is killed by cleanup. Backend lease expiry is the fallback if network close fails.
