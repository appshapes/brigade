# Brigade for Grok Build

Status: Supported, with known gaps. It works, and [its gaps are named](../../docs/team-member/install/grok.md#known-gaps).

Team messaging for direct `grok --no-leader` TUIs through a persistent native monitor, shipped since Brigade
0.35.0. Four native hooks register/maintain the receiver, offer one Stop-assisted setup attempt and signal the
receiver's watcher at end. The fifteen skills use the same Brigade commands and gateway rules as the other
packages; `team-messaging`, `setup` and `update` carry the other packages' text with this package's own monitor
sections.

[Install](../../docs/team-member/install/grok.md), [configure](../../docs/team-member/configure/grok.md),
[skills and monitor status](../../docs/agent/in-a-team/grok.md), and
[measured feasibility](../../docs/experiments/grok-participation.md).

The full native UUID, canonical cwd, TUI PID/start token and fresh incarnation bind the private local sink and
follower. Leader mode is refused. Normal monitor command permission applies. A denied/failed setup settles;
the user can request explicit retry. Startup notices are UI-only, never delivery.

ACK means successful local sink append/close, as on Muse, not model receipt. Pre-arm backlog remains in this
incarnation's sink files; helper restart can replay it. Fixed body-free hints coalesce at five seconds. `inbox read` drains retained rotations with validated cursors
and lossless pages capped at 1,800 serialized UTF-8 bytes — the proposed 2,048 corrupted escaped JSON at the
native shell's measured 2,000-column wrap. The installed package was accepted natively on macOS arm64 with
maximum-size bodies, a short burst, resume and cleanup; Linux, a 35-minute idle hold of the whole package and
broader burst or hint-loss envelopes are not measured.

`make grok-plugin-check` is strict: native manifest/catalog, four exact exec hook strings, fifteen reachable
skills, allowlist, no symlinks and the three bin assets equal to the canonical package. VERSION/checksums here
are identical copies of the release-produced pins of `plugin/bin`: every release rebuilds and pins the four
packages together. Grok Build installs the package as the repository's default branch has it, so a change to a
skill here reaches a member at their next `grok plugin update brigade`, before the release that names it.
