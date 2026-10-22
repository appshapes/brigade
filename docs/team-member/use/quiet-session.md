# Make one session quiet

Several sessions of yours run at once and one of them should take nothing from the team: a long private task, a
scratch session, a session no teammate's message should steer. Tell that session, and only that one:

```
/brigade:quiet                         # hold this session's team messages for you; say so on the roster
/brigade:quiet on --to my brf-127 session   # the same, and the roster line names where to write instead
/brigade:quiet on --refuse             # turn messages away instead of holding them
/brigade:quiet status                  # the policy in force and how many messages wait
/brigade:quiet off                     # back to normal
```

## What `on` does

Three things, all to that session alone:

- **Holds its messages.** The session runs `brigade inbound hold`: every message to it is recorded and neither
  delivered nor acknowledged, exactly as under `team_inbound: hold`, and the sender's `brigade send` prints a
  `waiting:` line saying so. `--refuse` runs `brigade inbound refuse` instead: nothing is recorded either, and
  `brigade send --member` skips the session when picking yours.
- **Says so on the roster.** Its line becomes `Quiet working session; not taking team requests.`, with
  `Message <where> instead.` after it when you gave `--to`. Teammates' sessions route by that line.
- **Instructs the model.** For the rest of the conversation it acts on no team message, reports any that still
  reaches it in one line, and sends nothing to the team, Slack or email unless you ask.

## What it does not touch

- The `team_inbound` option, Claude Code's `crossSessionInbound` setting, and every other session of yours: none
  changes. The switch is a request recorded for that one session, and it can only tighten.
- A session whose own policy is already `refuse` (the option, a `crossSessionInbound` setting, or a mode that
  bypasses prompts without the accept) stays `refuse`; `on` says so, and `off` cannot lift it.

## How long it lasts

Until you say `/brigade:quiet off` or close the session. `/clear` and `/compact` keep it; a new session, and
`claude --resume`, start normal.

## Reading what was held

In your own terminal, as for a `hold` session ([Read and release held messages](held-messages.md)):

```sh
brigade inbox                          # what waits
brigade inbox release --all            # deliver it to the session
```

`off` releases nothing: what was held stays held until you release it, and the session says how many wait.

## The command underneath

`brigade inbound` is what the skill runs; you can run it from the session's shell yourself:

```
brigade inbound                        # the policy in force, where it came from, how many messages wait
brigade inbound hold                   # hold team messages for this session
brigade inbound refuse                 # turn them away
brigade inbound accept                 # return to the session's own policy
```

It runs inside a session only, writes the session's own state, and sends one heartbeat so the roster shows the
new policy at once; the session's watcher applies it within seconds. `--json` gives the same as a document.
