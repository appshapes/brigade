# 0018. Say in the webhook gateway's 202 why an accepted message waits

- **Status:** Accepted
- **Date:** 2026-11-22
- **Supersedes:** none

## Context

A service behind the webhook gateway sends a message with a signed `POST /v1/messages`, and the gateway answers
`202` with the message's id and its recipient's session id (`docs/webhook-gateway.md`). The backend accepts a
message for any session of the team, whether that session will read it or not: one that is offline, and one whose
inbound policy holds or refuses team messages, get the message stored and waiting. So the gateway's `202` was the
same for a recipient that reads the message in a second and for one that never will.

`brigade send` had the same blind spot and tells its caller since 0.15.0 (card 34): it prints a `waiting:` line
for each of the three cases, from the roster it read before the send.

Work item 389 met the gateway's case twice on 2026-11-22. A member's one session ended, a request for that member
followed, and the answer was a plain `202` for the session that was gone. And after a team's move every session
that followed is offline on the old backend by design, so a gateway left running there answers every request
that way. The owner was asked the same day whether the gateway's `202` should say when its recipient is offline,
and said yes; the form of the answer was left to the session that did the work (work item 408).

## Decision

The status stays `202`: the message is accepted and stored, and that is what `202` says. When the recipient will
not read it now, the answer carries one more member, `waiting`, a list of one or two words:

- `offline`: the recipient's state on the roster;
- `hold` or `refuse`: its inbound policy.

They are the roster's own words, `offline` first. With nothing to say, the member is absent, and the answer is
byte for byte what it was.

One rule decides it for the gateway and for `brigade send`, `SessionRecord.Waits` in `internal/protocol`; `brigade
send` puts its sentences to the same words.

The roster is the one the gateway reads for the request, before the send. A request with `to` or `member` reads
it already. A request with `reply_to` alone took its recipient from the gateway's record of the thread and read
no roster: it reads one now, for this alone, with a wait of five seconds of its own, and a roster that cannot be
read or does not answer does not stop the reply, whose answer then has no `waiting`.

An answer with `duplicate` has no `waiting` either. The backend had that message already: it was accepted by an
earlier request and may have been read before its recipient went offline, and a service must never be told
"nobody is there" of a message that was read.

## Alternatives considered

- **Another status for a message that waits**, a `200` or a `207` beside the `202`. Lost: a service that tests
  for `202`, as the page told it to, would take a waiting message for a failure and send it again.
- **Refuse a message for a session that is offline**, with a `409`. Lost: the message is stored and is read when
  the session runs again, as a message from a teammate's session is; refusing it would make the gateway stricter
  than `brigade send`, and Brigade allows by default (the owner's words of 2026-11-21).
- **`offline` alone**, as the work item's title says. Lost: `hold` and `refuse` are the same fact to a service,
  accepted and not read now, the gateway has them in the same roster row, and a second change to the answer
  later costs every service a second reading of the page.
- **The sentences `brigade send` prints**, as the members of the list. Lost: the answer is for a program, which
  compares words; the page has the sentences.
- **The recipient's `state` and `inbound` as two members**, always present, for the service to work out. Lost:
  every service would write the same rule, and a recipient that reads now would change the answer too.
- **No roster for a `reply_to` alone**, leaving that answer as it was. Lost: a reply to a session that has ended
  since it asked is the commonest way a service's message goes unread, and after a team's move it is every reply.
- **Fail a `reply_to` whose roster cannot be read.** Lost: a reply never needed the roster to be sent, and the
  answer's new member is not worth a message.

## Evidence

- `TestTheAnswerSaysWhyAnAcceptedMessageWaits` holds the member for `to` and for `member` against a roster with
  each case and both pairs, and that it is absent for a recipient that reads now;
  `TestAReplysAnswerSaysWhenItsSessionIsGone` holds a `reply_to` alone, with the roster unread, unanswering, and
  with the session gone from it; `TestARepeatedRequestSaysNothingOfWaiting` holds the duplicate;
  `TestWaitsNamesWhyAMessageWaits` holds the rule.
- Through the built `brigade` and `brigade-webhook-gateway` as real processes, on 2026-11-22: a member's one
  session ends, and the gateway's answer to a request for that member, and to a late reply to it, is `202` with
  `"waiting":["offline"]`, where the answers before the session ended had no such member. For a team on a server,
  `TestTheWebhookGatewayOnAServerThroughTheRealBinary`, against the stand-in and against the built server; for a
  team on Supabase, `TestTheWebhookGatewayOnSupabaseThroughTheRealBinary`, against the local stack. With the
  member left out, both fail.
- Not measured against a hosted Supabase project or a team's own running server.

## Consequences

- A service can tell its person "sent, but nobody is there". One that reads the status alone is told what it was
  told before.
- A `reply_to` alone costs one more request to the backend, the roster's, and at most five seconds when that
  request does not answer.
- A request sent again after its answer was lost is told nothing of waiting, though its message may wait: the
  service was told as much before this decision, and less than a wrong "nobody is there".
- `waiting` is the roster as it was before the send. A session that comes back a second later still read the
  message, and nothing says when a waiting message is read.
- The contract page says that the answer may gain members and that a service ignores those it does not know.
- To maintain: `SessionRecord.Waits`, the one rule, and the table in `docs/webhook-gateway.md`.
- The hosted mail and Slack gateways answer a person, not a program, and are not changed.
- Revisit if a backend comes to tell a sender at the send itself whether its recipient reads now.

## References

- Work item 408 on the Brigade Board, whose first paragraph records the owner's word; work item 389, which found
  it.
- `docs/webhook-gateway.md`, "What the service sends"; `docs/project-owner/configure/webhook-gateway.md`, "When
  the team moves".
- `internal/gateway/webhook/http.go` (`route`, `waitsOf`), `internal/protocol/session.go` (`Waits`),
  `internal/harness/commands/send.go` (`waiting`).
