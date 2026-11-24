# 0031. Name the command by the package's path in the lines a session reads on a host with no brigade on PATH

- **Status:** Accepted
- **Date:** 2026-11-24
- **Supersedes:** none

## Context

The lines Brigade's hooks print to a model were written for Claude Code, where the plugin's `bin/` is on the Bash
tool's PATH. There the bare `brigade` is the only form to name: Claude Code's permission rules match a command's
text, and a line that named the binary's full path led the model to send through a spelling that neither the ask
rule nor the deny rule sees (finding F1, ruled 2026-09-04; `docs/security.md` section 5.1).

Muse Code, Codex CLI and Grok Build put no package's `bin/` on a session's PATH. A line that names the bare word
there asks for a command that does not exist: a Muse Code session ran `brigade doing` as its reminder said and
got exit status 127 (the live session of 2026-11-22, work item 381). Work item 249 gave the lines that ask for a
command the package's path on those hosts, work item 425 the skills, and work item 359 the lines about a team
file that does not match. The start line, which section 5.1 quotes, the line of a registration that failed, the
sync line of a team with a board and the watcher's stop notices still named the bare word (work item 432).

## Decision

On a host whose row in the host table says a session is found through a pointer (`Host.Pointed`: Muse Code, Codex
CLI and Grok Build), a line that names a command for the session to run names it by the package's own path, the
start line, the line of a failed registration, the board's sync line and the watcher's stop notices included. On
Claude Code every such line is what it was, byte for byte, and names only the bare `brigade`. The path replaces
the command in Brigade's own words alone, never in a name, a reason or a notice that someone else wrote.
`docs/security.md` section 5.1 says so of the start line in one sentence, which the owner approved.

## Alternatives considered

- **Keep the start line bare on the three hosts**, so that section 5.1 stays true as written. It lost: the line
  keeps naming a command those sessions do not have, which is the fault.
- **Leave the page as it is**, because section 5.1 is about Claude Code, whose line does not change. It lost on
  the owner's answer: the page would say of "that line" what the code contradicts on three harnesses.
- **Put the package's `bin/` on the session's PATH on those hosts.** Not open to Brigade: none of the three gives
  a package a way to do it, which is why work item 249 named the path.

## Evidence

- Live, Muse Code 1.4.25, 2026-11-23, a throwaway home with the checkout's package: the model quoted its start
  line with the package's path, ran the `sessions` command it names and got the roster with exit status 0.
- `TestAStartsLinesNameACommandTheSessionCanRun`, `TestTheBoardsSyncLineNamesACommandTheSessionCanRun` and
  `TestAStoppedWatchersNoticeNamesACommandTheSessionCanRun` hold the four kinds of line on Claude Code, Muse Code
  and Codex CLI; the subtests of the two other hosts fail against the code as it was. Grok Build has no test of
  its own start: it is held by its row in the host table and by the test of `commandIn`.
- Whether one of the three hosts has a permission rule that matches a command's text was not measured. The
  line next to the start line (Muse Code's arming line, Codex CLI's line) has named the same path to the model
  since cards 88 and 107.

## Consequences

- A line added to the hooks that asks a session for a command goes through `commandIn`, or takes the command's
  name as the arming line does, or it names a command three harnesses cannot run. The lines that still name the
  bare word there, and why, are in the execution log's row for work item 432.
- Two starts still say the bare word on the three hosts: the ones that fail before the hook has built the
  session's facts (hook input it cannot read; a session with no parent process to key on). A path with a space
  in it is named unquoted. Both are left as they are.
- The watcher writes its notices with the bare word and the prompt hook names the command when it prints them, so
  a notice the watcher adds that names a command needs nothing more, and a notice of another topic is never
  rewritten.
- Section 5.1's measurement (bare replies on Claude Code) says nothing of the three other hosts, and this
  decision does not claim it does.
- A host that gains text-matching permission rules, or a way to put a package's `bin/` on PATH, is a reason to
  read this again.

## References

- Work item 432, and the owner's answer of 2026-11-24 (UTC) recorded as a comment on it ("Add one sentence").
- Work items 249, 381, 425 and 359; `commandIn` and `commandName` in `internal/harness/hook/hook.go`.
- `docs/security.md` section 5.1; ADR 0019 (the host table).
