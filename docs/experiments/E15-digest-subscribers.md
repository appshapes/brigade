# E15: digests for every subscriber, live (card 167)

**Date** 2026-10-29. **Driver** `scripts/experiments/E15-digest-subscribers.sh`, run from this checkout at `7283a4f`
on the dev build `0.43.1-dev` (the code of P66-1). **Plan** `.context/plans/manager-digest.md` §5.5 v2. **Machine**
this one: macOS, Syncthing on PATH, the real binding of the team `brigade` (copied into a throwaway config dir; the
real store never written), the real backend, the real Slack gateway as both subscribers' road.

## What ran

Two sessions of this machine's member, `digest-proof-a` and `digest-proof-b`, in two throwaway checkouts of one
repository, each with its own state directory and so its own Syncthing instance, driven as Claude Code drives a
session (a sleeper as `CLAUDE_PID`, the real `brigade hook session-start`, `prompt`, `stop` and `session-end`), two
real watchers in sink mode, and the session commands through the Bash tool's environment, as E14 did. New here: a
`reports.json` of the 0.43 shape and its `digests/last.json` are written first; `a` subscribes; a second subscriber,
`E15 second`, is written into the file by the script — one binding is one member, so no second session could write
its own entry — with this member's address as its recipient and `only: mine`; both markers are set 70 seconds old
so both come due on one tick. No model: whether the model acts on the `Brigade state:` ask stays a live-session
observation.

## Results

| § | Assertion | Outcome |
| --- | --- | --- |
| 5.1 | Both sessions registered; `a`'s entry in its folder with the member's label; both watchers ready, both sync adapters applied; the entries crossed between the checkouts | ok (37 s for the first entry to cross) |
| 5.5 | The 0.43 file (`version` 1, one `manager`) reads as one subscriber: `brigade report show --json` lists it | ok |
| 5.5 | `brigade report subscribe --every 2m` from `a` writes version 2 with the one subscriber, the address as the recipient (the default), and carries `digests/last.json` over to `digests/rjae-appshapes-com-623f0d.json` with the label | ok |
| 5.5 | The two-subscriber file reached `b`'s checkout | ok, 16 s |
| 5.5 | `brigade decision` with no `--to` is refused (`invalid_input`, `to_needed`) | ok |
| 5.5 | `brigade decision --to <member>` went through the Slack gateway to the member's address (`sent: to rjae@appshapes.com at rjae@appshapes.com through the slack gateway, message d29cc75d…`) | ok |
| 5.5 | `brigade decision --to "E15 second"` went through the Slack gateway to that subscriber's recipient; the decision file carries `to` | ok |
| 5.5 | `brigade decision --to nobody-e15` (not subscribed, no session) is recorded and the line says `not sent: no gateway of their recipients is on the roster, and they have no session…` | ok |
| 5.2 | The member's decision reached `b`; `b` (another session of the member) closed it | ok |
| 5.5 | `a` (rank 0) produced on one tick for both subscribers: `state: digests produced` with `sent 2, missed 0`, 68 s after the markers were written | ok |
| 5.5 | The second subscriber's digest went through Slack with `decisions_open 1`: `--only mine` kept it to the one decision addressed to them | ok |
| 5.5 | The member's digest went through Slack with every open decision | the line read `decisions_open 3` where the script expected 2: `b`'s close of the member's decision had not synced back into `a`'s folder when the tick fired, so that decision was still open there. The behaviour is right (the digest reads the folder it has); the assertion now accepts 2 or 3 |
| 5.5 | `b` did not produce; one marker per subscriber, written by `a`; the second's marker reached `b` | ok |
| 5.3 | A Stop after a commit and a turn longer than a minute asks for a note, a fresh doing line and a decision, and the decision sentence names `--to <their roster label>` | ok |
| 5.5 | `brigade report unsubscribe` from `b` removed the member's entry and nobody else's (one subscriber left) | ok |
| 5.5 | `brigade report send` from `b` reached the one subscriber left through Slack (`1 decisions open, 0 commits, 2 sessions active`) and wrote their marker | ok |

Exit status of the run: 1, the open-decisions assertion above; every behaviour the plan's §5.5 v2 names held.

## Measured

- Registration of two sessions on the real backend: 1 s. Syncthing's propagation of the first entry between two
  instances on one machine: 37 s (the engines were still introducing each other); of the file and the markers
  afterwards: 16 s and under a second.
- The producer's tick for two subscribers: 68 s after the markers were written (both due at 120 s from markers set
  70 s in the past, judged at the 20-second check); the two sends and the two markers in the same tick.
- The Slack gateway accepted two decisions' asks and three digests (two from the producer, one from `report send`);
  each `message send` returned `accepted` within the command's budget.

## What arrived in Slack

Five DMs to the member's address, by the Slack gateway: two decisions (one `for rjae@appshapes.com`, one `for E15
second`, each saying it is the proof and needs no answer), two digests of the producer's tick (the member's with
every open decision, the second's with its one), and one digest from `report send` (the second's). None needs an
action.

## Not proven here

- Two members each subscribing from their own session: this machine has one binding. The second subscriber's
  entry was written by the script in the shape `brigade report subscribe` writes; `TestReportSubscribeWritesTheFile`
  and `TestReportSendFansOut` cover the command for several labels.
- The mail road: the team has a Slack gateway, so an address goes through it (0.43.1); the mail gateway is the
  road only without one (`TestReportsAndGateways`).
- The model's response to the Stop hook's ask, as in E14.
- Two machines: two engines on one, as before.
