# Update in Grok Build

In your own terminal:

```sh
grok plugin marketplace update brigade
grok plugin update brigade
```

Reload through Grok's Plugins tab or start a new `grok --no-leader` TUI. Native new/load hooks create a new
receiver incarnation; do not keep an old incarnation's monitor command. A prompt can replace an outdated
watcher while retaining this incarnation's local backlog. The next completed turn checks native monitor
state; there is at most one automatic setup attempt per incarnation, with explicit user retry available.
A new/load incarnation does not adopt old sink files: locally ACKed unread records from the previous
incarnation stay on disk and are not automatically redelivered.

No update changes trust or permissions. The [package](../../../grok/brigade/README.md) lists the native hooks
and canonical assets; the current implementation still awaits coordinator native acceptance and release.
