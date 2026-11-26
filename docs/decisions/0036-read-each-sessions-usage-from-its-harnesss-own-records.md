# 0036. Read each session's usage from its harness's own records

- **Status:** Accepted
- **Date:** 2026-11-26
- **Supersedes:** none
- **Corrected:** 2026-11-26: the assumption on `muse usage --json` was in part wrong. Muse Code 1.4.25 printed its
  data as `allowance`, as assumed, but with the time as `asOfMs`, not `observedAtMs`, and a window with no
  `windowDurationMins`. The reader takes `asOfMs`, and `observedAtMs` where `asOfMs` is not there (work item 349).

## Context

The board's page and a work item's budget need what each session spent (work items 344, 349 and 350): its
model, the context in use, the tokens of its model calls, its cost, the plan's limits and the paid extra usage.
The four harnesses keep these in different places, and some keep some of them nowhere Brigade can read
(`.context/plans/board-dashboard-344-investigation.md`, table 2). Claude Code gives the cost and the limits only to
the member's status line, which a plugin cannot set (https://code.claude.com/docs/en/statusline.md). The owner
ruled on 2026-11-24 that sharing usage with the team is one option, off by default (work item 348), so nothing
read here may leave the machine yet.

## Decision

1. The session's watcher reads the usage at each heartbeat from the harness's own records on the machine:
   Claude Code's transcript, Codex CLI's rollout file, Grok Build's `usage.json` and `signals.json`, and Muse
   Code's journal and `muse usage --json`, at most every 5 minutes. It reads no auth file and no key.
2. It writes one record for each session to `usage/` in Brigade's state directory, mode 0600. Every value is
   there, or not available with a reason; a value the harness does not give is never written as zero.
3. A sum is made from each call once: a Claude Code message by its id, a Muse Code completion by reading each
   line of the journal once. Codex CLI and Grok Build give their own totals: Codex in its `token_usage_record`,
   whose running total a context it marks full does not reset. Input is the tokens not read from the cache, in
   all four. On Claude Code the totals are the current transcript's, so they start again at `/clear`.
4. `brigade usage` shows the records. Nothing of them rides a heartbeat or goes to the backend, a teammate or a
   gateway.
5. `brigade statusline` is an optional step a member adds to their own Claude Code status line script. It keeps
   the cost, the context window and the limits for the watcher, prints nothing and never fails.

## Alternatives considered

- **New members of the roster's session record.** Every teammate would see the usage at once. It lost: it
  changes the frozen protocol and the frozen REST contract, and the owner ruled sharing off by default.
- **Muse Code's live session protocol (`muse attach --stdio`).** It gives the cost and the context window. It
  lost for now: nobody tested reading a live session that way, and the journal and `muse usage` need no
  connection.
- **`grok usage --json` for Grok Build's plan limit.** It refuses on the shell route, which is the one this
  machine has (the follower's run, 2026-11-26), so the record says why the limit is not available.
- **OpenTelemetry for Claude Code.** It gives the cost and the tokens, to a collector the member sets up. It
  lost on what a member must run; the status line step is one line.

## Evidence

- Claude Code repeats a message's usage on each of its records: in one session's transcript, 985 records with
  a usage carried 447 messages (2026-11-26). The reader's totals equalled an independent count over the same
  file, 468 calls, to the token.
- Codex CLI's `TokenUsage`, `TokenUsageRecord` and `RateLimitSnapshot` at upstream commit 8727186a
  (`codex-rs/protocol/src/protocol.rs`, lines 2600 to 2774), read by Frank's Codex session and here. Its
  `fill_to_context_window` replaces the event's totals with zeros, which is why the totals come from the
  record.
- Grok Build: 53 `usage.json` files whose session totals equal the sum of their turns; 56 `signals.json` files
  with the context in use and the window (Frank's Grok session, 2026-11-26).
- Muse Code: the journal's `model_completed.usage` field names from this machine's journals, and the
  `SubscriptionUsage` type of `muse schema generate-json-schema` (Muse Code 1.4.25). Its input includes the
  cache reads: in a live session's second call, input 44,859 and cache read 42,481 (2026-11-26). The reader's
  sums equalled an independent count over that journal, 3 calls.

## Consequences

- Work items 348 and 349 decide what is shared and put it on the page, from these records.
- A Claude Code member who does not add the status line step has no cost and no limits for their sessions.
- A harness's record format is its own to change; each reader takes only the members it names and leaves a
  value out, with its reason, when the shape is not what it expects.
- Assumption: `muse usage --json` names its data `allowance`, in the shape of Muse's `SubscriptionUsage`. Only
  its empty answer was seen on this machine.
- Revisit this when Grok Build gives its plan limit outside its own `/usage`, or when Muse Code's journal gives
  the cost.

## References

Work items 344, 347, 348 and 349; `internal/harness/usage`, `internal/harness/watch/usage.go`;
`docs/team-member/use/usage.md`; ADR 0035.
