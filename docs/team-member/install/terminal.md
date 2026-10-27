# Use brigade in a terminal

Inside a session the harness finds `brigade`. Your own terminal does not, and three commands run only there:
`brigade team revoke-member`, `brigade team transfer` and `brigade inbox release`. Inside a session each refuses
with `usage` (exit 2).

## The path

`brigade whoami`, run in a session, prints the plugin binary's absolute path on its `terminal:` line:

```
session 09365acd… "payments-api" in team "ops" (adapter brigade-adapter-supabase 0.36.1); inbound: accept
terminal: /Users/you/.claude/plugins/cache/brigade/brigade/0.36.1/bin/brigade
frame: open
```

Run that path in your terminal, with the command after it:

```sh
/Users/you/.claude/plugins/cache/brigade/brigade/0.36.1/bin/brigade inbox release --all
```

It is the bootstrap the session runs, and it runs the version it pins, so what your terminal runs is what the
session runs. The path carries the version: after a plugin upgrade, `brigade whoami` prints the new one. On
Windows the line names `…\bin\brigade.exe`, the launcher, which PowerShell and Git Bash both run
([Install on Windows](windows.md)).

- **Not a symlink, not a copy.** A `~/.local/bin/brigade` link or a copy goes stale at the next upgrade and keeps
  running the old version, silently inside a session that was already running when the plugin moved on. Where
  several Claude Code configuration directories each hold a plugin install, one link can agree with at most one of
  them. Brigade prescribed such a link until 0.36.1 (card 121): if you made one, remove it.
- **The shadowing line.** A `brigade` on your PATH that is not the plugin's own is what the session-start warning
  is about:

  ```
  Brigade: another `brigade` at <path> shadows the plugin's; remove it or the wrong version runs
  ```

  The plugin adds its `bin/` to the **end** of the Bash tool's PATH, so another `brigade` earlier on it runs
  instead. A link that resolves to the plugin's own bootstrap is not reported; a stale one is reported at the next
  session start, because it resolves to another version's file.
- **Muse Code and Codex CLI** put nothing on PATH either: the session-start line names the bootstrap under the
  harness's plugin cache, and that path is the command, in the session and in your terminal.

## From source

```sh
go install github.com/appshapes/brigade/cmd/brigade@v0.36.1
```

builds the command-line tool from source: no plugin, no hooks, no watcher, so no session integration. It reports
its version with a leading `v` (`v0.36.1`) where the released binary reports `0.36.1`, and if it sits on your PATH
ahead of the plugin's own copy every session starts with the shadowing line above.
