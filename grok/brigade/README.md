# Brigade for Grok Build — experimental scaffolding

This distinct package contains a native manifest, three trusted lifecycle hook declarations, seven skills
and the shared POSIX bootstrap source. It does not yet register a Grok session or deliver team messages.
The runtime emits a diagnostic and stops before configuration reads, registration or watcher spawn.

[Install status](../../docs/team-member/install/grok.md), [agent skills](../../docs/agent/in-a-team/grok.md),
and [measured feasibility](../../docs/experiments/grok-participation.md).

`bin/VERSION` and `bin/checksums.txt` are deliberately absent. Only the normal release sequence may produce
them from reproducible builds; no placeholder checksums or existing release pins were changed. The source
check is `make grok-plugin-check`; the strict release-asset check is
`sh scripts/ci/grok-plugin-check.sh` and currently fails on those missing assets. An installation of this
source tree is a packaging experiment, not supported participation, and its bootstrap cannot download a
release until those assets exist.

The native layout and hook declarations follow the pinned public Grok source guide, revision
`2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8`. That source differs from installed Grok Build 1.0.62
(`1474f9cbdd91`); normal installed/trusted validation remains a coordinator-owned gate.

The Go configuration foundation uses HOME-only paths. The unchanged shared bootstrap uses XDG for its cache
and developer pointer; its missing release assets currently stop execution first. Launcher/environment
agreement is an additional prerequisite before producing a runnable package.
