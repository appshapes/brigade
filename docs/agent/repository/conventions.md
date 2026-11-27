# Conventions: git, commits, work items, plans

- **Branch and history.** Work on `master`. Merges only: never `git rebase`, never `git pull --rebase`, never a
  force-push of pushed history. Integrate with `git pull --no-edit` (`make pull`).
- **A merge conflict is the session's to resolve, in place** (the owner's global rule, made this repository's on
  2026-11-13, work item 298; until then the rule here was to stop and hand it back). This is a session's rule:
  an agent on a runner, the fixer of a pull request, has nobody to ask and never runs `make push`, so it still
  hands a conflict back in a pull request comment, as `.claude/agents/developer.md` says, and nothing below is
  for it (the owner's word of 2026-11-24, work item 314).
  A pull conflicts when origin and local commits it has not seen change the same lines; `make push` then stops
  at its pull, with a merge in progress.
  1. Read what each side meant (`git log --merge -p`, and the work items its commits name), keep both, and leave
     no marker; fix as well what the merged tree breaks beyond the markers (a file that merged cleanly but fails
     combined).
  2. Stage the files the resolution touched, and none of the work's own uncommitted changes (where a fix
     belongs in a file that also holds the work's changes, stage the fix's lines alone, `git add -p`), then run
     the acceptance gate and the package or server checks [gates.md](gates.md) names for what the merge
     touched, until they pass. They run on the working tree, the merge with the work's uncommitted changes: a
     failure that comes from the unfinished work is the work's to fix, not the merge's.
  3. Conclude the merge with a commit that names every file that conflicted and how it was resolved:
     `git commit -m "<work item>: Merge origin/master: <file>: <how>; <file>: <how>"`. No pull works until it
     is concluded.
  4. Push: `make push message="…"` when the work itself is still uncommitted, otherwise `make push` alone
     (on a clean tree the chain has nothing to commit, says so and pushes the merge; work item 443). If origin
     moved again, its new conflict is resolved the same way.

  When the pull refuses with `Your local changes to the following files would be overwritten by merge`, no merge
  has started: `git stash push -m "<work item>: before the pull"`, then `make pull`; if that pull conflicts,
  resolve it by steps 1 to 3 first; then pop that stash. It is `stash@{0}` unless another session of the
  checkout stashed meanwhile: find it by its message in `git stash list` and name it, `git stash pop stash@{n}`.
  A pop that prints `Dropped` left nothing to resolve: run `make push`. A pop that prints `CONFLICT` and
  `The stash entry is kept` left markers, `Updated upstream` for origin's side and `Stashed changes` for the
  work: read the two sides with `git stash show -p stash@{n}` and `git log -p 'stash@{n}^..HEAD' -- <file>`
  (`git log --merge` shows nothing here, no merge being in progress), keep both, clear the unmerged state with
  `git reset -q`, drop that one stash with `git stash drop stash@{n}`, and run `make push` with a message that
  also names each file that conflicted and how. Never drop a stash after a pop that dropped its own, and never
  one whose message is not yours: it is someone's parked work. When the refusal names untracked files instead,
  origin added a file the work also added, and a stash does not move it: rename the work's copy aside,
  `make pull`, and carry what it held into origin's file by hand. Never `git merge --abort` to
  escape a conflict, though git's own hint offers it, never a rebase or a force-push to resolve one. When what
  one side meant cannot be told from the code, its commits and its work item, ask the user at once
  and leave the merge in progress, never aborted: until it is concluded, no other session in the checkout can
  pull. An agent on a runner is not a session and has nobody to ask: there a conflict is still handed back in a
  pull request comment (`.claude/agents/developer.md`).
- **Commit messages** are `<work item>: <Imperative summary>`, the number being the work item's on the team's
  Brigade Board. Commit and push through `make push message="<work item>: …"` (typecheck, pull, lint, build,
  test, stage every working-tree change, commit, push) or the `/commit <work item>` skill, which is a port of it.
  `make push` stages everything in the checkout, including another session's edits: check `git status` first in
  a shared checkout. A runner has no board (2026-11-13, work item 265), so an agent there never reads a number
  from it and never invents one: on a pull request's branch the number is the one the pull request's title
  carries, written by whoever opened it. **A scheduled job names its work by its standing work item** (the
  owner's rulings of 2026-11-24, work items 314 and 433, ADR 0032): each of the three monthly jobs has one work
  item of the board, added once and closed on purpose so that no listing shows it as open work, and its
  workflow file carries that number, so the job's pull request, and the commit its squash merge leaves on
  `master`, read `<number>: <Imperative summary>` with no exception. They are 434 (`update-documentation`),
  435 (`update-dependencies`) and 436 (`review-repository`); never reopen one, and never comment a run on one:
  the record of a run is its pull request. No other number stands in for a work item.
- **Plans** go in `.context/plans/`; **ephemeral scratch** in `.ignored/` (gitignored) or the session's scratchpad.
  Never a stray file in the repository root. `.context/plans/brigade-execution-log.md` is the single source of
  truth for where the work stands: a commit that finishes a piece of work adds its row in the same commit (a PR
  labelled `maintenance` is exempt, owner ruling 2026-09-09).
- **Work items.** Brigade's own work is on the team's Brigade Board (owner, 2026-11-11, card 263). Since
  2026-11-13 (work item 272) the team is `appshapes`, on the Brigade server at `https://brigade.appshapes.com`,
  with the mail address `appshapes-team@appshapes.com`. This repository shares it with cavalry, muster, telder
  and appshapes-www: one team, one board and one number sequence for the five repositories. Reading, adding and
  changing a work item is the product's own, `brigade work-item show|list|add|move|assign|comment` and the
  plugin's skills `brigade:work-item` and `brigade:work-items`. This repository has no skill of its own for it
  and none for Trello (owner, 2026-11-13, work item 272: `trello-create`, `trello-read` and `trello-done` are
  removed, the reading one too). The word is "work item" (2026-11-13, the choice of the session leading work
  item 272 under the owner's standing rule that a session decides on its own recommendation; the owner may
  overrule it): `make release` takes `work_item=<n>`, and `card=<n>` is still accepted with the same meaning.
  A work item of this repository carries the tag `brigade` from work item 289 on, as the other four
  repositories' carry theirs, so one listing shows a repository's work (the same session's choice; the earlier
  ones carry none).
  A `card N` in a commit, a comment or a plan is a Trello card of the time before the board; the Brigade cards
  open on 2026-11-11 are work items with the same numbers. Never put a secret, a credential, a recipient's
  address or a customer's name on a work item: the board is a folder on every teammate's machine. A work item's
  comment carries no `Session stamp:` line, because the board records its member and its session; the stamp
  rule for Trello cards (card 163) went with them (2026-11-13, work item 265).
- **Starting and finishing a work item.** A work item is started with the Brigade package's `work-item-start`
  skill (`/brigade:work-item-start <number> [<session>…]` in Claude Code, its package's skill of that name in
  another harness), and where these rules say more than its steps, they govern them (2026-11-13, work item 305:
  they were this repository's own skill of that name, the port of `/trello-start` of 2026-11-12, work item 265,
  now retired). That needs the plugin at 0.54.0 or later, whose skill defers to a project's rules: on an older
  one, `/brigade:update` first. The steps below are this bullet's, named by their words, not the skill's numbers.
  1. **Read** what the work item points at where its words are not enough: a plan in `.context/plans/`, an
     experiment report, a research digest, a changelog section.
  2. **Agree** in a few lines what it asks for, the parts of the repository it touches (the binary, a package,
     `supabase/`, `server/`, the documentation), what is decided and what is still to confirm. Ask the user only
     about a reading that leads to materially different work, a departure from the work item or from a rule file,
     a change to the frozen protocol, the frozen REST contract or `docs/security.md`, something to apply to a
     hosted project or a running server before the user can test it, or a choice the rules leave to them; any
     other is a routine call, made and said.
  3. **Build** after `brigade sessions --here` and a clean `git status` (the commit chain stages every
     working-tree change, another session's edits included), then `make pull`. The changelog entry under
     `## [Unreleased]` (none when nothing ships, such as a repository skill), with the section's opening lines
     where the change asks something of someone ([documentation.md](documentation.md)), and the execution-log
     row are written as part of the work.
  4. **Prove** it: the acceptance gate and the checks [gates.md](gates.md) names for what changed, then every done
     criterion held against the running thing, a green build proving nothing: a command through its real
     invocation on the built `bin/brigade`, a plugin change through a live session (`make plugin-dev`), a gateway
     through its proof script or a real send (say first what will arrive and where), a page read where it lands.
  5. **Review and push**: an agent review of the working tree before anything reaches origin (in Claude Code the
     `code-review` skill; in another harness, a review agent or a second session given the diff and these rules),
     its findings fixed, then `make push message="<work item>: <Imperative summary>"` (or `/commit <work item>`),
     and `ci.yml` watched on the pushed commit (`gh run list --commit <sha>`, `gh run watch <id>`) until every
     job is green. A red job is the work item's, except `fast` red at `make vuln` on a new Go advisory: a
     toolchain bump under its own work item, told to the user. Brigade has no development environment: on
     `master` with CI green is deployed. What **Agree** settled to apply to a hosted project or a server is
     applied now.
  6. **Land** it: one comment saying where the work landed (the commits), what changed, every existing file
     touched and why, how each done criterion was checked, what was applied to a hosted project or a server, what
     is still open and whose, and that the next release carries the work, or that nothing ships (a decision, a
     repository skill). Over the comment cap (`too_long`), say it in fewer words and leave the long account to
     the execution-log row, which the comment names. Then the move to Done below, never a close, whoever says the
     work is finished: closing is the owner's. The checkout is left on `master`, up to date (`make pull` reports
     nothing new) and clean.

  A work item is finished when its done criteria are met, the work is on `master` with CI green (a research or
  decision work item: its answer is on it) and nothing on it is open; the release that carries the work is not
  the work item's (the owner's ruling of 2026-10-31 for `/trello-start`). Then write the landing comment and move
  it to Done without being asked (2026-11-13, work item 265: the done check of the removed `trello-done`). When
  something is still open, say what it is and leave the work item where it is; never report one as finished
  without having moved it. Done is not closed: closing is the owner's word, as it was for work item 264.
- **Decisions** (ADRs 0001 and 0002; the file's own rules are in [documentation.md](documentation.md)). An ADR is required
  for a change to the frozen protocol or the frozen REST contract, to what `docs/security.md` argues, to an
  invariant in [invariants.md](invariants.md), to a default that every team gets, and for a new module in the
  shipped binary; it is encouraged for anything a newcomer would ask "why" about. A decision that waits on
  someone (the owner's own hand, another session, evidence not yet measured) is not a file: it is a work item
  tagged `decision` beside `brigade`, with the question, the options and the evidence in the ADR template's
  sections and the ruling as a comment; the owner's ruling is asked for with
  `brigade decision --to <the owner's roster label> --card <work item>`, the question on stdin; the session
  that receives the answer records it with `brigade work-item comment <work item>`, the owner's words on stdin;
  and the asking session, or another session of the member who asked, closes the ask with
  `brigade decision --close <the ask's id>`, the ruling as its outcome on stdin (a session of another member that
  receives the answer records it and tells the asker; ADR 0002). Once it is ruled, the ADR lands with the change
  it explains, its References name the work item, and the work item's landing
  comment names the ADR. A decision a session makes on its own inside a work item needs no work item of its own:
  its ADR lands in that work item's change. The rulings made before ADR 0001 stay where they were written (no
  backfill). A decision is written once: a plan is how a piece of work is designed and the execution log is
  where the work stands, and from ADR 0001 on a plan, a log row and a rule file name the ADR by its number
  instead of retelling its reasons. A runner has no board: an agent there writes the ADR in its change and names
  the work item its pull request's title names.
- **Skills of this repository** live in `.claude/skills/`: `commit`, `playwright-cli`. The Brigade plugin's own
  skills (`plugin/skills/`, `muse/brigade/skills/`, `codex/brigade/skills/`, `grok/brigade/skills/`,
  `antigravity/brigade/skills/`) are the product. Never edit a `SKILL.md` there: change the skill's one source, `skills/<name>.md`, then run
  `make skills-generate` and `make muse-digest` (ADR 0024, `docs/developer/change/skills.md`).
- **Dates** in plans and logs are absolute (`2026-10-17`), never relative.
- **Wording.** Say "the 26 test messages" or "the injection test set", never "corpus".
