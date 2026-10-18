# Switch file sync off

The folder list is the project's: whoever commits `.brigade.json` decides what every checkout syncs. The option is
yours: it can switch sync off, never add a folder.

Set the plugin option `sync` to `off`, from `/plugin` in a Claude Code session, in `muse-options.json` in Muse Code,
in `codex-options.json` in Codex CLI, or on the command line:

```sh
claude --settings '{"pluginConfigs":{"brigade@brigade":{"options":{"sync":"off"}}}}'
```

From the next session start that session syncs nothing and starts no Syncthing. `on` is the default. Any other
value is off, with one line:

```
Brigade: sync must be "on" or "off"; the value set is neither, so file sync is off for this session.
```

No permission rule sees file sync: the watcher drives the sync adapter itself, not through the shell tool, so the
folders a project lists sync whenever a session is active, with no prompt for any file. This option is the one
switch. What a teammate's machine can do in a listed folder on yours:
[docs/security.md, section 12](../../security.md#12-file-sync). How sync works and what you see: [Sync folders](../use/sync-folders.md).
