# Configure status for Grok Build

Grok Build participation is experimental. This source tree does not register Grok sessions or deliver team messages; native identity, session-specific liveness and admission remain gated.

The foundation selects `~/.config/brigade/grok-options.json`, independently of `muse-options.json` and
`codex-options.json`. Grok uses the HOME-only Brigade namespace; inherited `BRIGADE_*`, `XDG_*`, and Claude
plugin options cannot select its credential store or override file defaults. Hook/tool HOME agreement remains
a live acceptance gate. The current hook gate runs before reading this file.

When lifecycle registration is enabled, its parser accepts the [common option names](options.md):

```json
{ "team_inbound": "hold", "label": "none", "config_dir": "/absolute/brigade-persona" }
```

A missing file gives defaults; malformed or oversized input gives a fixed warning without echoing content.
Unknown keys and nested values are ignored. `config_dir` relocates credentials after the options file has
been found. `claude_inbound_setting` has no Grok meaning. No Claude account is read for a Grok session;
unknown name/model/context sources stay omitted until measured. A parser test is not a live-host promise.

The shared bootstrap source still resolves its binary cache and developer pointer through XDG. Its VERSION
and checksum files are absent, so this scaffold stops before those paths. Launcher directory agreement must
be resolved with the measured hook/tool environment before release assets make the package runnable; the
Go resolver tests alone do not establish that launcher contract.
