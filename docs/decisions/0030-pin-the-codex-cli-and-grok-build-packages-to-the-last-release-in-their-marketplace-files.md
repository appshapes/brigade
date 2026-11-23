# 0030. Pin the Codex CLI and Grok Build packages to the last release in their marketplace files

- **Status:** Accepted
- **Date:** 2026-11-23
- **Supersedes:** none

## Context

Codex CLI and Grok Build are given this repository as a plugin marketplace, and each read its package from the
default branch: `.agents/plugins/marketplace.json` and `.grok-plugin/marketplace.json` named `./codex/brigade` and
`./grok/brigade`. So what a member had was not a release. It was what `master` held when their harness last
looked.

Three things followed, each seen. A push reached members before a release did: between 0.57.0 and 0.58.0, ten
commits changed `codex/brigade/` and eleven `grok/brigade/`. A skill named a command the package's pinned binary
did not have: from 2026-11-16 to 2026-11-23 the `work-items` skill of both packages offered `page` while
`bin/VERSION` pinned 0.57.0, which has no `brigade work-item page`. And in Codex CLI a changed package stops
Brigade: Codex fetches the marketplace by itself when its app server starts, its trust of a plugin's hooks is a
hash over every file of the installed package, and until the member reviews the hooks again none of them runs
(work items 381 and 393, measured 2026-11-22 and 2026-11-23).

Work item 424 asks that a package reach a member as a release has it, and leaves the way to the working session.

## Decision

Master's two marketplace files name the package at one commit, the commit of the last published release, and only
a release moves it.

- Codex CLI's entry is `{"source": "git-subdir", "url": "https://github.com/appshapes/brigade.git", "path":
  "codex/brigade", "sha": "<commit>"}`. Grok Build's is `{"source": "url", "url":
  "https://github.com/appshapes/brigade.git", "sha": "<commit>", "path": "grok/brigade"}`.
- The release sequence ends with a commit on `master` that sets both to the tagged commit, after the release is
  published. One command writes both files; nobody edits the commit by hand.
- The package checks hold the form of both entries, that they name the same commit, and that the commit is a
  release tag's and holds both packages.
- The pin is always a release's commit. The first is 0.59.0's.
- A release whose last step did not run leaves members on the release before. The package checks and the next
  `make release` say that the packages are behind, and the step can be run by itself.
- The install and update commands do not change. Nothing is asked of a Codex CLI member. A Grok Build member
  who installed before the pin removes the package and installs it again, once.

## Alternatives considered

- **A `release` branch that only a release moves, named in the marketplace entry.** Lost on a measurement: Codex
  reinstalls a plugin only when the marketplace's own revision moves. The branch would move after publication
  with `master` standing still, and the release would wait for the next unrelated push. It also needed a new
  rule on GitHub for who may move the branch.
- **The member pins** (`codex plugin marketplace add … --ref`, `grok plugin install …@<ref>#grok/brigade`). Both
  work. Lost: every existing member of both harnesses removes and installs again, and a new member who follows
  an old command is back on the default branch.
- **A tag's name in place of the commit.** Works in both harnesses. Lost: a commit cannot move, Grok Build's
  guide documents `sha` and its `require_sha` policy accepts nothing else, and one form serves both.
- **Change the two packages on `master` only at a release** (generate at release). Lost in ADR 0024 for the
  skills, and it would hold every other file of the packages to the same rule.
- **Leave Grok Build on the default branch**, because its existing members have to install again. Lost: a push
  would go on reaching them between releases, which is what the work item ends, and the cost is one command
  pair, once.

## Evidence

Measured 2026-11-23 with Codex CLI 0.166.0 and Grok Build 1.0.92 on macOS arm64, each in a throwaway home with no
sign-in, against a scratch copy of this repository served on localhost.

- Codex CLI, `git-subdir` with a `sha`: the plugin installs, and the cache is identical to the package at that
  commit. After a push that changes `codex/brigade/`, `codex plugin marketplace upgrade` moves the snapshot and
  leaves the cache unchanged by a byte. After the commit that moves the pin, the cache is the new release's.
- Codex CLI by itself: with no command but a start of its app server, the same two results.
- Codex CLI with the entry at a branch: after the branch moved with `master` unmoved, the upgrade said "already
  up to date" and the cache stayed on the release before; only `codex plugin add` again moved it.
- Codex CLI with a pin that names nothing: the upgrade reports the failure and keeps the installed package.
- Codex CLI, an install made while the entry named the directory: after the entry took the pinned form, the
  upgrade alone moved the cache from `master`'s package to the pinned commit's, identical to it.
- Grok Build, `url` with `sha` and `path`: `grok plugin install brigade --trust` installs a clone of the
  repository at that commit, with the plugin in its `grok/brigade`. After a push to `master`, `grok plugin
  marketplace update` and `grok plugin update` leave the installed package unchanged by a byte; after the pin
  moves, they move it. `path` is not in Grok Build's guide.
- Grok Build, an install made while the entry named the directory: after the entry took the pinned form, `grok
  plugin update brigade` fails with `plugin 'grok/brigade' not found in install registry` and the installed
  package stays as it was. `grok plugin install brigade --trust` over it leaves two installs listed. `grok
  plugin uninstall brigade` and then the install leave one, identical to the package at the pinned commit, and
  its updates work.
- Codex CLI's source at `rust-v0.166.0`: the entry's forms (`core-plugins/src/marketplace.rs`), the start-up
  upgrade and its forced reinstall (`manager.rs`, `marketplace_upgrade.rs`), the hash of the package's files
  (`hooks/src/engine/plugin_fingerprint.rs`).
- Not measured before the change landed: the pin against GitHub itself, Codex CLI's screens under it, and
  whether a Grok Build session start brings a moved pin by itself. The work item's live sessions show them, and
  its landing comment and the execution log's row have what they showed.

## Consequences

- A push to `master` that changes either package changes nothing for a member. A release reaches a Codex CLI
  member at the start after the pin moved, with one review of the hooks, and a Grok Build member at their next
  update.
- The change itself takes effect when it reaches `master`, not at a release: from then on a Codex CLI member is
  moved to 0.59.0's package at a start, and a Grok Build install from before stops updating until it is
  installed again.
- A release is one commit longer, and its last step waits for the publication. A release that stops before it
  leaves members on the release before, which is safe; `make release-pin` finishes it.
- A marketplace added from a local clone fetches the package from GitHub at the pin. An unreleased package is
  tried another way, which the developer's page says.
- ADR 0024's reason for generating the skill copies in every commit, that the two harnesses read them from the
  default branch, no longer holds for members. The copies stay generated in every commit for the reviewer.
- `release.yml` runs Grok Build's package check at every tag, and that check now reads the pin. Its rules are
  changed, like the release guard, only just after a release.
- Grok Build's `path` beside a `sha` is undocumented. If a version stops reading it, the package would install
  from the repository's root and fail; then Grok Build's members pin for themselves, and this is revisited.
- Claude Code and Muse Code are not changed. Claude Code moves an install only when the manifest's version
  changes; Muse Code reads a snapshot the member refreshes.

## References

- Work item 424 on the Brigade Board; work items 381 and 393, whose live sessions found it.
- `.agents/plugins/marketplace.json`, `.grok-plugin/marketplace.json`; `internal/packagecheck/pin.go`;
  `scripts/release-pin.sh`, `scripts/release-prep.sh`; `docs/agent/repository/gates.md`, "Package checks" and
  "Release pins"; `docs/developer/release/cut-a-release.md`, "The last step".
- `.context/plans/packages-pinned-to-a-release.md`.
- https://github.com/openai/codex at `rust-v0.166.0`; Grok Build's guide, `09-plugins.md`, as shipped with 1.0.92.
