# Configure in Muse Code

Muse Code runs no plugin `userConfig`, so a Muse session reads its options from one file Brigade owns:

```
~/.config/brigade/muse-options.json
```

A JSON object whose keys are the option names of [Know the options](options.md) and whose values are strings,
booleans or numbers:

```json
{ "team_inbound": "hold", "label": "none", "message_sound": true, "message_interval": 60 }
```

- A missing file is the defaults. A file that is not a JSON object, or is too large, earns one line at session
  start and the defaults. Unknown keys and nested values are ignored.
- `claude_inbound_setting` has no meaning here: the hooks see no Claude settings, so `team_inbound` is the one
  inbound knob, `accept` by default.
- A Muse session resolves Brigade's directories from `HOME` alone. A member who sets `XDG_CONFIG_HOME` and keeps
  the credential store there puts `config_dir` in this file.
- A change takes effect at the next session start.

What Muse's own layers add: [docs/security.md, section 15](../../security.md#15-muse-code-sessions).
