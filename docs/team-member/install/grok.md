# Install status for Grok Build

Grok Build participation is experimental. This source tree does not register Grok sessions or deliver team messages; native identity, session-specific liveness and admission remain gated.

The native package is `grok/brigade/`; `.grok-plugin/marketplace.json` points to it. Release assets are absent,
so the bootstrap cannot download a release. There is no minimum supported Grok version yet.

For a coordinator-owned packaging experiment after release assets have been generated, the source guide's
native flow is `grok plugin marketplace add <owned-checkout>`, then `grok plugin install brigade --trust`.
Trust is a host decision made through the normal install flow. Do not change trust files, disable trust or
borrow a Claude package install. Confirm the native manifest, all seven skills, and three hooks in that owned
installation. No such live install validation has passed for this package.

`GROK_SESSION_ID` is recognized as an isolated host marker; an environment containing another host's marker
is refused before pointer resolution. The planned shell command is the installed package's `bin/brigade`,
with the exact path learned from the host, rather than an assumed cache path. Hook stdout is not delivery.

See [feasibility evidence](../../experiments/grok-participation.md),
[configuration](../configure/grok.md), [security limits](../../security.md#17-experimental-grok-build-foundation) and [package status](../../../grok/brigade/README.md).
