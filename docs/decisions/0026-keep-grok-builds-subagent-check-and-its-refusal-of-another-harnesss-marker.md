# 0026. Keep Grok Build's subagent check and its refusal of another harness's marker

- **Status:** Accepted
- **Date:** 2026-11-23
- **Supersedes:** none

## Context

ADR 0021 opened the refusals of the Grok Build path that no measurement backs, behind the member's option
`grok_strict`, and kept those with a measured reason. Two fitted neither side and it left them as they were
until the owner answered:

- **A subagent's hook.** Grok Build's guide says `SessionStart` does not fire for a subagent, but `Stop` and
  `SessionEnd` do, carrying `subagentType`, and tells a hook to end early there. Brigade's hook does. The check
  refuses no session. Brigade's own runs were made with `--no-subagents`, so nothing of Brigade's measures it.
- **Another harness's marker in the environment** (`ambiguous_host`): a Grok Build started from the shell of a
  Claude Code, Muse Code or Codex CLI session. No run backs the refusal. Its reason holds by construction:
  `brigade` reads its host from the environment in a fixed order, Claude Code's marker first and Grok Build's
  last (`HostOf`, `internal/harness/config/host.go`), so a command the Grok Build session's model runs there
  speaks as the outer session.

By the words of the ruling behind ADR 0021 ("keep the measured, open the rest") both would open. The owner was
asked on each, with the options and the working session's recommendation, on 2026-11-23 (work item 377).

## Decision

Both stay as they are, whatever `grok_strict` says.

- The hook goes on ending early for a subagent. The owner's answer: "Keep it as it is".
- A Grok Build session whose environment carries another harness's marker is not registered, and its start
  shows one line with the reason. The owner's answer: "Keep it, with its reason on the page". Grok Build's
  configuration page carries the reason.

Each is kept on a reason and on the owner's word, and on no measurement of Brigade's. The pages say so.

## Alternatives considered

- **Open the subagent check with the rest.** Lost: it refuses nobody, Grok Build's own guide prescribes it, and
  opened, a subagent's end would retire the main session's receiver, which then stops receiving.
- **Open the marker's refusal as it stands.** Lost: the session would register and its own commands would then
  send as the outer session.
- **Make `brigade` in a Grok Build shell prefer Grok Build's marker where its hooks registered that session,
  and then open the refusal.** Lost for now: it changes the order every host's commands resolve by, which is
  more work and a risk of its own, for a case nobody has asked for. It is what to take up if a member needs to
  start Grok Build from inside another harness.

## Evidence

- The finding per check, the first comment of 2026-11-22 on work item 377 by the working session, from
  Grok Build 1.0.92's hooks guide and the code.
- The owner's two answers, comments of 2026-11-23 on work item 377, each with the question and the options as
  put to him.
- The marker's refusal, seen live on 2026-11-23: Grok Build 1.0.92 on macOS arm64, the build of `d5cc0324`,
  started with `CODEX_THREAD_ID` set in its environment. The session did not register, and its start showed
  "Brigade: this Grok session's environment also carries another harness's session marker, so its identity
  cannot be proven and Brigade is off here. Start Grok from a plain terminal."
- `TestGrokRefusesUnboundInputs`: both are refused with the option off and on.
- None measured: a subagent's `Stop` or `SessionEnd` reaching Brigade's hook. No live run used a subagent.

## Consequences

- A member who starts Grok Build from inside another harness's session gets no Brigade there, and reads why in
  one line at the start.
- `grok_strict` stays the one option for what ADR 0021 opened. These two are outside it and need no option.
- Revisit the subagent check when a run with a subagent shows what Grok Build sends, and the marker when a
  member needs Grok Build inside another harness.

## References

- Work item 377 on the Brigade Board; ADR 0021, which left these two open.
- `internal/harness/hook/grok.go` (`decodeGrok`), `internal/harness/config/host.go` (`HostOf`, `NativeID`).
- `docs/team-member/configure/grok.md`, "The strict checks", which carries both reasons;
  `docs/security.md` section 17.
