# Update in Grok Build

In your own terminal:

```sh
grok plugin marketplace update brigade
grok plugin update brigade
```

Reload through Grok's Plugins tab or start a new `grok --no-leader` TUI. A new native session or load creates
a new receiver incarnation; do not keep an old incarnation's monitor command. Repeated new hooks for the same
live UUID and TUI keep its existing receiver and monitor state. A prompt can replace an outdated
watcher while retaining this incarnation's local backlog. The next completed turn checks native monitor
state; there is at most one automatic setup attempt per incarnation, with explicit user retry available.
A new/load incarnation does not adopt old sink files: locally ACKed unread records from the previous
incarnation stay on disk and are not automatically redelivered.

`grok plugin update brigade` prints the version it left and the one it reached. The marketplace names the
package of the last published release ([What you get, and when](../install/grok.md#what-you-get-and-when)), so
between two releases the two commands leave the installed package as it is, whatever was pushed to this
repository meanwhile, and after a release they bring that release's package.

## An install from before 2026-11-23

Until work item 424 the marketplace named the package's directory on `master`, and Grok Build recorded such an
install as a directory of its marketplace snapshot. It cannot follow the pin: `grok plugin update brigade`
fails with `plugin 'grok/brigade' not found in install registry`, and the installed package stays as it was.
It keeps working, and it never updates. Once, in your own terminal:

```sh
grok plugin marketplace update brigade
grok plugin uninstall brigade
grok plugin install brigade --trust
```

Uninstall first: an install over the old one leaves two installs listed (Grok Build 1.0.92, measured
2026-11-23). `grok plugin list` then shows one `brigade`, from `git`, and updates work from there on.

No update changes trust or permissions. The [package](../../../grok/brigade/README.md) lists the native hooks
and canonical assets.
