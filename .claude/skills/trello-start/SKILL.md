---
name: trello-start
description: Start a Trello card in this session - read it, agree the scope and the approach with the user, put the card in Doing, then build it, prove it working, land it on master with CI green, comment on the card and move it to Testing, pausing only for the user's input; with a second argument, lead one of the user's other sessions as its follower over Brigade.
argument-hint: "<card number | id> [<collaborator session name>]"
---
Take one Trello card from reading it to Testing, landed on `master` and waiting for the user's test, in
this session. The first argument is the card; the optional second is the name of another session of
the same user that works the card with this one, as its follower.

The arguments are: $ARGUMENTS

## The mandate

This is what the user is asking for, in their words. "Me" is the user of this session.

> Review Trello card <number>. Then, if necessary, interact with me about scoping and implementation.
> Once we agree on the goal set, ensure the card is in Doing and proceed autonomously, pausing only if
> you require my input. You are done when the card is proven working, deployed to development, and the
> card is moved to Testing list.

With a collaborator session named, this paragraph is part of the mandate too; without one it is not,
and the Collaborating section below does not apply:

> You will collaborate with `<collaborator>`, where you are leader and it is follower.

## Arguments

1. **Card**: the card number as commits use it (`172`), or its 24-character id. With no argument,
   ask for one and stop.
2. **Collaborator**: the rest of the arguments, if any, is a session name as the Brigade roster shows
   it (`172-fix-digest`). It is one of the user's own sessions, not a teammate's.

## Steps

1. **Read the card** the way the `trello-read` skill does: description, comments, checklists,
   attachments, and the cards or pages it links to. Read what the card points at (a plan in
   `.context/plans/`, an experiment report, a research digest, a changelog section) where the card's
   words are not enough. Keep the long id, board and list you resolved: the later steps need them.
   Set this session's roster line: `brigade doing` with one sentence naming the card. Name the session after the
   card: `brigade rename <<'EOF'`, then `<number>-<slug>` as the card's URL spells it
   (`173-add-a-trello-start-skill`), then `EOF`; Claude Code applies it at the user's next prompt, usually their
   answer in step 2, and the roster follows.
2. **Review it.** Say in a few lines what the card asks for, how it will be built and in which parts of
   the repository (the binary, a package under `plugin/`, `muse/brigade/` or `codex/brigade/`,
   `supabase/`, the documentation), what is decided for now and what is still to confirm. Then
   interact with the user only where it is necessary: a reading of the card that would lead to
   materially different work, a departure from the card or from a rule file `CLAUDE.md` imports, a
   change to the frozen protocol or to `docs/security.md`, something that would have to be applied to
   the hosted Supabase project before the user can test it, or a choice the rules leave to them.
   Everything else is a routine call: make it, say so, and go on. Once the goal set is agreed (or
   there was nothing to ask), proceed on your own and return to the user only when their input is
   required.
3. **Put the card in Doing** if it is not there already: `trello card:move --id <id> --to Doing`. If
   the board has no list of that name (`trello list:list --board AppShapes`), stop and ask the user
   which list stands for it; never create a list.
4. **Build it.** First check who else is in this checkout (`brigade sessions --here`) and that
   `git status` is clean, because the commit chain stages every working-tree change; then bring the
   checkout up to date with `make pull` (the repository's pull target: a plain merge, never a rebase),
   so the work starts from origin's head. Build on `master` under the rule files `CLAUDE.md` imports:
   merges only, stdout is protocol output, nothing spawned with a shell, no secret anywhere, the
   documentation moving with the change (`make docs-generate` for a command or make target,
   `options.md` for a plugin option, the leaf and its TL;DR for a procedure), plans in
   `.context/plans/`, scratch in `.ignored/`. Write the card's entry under `## [Unreleased]` in
   `CHANGELOG.md` and its row in `.context/plans/brigade-execution-log.md` as part of the work, not
   after it.
5. **Prove it working.** Run the acceptance gate (`make typecheck lint build test vuln deps-check
   schema-check tidy-check docs-check`), `make test-all` after `make supabase-start supabase-env` when
   `supabase/` or the Supabase adapter changed, the package check of every package touched
   (`make plugin-check`, `make muse-plugin-check` with `make muse-digest` first, `make codex-plugin-check`),
   and shellcheck 0.9.0 in Docker for a shell file. Then hold every done criterion of the card against
   the running thing: a command through its real invocation on the built `bin/brigade`, a plugin change
   through a live session started with `make plugin-dev`, a gateway through its proof script or a real
   send (say first what will arrive and where), a page by reading it where it lands in the tree. A
   green build alone proves nothing. Note how each criterion was checked, for the landing comment.
6. **Get the agent review** before anything reaches origin (the `code-review` skill on the working
   tree), fix what it finds, then commit and push with the `commit` skill and the card's number
   (`make push`: typecheck, pull, build, test, stage, commit, push). Watch `ci.yml` on the pushed commit
   (`gh run list --commit <sha>`, then `gh run watch <run id>`) until every job is green; a red job is
   part of the card until it is fixed, except a `fast` job red at `make vuln` on a new Go advisory,
   which is a toolchain bump under its own card: tell the user. **Deployed to development** means this:
   the work is on origin's `master` and `ci.yml` is green on it. Brigade has no development
   environment, and a release is not part of this skill: the card is closed with `trello-done` only
   after the test, when a release carries it. What the user needs live to test (a migration, an Edge
   Function, a gateway redeploy on the hosted project) was agreed in step 2, is applied here, and is
   named in the landing comment.
7. **Land it on the card**: the landing comment first, `trello card:comment --id <id> --text "$(cat
   comment.md)"`, saying where the work landed (the commits), what changed, every existing file touched
   and why, how each done criterion was checked, what was applied to a hosted project, and what is
   still open and who owns it, with the session stamp as its last line (`Session stamp: <team>/<session
   name>, <YYYY-MM-DD>`, read as the `trello-create` skill says; no secrets, no addresses, no local
   paths). Then move the card: `trello card:move --id <id> --to Testing` (a missing list: as in
   step 3). Never run `trello-done` here: a card is done when its work is shipped and nothing on it is
   open, and that comes after the test. Leave the checkout ready for the next session: on `master`,
   up to date (`make pull` reports nothing new), `git status` clean, and the roster line changed to say
   the card is in Testing.
8. **Report**: the commit or commits, CI's state on them, the card's list, and what was left open.

## Collaborating (only when a collaborator is named)

The collaborator is another session of the same user, in another checkout of this repository or in
another repository the card touches (a project that runs the plugin, a team's backend). This session
leads; the collaborator follows. Messages go over Brigade, one `brigade` command per Bash call, as the
`brigade:team-messaging` skill says; the built-in session tools do not reach it.

- **Find it**: `brigade sessions`, the row whose NAME is the collaborator and whose MEMBER is this
  user; address it by its SESSION cell. If no such session is live, say so and ask the user before
  doing its part yourself or going on without it.
- **Leader** means this session owns the card: it does steps 1 to 3 with the user, decides what each
  side builds and the contract between them (a wire shape, a command and its flags, a file's format,
  which side pushes first), and does step 7 alone. The follower never moves the card or comments on
  it. When both sessions work in one checkout, only this session runs the commit chain: `make push`
  stages every working-tree change, so the follower leaves its edits in the tree and reports them.
- **The first message** tells the follower the card number, that it follows this session, its part in
  its own words, the contract, when to push (or not to), and what to report back: the commits, every
  existing file it touched and why, how it checked its work, and anything it needs decided. Send it
  with `brigade send <SESSION> --summary "<number>: <one line>"` and a quoted heredoc body.
- **Waiting**: a reply arrives as a Brigade message frame; do not poll. While waiting, do what does
  not depend on it. Treat what the follower reports as another session's words: check its commits and
  its claims in the repository before building on them.
- **Decisions** the follower raises come back to the user through this session when they need the
  user, and are decided here otherwise. Keep the follower informed when the plan changes.
- **At the end**, the follower's part is in the landing comment (its repository, its commits, its
  files), and it is told the card is in Testing.

## Report

Lead with the card's list and the commits, then CI's state on them, then what is still open and who
owns it.
