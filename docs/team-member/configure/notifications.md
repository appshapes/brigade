# Be told when a message arrives

Ask your session, in Claude Code: `Turn on message_sound and message_notification.` Start a new session for the
change to take effect. The options combine freely.

- `message_sound: on` plays one quiet system sound when a teammate's message reaches this session: delivered into
  it, or held for your release under `team_inbound: hold`.
- `message_notification: on` shows a desktop notification: the title `Brigade` and `A message arrived for <this
  session's name>.`, or `3 messages arrived for …` when several came inside one interval.
- `message_interval` is the least seconds between two sounds, or two notifications: 30 by default, 5 to 3600, so a
  burst of messages is one sound and one banner.

## What makes no sound

A refused message, a duplicate the backend redelivers, a message you release yourself, and a notice from Brigade.
A message whose delivery failed and is delivered again counts as arriving again. A replaced watcher starts its
interval afresh. The harness itself does not notify on an arriving message.

## What runs

The sound is `afplay` with macOS's Glass sound at a quarter of its volume, or `paplay` with the desktop's
new-message sound on Linux. The notification is `terminal-notifier` when installed (one banner per session that a
burst replaces), else `osascript` (macOS attributes the banner to Script Editor and asks once to allow it), or
`notify-send` on Linux, which needs the user bus at `$XDG_RUNTIME_DIR/bus`. The watcher starts each program with
nothing from the message: the sound's argument list is fixed; the notification gets the title, the text and the
count as arguments, never spliced into a script, so a session name is data whatever it holds.

On Windows nothing is started (0.42.0): the watcher plays your sound scheme's notification event through
Windows' own sound library at about a quarter volume, and shows the notification as a notification-area balloon,
which Windows presents as a toast headed "Brigade" for about six seconds and does not keep in the notification
centre; an icon appears in the notification area for the showing and goes again two seconds later. Focus assist
and do not disturb hold the toast back as they hold any other.

If an option is on and the machine cannot play or notify, session start prints `Brigade: message sound off (<why>).`
or `Brigade: message notification off (<why>).` once; a session with no watcher prints the same.
