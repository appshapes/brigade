# Brigade for Grok Build

Team messaging for direct `grok --no-leader` TUIs through a persistent native monitor. Four native hooks
register/maintain the receiver, offer one Stop-assisted setup attempt and signal the receiver's watcher at end.
The seven skills use the same Brigade commands and gateway rules as the other packages.

[Install](../../docs/team-member/install/grok.md), [configure](../../docs/team-member/configure/grok.md),
[skills and monitor status](../../docs/agent/in-a-team/grok.md), and
[measured feasibility](../../docs/experiments/grok-participation.md).

The full native UUID, canonical cwd, TUI PID/start token and fresh incarnation bind the private local sink and
follower. Leader mode is refused. Normal monitor command permission applies. A denied/failed setup settles;
the user can request explicit retry. Startup notices are UI-only, never delivery.

ACK means successful local sink append/close, as on Muse, not model receipt. Pre-arm backlog remains in this
incarnation's sink files; helper restart can replay it. Fixed body-free hints coalesce at five seconds. `inbox read` drains retained rotations with validated cursors
and lossless pages capped at 1,800 serialized UTF-8 bytes — the proposed 2,048 corrupted escaped JSON at the
native shell's measured 2,000-column wrap. Long-idle, payload/burst and lifecycle native acceptance remains coordinator-owned.

`make grok-plugin-check` is strict: native manifest/catalog, four exact exec hook strings, seven reachable
skills, allowlist, no symlinks and the three bin assets equal to the canonical package. VERSION/checksums here
are identical copies of the existing release-produced pins, not new hashes or a version bump. The next normal
release rebuilds/pins all packages together; this source implementation is not in the already published binary.
