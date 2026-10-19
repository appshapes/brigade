# Leave and uninstall from Grok Build

Use the [common leaving procedure](leave-the-team.md) before deleting credentials. A membership and the plugin
are separate; leaving closes that member's sessions, while uninstalling alone leaves membership intact.

```sh
grok plugin uninstall brigade
```

Follow its normal confirmation. Remove the marketplace with `grok plugin marketplace remove <source>` only
when no other plugin needs it. Do not delete shared Brigade state used by Claude, Muse or Codex siblings.

Normal SessionEnd signals only this receiver's watcher, which closes its backend session within a bounded
budget. If the TUI crashes or the hook is missed, the watcher independently observes the actual TUI's death or
PID reuse. The bound follower stops when registration/incarnation changes or the consumer closes. No shared
leader or TUI is killed by cleanup. Backend lease expiry is the fallback if network close fails.
