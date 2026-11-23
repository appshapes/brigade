# One source per skill (work item 393)

Started 2026-11-23 by the session `393-write-each-skill-once-and-generate`. The work item is the proposal; nothing
in it is ruled by the owner. This file says how it is built and, after each part, where the work stands.

## What is being built

The fifteen skills are four hand-edited copies each. They become one source each, `skills/<name>.md` at the
repository's root, and a generator that writes the four packages' copies from it. Four copies still ship, because
each harness loads its own; none is edited by hand any more, and a gate fails on a copy that is stale.

- **The source** is the Claude Code package's text with marks where a harness differs. It is a Go
  `text/template`.
- **The table** is `skillgen.Harnesses` (`internal/skillgen/skillgen.go`): for each harness, how a skill is
  invoked there and which frontmatter keys its loader takes.
- **A mark** is one of a few functions, and nothing else:

  | Mark | What it gives |
  | --- | --- |
  | `{{invoke "join"}}` | `/brigade:join` in Claude Code and Grok Build, `$brigade:join` in Codex CLI, `brigade:join` in Muse Code |
  | `{{argsOf "join"}}` | what a skill's arguments came after: "the command name, `/brigade:join`" in Claude Code, "the skill's name, `brigade:join`" in Muse Code, the invocation alone in the other two |
  | `{{argsOfCommand "join"}}` | the same where Claude Code's text names no invocation: "the command name" in Claude Code and Muse Code |
  | `{{say "claude" "…" "*" "…"}}` | a few words that differ by harness; every harness must have words |
  | `{{if in "muse" "codex"}}…{{end}}`, `{{if notin "claude"}}…{{end}}` | a block one or more harnesses alone have |

- **The frontmatter** is the source's, with the keys a harness does not take left out: Claude Code takes
  `user-invocable`, `argument-hint` and `allowed-tools`; Muse Code the first two; Codex CLI and Grok Build none.
- **The command** is `go run ./cmd/brigade-skills write | check`. `write --out <dir>` writes into another
  directory, to look at.

## The rule for a difference between two copies

Each difference a source meets is one of three, and is handled as that and no other way:

1. **A fact of the harness** (how a skill is invoked, the tool's word, where an option is set, where `brigade`
   is found, how a message arrives): a row of the table, a mark, or a block for that harness.
2. **The same thing said in two wordings** (the copies were substituted by hand, and drifted apart in phrasing):
   the source's wording, which is Claude Code's with the table's forms, is the one that stays. The meaning does
   not change.
3. **Two copies that disagree, one of them wrong**: chosen, and listed below under "Choices".

A change of what a skill tells a model is not this work item's.

## Parts, and where the work stands

1. **The generator, the table, the first sources, their tests.** No package file changes. Done: 2f629a7a.
2. **The other seven sources.** No package file changes. Done: 1ce73167 (merge 969ca97a).
3. **The switch**, in one commit. Done in this part: `brigade-skills write` rewrote the copies (26 of the sixty
   changed, in 86 lines, each read; 34 came out byte for byte, Claude Code's fifteen among them); `make skills-generate` and `make skills-check`;
   the check in CI's `fast` job and, as `TestCopiesAreCurrent`, in `make test`; `make muse-digest`; ADR 0024;
   `docs/developer/change/skills.md`. The interim tests are gone: the check holds the copies now. This is the one
   change of the four packages.
   Done: 55a64a1f (merge 41ba249a).
4. **The rule files' sentence** on how a skill is changed: one sentence in the "Skills of this repository"
   bullet of `docs/agent/repository/conventions.md`. Work item 395 changed nothing in those files, so it did not
   have to be waited for. Done in this part.
5. **Each harness loads its skills once in a live session**: waits on the owner's word on live sessions. It is
   the one thing 393 is still in Doing for.

### The fifteen sources

| Skill | How its source was written | Its four copies |
| --- | --- | --- |
| `work-item`, `work-item-next`, `work-item-start`, `work-items` | Claude Code's text with the invocation marks | reproduced word for word |
| `update` | a block for each harness: the commands that refresh a package are the harness's own from the first line to the last | reproduced byte for byte |
| `sessions`, `whoami`, `join`, `email`, `slack` | Claude Code's text with the marks | Muse Code's keep their own wording wherever a skill is named or asked for ("the `join` skill", "this skill, given a path", "when they invoked this skill"): a Muse Code member asks for a skill in words, as `docs/team-member/join/muse-code.md` says, so that is a fact of the harness (rule 1), in a block. Codex CLI's and Grok Build's said the same as Claude Code's in other words, and take the source's wording (rule 2); the list is below |
| `subscribe`, `quiet`, `move` | Claude Code's text with the marks | where an option is set, the tool's word, where `brigade` is found and Claude Code's pre-approved commands are marked for each harness (rule 1); the rest as the row above |
| `setup`, `team-messaging` | paragraph by paragraph from the four copies: a paragraph, list item or code block that all four carry with the same words is written once; between two such, each harness keeps its own text in a block, and harnesses with the same text share the block | reproduced word for word but for the choices below |

`setup` writes 23 of its 44 units once and `team-messaging` 77 of its 105. Their blocks are where the next work is:
a block that says for three harnesses what a mark could say once is still three texts. That narrowing changes no
copy and can be done a block at a time, with `brigade-skills check` holding the copies still.

Until the switch, tests held every source to the hand-written copies: rendered for Claude Code it was the package's
copy byte for byte, and each copy no choice touches kept its words. Since the switch the copies are the sources'
renderings, and `make skills-check` holds that.

## One wording where there were two (rule 2)

What changed in a copy at the switch without a choice behind it. Each is the same thing in the source's words.

- **Codex CLI's and Grok Build's `email`, `slack`, `quiet` and `subscribe`**: "the `team-messaging` skill" reads
  "`brigade:team-messaging`", the name every package's `sessions`, `whoami` and work-item skills already use.
- **Codex CLI's and Grok Build's `sessions` and `whoami`**: "the `$brigade:sessions` skill runs" reads
  "`$brigade:sessions` runs", and "with `--all` it includes … with `--here` only … with `--member <who>` only"
  reads "`$brigade:sessions --all` includes … `$brigade:sessions --here` shows only … `$brigade:sessions
  --member <who>` only". Both harnesses take a typed skill with arguments (their join pages show it).
- **Codex CLI's and Grok Build's `join`**: "`$brigade:join` with no path" reads "`$brigade:join` alone", and
  "typed by them after" reads "typed by them through".

Nothing else differs but line breaks.

## Choices (rule 3)

Each changed a word of a copy at the switch.

- **Muse Code's `team-messaging`**: the sentence a search and replace garbled on 2026-10-17 ("the `email` skill
  ( <address> <text>` …") reads as Claude Code's does, with Muse Code's form: "`brigade:email <address> <text>`
  (the `brigade:email` skill) and `brigade:slack <to> <text>` (the `brigade:slack` skill) are the one-line forms".
- **Muse Code's and Codex CLI's `team-messaging`**: "and never the binary by its full path" is gone from the rule
  on evasive forms. Both harnesses run the bootstrap by its full path; the clause is Claude Code's, where the
  bare word is on PATH. Grok Build's copy never had it (work item 383).
- **Muse Code's `team-messaging`**: its frontmatter carried `allowed-tools`, which no other Muse Code skill
  does and the table does not give Muse Code. Left out.
- **Muse Code's `setup`**: step 1 of leaving said "(with `!` in a session)", and the same copy says higher up
  that a Muse Code session has no `!` prefix. The words are gone from Muse Code's copy.
- **Muse Code's `email` and `slack`**: four parentheses the same search and replace left open ("the `email`
  skill (`brigade:email <address> <text>` sends", "through the `email` skill (`brigade:email`, or") are closed.
- **Muse Code's `slack`**: "A teammate's Muse Code session is addressed with…" named one harness for a teammate
  who may use any; it reads "A teammate's session".
- **Muse Code's `sessions`**: a sentence began "with `--all` it includes", lower case after a full stop. It
  begins with a capital.
- **Codex CLI's and Grok Build's `slack`**: their description ended "; a person on email with `$brigade:email`",
  which Claude Code's and Muse Code's never had. Left out, as `email`'s own description says when it is used.
- **Grok Build's `subscribe` and `quiet`**: "the `label` option" and "the `team_inbound` option" named no options
  file where the other two packages' copies name theirs. Grok Build's now say it as theirs do.

**Seen and left as it is**, because changing it would change what a skill tells a model: the rule on a long
command in Muse Code's and Codex CLI's `team-messaging` still gives Claude Code's measured 10,000 characters and
its `-p` runs; the three non-Claude copies of `join` and `move` say that the `Brigade:` line of the session named
the command, which in Grok Build reaches the user and not the model; Codex CLI's `setup` says "(with `!` in a
session)" for leaving, which was not checked against Codex CLI.

**The review before the switch** (2026-11-23) found that the sources of parts 1 and 2 would have given Muse
Code's copies Claude Code's typed forms ("`brigade:join <path>` runs") and Codex CLI's and Grok Build's a doubled
invocation ("came after `$brigade:email`: typed by the user through `$brigade:email`"). Both were put right in
the sources before any copy was written. It also asked for two things the check now does: a skill in a package
with no source fails it, and a test ties the table to `docs/harnesses.json`.

## What it will cost members, at the switch

Three packages' skills changed in one commit; Claude Code's did not change by a byte. Codex CLI asks its members
to review Brigade's hooks again, which the changelog's opening lines already said for this release;
`marketplace.json` has a new digest; Grok Build and Codex CLI read the new text from the default branch at once,
before a release.
