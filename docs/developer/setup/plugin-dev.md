# Run the plugin from your checkout

The bootstrap normally downloads the pinned release. Developers point it at a local build with the dev-binary
pointer and drive the whole chain against the filesystem test adapter. Once, in your own terminal (`make
plugin-dev` runs outside a session):

```sh
make build
printf '{"fs": ["<this checkout>/bin/brigade-adapter-fs"]}\n' > ~/.config/brigade/adapters.json && chmod 600 ~/.config/brigade/adapters.json
cd <a project checkout>
bin/brigade team create --adapter fs --url http://127.0.0.1:1 --key placeholder --name ops --label dev --secret-file ~/brigade-ops.secret
```

Registering the fs adapter by name lets a project's `.brigade.json` name `fs`; `team create` in the checkout writes
the file and the pin, so from then on:

```sh
make plugin-dev                          # start Claude Code with the local plugin, already attached
make plugin-dev adapter=fs               # the adapter as a per-session override
make plugin-dev config_dir=<dir>         # a second persona with its own store on the same machine
make plugin-dev mode=bypassPermissions   # the permission mode
make plugin-dev-off                      # remove the pointer; the bootstrap uses the pinned release again
```

`make plugin-dev-pointer` writes the pointer without launching anything (honours `XDG_CONFIG_HOME`). The
`--settings` form for a second persona is
`--settings '{"pluginConfigs":{"brigade@inline":{"options":{"config_dir":"<dir>"}}}}'`; the options key is
`brigade@inline` for a `--plugin-dir` checkout.

- Symlinking your **own build** onto PATH (`ln -s <repo>/bin/brigade ~/.local/bin/brigade`) makes every session
  start with ``Brigade: another `brigade` at … shadows the plugin's``, because that path does not resolve to the
  plugin's `bin/brigade`. That is the check doing its job; the symlink to `${CLAUDE_PLUGIN_ROOT}/bin/brigade` is
  silent.
- `claude --plugin-dir ./plugin` loads the plugin from the checkout in place without the pointer, with the pinned
  release binary.
- `make harness-smoke` is the headless `claude -p` smoke test with the fs adapter (`mode=bypassPermissions` is
  card 50's arm); `internal/adapters/fs/README.md` has the second-persona join and the two-store variant;
  `docs/experiments/E3-wiring.md` is the measured record of both.
