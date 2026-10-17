# Use brigade in a terminal

Inside a session the harness finds `brigade`. Your own terminal does not, and three commands run only there:
`brigade team revoke-member`, `brigade team transfer` and `brigade inbox release`. Inside a session each refuses
with `usage` (exit 2).

## The symlink

`brigade whoami`, run in a session, prints the plugin binary's absolute path on its `terminal:` line:

```
session 09365acd… "payments-api" in team "ops" (adapter brigade-adapter-supabase 0.31.0); inbound: accept
terminal: /Users/you/.claude/plugins/cache/brigade/brigade/0.31.0/bin/brigade
frame: open
```

Symlink it onto your PATH; the bootstrap resolves its own symlinks and execs the binary `bin/VERSION` names:

```sh
ln -sf /Users/you/.claude/plugins/cache/brigade/brigade/0.31.0/bin/brigade ~/.local/bin/brigade
```

- **Re-point it after a plugin upgrade.** The path carries the version and each version is copied into its own
  directory. Run `brigade whoami` again, copy the new line, run the same `ln -sf`. A checkout loaded with
  `--plugin-dir` has no version in its path, so a symlink to it survives. In Muse Code and Codex CLI the bootstrap is
  under the harness's plugin cache; the session-start line names it.
- **A symlink, not a copy.** A copy goes stale at the next upgrade, and a `brigade` on your PATH that is not the
  plugin's own is what the session-start shadowing warning is about:

  ```
  Brigade: another `brigade` at <path> shadows the plugin's; remove it or the wrong version runs
  ```

  The plugin adds its `bin/` to the **end** of the Bash tool's PATH, so another `brigade` earlier on it would run
  instead. A symlink that resolves to the plugin's bootstrap is not reported.

## From source

```sh
go install github.com/appshapes/brigade/cmd/brigade@v0.31.0
```

builds the command-line tool from source: no plugin, no hooks, no watcher, so no session integration. It reports
its version with a leading `v` (`v0.31.0`) where the released binary reports `0.31.0`, and if it sits on your PATH
ahead of the plugin's own copy every session starts with the shadowing line above.
