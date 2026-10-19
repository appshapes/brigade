# Leave an experimental Grok installation

Grok Build participation is experimental. This source tree does not register Grok sessions or deliver team messages; native identity, session-specific liveness and admission remain gated.

If a terminal membership was created, use the [common leaving procedure](leave-the-team.md) before deleting
credentials. Membership removal and plugin removal are separate actions. Do not delete shared Brigade state
while sibling Claude, Muse or Codex sessions use it.

For an owned experimental installation, use the native command `grok plugin uninstall brigade`; follow its
normal confirmation. Remove its marketplace with `grok plugin marketplace remove <source>` only when no
other plugin needs it. Never edit Grok trust or configuration files directly. This foundation starts no Grok
watcher and must not kill a shared leader during cleanup.
