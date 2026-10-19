# Configure in Grok Build

Grok options come from one JSON file, independently of other harness packages:

```
~/.config/brigade/grok-options.json
```

Keys are the [common option names](options.md); values are strings, booleans or numbers:

```json
{ "team_inbound": "hold", "label": "none", "config_dir": "/absolute/brigade-persona" }
```

The Go resolvers and the shared bootstrap use HOME-only Brigade directories inside Grok. Inherited
`BRIGADE_*`, `XDG_*` and Claude plugin options cannot relocate Grok's store or supply its option defaults.
A Claude, Muse or Codex session started from Grok keeps those variables; Grok strips them only when it is
the host.
`config_dir` relocates credentials only after this options file is found. Missing files give defaults;
malformed/oversized files produce a fixed warning; unknown keys and nested values are ignored.

`team_inbound` controls local injection: accept appends to the private sink, hold retains server-side pending
messages until the human releases them, and refuse injects/ACKs nothing. `poll_on_prompt` does not deliver on
Grok: native UserPromptSubmit stdout is discarded. `claude_inbound_setting` has no meaning. No Claude settings,
account or arbitrary Grok history is read. Name falls back to `grok`; unknown model/context stay blank.

Run `brigade whoami` to see watcher connection and the last native monitor observation. States include
`unarmed`, `arming-attempted`, `unverified`, `paused` and `armed`. Armed is an exact native task/command
observation, not a message-processing receipt. Setup denial is not retried automatically; request the
team-messaging skill's monitor setup explicitly. A restarted helper can replay local messages by ID.
