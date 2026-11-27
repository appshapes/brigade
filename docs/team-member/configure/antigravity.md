# Configure in Antigravity CLI

An Antigravity CLI session reads its options from one file that Brigade owns:

```
~/.config/brigade/antigravity-options.json
```

The file is a JSON object. Its keys are the option names of [Know the options](options.md). Its values are
strings, booleans or numbers:

```json
{ "team_inbound": "hold", "label": "none", "sync": "off" }
```

- If there is no file, the defaults apply. If the file is not a JSON object, or is too large, the session
  start shows one line and the defaults apply. Brigade ignores unknown keys and nested values.
- `claude_inbound_setting` does nothing here. `team_inbound` is the one inbound option, and `accept` is its
  default.
- `poll_on_prompt` does nothing here. A message always comes from the watcher's local file, at the next model
  call.
- `share_doing` and `status` make Brigade ask the model for nothing here: an Antigravity session gets no
  reminder for its roster line and no ask at the end of a turn ([In trial](../install/antigravity.md#in-trial)).
  `share_doing: false` still blanks the line and makes `brigade doing` refuse.
- If you do not set `label`, it is `account`: where a Claude account is on this machine, its email address is
  your default label ([Choose your label](../join/your-label.md)). `none` sends no label. Any other text is your
  label.
- An Antigravity CLI session finds Brigade's directories from `HOME` alone. It ignores inherited `BRIGADE_*` and
  `XDG_*` variables, and Claude Code's plugin options. If you keep the credential store in another directory, put
  `config_dir` in this file.
- An option whose values are `on` and `off` takes those two words, in quotes. A JSON `true` or `false` is
  neither, so Brigade reads it as `off` and the session start says so
  ([A value Brigade cannot use](options.md#a-value-brigade-cannot-use)).
- A change has an effect at the next session start.
