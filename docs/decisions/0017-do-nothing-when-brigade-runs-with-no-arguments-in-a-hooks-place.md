# 0017. Do nothing when brigade runs with no arguments in a hook's place

- **Status:** Accepted
- **Date:** 2026-11-22
- **Supersedes:** none

## Context

The Claude Code package's four hooks are in the exec form: the binary in `command`, its words in `args`, as
`"command": "${CLAUDE_PLUGIN_ROOT}/bin/brigade", "args": ["hook", "prompt"]`. That is on purpose. Brigade never
has a shell run its hooks (plan 6.3, the invariant "never spawn with a shell"), and check 5 of
`scripts/ci/plugin-check.sh` refuses a hook whose command is a string with arguments in it.

GitHub issue 59, an outside report of 2026-10-05, says that Cursor loads Claude Code's plugins and runs a hook's
`command` without its `args`. So it runs `brigade` with no arguments, on every hook. A `brigade` with no command
is a usage error: one line on stderr, `no command given`, and exit status 2. Exit status 2 is the status a hook
blocks with, so the report's host blocked every prompt. The reporter first proposed the string form, withdrew it
after reading why the exec form is kept, and proposed that a bare `brigade` in a hook's context does nothing.

It reproduced on 2026-11-22 in Claude Code itself, 2.1.330, given a copy of the plugin whose hooks had lost their
`args`: with the released 0.57.0 the one prompt was answered `UserPromptSubmit operation blocked by hook`, and no
turn ran.

The owner was asked on 2026-11-22 what should happen about the issue, with three options: make the change,
answer only, or make the change and leave the answer to him. He chose to make the change: where a hook's markers
are present a bare `brigade` does nothing and exits 0, and a bare `brigade` in a terminal keeps its usage error.
What counts as a hook's markers was left to the session that did the work.

## Decision

A `brigade` started with no arguments at all, which carries a mark of a hook, does nothing, writes nothing to
stdout or stderr, and exits 0. With no mark it answers as before: the usage error, exit status 2. A call with any
argument is not touched, a flag alone included.

The marks are two, and either is enough:

1. `CLAUDE_PLUGIN_ROOT` is in the environment and not empty. Claude Code gives it to a plugin's hook.
2. Stdin begins with a JSON object that has, at its top level, a `hook_event_name` that is a text and not empty.
   It is told from the document's start, member by member, and what follows the event is not read. So a document
   that `brigade hook prompt` would refuse and exit 0 on, one that is too long or holds text that is no UTF-8, is
   a hook's all the same. Stdin is not read when it is a terminal, and a stdin that nobody writes to is waited on
   for three seconds, under the five that the shortest of the hooks is given.

`hook.BareCall` holds the rule, and `app.Run` asks it before the command table, where the binary already takes a
hook aside because a hook must never fail its session.

The hooks stay in the exec form.

## Alternatives considered

- **The string form of the hook's command**, the report's first proposal. Lost: it has a shell run the hook, which
  is what the exec form is there to prevent, and the reporter withdrew it for that reason.
- **Exit 0 for every `brigade` with no arguments.** Lost: a person at a terminal and a model in a session's shell
  tool made a slip and need the usage line and its status; the owner's ruling keeps it for them.
- **One mark, not two.** Lost: the report shows that its host found the binary through `${CLAUDE_PLUGIN_ROOT}`,
  and does not show whether the variable was in the hook's environment or put into the path, nor what the hook
  was given on stdin. A host may hand over one and not the other, and each mark is a few lines.
- **Only a root that holds Brigade's own launcher as the first mark.** Lost: Brigade allows by default (the
  owner's words of 2026-11-21), and what it would catch is a slip in another plugin's hook, which the
  Consequences name.
- **The other packages' plugin roots as marks** (`MUSE_PLUGIN_ROOT`, `GROK_PLUGIN_ROOT`). Lost: Muse Code's hooks
  are wrapper scripts and Codex CLI's and Grok Build's are one string each, so no host can run them without
  their words.
- **A line on stderr saying that Brigade is inactive.** Lost: the ruling's word is that the call does nothing,
  what a host does with the stderr of a hook that succeeded is not measured for the report's host, and a line
  on every prompt is the report's complaint in a smaller size.
- **The check in the launcher**, `bin/brigade`, so that a bare call on a cold cache downloads nothing. Lost: the
  launcher is a shell script copied into four packages and a Windows program beside it, a shell script cannot
  tell a hook's document, and the launcher's own failures exit 9 or 11, never 2.

## Evidence

- Tests: `TestABareCallCarriesAHooksMarks` and `TestABareCallWithNoMarkIsNoHooks` hold the two marks, each alone,
  documents that the prompt hook would refuse, and fifteen calls that carry neither;
  `TestABareCallInAHooksPlaceDoesNothing` and
  `TestABareCallElsewhereKeepsItsUsageError` hold the exit status and both streams through `app.Run`; the script
  `bare.txtar` holds them through the real binary. With the check switched off, the script and the test fail.
- The built binary, on 2026-11-22, in a throwaway home: exit 0 and nothing written with the root in the
  environment, with a hook's document on stdin and no root, and with a document whose pipe stayed open; exit 2
  and the usage line with neither mark, with a session's variables and no root, with a JSON object that is no
  hook's, at a terminal (a pty), and after three seconds on a pipe nobody wrote to.
- Through the plugin's launcher as a host that drops `args` runs it, with a hook's document on stdin: the
  released 0.57.0 exits 2 with the report's line; this change exits 0 and writes nothing.
- Claude Code 2.1.330, headless, with a copy of the plugin whose hooks have no `args`: on 0.57.0 the prompt was
  blocked, as above; with this change the session-start hook ended with status 0 and nothing on either stream,
  and the prompt was answered.
- In a Claude Code session's own shell tool, on 2026-11-22: the environment has `CLAUDE_PID` and no
  `CLAUDE_PLUGIN_ROOT`, stdin ends at once, and a bare `brigade` printed the usage line with status 2 in under a
  second.
- Not measured: the report's host. Nobody of this repository ran Cursor with the change, and which of the two
  marks it hands a hook is not known. Nor whether Claude Code gives `CLAUDE_PLUGIN_ROOT` to a plugin's processes
  other than its hooks.

## Consequences

- In a host that runs Claude Code's hooks without their `args`, Brigade is inactive and says nothing: no session
  is registered, no message arrives, and the host's prompts are not blocked. A member who wonders asks
  `brigade whoami`, which refuses with `not_in_session` where no hook registered a session.
- Every process that inherits a `CLAUDE_PLUGIN_ROOT` that is not empty gets silence and status 0 from a `brigade`
  with no arguments, whatever plugin the root is of: a person who exported it in a shell, and another plugin's
  hook that ran `brigade` by mistake. So does a person who pipes a hook's document into it. `brigade help` still
  prints the command list there.
- A `brigade` with no arguments whose stdin is open and never written to answers three seconds later than it
  did.
- On a cold cache the launcher still downloads the binary for a bare call, once per version.
- To maintain: `hook.BareCall`. A fifth package whose hooks are in the exec form adds its own root to the marks.
- Revisit if Claude Code stops giving a hook its plugin's root or its document, or when a host's handling of
  these hooks is measured and wants something else.

## References

- Work item 402 on the Brigade Board, whose comment of 2026-11-22 records the owner's ruling.
- GitHub issue 59 of `appshapes/brigade`, the report and the reporter's second proposal.
- `internal/harness/hook/bare.go` (`BareCall`) and `internal/app/app.go` (`Run`).
- `plugin/hooks/hooks.json`, the hooks; `scripts/ci/plugin-check.sh`, check 5, which holds the exec form.
