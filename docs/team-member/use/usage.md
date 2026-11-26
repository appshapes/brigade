# See what your sessions spend

Each session's watcher reads what the session spent, from the harness's own records on your machine. The
values are the model, the context in use, the tokens, the cost, the plan's limits and the paid extra usage.
`brigade usage` shows them. They stay on your machine: nothing goes to the team, the backend or a gateway
([ADR 0036](../../decisions/0036-read-each-sessions-usage-from-its-harnesss-own-records.md)).

## Show it

```
$ brigade usage
347-read-the-usage-of-each-session (claude-code, claude-opus-5-5), read 12s ago
  context: 459k of 1.0M
  tokens: input 970, output 325k, cache read 123.1M, cache write 1.4M; 468 model calls
  cost: $41.20, estimated at list price
  limits: 5-hour 23.5% (resets 2026-11-26 14:00 UTC), weekly 41.2%; as of 12s ago
  extra usage: not available: the harness does not give it
```

- In a session, `brigade usage` shows that session; `--all` shows every session whose watcher ran on this
  machine. In a terminal, `brigade usage` shows every one.
- `--json` gives each session's record whole. A value is `{"value": …}` or `{"unavailable": "<reason>"}`.
- A value the harness does not give is never shown as zero. It says `not available` and why. A record that
  is not in the shape Brigade reads says so, and its numbers are left out.
- The watcher reads at each heartbeat, about every 30 seconds while the session runs. `read` says when.

## What each harness gives

| Value | Claude Code | Codex CLI | Grok Build | Muse Code |
| --- | --- | --- | --- | --- |
| Tokens | transcript, each message once | rollout's `token_usage_record` | `usage.json` | journal, each completion once |
| Model calls | transcript | not given | `usage.json` | journal |
| Context and window | transcript; window from the status line step | rollout | `signals.json` | in use from the journal; window not given |
| Cost | status line step, estimated at list price | not given on a ChatGPT plan | `usage.json`; Grok Build does not say whether the cost is a list price or a bill | not given in the journal |
| Plan limits | status line step (Pro, Max and Ultra) | rollout: 5-hour and weekly | not read. Brigade does not run `grok usage`, which refuses on the shell route; Grok Build's own `/usage` shows the limit | `muse usage --json`, every 5 minutes |
| Extra usage | not given | rollout: credits and their balance | not given | not given |

Input tokens are the ones not read from the cache. Codex CLI, Grok Build and Muse Code count the cache reads in
their input; Brigade takes them out, so the four harnesses add up the same way.

On Claude Code, `/clear` starts a new transcript, so the tokens and the cost start again from zero, as Claude
Code's own cost does. The record stays the session's.

## Add the status line step (Claude Code)

Claude Code gives the cost, the context window and the plan's limits only to your status line, and a plugin
cannot set it. To give them to `brigade usage`, add one line near the start of your own status line script:

```bash
#!/bin/sh
input=$(cat)
printf '%s' "$input" | brigade statusline
# ... the rest of your script, which reads "$input" and prints your line
```

`brigade statusline` reads the document, keeps those values under the state directory, prints nothing and never
fails, so your line stays as it was. Without the step, a Claude Code session shows its tokens and context, and
the other three values say why they are not available.

## By work item

At each heartbeat, the watcher adds what the session spent since the heartbeat before to the work item it
works on then: the one `brigade work-item start` recorded
([Say which work item a session works on](work-items.md#say-which-work-item-a-session-works-on)). What a
session spends with no work item open goes to none. `brigade usage` lists the sums under `work item <number>`,
and each card of `brigade work-item page` shows them.

The estimated cost is the tokens times a price table, at list price. It is never what anyone is billed:

- **Shipped prices.** Brigade ships the prices it can cite, each with its source and the day it was read: the
  current Claude models, from Anthropic's pricing page. Cache writes are priced at the 5-minute rate. Claude
  Haiku 5.5 costs more for a prompt over 100,000 tokens; a sum of tokens has lost each prompt's size, so its
  lower price stands for all.
- **Your prices.** `prices.json` in Brigade's config directory adds a price, or changes a shipped one, in US
  dollars for each million tokens:

  ```json
  {"models": {"gpt-6.2-astra": {"input": 1.25, "output": 10, "cache_read": 0.125, "cache_write": 0, "source": "the vendor's pricing page", "as_of": "2026-11-26"}}}
  ```

  A file that is not usable leaves the shipped prices, and the watcher's log says so. A session reads the file
  when its watcher starts.
- **No price.** A model with no price shows `cost not available: no price for <model>`, and its tokens still
  count. A work item with some sessions priced and some not says `part not priced`.

## Where it is

One file for each session, `usage/<key>.json` in Brigade's state directory, mode 0600, and the status line's
under `usage/statusline/`. The watcher writes a session's file when a value changes, and every 5 minutes while it
runs. Whether a member shares any of it with the team is work item 348, off by default.
