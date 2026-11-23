# Change a skill

A skill is written once. `skills/<name>.md` at the repository's root is its source, and the four packages'
copies (`plugin/skills/<name>/SKILL.md`, and the same under `muse/brigade/`, `codex/brigade/` and `grok/brigade/`)
are written from it. Four copies ship because each harness loads its own. None is edited by hand
([ADR 0024](../../decisions/0024-write-each-skill-once-and-generate-the-four-copies.md)).

## The steps

```sh
$EDITOR skills/team-messaging.md
make skills-generate      # writes the copies that changed
make muse-digest          # the Muse catalog counts every file of muse/brigade/
make skills-check         # what CI runs: fails on a copy that is not what its source renders
```

`go run ./cmd/brigade-skills write --out <dir>` writes the copies into another directory, to read a change
before it touches a package. `make test` runs the same check as `make skills-check`. The check also fails on a
skill that a package carries and no source renders; the generator never deletes one, so a skill is removed by
removing its source and its four directories.

A `description` is at most 1,024 characters as a loader reads it, in every harness's rendering: the generator
refuses a longer one and so do the four package checks
([ADR 0025](../../decisions/0025-hold-a-skills-description-to-the-length-a-loader-accepts.md)).

## What a source is

The Claude Code package's text, as a Go `text/template`, with a mark wherever a harness differs:

| Mark | What it gives |
| --- | --- |
| `{{invoke "join"}}` | how the skill is invoked: `/brigade:join` in Claude Code and Grok Build, `$brigade:join` in Codex CLI, `brigade:join` in Muse Code |
| `{{argsOf "join"}}` | what the skill's arguments came after: "the command name, `/brigade:join`" in Claude Code, "the skill's name, `brigade:join`" in Muse Code, the invocation alone in the other two |
| `{{argsOfCommand "join"}}` | the same where Claude Code's text names no invocation: "the command name" in Claude Code and Muse Code |
| `{{say "claude" "Bash" "*" "shell"}}` | a few words that differ: pairs of the harnesses, by id and separated by spaces or `*` for the rest, and their words. Every harness must have words |
| `{{commandAt}}` | the sentence that says where `brigade` is, for a harness whose host does not put it on PATH: the package's own `bin/brigade`, found from the skill's own file, which it names. Never rendered for Claude Code: it stands inside a block that leaves `claude` out. `{{commandAt "   "}}` indents its later lines, inside a list item |
| `{{if in "muse" "codex"}}…{{end}}` | a block those harnesses alone have |
| `{{if notin "claude"}}…{{end}}` | a block every harness but those has |

The ids are `claude`, `muse`, `codex` and `grok`. A mark that names another, or a `say` with no words for a
harness, fails the generator.

A copy that has the model run a `brigade` command says where the command is, in the three packages other than
Claude Code's (work item 425): none of those hosts puts `brigade` on the PATH of the model's shell, and each
gives the model the skill's file by its path. Most say it with `commandAt`. Five say it in words of their own:
`setup` and `team-messaging` in all three, and `join` and `move` in Muse Code and Codex CLI, where the line of
the session's start that names the command reaches the model; in Grok Build that line reaches the user alone,
so there `join` and `move` carry `commandAt`. `TestASkillThatRunsBrigadeSaysWhereItIs` reads each copy, and
fails on one that runs a `brigade` command and says it in none of these ways.

The frontmatter is the source's, and a harness's copy carries the keys its loader takes: `name` and
`description` everywhere, `user-invocable` and `argument-hint` in Claude Code and Muse Code, `allowed-tools` in
Claude Code alone. The table is `Harnesses` in `internal/skillgen/skillgen.go`.

## Where a difference goes

- A fact of the harness, such as the tool's word, where an option is set, where `brigade` is found or how a
  message arrives: a mark or a block. If it is the same in every skill, a row or a function of the table.
- The same thing in other words: there is one wording. Write it once.
- Muse Code's copies name a skill in words ("the `join` skill", "this skill, given a path") where the others give
  the typed form: a Muse Code member asks for a skill, and does not type one. Keep that in an
  `{{if in "muse"}}…{{else}}…{{end}}` block.
- `update` is a block for each harness from its first line to its last, because what refreshes a package is the
  harness's own commands. `setup` and `team-messaging` have many blocks; where two or three harnesses share a
  block's text they share the block. Narrowing a block to a mark changes no copy, and `make skills-check` shows
  that it did not.

## What a change costs members

A changed copy is a changed package, and it reaches every member with the next release and not before: Grok
Build and Codex CLI take their package at the commit of the last published release, which the release
sequence's last step moves (work item 424,
[ADR 0030](../../decisions/0030-pin-the-codex-cli-and-grok-build-packages-to-the-last-release-in-their-marketplace-files.md)).
Codex CLI asks its members to review Brigade's hooks again once for that release, whatever changed in its
package, and a release's "Who must act" says so.
