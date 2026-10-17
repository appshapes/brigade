# Update in Muse Code

Muse installs the package from a local bundle, so an update is a pull and one command:

```sh
git pull                        # in the clone of this repository you installed from
muse plugins update brigade
```

then start a new session. A running session keeps the version it started with.

The symlink from `~/.local/bin/brigade` points at the bootstrap under Muse's plugin cache, which resolves the
pinned binary by `bin/VERSION`; `brigade version` after the update confirms the new pin.
