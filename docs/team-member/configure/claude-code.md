# Configure in Claude Code

## Three ways to set an option

- **From `/plugin`** in a session: open Brigade's options and set the value.
- **Ask your session**: `Set message_sound to on.` It edits `pluginConfigs` in your user settings file,
  `settings.json` in your Claude Code configuration directory (`CLAUDE_CONFIG_DIR`, or `~/.claude`). It does this
  only when you ask; a teammate's message cannot change your settings, and every such request in the measured set
  was refused.
- **On the command line**, for one session:

  ```sh
  claude --settings '{"pluginConfigs":{"brigade@brigade":{"options":{"team_inbound":"refuse"}}}}'
  ```

  The key is `brigade@brigade` for a marketplace install and `brigade@inline` for a `--plugin-dir` checkout.

Option values are read from user settings, `--settings` and managed settings only, never from a project. Start a
new session for a change to take effect.

## Claude Code's own inbound setting

Claude Code has a setting of its own for messages from another session, `crossSessionInbound`. It sits between
Brigade and your session, and Brigade cannot release from it.

- **If you start Claude Code with `--dangerously-skip-permissions`**, Brigade adds one line to your user settings
  file the first time such a session starts without it, and prints a line saying so:

  ```json
  "crossSessionInbound": "accept"
  ```

  Without it Claude Code holds every team message for your approval in a dialog that expires after five minutes.
  Nothing else in the file is touched; a `hold` or `refuse` you set yourself is left alone. To keep Brigade out of
  the file, set `claude_inbound_setting` to `false` and add the line yourself; until it is there Brigade refuses
  team messages in such a session and warns, naming the file, and the prompt after you add it switches Brigade back
  to `accept`. The line in a repository's `.claude/settings.json` does nothing: Claude Code lets a repository only
  tighten this setting.
- **If a settings file sets `crossSessionInbound` to `hold` or `refuse`**, Brigade sets its own policy to `refuse`
  and says so: releasing into a session Claude Code will not deliver to would throw the message away and tell its
  sender it arrived. Remove the setting, or set it to `accept`, then use `team_inbound: hold` for the review step.
- **One blind spot.** A `crossSessionInbound` passed with `--settings` on the command line is invisible to
  Brigade's scan: a session started that way can hold or drop Brigade frames without Brigade knowing, and an
  `accept` passed that way is not seen either; add it to the user file as well.

What that costs a sender, and what `injected` does and does not mean:
[docs/security.md, section 6](../../security.md#6-claude-codes-own-inbound-setting-is-a-second-layer).
