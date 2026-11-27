# Update in Antigravity CLI

The package has no marketplace entry, so nothing updates it by itself. To update, install the newer release
over the installed copy:

```sh
agy plugin install https://github.com/appshapes/brigade/tree/v<version>/antigravity/brigade
```

`<version>` is the newer release number, without the `v`, from the
[Releases page](https://github.com/appshapes/brigade/releases). The install replaces the copy in
`~/.gemini/config/plugins/brigade/`.

Then start a new `agy` session and type one prompt. A session that is open keeps the hooks that it loaded at its
start. The new session replaces the old watcher with the new version.

The path of the bootstrap does not change with an update. `brigade version` in the new session shows the new
version.
