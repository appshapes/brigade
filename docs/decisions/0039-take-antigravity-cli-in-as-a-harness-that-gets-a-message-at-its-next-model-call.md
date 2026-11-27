# 0039. Take Antigravity CLI in as a harness that gets a message at its next model call

- **Status:** Accepted
- **Date:** 2026-11-27
- **Supersedes:** none

## Context

Frank asked on 2026-11-27 for Google's Antigravity CLI (`agy`) as a fifth harness (work items 531 and 532). The
live test of work item 531 ([the report](../experiments/antigravity-participation.md), agy 1.5.18) found these
facts:

- A hook gets one variable of agy's, `ANTIGRAVITY_CONVERSATION_ID`. Its parent process is the `agy` process. One
  `agy` process can hold one conversation after another (`/new`), and `agy --conversation <id>` takes one up again
  in a new process. There is no session-end hook.
- A hook reads a camelCase document and prints one JSON document. `SessionStart` and `PreInvocation` may add steps
  to the conversation (`injectSteps`), each a system message or a user message. `PreInvocation` runs before every
  model call. `Stop` may answer `"decision": "continue"`, and agy hands its reason to the model as a system message.
- Only a command that the agent starts gets what `agy agentapi send-message` needs to wake an idle session. A hook
  does not, so a watcher that a hook starts cannot wake one. A message file written into agy's own folder is not
  taken.
- A plugin's hooks run in the plugin's folder. `agy plugin install <GitHub URL>/tree/<tag>/<folder>` installs that
  folder at that tag, and a second install replaces the first.

ADRs [0019](0019-keep-what-differs-between-the-harnesses-in-one-host-table.md) and
[0020](0020-put-delivery-local-facts-and-the-hooks-dialect-behind-three-interfaces.md) put what differs between
harnesses in one host table, with one implementation for each kind of delivery and of hook dialect.

## Decision

1. **A host row of its own**, `antigravity`: the marker `ANTIGRAVITY_CONVERSATION_ID`, pointed, its own options
   file (`antigravity-options.json`), inherited Claude plugin options not taken as defaults, the plugin root from
   the hook's `--plugin-root`, a frame variant, and a Stop hook. The package's hooks name the host on their argv.
   Like Grok Build's, the row is exclusive: an agy started from another host's shell inherits that host's
   marker, so its hook refuses to take any session, says so at the start, and writes no map.
2. **A new hook dialect, `inject-steps`.** The hook runs the shared start and prompt logic and prints one JSON
   answer. Nothing it adds to the conversation is a system message: Brigade's own text (the context line, the line
   on how a message arrives) and, in a step of their own, the teammates' messages are user messages, the place
   both have on the other harnesses, where they ride on the user's turn. `PreInvocation` is the prompt hook at the
   first model call of a turn (`invocationNum` 0); at a later call it only hands over the messages that arrived.
   `Stop` answers `continue` with one fixed line, that a message waits, only after a turn that ended by itself
   (`NO_TOOL_CALL`, `model_stop`) with no error and not on a turn a Stop answer already continued; agy hands that
   reason to the model as a system message, so it carries no name and no text a teammate wrote, and the message
   follows at the next model call. The roster-line reminder and the end-of-turn ask are not said: both wait for a
   permission mode, which agy's documents do not carry. Every failure is the empty answer `{}`. A hook whose argv
   names another host is that host's, even with an inherited `ANTIGRAVITY_CONVERSATION_ID` (the owner's ruling of
   2026-11-27: a Codex CLI started from an agy agent's command joins as Codex).
3. **A new delivery kind, `next-call`.** The watcher writes each frame to the session's sink file, as it does for
   a followed sink. The hook takes what is new before the next model call, from a cursor beside the sinks that
   keeps one offset for each sink: every sink, oldest first, each from its own offset, under a lock on the
   cursor, and the cursor moves only after the hook's answer is written (`inbound.TakeNew`, `Batch.Commit`). A
   record that an old watcher writes after a newer sink exists, or that was half written at the last drain, is
   read in its turn, and a line that is no record is passed over. So two hooks at once never hand over one message twice,
   and an answer that was not written loses nothing. A hook that only reads a document of another conversation
   takes no message. The prompt hook's `poll_on_prompt` fallback does not run on this host: it would print a
   frame among Brigade's own lines. Nothing wakes an idle session in this decision: that is work item 533, and it
   needs a process the agent starts.
4. **The session is keyed by its conversation**, as a Codex thread is, in a range of keys of its own, and the `agy`
   process is kept for liveness. The watcher ends the session when that process is gone.
5. **A relative `--plugin-root` is taken from the working directory** for a host whose row says it runs a plugin's
   hook in the plugin's folder (`Host.PluginRootIsWorkingDir`, Antigravity CLI alone), because agy's `hooks.json`
   has no variable for it. For every other host a relative one names nothing, as before.
6. **No catalog and no pin.** A member installs one release by its tag
   (`agy plugin install https://github.com/appshapes/brigade/tree/v<version>/antigravity/brigade`), so a push to
   `master` reaches nobody. The release sequence bumps the package's manifest and copies the three pins into its
   `bin/`, and `make antigravity-plugin-check` holds them.
7. **The shared bootstrap knows the host.** A hook whose argv names `--host antigravity` gets JSON only from
   `plugin/bin/brigade`, whatever the environment says: on a cold cache, and on a failure before the binary runs,
   which it reports on stderr while it answers `{}` and exits 0. In an Antigravity session it ignores inherited
   `BRIGADE_*` and `XDG_*`, as for Grok Build.

## Alternatives considered

- **Reuse the followed-sink kind as it is.** The watcher's side is the same, but a session's start line would tell
  the model to arm a monitor that agy does not have. The new kind shares the watcher's implementation and says how
  a message arrives.
- **Hand a message over as a system message.** One step would carry all of Brigade's text, but untrusted text would
  stand where the model reads the system's own words. Lost.
- **Brigade's own lines as a system message, the messages as a user message.** Some of Brigade's lines carry a name a
  teammate chose, which would then stand where the model reads the system's own words. Lost (the owner's review of
  2026-11-27).
- **Hand a message over in the Stop answer.** A reply could come one model call sooner, but agy hands that answer's
  reason to the model as a system message. Lost.
- **Key the session by the `agy` process.** Two conversations of one process (`/new`) would then share one map, and
  a resumed conversation would be a new session. Lost.
- **One cursor position, a sink and an offset.** Simpler, but the cursor then passes an older sink when it moves
  to a newer one, and a record written there later is never read. Lost (reviews of 2026-11-27).
- **A marketplace catalog pinned to a release (ADR 0030).** agy installs a folder at a tag without one, so a pin
  would add a file and a release step for nothing. Lost.
- **Wake the idle session from the watcher.** The watcher gets neither the address nor the token `agentapi` needs.
  Not possible (work item 531); work item 533 takes the route that is.

## Evidence

- Work item 531's live test, 2026-11-27, agy 1.5.18 on macOS:
  [docs/experiments/antigravity-participation.md](../experiments/antigravity-participation.md).
- On 2026-11-27, `agy plugin install` of `https://github.com/appshapes/brigade/tree/v0.61.0/<folder>` found a folder
  only where the tag had it, and a second install answered "replaced existing installation".
- `agy plugin validate antigravity/brigade --strict` passed on 2026-11-27.
- The tests in `internal/harness/hook/antigravity_test.go` and `internal/harness/inbound/drain_test.go`.

## Consequences

- An Antigravity session joins the roster and takes a message at its next prompt. A message to an idle session
  waits, so a reply waits for the member, until work item 533.
- The host table, the frame and the generated skills have a fifth row. A sentence of a skill source that names
  each harness must name this one too, or the generator refuses.
- The section of `docs/security.md` for Antigravity CLI sessions is ruled by the owner (work item 535).
- The roster line is not asked for in an Antigravity session until agy gives its hooks a permission mode, or a
  later work item maps one.
- Revisit when agy gives a hook what wakes a session, or adds a session-end hook.

## References

- Work items 531 (the test), 532 (this package), 533 (the idle wake), 534 (usage) and 535 (the security text).
- [ADR 0019](0019-keep-what-differs-between-the-harnesses-in-one-host-table.md),
  [ADR 0020](0020-put-delivery-local-facts-and-the-hooks-dialect-behind-three-interfaces.md),
  [ADR 0030](0030-pin-the-codex-cli-and-grok-build-packages-to-the-last-release-in-their-marketplace-files.md).
- Antigravity's hooks and plugins pages: https://antigravity.google/docs/hooks, https://antigravity.google/docs/build-plugins.
