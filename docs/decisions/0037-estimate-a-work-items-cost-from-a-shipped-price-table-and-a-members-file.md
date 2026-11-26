# 0037. Estimate a work item's cost from a shipped price table and a member's file

- **Status:** Accepted
- **Date:** 2026-11-26
- **Supersedes:** none

## Context

The board's page shows on each work item the sessions that worked on it and what they cost (work items 344 and
349). Frank decided on 2026-11-15 that the cost is an estimate at list price: Brigade multiplies the tokens of
each model by one price table. Only Claude Code gives a cost of its own, and only to the status line; Codex CLI
gives none on a ChatGPT plan; Grok Build gives one without saying what it is; Muse Code gives none in its journal
(ADR 0036). A session can work on several work items one after the other (ADR 0035).

## Decision

1. At each heartbeat, the watcher adds the tokens its session spent since the heartbeat before to the work item
   the session works on then, priced at the price of the session's model at that moment. The sums are kept by
   work item in the session's usage record. A count that fell (Claude Code's `/clear`) starts the stretch from
   zero. A new watcher of the same session carries the sums on from the record.
2. The price table is the prices Brigade ships, each with its source and the day it was read, with a member's
   `prices.json` in Brigade's config directory over them (Frank's decision of 2026-11-26). Brigade ships only the
   prices it can cite: the current Claude models, from Anthropic's pricing page.
3. A model with no price has no estimate: its tokens still count, and the page says the cost is not available or
   that part of it is not priced. A harness's own cost is not used for the estimate.
4. The local page shows this machine's sessions only. Sending usage to the team page is work item 508.

## Alternatives considered

- **The harness's own cost.** It would need no table. It lost: two of four harnesses give none, Grok Build's is of
  unknown kind, and the totals of the four would not be comparable.
- **A shipped table only.** It lost: a model Brigade cannot cite would have no estimate until a release, and a
  team with a negotiated price could not use it.
- **A member's file only.** It lost: nothing would show a cost until someone filled it.
- **Several work items at once, the stretch divided between them.** It lost with ADR 0035: one at a time.

## Evidence

- Anthropic's pricing page (https://platform.claude.com/docs/en/about-claude/pricing), read on 2026-11-26: input,
  output, cache-hit and cache-write prices of the Claude Fable, Opus, Sonnet and Haiku models in the table.
- The tests of work item 349: the prices, the stretches and a reset (`internal/harness/usage/prices_test.go`), the
  sums by work item across a switch and a new watcher (`internal/harness/watch/hostfacts_usage_test.go`), and the
  page's agents, totals and limits (`internal/harness/commands/workitem_page_usage_test.go`).

## Consequences

- A cost is as good as the table: a price that changed after its `as_of` day misleads until a release or the
  member's file changes it.
- Cache writes are priced at the 5-minute rate, and Claude Haiku 5.5 at its price for prompts of at most 100,000
  tokens: a sum of tokens keeps neither the cache's duration nor each prompt's size.
- A stretch goes to the work item open at the heartbeat, about 30 seconds: work just before a `start` or a
  `stop` can land on the other side of it.
- Revisit this when the team page (work item 508) needs one table for the whole team.

## References

Work items 344, 349 and 508; ADRs 0035 and 0036; `internal/harness/usage/prices.go`;
`docs/team-member/use/usage.md`, "By work item".
