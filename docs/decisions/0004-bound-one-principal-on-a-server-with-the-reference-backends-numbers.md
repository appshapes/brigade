# 0004. Bound one principal on a server with the reference backend's numbers

- **Status:** Accepted
- **Date:** 2026-11-14
- **Supersedes:** none

## Context

A Brigade server kept the send budgets of BAP/1 4.5.12 and no other bound: nothing limited the teams one member's
installation creates, the sessions it opens or the joins it fails (`docs/developer/change/server.md`, "Known
limits"; `docs/security.md` section 20). Since 2026-11-12 anyone who can reach a server may create a team on it
(`.context/plans/brigade-server.md` section 10, ruling 2), and since 2026-11-13 a team depends on one
(work item 272).

The reference backend, a Supabase project, bounds all three for one principal: five teams created in an hour, five
failed joins in fifteen minutes, counted over all teams, checked before the secret and cleared by a join that
succeeds, and 120 new sessions in an hour, a resume not counted. On 2026-11-12 the owner was asked whether a
server gets the flood bounds the Supabase backend has, and answered: "Agreed. But a reinforcing reminder: Brigade
is open by design, and only closed by choice/configuration" (the plan's section 2, item 14). The numbers were put to him on 2026-11-13 as work item
313's part 3, with the reference backend's recommended; he named other parts and said, for what he did not name,
"use your own recommendation on decisions", so that recommendation stands by his word.

Three facts shape what a server can do. A caller with no credential the server knows has no principal there
until its create or its join succeeds, where the reference backend gives every caller an account first
(`server/crates/brigade-core/src/execute.rs`, `principal_or_new`). A request that is refused makes no entry, and
the state is built from entries alone. And BAP/1 4.6 lists the reasons a `rate_limited` answer names without
`team_create`, which the reference backend has sent since its first schema of 2026-08-30.

## Decision

One principal on a Brigade server:

- creates at most five teams in any hour;
- opens at most 120 sessions in any hour, over all its teams; a resume opens none and is never refused for it;
- after five joins that failed for their secret within fifteen minutes, over all teams, has its next join
  refused before the secret is looked at, the right one included, until the oldest of those failures is fifteen
  minutes old; a join that succeeds clears them.

Each refusal is `rate_limited` (429 over HTTP) with the reason the reference backend sends, `team_create`,
`register_session` or `join_attempts`, and with `retry_after_ms`: the time until the oldest counted event leaves
its window, and never under a minute for a join.

The teams and the sessions are counted from the record times of the log, in the state, so a restart and a
snapshot keep the count. The failed joins are counted in the memory of the process that answers, and a restart
forgets them.

A join that cannot be read is told so before the bound is asked, as the reference backend tells it, and is not
counted. A gateway's join is not counted either: it is made inside the server, and a gateway that was put out
of its team tries again each minute with the file its operator keeps, so it must be let in the minute that file
is right (`docs/project-owner/server/gateways.md`).

The numbers are the server's own constants, the same for every team on every server. There is no option for
them.

A caller with no credential the server knows is held by none of the three: it is a new principal with each
request. A limit by address is the proxy's in front of the server. Nothing caps a total either: the teams a
server holds and the members a join secret admits stay unbounded, and nothing closes by default.

BAP/1 4.6's list of reasons gains `team_create`. No wire shape changes: the schema never listed the reasons, and
both adapters already carry the one they are given.

## Alternatives considered

- **No bound, said on the page.** The other answer put to the owner. It lost because the case that happens is
  not an attacker but an installation that loops, a hook that registers again and again or a stale secret
  retried, and because a team would meet limits on one backend and none on the other.
- **An operator's option for each number, or a switch.** Nobody has asked for other numbers, the reference
  backend has none, and each option is one more default to explain. "Closed by configuration" is about who may
  come in; a rate keeps nobody out. We would add an option when an operator needs another number.
- **A record of each failed join**, so that the count outlives a restart and reaches a standby, as the reference
  backend's table does. It would make a refusal write to the log for one request, where the rule everywhere else
  is that a refusal makes no entry; it would add a record a server before it refuses to start on; and it would
  let a member with a credential grow the log by failing. The bound is for an installation that repeats itself,
  and a restart that forgets five failures does not weaken that.
- **A bound by address inside the server.** Behind a proxy the server sees one address. The proxy has the real
  one and the means; the Project Owner's page says so.
- **A team's own count of failed joins**, refusing a team whose joins fail often. The reference backend counts
  it and only logs it, for the reason it lost here: a stranger who fails on purpose would lock the team's own
  members out.

## Evidence

- The reference numbers: `supabase/migrations/20260830120000_brigade_schema.sql` (`create_team`: five in an
  hour, `brigade:rate_limited:team_create:3600`; `join_team`: five failures in fifteen minutes before the secret
  is checked, a wait of at least 60 seconds, cleared by a success) and
  `supabase/migrations/20260923022323_session_sync_peer.sql` (`register_session`: 120 in an hour, resumes not
  counted, with the note that one slow conformance run registers about 55 sessions on its busiest principal).
- The conformance suite fits inside them. Counted on 2026-11-14 in `internal/conformance/cases`: one principal
  creates at most one team, fails about three joins (C-49) and registers about 47 sessions, 55 with `--slow`. Run
  that day against the built server with the bounds, on the file log and with `--slow` on the Cavalry feed
  (`make server-test`): green, with the skips it had.
- The rules themselves: three tests in `server/crates/brigade-core/tests/rules.rs`, with the clock in the test's
  hands, and one in `server/crates/brigade-server/tests/server.rs` through a running server over HTTP and across
  a kill.
- A second agent's review of the first build, 2026-11-14, found that counting every join would lock a server's
  own gateway out: one that was put out of its team fails once a minute, so after five minutes the right
  secret would have been refused for ten more. That is why a gateway's join is not counted.
- None measured under real load: the one team on a server has two active members, far below every number.

## Consequences

- A team meets the same limits on either backend, and the pages state one set of numbers.
- An installation that loops is told to wait, with how long, instead of filling a team's roster and the log.
- A stranger can still fill a server it can reach, one new principal at a time. `docs/security.md` section 20
  keeps saying so, and the operator's answers stay what they were: a creation token, a server that is not on
  the public internet, or a limit by address in the proxy.
- The count of failed joins belongs to one process. A restart forgets it, and a standby (work item 312) starts
  with none.
- The state holds two more lists for a principal that created a team or opened a session: the times of the hour
  before its latest. They are let go of when one is added, not by the sweep, which would otherwise visit every
  principal a server ever made. A snapshot carries them with no new format: a build before them drops them and
  has no bound to hold against them.
- A conformance case must stay inside the numbers: one that creates a sixth team, fails a fifth join or opens a
  121st session for one principal inside the window is refused by a server, as it is by the reference backend.
- We would revisit this when an operator needs another number, when a real installation meets 120 sessions in an
  hour, or when a standby makes the forgotten failures matter.

## References

Work item 313, part 3, and its comment of 2026-11-14 01:44 UTC with the owner's words;
`.context/plans/brigade-server.md` section 2 item 14 and section 10 ruling 2; `docs/protocol-v1.md` 4.6;
`docs/security.md` section 20; `docs/project-owner/server/run-the-server.md`;
`server/crates/brigade-core/src/limits.rs` and `attempts.rs`; ADR 0001.
