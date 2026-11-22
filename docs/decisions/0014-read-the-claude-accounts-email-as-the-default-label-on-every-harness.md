# 0014. Read the Claude account's email as the default label on every harness

- **Status:** Accepted
- **Date:** 2026-11-22
- **Supersedes:** none

## Context

A member has a display label, and a member who names none gets a default: the email address of the Claude account
on the machine, read from Claude Code's own configuration file. It is sent by a `team join` or `team create` with
no `--label`, and by every session's registration, which a backend adopts only while the membership has no label.
The `label` option turns it off (`none`) or names a text.

Brigade then gained three more harnesses, Muse Code, Codex CLI and Grok Build, and with each the question of
whether its sessions read that account was answered where the code happened to be. The repository review of
2026-11-21 found the rule written five times under three conditions (its section 1, work item 379):

- every harness but Claude Code sends no account label: the session-start hook, and the author of a change on
  the Brigade Board;
- Grok Build alone sends none: the watcher's re-registration, and the list of files the account is read from;
- no condition at all: the default of `team join` and `team create`.

So on a machine with a Claude account, a Muse Code or Codex CLI member's `team join` sent the account's email,
their session start sent no label, and their watcher sent the email again when it registered the session anew:
what a team held for the member depended on which of the three had spoken last. The join pages of both said the
Claude account email is not read.

The owner was asked on 2026-11-22 which rule he wants, with three options: read it on every harness, read it
except in Grok Build, or not read it outside Claude Code.

## Decision

One rule for the four harnesses and for a terminal: while the `label` option is `account`, its default, the
member's default label is the email address of the Claude account on the machine, when there is one. `none` sends
no label and any other text is the label, on all of them alike.

One function holds it, `account.Label`, and every place that resolves a default label calls it: the session-start
hook's registration, the watcher's re-registration, `team join` and `team create`, and the author of a change on
the Brigade Board. It asks which harness it runs in nowhere.

The account is looked for in the same files everywhere: `.claude.json` under `CLAUDE_CONFIG_DIR` when the
session's environment carries that variable, else under Claude Code's default directory in the home directory,
and then `.claude.json` in the home directory itself. A Muse Code, Codex CLI or Grok Build session resolves
Brigade's own directories from `HOME` alone and ignores `XDG_*` and `BRIGADE_*`; `CLAUDE_CONFIG_DIR` is neither,
and such a session honours it where it inherited it from the shell it was started in. Nothing else of Claude
Code's is read for this: no settings, no session registry, and never a `sessions/*.key`.

On a machine with no Claude account the default sends no label, on every harness, as before.

## Alternatives considered

- **Not read outside Claude Code**, which is what the pages said and what the work item's own fix proposed. Lost:
  the owner chose otherwise. Brigade allows by default and is turned off by configuration (his words of
  2026-11-21), a roster of people reads better than one of references, and the member has one option that turns
  it off on every harness.
- **Read except in Grok Build**, the state of two of the five places. Lost: it keeps a rule with an exception,
  and the exception's reason was that no account of Grok's own had been measured, which says nothing of the
  Claude account on the same machine.
- **A harness's own account** as its default label (the address a Muse Code, Codex CLI or Grok Build install is
  signed in to). Lost for now: none of the three has a measured, documented place to read one from, and a
  harness's auth file is never read or copied (the repository's invariants).
- **One predicate, `reads the Claude account`, in a table of hosts**, with the five places asking it. Lost: under
  this decision it answers the same for every host, and a function that cannot answer otherwise is no rule. The
  rule is that there is no host to ask about; it lives in the one resolver.

## Evidence

- A probe test of 2026-11-21 in a scratch copy of `b4b97790`, with a Claude account file present: `defaultLabel`
  returned the account's email in a Codex CLI and in a Muse Code environment and nothing in Grok Build's, and the
  watcher's `humanLabel` did the same, while the hook's sent none in all three.
- With this decision, on 2026-11-22: `TestSessionStartRegistersTheAccountLabelOnEveryHost` holds the
  registration each harness's session start sends, `TestTeamJoinSendsTheAccountLabelOnEveryHost` the join's
  default and `TestTheWatcherSendsTheAccountLabelOnEveryHost` the watcher's, each for the four harnesses and
  each against the same account file; `TestTheRuleIsOneOnEveryHost` holds the files and the option. With a
  harness left out of the hook again, that harness's case fails.
- None measured in a live Muse Code, Codex CLI or Grok Build session by the day of this decision; the work
  item's record says what was run.

## Consequences

- A Muse Code, Codex CLI or Grok Build member on a machine that has a Claude account shares that account's email
  address with the team, and with whoever runs its backend, by default: at their next join, and at their next
  session start while the team holds no label for them. A label the team already holds is not touched. Setting
  `label` to `none`, or to a text, in the harness's options file before that turns it off.
- A Grok Build session reads one file of Claude Code's configuration, where it read none.
  `docs/security.md` said so in its section on Grok Build, and that sentence is corrected with this change.
- The join pages, the configure pages and the `setup` skills of the three harnesses say what the code does.
- Easier: one place to change the rule, and one answer to "what label does my session send".
- To maintain: `account.Label` and `account.Paths`. A harness added later reads the account without a line of
  its own.
- Revisit when a harness documents where its own account is kept, or if a team asks that a harness's sessions
  be labelled apart from the member's Claude Code ones.

## References

- Work item 379 on the Brigade Board, whose comment of 2026-11-22 records the owner's ruling; work item 380, the
  table of hosts, which this decision leaves nothing of the rule for.
- `internal/harness/account/account.go` (`Label`, `Paths`) and its callers in `internal/harness/hook`,
  `internal/harness/watch` and `internal/harness/commands`.
- `docs/team-member/join/your-label.md`, the member's page on the label; `docs/security.md`, "Where the default
  label comes from".
