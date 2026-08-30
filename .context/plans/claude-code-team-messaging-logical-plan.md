# Claude Code Team Messaging: Adapter Architecture Research

Date: 2026-08-29  
Status: Research and conceptual design only; no implementation has begun.

## Executive conclusion

The project is ready to move from broad feasibility research into a short protocol-definition phase followed by a vertical proof of concept.

The recommended architecture is:

> A vendor-neutral CLI adapter is the portability boundary; Supabase is the default adapter, not part of the messaging protocol.

The Claude Code harness should not implement or standardize a particular identity system. It must, however, require every conforming adapter to enforce the security outcomes on which the product depends: commands are scoped to one configured team, sessions outside that team cannot be discovered or messaged, and the adapter supplies authoritative sender identity rather than trusting caller-provided sender fields.

Object storage may eventually implement the same adapter contract, but Supabase is the better initial backend because it combines durable storage, anonymous authentication, row-level authorization, and realtime delivery.

## Recommended system boundary

```text
Claude Code integration
  - stable tools and instructions presented to Claude
  - session lifecycle integration
  - inbound-message delivery
              |
              v
     Adapter CLI specification
  - sessions
  - durable messages
  - delivery semantics
  - required team isolation
              |
       +------+---------+
       |                |
       v                v
Supabase adapter    Future adapters
                    (S3, R2, other services)
```

The harness should not need to know:

- Whether the adapter uses OAuth, anonymous authentication, IAM, a shared credential, or another mechanism.
- How credentials are created, refreshed, or stored.
- How team membership is represented internally.
- Which database, queue, realtime service, or object store is used.
- Whether inbound delivery uses a subscription, long polling, or object-store polling.

The adapter contract should require:

- One configured adapter profile operates within exactly one team context.
- A session outside that team cannot list or message the team's sessions.
- The adapter authenticates or otherwise validates access to that team.
- The adapter determines and stamps the authoritative principal and sending session.
- A caller cannot impersonate an existing session merely by supplying its identifier.
- Credentials never appear in ordinary command output or command-line arguments.
- The adapter reports its protocol version, delivery behavior, and optional capabilities.

This means the core product stays out of implementing identity and authorization, but not out of specifying the security properties adapters must provide.

## Relevant Claude Code functionality

Claude Code now includes built-in cross-session messaging, separate from Channels and Agent Teams. It exposes `ListAgents` and `SendMessage`. Same-machine messages travel through a per-session Unix-domain socket or Windows named pipe; sessions on another machine can be reached through Anthropic while both sides are connected through Remote Control. This built-in feature is limited to the user's own sessions and therefore does not satisfy cross-account, team-scoped messaging.

It nevertheless validates the use case and supplies useful behavioral precedents:

- Discover recipients before sending.
- Address a session by a friendly name and use a short identifier when the name is ambiguous.
- Deliver an incoming message between tool calls rather than interrupting a running tool.
- Start a new turn when an idle session receives a message.
- Send text rather than conversation history or files.
- Identify the input as coming from another session, not from the human user.
- Never treat a peer message as permission or human consent.
- Never execute slash commands contained in message text.
- Keep the receiving session's normal permission boundary intact.
- Rate-limit loops, suppress immediate duplicates, and bound inbound queues.

Claude Code's built-in implementation currently uses plain-text messages for independent sessions. Structured plan-approval and shutdown messages remain internal to Agent Teams. Version one of this project should likewise support plain text while reserving a message `kind` field for future expansion.

Claude Code also exposes the current session's local inbox socket and a per-session token to hooks and Bash child processes through `CLAUDE_CODE_MESSAGING_SOCKET` and `CLAUDE_CODE_MESSAGING_TOKEN`. Its documentation explicitly discusses scripts and hooks posting into a session. This is a promising inbound delivery mechanism that could start a turn in an idle session without Claude Code Channels. Its exact supported payload schema and sender attribution should be validated in the first integration experiment before it becomes a hard dependency.

Relevant documentation:

- [Cross-session messaging](https://code.claude.com/docs/en/cross-session-messaging)
- [Claude Code tools reference](https://code.claude.com/docs/en/tools-reference)
- [Claude Code hooks](https://code.claude.com/docs/en/hooks)
- [Claude Code Agent Teams](https://code.claude.com/docs/en/agent-teams)

## Why the project remains distinct

| Capability | Claude Code built-in | Proposed system |
| --- | --- | --- |
| Same-account sessions | Yes | Yes |
| Cross-machine sessions | Through Anthropic and Remote Control | Through the configured adapter |
| Cross-user or cross-account | No | Yes |
| Explicit shared team | No | Yes |
| Pluggable backend | No | Yes |
| Harness-independent protocol | No | Intended eventually |

Claude Code's built-in `ListAgents` and `SendMessage` may coexist with the project. The plugin's tools should have distinct names, such as `TeamListSessions` and `TeamSendMessage`, and its instructions should clearly distinguish team messaging from Claude's same-account messaging.

## Identifier model

### Human or principal

An email address should not be called a verified human identifier unless an adapter actually verifies it. With a shared team credential, any holder can claim any email address.

Recommended fields:

```text
principal_ref   Adapter-issued opaque identifier
human_label     Optional, unverified display string
```

For the Supabase adapter, `principal_ref` may internally map to a Supabase Auth user ID. The harness need not understand its format or origin. An email address can be used as `human_label`, but the protocol and UI must not imply that it was verified.

### Session

Recommended fields:

```text
session_id           Opaque, immutable identifier
session_name         Mutable, human-friendly name
session_description  Optional description
```

`session_name + session_hash` should not be the canonical identity because names change and collide. The opaque session ID is the address; the name is presentation. When a friendly name matches more than one session, the harness should show a short disambiguating portion of the ID.

A Claude Code `SessionStart` hook receives the native `session_id` and, when available, `session_title`. The integration can use these inputs to establish a stable adapter-facing identity while avoiding unnecessary disclosure of the native identifier.

### Team

Recommended fields:

```text
team_ref       Opaque adapter-specific identifier
team_name      Display name
join_secret    Credential used during joining
```

The team name is not an identifier or a secret. Prefer a generated, high-entropy join secret over a human-selected password. The secret is a bearer capability: anyone possessing it can join. Calling it a join secret makes this trust model explicit.

The configured adapter profile should bind to one team. Normal runtime operations should not repeatedly accept a team name or join secret. This prevents accidental cross-team operations and keeps secrets out of process listings, shell history, logs, and agent context.

## Proposed CLI adapter surface

The three user-visible concepts remain correct:

- List sessions.
- Send a message to a session.
- Receive new messages for the current session.

The harness requires a somewhat larger lifecycle surface:

| Operation | Purpose |
| --- | --- |
| `describe` | Report protocol version and capabilities. |
| `session register` | Make the current session discoverable and establish ownership. |
| `session heartbeat` | Renew the session lease and update state or metadata. |
| `session list` | Discover recipients in the configured team. |
| `message send` | Durably accept a message for a recipient. |
| `message watch` or `message receive` | Deliver new messages, including while the session is idle. |
| `message ack` | Confirm successful injection into the receiving harness. |
| `session close` | Best-effort clean shutdown; lease expiry remains authoritative after crashes. |

Only list and send must necessarily be model-visible. Registration, heartbeat, watching, acknowledgment, and closure are harness plumbing.

`message watch` should produce newline-delimited JSON, one complete event per line. An implementation may internally use Supabase Realtime, WebSockets, long polling, or repeated object-store listing. The harness should not care.

The harness should invoke the adapter executable directly using an argument array, not through a shell command string. Message bodies and structured inputs should be provided through standard input. Diagnostics belong on standard error; machine-readable results belong on standard output.

## Required protocol semantics

Command compatibility alone is insufficient. The following semantics should be part of the first specification:

- A successful send means **durably accepted**, not read or processed by the recipient.
- Adapter-to-harness delivery is **at least once**.
- Every message has a globally unique `message_id`.
- A retry using the same idempotency key does not create another logical message.
- The receiving harness acknowledges only after successful local injection.
- Duplicate delivery is permitted and recipients deduplicate by `message_id`.
- Offline messages remain available for a documented retention period.
- Sessions use expiring leases; clean closure is an optimization rather than the sole offline mechanism.
- No global ordering is guaranteed.
- Version one carries size-limited UTF-8 plain text.
- All message text is untrusted input even when the sender belongs to the team.
- A remote message cannot grant permission, answer a permission dialog, change configuration, or act as human approval.
- Rate limits, duplicate suppression, and bounded queues prevent runaway agent loops.

Delivery states should be named precisely:

```text
accepted     Stored by the adapter
injected     Passed to the target harness/session
processed    Optional and generally unknowable without an explicit agent action
```

The product should never report `delivered` when it only knows `accepted`.

## Minimal message envelope

```json
{
  "protocol_version": "1",
  "kind": "text",
  "message_id": "opaque-id",
  "sender": {
    "principal_ref": "adapter-issued-principal",
    "human_label": "alice@example.com",
    "session_id": "opaque-session-id",
    "session_name": "payments-api"
  },
  "recipient_session_id": "opaque-recipient-id",
  "summary": "Migration completed",
  "body": "The tenant_id migration has landed.",
  "created_at": "server-assigned timestamp",
  "reply_to": null
}
```

The adapter must populate authoritative sender fields and server time. The caller supplies the recipient, body, optional summary, optional reply reference, and idempotency key.

Potential session-list fields include:

- `session_id`
- `session_name`
- `session_description`
- `principal_ref`
- `human_label`
- `state` such as active, idle, or offline
- `last_seen_at`
- `harness` and harness version
- Optional privacy-controlled workspace, repository, or branch label

## Claude Code harness recommendation

The adapter CLI is the backend service-provider interface, but Claude should preferably not compose raw shell commands for normal messaging.

A Claude Code plugin can include a small standard MCP shim exposing stable structured tools such as:

```text
TeamListSessions
TeamSendMessage
```

The shim invokes the configured CLI adapter without a shell, validates JSON results, normalizes errors, and prevents adapter-specific flags from leaking into the model instructions. This avoids shell quoting and injection problems, operating-system argument length limits, repeated permission friction, and accidental confusion with Claude's built-in messaging tools.

Candidate harness lifecycle:

1. A `SessionStart` hook registers or resumes the session.
2. The MCP shim provides list and send operations.
3. A background watcher receives messages from the adapter.
4. The watcher injects messages into the current Claude Code session.
5. The watcher renews the session lease and acknowledges injected messages.
6. A session-end hook attempts clean closure, while lease expiry handles crashes.

### Inbound delivery candidates

1. **Claude Code inbox socket**

   This is the preferred research candidate because it is intended for posting into the running session and may preserve native peer-message presentation, inbound controls, and idle-session wake behavior. Its precise message schema and remote-sender attribution require validation.

2. **Plugin Monitor**

   Claude Code plugin monitors run a persistent command for the life of an interactive session and pass every standard-output line to Claude as a notification. An adapter watcher can therefore emit one message event per line. Monitors are currently experimental and unavailable on Bedrock, Google Cloud's Agent Platform/Vertex, and Microsoft Foundry. They are also disabled by certain telemetry/nonessential-traffic settings.

3. **Polling on user turns**

   This is a compatibility fallback but cannot wake an idle session promptly. It can check the inbox from `SessionStart`, `UserPromptSubmit`, or another lifecycle event.

Claude Code Channels should not be the initial foundation. Channels are a research preview, require organization enablement in some plans, and custom channel plugins currently face allowlisting or development-flag requirements. The inbox socket or Monitor path is more compatible with the adapter architecture.

Relevant documentation:

- [Plugin creation](https://code.claude.com/docs/en/plugins)
- [Plugin monitors](https://code.claude.com/docs/en/plugins-reference#monitors)
- [Channels](https://code.claude.com/docs/en/channels)
- [Channels reference](https://code.claude.com/docs/en/channels-reference)

## Default Supabase adapter

A Supabase publishable key identifies a project but does not authenticate an individual. Without a user session, requests receive the `anon` database role. Secret and service-role keys bypass row-level security and must never be distributed in the CLI or plugin.

Recommended default-adapter flow:

1. The adapter anonymously signs into Supabase once for each local adapter profile.
2. Supabase gives that installation an authenticated user ID and renewable session.
3. The adapter stores the refresh credential in appropriate local secure storage.
4. The user supplies the team's high-entropy join secret.
5. A protected operation validates the secret and creates a membership row for the authenticated user ID.
6. Row-level security restricts sessions and messages to memberships associated with `auth.uid()`.
7. Session registration binds each session to the authenticated user ID.
8. Database operations stamp sender ownership rather than trusting sender fields supplied by the client.
9. Messages are persisted before realtime notification, so an offline session can catch up later.
10. The watcher combines realtime notifications with durable cursor-based catch-up and deduplication.

Supabase anonymous users receive real authenticated JWTs without requiring email, OAuth, or an account-registration UI. This allows the default adapter to enforce per-installation ownership and team membership while the core harness remains unaware of the authentication mechanism.

Accepted tradeoffs:

- `human_label` is not verified identity.
- Anyone with the join secret can become a team member and choose any label.
- Losing local anonymous-auth credentials creates a new principal that must rejoin.
- There is no natural person-level account recovery.
- Individual revocation is against an adapter principal, not a verified human.
- Rotating a join secret prevents new joins but does not automatically revoke existing memberships unless explicitly designed to do so.
- A low-entropy password would be vulnerable to online guessing; the default should generate a high-entropy join secret and apply rate limiting to join attempts.

Relevant documentation:

- [Supabase API keys](https://supabase.com/docs/guides/getting-started/api-keys)
- [Supabase anonymous sign-ins](https://supabase.com/docs/guides/auth/auth-anonymous)
- [Supabase Row Level Security](https://supabase.com/docs/guides/database/postgres/row-level-security)
- [Supabase Realtime authorization](https://supabase.com/docs/guides/realtime/authorization)

## Object storage as a future adapter

Object storage remains viable as a durable mailbox adapter if it supports a small contract:

- Immutable conditional object creation
- Object retrieval
- Strong prefix listing with pagination
- Outbound HTTPS
- Scoped credentials
- Optional deletion and lifecycle expiration

It is not a complete replacement for the default Supabase adapter's identity, membership, and realtime capabilities. Without a notification service, it requires polling. Its main value is as a portability target that validates the adapter boundary, not necessarily as the first production backend.

An object-store adapter should use one immutable object per message, local or server-side cursor state, idempotent IDs, explicit acknowledgments, and presence leases. Provider-specific IAM, event notifications, lifecycle configuration, and logging should remain outside the portable adapter protocol.

## Decisions to freeze before implementation

The first short protocol RFC should decide only:

1. The shared join-secret trust model.
2. Opaque identifiers versus display labels.
3. Adapter profile and team scoping.
4. At-least-once delivery and the acknowledgment point.
5. Session leases, active/idle/offline semantics, and retention.
6. Required JSON and NDJSON command behavior.
7. Standard exit codes and errors such as unauthenticated, unauthorized, unavailable, conflict, invalid input, and rate limited.
8. Protocol version and capability negotiation.
9. Inbound-message security and loop controls.
10. The Claude inbox-socket versus plugin-Monitor integration experiment.

Do not freeze these yet:

- Task assignment
- Broadcast messaging
- Attachments or file transfer
- Typing indicators
- Threads or rich conversations
- Total message ordering
- Remote permission approval
- Verified human identity
- Administrative roles

## Recommended first proof

The first vertical proof should establish exactly this outcome:

> Two Claude Code sessions owned by different Supabase anonymous principals join one team, discover each other, exchange durable text messages, wake when messages arrive, and cannot discover or message a session in another team.

Success criteria:

- The two principals are distinct and use no shared Supabase service credential.
- The team join secret is never passed in routine messaging commands.
- Session names may collide without misdelivery.
- Sending reports durable acceptance accurately.
- The recipient receives at least once and deduplicates by message ID.
- An offline recipient catches up after reconnecting.
- A session outside the team is invisible and unreachable.
- The incoming message is clearly attributed to another session and cannot act as user approval.
- A short message loop is throttled or bounded.
- The Claude Code integration does not depend on Channels.

If this proof succeeds, the central technical thesis is validated without coupling the protocol to Supabase.
