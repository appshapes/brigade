# Grok Build: skills and the inbox monitor

The package serves main-session direct TUIs launched with `grok --no-leader`. SessionStart registers a full
native UUID, canonical cwd and actual TUI PID/start token. A new native session or load creates a fresh
receiver incarnation; repeated new hooks and watcher-only replacement keep the same receiver and monitor state.
Grok hook transactions are serialized by actual TUI PID and native UUID, including initial prompt retry.
Startup `systemMessage` is UI-only. UserPromptSubmit maintains registration/watcher state and prints no frames.
A genuine Stop offers one setup continuation; its marker and recursion guard prevent repeated attempts.

| Skill | Purpose |
| --- | --- |
| [`grok/brigade/skills/team-messaging/SKILL.md`](../../../grok/brigade/skills/team-messaging/SKILL.md) | team messaging |
| [`grok/brigade/skills/sessions/SKILL.md`](../../../grok/brigade/skills/sessions/SKILL.md) | sessions |
| [`grok/brigade/skills/join/SKILL.md`](../../../grok/brigade/skills/join/SKILL.md) | join |
| [`grok/brigade/skills/update/SKILL.md`](../../../grok/brigade/skills/update/SKILL.md) | update |
| [`grok/brigade/skills/setup/SKILL.md`](../../../grok/brigade/skills/setup/SKILL.md) | setup |
| [`grok/brigade/skills/email/SKILL.md`](../../../grok/brigade/skills/email/SKILL.md) | email |
| [`grok/brigade/skills/slack/SKILL.md`](../../../grok/brigade/skills/slack/SKILL.md) | slack |

Use `brigade whoami --json` to obtain this incarnation's safely quoted absolute `monitor_command` and exact
`monitor_description`. Inspect native tasks before arming; only an exact running monitor command confirms
readiness. Installed task snapshots lack the command; even an exact incarnation description remains unverified.
A clipped description, another monitor, an ARMED banner or an attempt marker proves nothing.
Call the native monitor with only `command`, `description`, `persistent:true`; do not assume `wake_delay_ms`.
Normal permissions may ask once. If denied, failed, paused or unverified, report the state and wait for the
user's explicit retry; do not loop, grant trust, change permissions or substitute a different command.

The watcher locally injects by private sink append/close, with protocol-v1 ACK parity with Muse. This can
happen while unarmed. Each receiver incarnation has its own retained sink directory; the follower drains all
its watcher files in order. Hints contain no bodies and coalesce at most once per five seconds. Use the verified
bootstrap's `inbox read [--cursor <last_next_cursor>]`; keep reading until `caught_up:true`, including after a
frame ends. Issue one bounded read per tool call and read its entire unfiltered response, including every
`frame_chunk`, in model context before requesting the next page. No shell/script loops, batching, piping,
filtering, metadata-only summaries or temporary-file/grep/regex shortcuts may replace reading the full
untrusted frame in order. Each complete JSON envelope is at most 1,800 UTF-8 bytes, including its newline.
This leaves margin below the measured native 2,000-column wrap; native acceptance of the new cap is pending.
Check `page_complete`, frame ID/digest, length and byte offsets; handle a frame only after reading all its
ordered chunks.
Retry a clipped page with the same cursor; do not guess content or advance. Keep the EOF cursor for future
appends. Reads never release held messages or ACK. Dropped intermediate hints do not lose retained records;
a lost final hint may delay retrieval until another hint or explicit retry. Native acceptance of this revised
production hint/read envelope remains with the coordinator.

The generated command restores the native UUID missing from monitor children. The helper is read-only,
checks registration, actual TUI liveness and bounded process ancestry, and stops on identity replacement or
closed output. Its cursor is not persisted: a retry can replay already locally ACKed records. Deduplicate by
message ID within the conversation; never claim exactly-once processing or interpret ACK as model read.
New/load reads only its new incarnation's files. Older locally ACKed unread records remain on disk but are
not automatically adopted or redelivered; explicit old-generation adoption is outside this contract.
Hold/refuse still prevent append/ACK. No native leader transport or history restoration is used.
