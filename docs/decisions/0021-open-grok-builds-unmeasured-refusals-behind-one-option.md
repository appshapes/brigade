# 0021. Open Grok Build's unmeasured refusals behind one option

- **Status:** Accepted
- **Date:** 2026-11-22
- **Supersedes:** none

## Context

Brigade allows by default and disallows by configuration (the owner's words, 2026-11-21). The Grok Build path
was built in one pull request under a verify-or-refuse rule and has shipped since 0.35.0. Its hooks refused a
session wherever a fact was not the one Brigade expected. Each such refusal is a place where a Grok Build update
switches Brigade off for that harness with nothing else changed, and one did: Grok Build 1.0.67 renamed its
binary and every registration stopped (work item 131).

The repository review of 2026-11-21 listed the refusals and could not tell which rest on a measurement. The
owner ruled on 2026-11-22 (work item 377): establish it check by check; a check with a measured reason stays,
with the reason on its page; every other becomes "register, say so in the log and to the user, and go on"; and
one option restores every opened refusal at once.

What each check rests on is the first comment of 2026-11-22 on work item 377 by the working session. In short:

- measured: leader mode (the hook's parent is the shared leader, a normal exit leaves the actor resident and a
  prompt sent afterwards still succeeds, so nothing identifies a live terminal), and the reading of a message
  in pages of 1,800 bytes, one tool call each (a 2,048-byte page was corrupted by the shell's wrap at column
  2,000, and a model that batched pages in a script was not shown to have read the frame);
- part measured: the process name (the hook's parent is the TUI, and its kernel name on macOS arm64 in 1.0.62,
  1.0.67 and 1.0.92; the Intel and Linux names are derived from the installer's file names), and the start
  source (`new` and `load` were seen; no other value is known to exist);
- never seen to occur: two spellings of a field that disagree, a session id that is no UUID or differs from
  the environment's, a workspace that does not agree with the working directory or the environment;
- no measurement, and no refusal of a session either: the three places where the package's skills hold back
  (a doing line only when asked, a gateway send only when the user authorized it, an update only when the
  user requests it), which follow the acceptance run's scope of default permissions and commands approved by
  hand.

## Decision

A Grok Build hook goes on over a fact that no measurement backs refusing, and says so. The member's option
`grok_strict`, set to `on` in `~/.config/brigade/grok-options.json`, makes each of them the refusal it was. Its
default is `off`.

Opened, behind the option:

| What the hook finds | What it does with `grok_strict` off |
| --- | --- |
| the process that ran it is a live process of this user under a name not on Brigade's list | binds it by its PID and start token, as it binds a known one |
| a session start whose source is neither `new` nor `load` | starts a receiver as `new` does |
| two spellings of one field of the hook's document that disagree | keeps Grok Build's own camelCase spelling |
| a document that names another event than the hook it ran as | goes by the hook's own event, which is on its argv |
| a session id that is no UUID | uses it; it is still held to a safe path component, and the session's name and model, which are read from Grok Build's own files by a UUID, stay `grok` and blank |
| a document that names no session id | uses the environment's |
| a session id in the document that differs from `GROK_SESSION_ID` | uses the environment's, which the model's shell and its monitor carry |
| a workspace that is missing, does not resolve, differs from `GROK_WORKSPACE_ROOT` or does not contain the working directory | binds the working directory, which is all it ever bound |

For each, the hook writes a warning to the log, and the start of a session that registered shows the user one
line that names what was found and the option. A receiver's liveness is its process by PID and start token; the
name is no part of it. A start that Brigade refuses shows the user one line with the reason, where it used to
show nothing.

The skills of Grok Build's package say what the other three packages' say about a doing line, a gateway send
and an update. `brigade whoami` prints `strict: on` for a session registered under the option, and the skills
hold back as before where it does.

Kept as refusals, whatever the option says:

- leader mode, for its measured reason;
- the reading of a message in pages of 1,800 bytes, one tool call each, for its measured reason;
- what leaves nothing to bind: a document that is no JSON, a working directory that is not absolute or does not
  resolve, a native id that is unset or no safe path component, a parent that is not a live process of this
  user, and a parent that is a shell, which ends with the hook (the package's hooks `exec` so that their parent
  is the TUI);
- a subagent's hook, and another harness's marker in the environment. Neither has a measurement, each has a
  reason, and the owner's answer on each is asked on work item 377. Until it is there they are as they were.

## Alternatives considered

- **One option for each check.** Lost by the owner's ruling of 2026-11-22: one thing to document and to test,
  and a team that wants the strict Grok Build path gets all of it.
- **No option.** Lost by the same ruling: a member who wants the path as it shipped can have it.
- **The option in the team's file**, so that a team sets it for every member. Lost: the checks run in the
  member's own hook before a team is known, a project's file sets no option of a member's, and the other
  options that tighten a session (`team_inbound`, `frame`) are the member's too. A team that wants it tells its
  Grok Build members to set it.
- **Keep the name in a receiver's liveness.** Lost: a session registered under an unknown name would read as
  dead at once, and a start token already is one process's.
- **Refuse an unknown name only on a platform where the names were measured.** Lost: the one failure on record
  was on that platform.
- **Open the subagent check and the other harness's marker with the rest.** Not decided here. Opened, a
  subagent's end would retire the main session's receiver, and a session whose environment carries another
  harness's marker would register and then send as that other session, because `brigade` in its shell reads the
  host from the environment with Grok Build's marker last.

## Evidence

- The finding per check on work item 377, from `.context/plans/grok-build-participation.md`,
  `docs/experiments/grok-participation.md`, `docs/experiments/grok-participation-contract.md` and Grok Build
  1.0.92's own hooks guide.
- `TestGrokOpensWhatNoMeasurementBacks`: each opened check registers the session by default, with the line for
  the user and the warning in the log, and is refused with `grok_strict` set to `on` or to a value that is
  neither word. `TestGrokRefusesUnboundInputs`: what is kept is refused under both. `TestWhoamiSaysGrokStrict`.
- Not yet shown: a live Grok Build session on this build. It waits on the owner's word and is recorded on the
  work item when it is run.

## Consequences

- A Grok Build update that renames its binary, adds a start source or changes a spelling no longer stops
  registration. The member reads one line at the session's start, and the log has each fact.
- A hook that Grok Build some day runs under a wrapper process that is no shell would bind that process, which
  ends before the session does, and the receiver would be retired with it. Under `grok_strict` it is refused as
  before. The line at the start is what tells the member. Under a shell it is refused either way, with its
  reason shown.
- Section 17's sentence that called native acceptance "coordinator gates" now says what the experiment report
  measured and what it did not. That corrects a statement of fact and changes nothing the section argues.
- Section 17 of `docs/security.md` says which checks fail closed and which are opened, and names the option.
- A new check of the Grok Build path says, where it is written, what measurement it rests on, or goes through
  `grokOpen`.
- Revisit when the owner answers on the two kept checks, and when a measurement of an opened one shows a
  refusal is needed.

## References

- Work item 377 on the Brigade Board, with the owner's rulings of 2026-11-22 as comments; work item 131, the
  rename; work item 383, which brought the package's skills level.
- `internal/harness/hook/grok.go` (`grokOpen`, `decodeGrok`, `grokProcess`), `internal/procutil/grok.go`,
  `internal/harness/sessionmap/grok.go` (`GrokLiveness`), `internal/harness/config/options.go`
  (`ParseGrokStrict`).
- `docs/security.md` section 17; `docs/team-member/configure/grok.md`, where the option is described.
