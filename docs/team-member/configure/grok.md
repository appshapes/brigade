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
Grok: native UserPromptSubmit stdout is discarded. `claude_inbound_setting` has no meaning. No Claude settings and
no arbitrary Grok history is read. The Claude account's email address is read, for the default label, while `label`
is left at `account` ([Choose your label](../join/your-label.md)). The roster name is that session's `generated_title` from its own `summary.json`; with no title it falls back to `grok`. The model is `current_model_id` from that same file; with none it stays blank. Context stays blank.

## The strict checks: `grok_strict`

One option is Grok Build's own. `grok_strict` is `off` by default; set it to `on` to have Brigade refuse a
session wherever a fact is not the one it expects:

```json
{ "grok_strict": "on" }
```

With it `off`, Brigade registers the session, shows one line at its start that names what it found, writes the
same to its log, and goes on. What it goes on over, and refuses with the option `on`:

- the process that ran the hooks has a name Brigade does not know Grok Build by (a release that renames its
  binary; this stopped every session at Grok Build 1.0.67);
- the start names a source other than `new` or `load`, which is then treated as `new`;
- two spellings of one field of a hook's document disagree, or the document names another event than the hook
  it ran as;
- the session id is not a UUID (the session's roster name then stays `grok` and its model blank), or the
  document's is missing or differs from `GROK_SESSION_ID`, which is the one used;
- the workspace does not agree with the working directory or with `GROK_WORKSPACE_ROOT`.

None of these was ever measured to happen, which is why they no longer stop a session
([ADR 0021](../../decisions/0021-open-grok-builds-unmeasured-refusals-behind-one-option.md)). With the option
`on`, the package's skills also hold back as they did: a doing line only when Brigade asks for one, a message
to a gateway only when you authorized it, an update only when you request it. `brigade whoami` prints
`strict: on` for such a session. A value that is neither word is read as `on`, and the session's start says so.

Some things are refused whatever the option says, each for a reason, and the session's start shows the reason
in one line. Leader mode: the shared leader process outlives its terminal, so nothing there identifies a live
session (measured). Another harness's session marker in the environment, which is a Grok Build started from
that harness's shell: `brigade` there cannot tell the two sessions apart. A subagent's hook: it is not the
session's. A hook that ran under a shell, or whose parent is no live process of yours: there is no session
process to bind.

## Status

Run `brigade whoami` to see watcher connection and the last native monitor observation. States include
`unarmed`, `arming-attempted`, `unverified`, `paused` and `armed`. Armed is an exact native task/command
observation, not a message-processing receipt. Setup denial is not retried automatically; request the
team-messaging skill's monitor setup explicitly. A restarted helper can replay local messages by ID.
