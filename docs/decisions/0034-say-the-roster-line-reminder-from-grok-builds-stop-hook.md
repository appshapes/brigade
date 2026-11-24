# 0034. Say the roster-line reminder from Grok Build's Stop hook

- **Status:** Accepted
- **Date:** 2026-11-24
- **Supersedes:** none

## Context

Brigade reminds a session's model to keep its one-sentence roster line current with one of three fixed lines,
which the prompt hook prints, at most one in ten minutes, and only where the call will neither raise a
permission dialog nor be denied (`docs/security.md` section 5). Grok Build shows its model nothing that a prompt
hook prints: the host table has it as `promptDiscarded`, measured under work items 120 and 377. So on Grok Build
the hook said the reminder into a buffer nobody reads, and wrote the stamp that says it was said.

What a Grok Build model is shown is the Stop hook's answer, which continues the turn: the ask to arm the inbox
monitor, and the end-of-turn ask of section 18. That ask speaks of the roster line only after a turn of ten
minutes or more, and is made only in a project that shares a state folder. In a project without one a Grok Build
session was never reminded at all (measured in a live session of Grok Build 1.0.92, work item 440).

## Decision

On a host that shows the model nothing a prompt hook prints, the Stop hook's answer carries the reminder. The
gates and the stamp are the prompt hook's own: the same three texts, the same rule for where (Grok Build gives the
hook Claude Code's words for its modes, so under always-approve alone), at most one in ten minutes, the full line
once more after a stretch in a mode where it could not be said. It needs no state folder. The prompt hook on such
a host says nothing and stamps nothing; the stamp is written at the Stop that shows the line.

A turn's end says one thing. The arming ask comes first; then the end-of-turn ask when it is due, and when that
ask spoke of the roster line and the short line was the one due, the reminder's stamp says the model was
reminded; else the reminder. Because it costs a turn there, it is never said at the end of a turn that a Stop
answer started, at the end of a turn that did not end by itself, in a quiet session, nor with under a second of
the hook's budget left.

`share_doing` switches it off, as it switches the reminder off everywhere; `status` stays the switch of the
end-of-turn ask alone. The other three harnesses are told nothing new. `docs/security.md` section 5 says this in
four added sentences, which the owner approved; no sentence that was there changes.

## Alternatives considered

- **Leave it, and say so on the pages.** That is what work item 440 did. It leaves a Grok Build session's line
  blank in every project without a state folder, and the roster is what teammates route by.
- **Fold the reminder into the end-of-turn ask.** The ask needs a state folder and `status`, and is made after a
  commit or a long turn. The reminder would inherit conditions that have nothing to do with it, and a project
  without a state folder would still have none.
- **Say it from SessionStart.** Grok Build shows a start hook's `systemMessage` to the person and not to the
  model (the participation report), so the model would not be told there either.
- **Let `status: off` stop it on Grok Build as well**, since there it costs a turn at a turn's end. One more rule
  that holds on one harness only; the reminder is the same reminder everywhere, and one option stops it.

## Evidence

- Grok Build 1.0.92, one live session, 2026-11-24 (work item 440): the hook is given `bypassPermissions` under
  always-approve, `default` with no mode shown, `plan`, and `auto` under auto-review; the Stop hook's answer
  reached the model ("Stop blocked by plugin/brigade/hooks, continuing: …") and the model acted on it; the
  prompt hook wrote the reminder's stamp with nothing shown.
- The live session of this work item, in a project with no state folder: recorded in the execution log's row
  and on the work item.
- `internal/harness/hook/stop_test.go`: the reminder at Grok Build's Stop, its gate, one answer at a turn's end,
  and the other hosts unchanged. Each fails on the code before this decision but the last.

## Consequences

- A Grok Build session under always-approve sets its roster line as a session of the other harnesses does.
- Each reminder costs a turn of the model there, at most one in ten minutes, where a printed line costs its
  tokens. A member who does not want it turns `share_doing` off.
- Under `default`, `plan` and auto-review a Grok Build session is still not reminded: Brigade reads no setting of
  Grok Build's that could say the call will not be stopped.
- The first line says the session's line is blank, and on Grok Build it is first said after the turn that armed
  the monitor. A model that set its line unasked before then is told that it is blank, and spends a turn on it:
  Brigade keeps no record of a published line where there is no state folder.
- A host added later that discards a prompt hook's output and has no Stop hook would be reminded by nothing: the
  host table's row is where that would show.

## References

Work item 442; work item 440, where it was found; `docs/security.md` section 5;
`docs/team-member/use/your-session-on-the-roster.md`; `docs/experiments/grok-participation.md`.
