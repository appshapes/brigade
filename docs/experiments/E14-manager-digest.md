# E14: the manager digest, live (card 162)

**Date** 2026-10-29. **Driver** `scripts/experiments/E14-manager-digest.sh`, run from this checkout at `d57da9f`
on the dev build `0.42.0-dev`. **Plan** `.context/plans/manager-digest.md` §8. **Machine** this one: macOS,
Syncthing 2.1.5 on PATH, the real binding of the team `brigade` (copied into a throwaway config dir; the real store
never written), the real backend, the real mail gateway as the manager's road.

**Superseded in part by [E15](E15-digest-subscribers.md)** (card 167, 0.44.0): `brigade report configure` and the manager
role are gone, so the §8.6 and §8.3 steps of this driver no longer run as written; the entries, the Stop hook's note
ask, the rank and the hand-over it proved are unchanged.

## What ran

Two sessions of this machine's member, `digest-proof-a` and `digest-proof-b`, in two throwaway checkouts of one
repository (`origin` `proof/digest-proof`, so both derive the same repository name and folder ids), each with its
own state directory and so its own Syncthing instance, driven as Claude Code drives a session: a sleeper as
`CLAUDE_PID`, the real `brigade hook session-start`, `prompt`, `stop` and `session-end` with Claude Code's own
documents on stdin, two real watchers in sink mode with the environment the hook builds, and the session commands
through the Bash tool's environment. A third session, `digest-proof-c`, with `sync: off`; a fourth checkout, `d`,
that does not ignore the folder, for the address rule. No model: the model half of §8.2 — that the model acts on
the `Brigade state:` ask — is a live-session observation after the release.

## Results

| § | Assertion | Outcome |
| --- | --- | --- |
| 8.1 | Both sessions registered on the real team; `a`'s map names the state folder | ok, 1 s |
| 8.1 | After the prompt, `a`'s entry is in `a`'s folder with the member's label and `start_head` = HEAD | ok |
| 8.1 | Both watchers ready; both sync adapters applied | ok |
| 8.1 | `a`'s entry reached `b`'s checkout through Syncthing, and `b`'s reached `a`'s | ok, 2 s |
| 8.1 | `brigade doing` in `a` appears in the entry's doing history | ok |
| 8.6 | `brigade report configure` with an address in the ignored checkout writes `reports.json` (`every 2m`) | ok |
| 8.6 | The same in checkout `d`, which does not ignore the folder, is refused with the address rule's line | ok (the script's first run grepped for the reason token where the human line prints the message; the captured line is the refusal, and the assertion now reads it) |
| 8.3 | `brigade decision` in `a` writes `decisions/<id>.json` and sends the ask through the mail gateway (`sent: to rjae@appshapes.com through the email gateway, message ea58da14…`) | ok |
| 8.3 | The decision reached `b`'s checkout; `b` (another session of the member) closed it with the outcome | ok |
| 8.2 | A Stop after a commit asks for a note (`{"decision":"block","reason":"Brigade state (one ask, then finish): this turn made a commit and no note followed; …"}`) | ok |
| 8.2 | The Stop with `stop_hook_active` asks nothing; the next real Stop asks nothing (never twice in a row) | ok |
| 8.4 | `a` (rank 0, the older registration) produced the digest; `b` did not; the marker reached `b`'s checkout | ok, 8 s after the period's conditions held |
| 8.5 | `brigade report show` in `b` lists `digest-proof-c` (sync off) as roster only | ok |
| 8.4 | `a`'s watcher stopped and its session ended; `b` wrote a note; `b` produced the next digest once the 2-minute period elapsed | ok, 108 s after the note |

Exit status of the run: 1, the §8.6 assertion above; every behaviour the plan's §8 names held. The marker `a`
wrote: `{"version":1,"produced_at":"2026-10-29T16:55:28…","session_id":"9465a3fe…","session_name":"digest-proof-a","decisions_open":1,"commits":1,"active":2}`.

## Measured

- Registration of two sessions on the real backend: 1 s. Syncthing's propagation of a new entry between two
  instances on one machine: 2 s (local discovery).
- The producer's first digest: 8 s after `reports.json` landed and the entry changed, at the state goroutine's
  20-second tick; the hand-over to the second session: 108 s after its note, the 2-minute period from the first
  marker.
- The mail gateway accepted one decision's ask and two digests addressed to the manager; each `message send`
  returned `accepted` within the command's budget.

## Not proven here

- The model's response to the Stop hook's ask (§8.2's first half): the hook asked once after the commit, asked
  nothing on the continuation and nothing twice in a row; whether the model records the note is the next live
  session's to observe, as E10 did for the doing line.
- The Slack road (§8.3 names Slack): the proof's manager is an email address, and 0.43.0 sent an address through
  the mail gateway only, so the ask and the digests went by mail. 0.43.1 routes an address through the Slack
  gateway when the team has one, and a second check the same day (E14b, by hand on the dev build after 0.43.1:
  one session, `brigade report configure --manager <address>` with the recipients defaulting to the label, one
  `brigade decision`) answered `sent: to <address> through the slack gateway, message f695573f…` and closed the
  decision; whether the DM landed in the manager's Slack is the manager's to confirm.
- Two machines: two engines on one, as folder-sync's proof did.
- §8.7, the body under 16 KiB with the caps hit: `digest.TestBuildCaps`.
