# 0013. Derive a send's key from the reply and the summary too

- **Status:** Accepted
- **Date:** 2026-11-22
- **Supersedes:** none

## Context

Every message a session sends carries an idempotency key that the harness derives, so that a send the model or
the harness makes again is one message: a backend answers a key it has seen with the first message, and refuses
the key with another payload as `conflict` (`docs/protocol-v1.md`, 4.5.4). The derivation is decision D11 of
2026-08-30, made before ADR 0001 and written in `.context/plans/implementation/02-decisions.md`: the hash of the
sender, the recipient, the body and the minute.

A message is more than those. `brigade send --reply-to <message>` says which message it answers and `--summary`
gives it a line of its own, and the key covered neither. So the same short body to the same session inside one
minute had one key whatever it answered. The repository review of 2026-11-21 found what follows (its section 3,
work item 392): a session answers two messages of one sender, or two Slack threads through the gateway, with
"Done." each, inside a minute, and the second answer is never stored.

What the sender sees depends on what the backend compares under a key, which 4.5.4 leaves to it beyond the
recipient and the body. A Supabase project compares those two, takes the second answer for a repeat and prints
`accepted … duplicate of an earlier send`: the answer is lost with no sign of it. A Brigade server, the stand-in
and the fs adapter also compare the summary and the reply, and refuse the second answer as `conflict`,
`idempotency_key_reused`: the send fails, for a message that was a new one.

## Decision

The key of a send is derived from everything that makes the message the one it is: the base64url of the SHA-256
of the sender, the recipient, the message it answers, its summary and its body, each followed by a NUL, and then
the minute. The body stays the last part before the minute, because it is the one part that can carry a NUL.

One function derives it from the request that is sent, `digest.IdempotencyKey`, and every send of the harness
takes its key from it: `brigade send`, the ask of `brigade decision`, the digest, and an ask the watcher held.
The last keeps the hold's own time in place of the minute, as before, so every attempt at one ask is one message.

The same body in reply to the same message under the same summary inside a minute is still one message.

## Alternatives considered

- **A random key for each call.** Lost for D11's own reason: a retry would be a new message on every backend,
  and the model does send a message again when it is unsure the first went.
- **Add the reply only, as the work item's title has it for `send`.** Lost: the summary is part of the message a
  backend stores and a recipient reads, three of the four backends already refuse the key under another summary,
  and a rule that names two of the parts invites the next omission. The three other senders set no reply and
  derive their summary from the facts their body is made of, so for them the whole request and the body alone
  tell the same messages apart.
- **Let each sender hash what it chooses.** Lost: four call sites passing strings in an order is how the reply
  was left out of the first.
- **Change what a backend compares**, so that a key with another reply is a second message. Lost: 4.5.4 is the
  frozen protocol's, a key that names two messages is no key, and three backends would change for what the
  client can say itself.
- **A shorter window than a minute.** Lost: it narrows the loss and keeps it, and it narrows the retry it is for.

## Evidence

- Measured on 2026-11-22 with the key as it was, through the built binary: on the fs adapter, on the REST
  stand-in and on the built server 0.9.0 over TLS, the second of two sends of one body in reply to two messages
  exits 7 with `conflict`, `idempotency_key_reused`. With this decision both are stored on all three, and the
  third send, the first one again, is answered `duplicate` with the first message's id
  (`TestTwoIdenticalRepliesInAMinuteAreBothStored`, and the same check in
  `TestATeamOnAServerThroughTheRealBinary`, which `make test` runs against the stand-in and CI's `server` job
  against the built server).
- Read, not run: `brigade.send` of the Supabase schema compares `body_hash` and the recipient
  (`supabase/migrations/20260830120000_brigade_schema.sql`), which gives the silent `duplicate` the review
  reported; the server compares the recipient, the body, the summary and the reply
  (`server/crates/brigade-core/src/execute.rs`), as the fs adapter does.
- None measured against a hosted Supabase project or a team's running server: the key is the client's own, and
  no backend reads what it is made of.

## Consequences

- Easier: a short answer can be given twice in a minute, to two messages, on every backend. A sender added later
  gets the whole derivation by passing its request.
- Harder: nothing for a caller. A send made again with another summary is now a second message, where a Supabase
  project took it for the first; that is what the summary says it is.
- No wire shape changes, and no backend: a key is opaque to it. A session on an older plugin derives the old key
  and keeps the old behaviour until it updates. A key is compared within one sender's session, so the two
  derivations meet only where one session sends a message again across its own update: an ask the watcher held,
  sent by the old watcher with the answer lost and tried again by the new one, or a `brigade send` repeated
  inside the minute of the update. That one message is then stored twice, once. It is accepted: keeping the old
  derivation for the retry would keep the fault for as long.
- To maintain: the list of parts in `digest.IdempotencyKey`. A member added to `SendRequest` that makes a message
  another message joins it.
- Revisit if a retry is ever made by a caller that cannot repeat its request exactly.

## References

- Work item 392 on the Brigade Board, from the repository review of 2026-11-21.
- Decision D11, `.context/plans/implementation/02-decisions.md`; `docs/protocol-v1.md`, 4.5.4.
- `internal/harness/digest/digest.go` (`IdempotencyKey`), and its four callers in `internal/harness/commands`,
  `internal/harness/digest` and `internal/harness/watch`.
