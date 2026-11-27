# Cut a release

```sh
make release version=0.32.0 work_item=110     # on a clean master, with bin/goreleaser and bin/golangci-lint (make setup)
```

`work_item` is the number of the release's work item on the team's Brigade Board. `card=<n>`, its earlier name,
is still accepted with the same meaning; given both, they must agree. `scripts/release-prep.sh` does, in order:

0. **Preconditions**: a version token, a clean tree (untracked files included: step 4 stages everything), the
   expected branch, the manifests, `bin/goreleaser`, `bin/golangci-lint` (step 4 lints), the Muse catalog `marketplace.json` current
   (`brigade-muse-digest check`) and no file git does not track under `muse/brigade/` (step 4 rewrites the
   catalog's digest, which counts every file there; no Muse Code is needed since card 114), a
   selectable `go<go.mod line>` toolchain, a `## [<v>]` section in `CHANGELOG.md` (the `## [Unreleased]` section
   given the version and the date; read its "Who must act" lines against its entries first, because they are what
   a member reads before an update and what the release-notes email's "Before you update" is taken from; and read
   each harness's `status` in `docs/harnesses.json` against what happened since the last release, a live session
   that passed or failed, a gap closed or found, because the install pages show it and no check can judge it:
   ADR 0022),
   `make docs-check` green, no
   tag `v<v>` yet, here or on origin, `git pull --no-edit`, and a head that origin has. Then **the commit's CI**: `ci.yml`'s run on the commit the release
   starts from must be green ([The two CI checks](#the-two-ci-checks)); otherwise it stops with nothing changed.
1. **Bump the pins**: `plugin/bin/VERSION`, `plugin/.claude-plugin/plugin.json`, `muse/brigade/.muse-plugin/plugin.json`,
   `codex/brigade/.codex-plugin/plugin.json`, `grok/brigade/.grok-plugin/plugin.json`, `antigravity/brigade/plugin.json`.
2. **Reproducible build**: `make cross version=<v>` builds the six targets (darwin, linux and windows, amd64 and
   arm64) with `GOTOOLCHAIN` forced, `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false` and a version-only `-X`, so
   the bytes do not depend on the commit.
3. **Cross-check**: goreleaser's own build of the same six targets must produce an identical `checksums.txt`.
4. **Commit**: `cp dist/checksums.txt plugin/bin/checksums.txt`, the same into `muse/brigade/bin/`,
   `codex/brigade/bin/`, `grok/brigade/bin/` and `antigravity/brigade/bin/`,
   `go run ./cmd/brigade-muse-digest write` (the digest Muse computes over the final `muse/brigade/`, and the
   version, into `marketplace.json`), then `make push message="<work item>: Release <v>"` (typecheck, pull, lint, build, test with the
   documentation gate, commit, push).
5. **The release commit's CI, then the tag**: it waits for `ci.yml`'s run on the commit step 4 pushed, about as
   long as CI takes, and only over a green one runs `git tag -a v<v> <that commit> && git push origin v<v>`.
6. **The Codex CLI and Grok Build packages**: `scripts/release-pin.sh <v>` waits until `release.yml` has
   published the release, writes the tag's commit into `.agents/plugins/marketplace.json` and
   `.grok-plugin/marketplace.json` (`go run ./cmd/brigade-packages pin <commit>`), and pushes that through the
   same chain as `<work item>: Point the Codex CLI and Grok Build packages at <v>`
   ([The last step](#the-last-step)). A pre-release moves nothing.

`release.yml` then checks the tag is not already published, asks the CI check again
([The workflow asks again](#the-workflow-asks-again)), guards that the plugin pins this tag, the four
manifests and the Muse catalog agree, the four `checksums.txt` are equal (`make grok-plugin-check` and
`make antigravity-plugin-check` hold the Grok and Antigravity packages' manifests and assets), the catalog's digest is the package digest of `muse/brigade/` at the tag
(`brigade-muse-digest check`), `make docs-check` passes and `make cross` reproduces the committed
checksums; goreleaser builds a draft; `scripts/ci/release-verify.sh` compares its checksums with the committed
file; the draft is published and the release-notes workflow dispatched. A failure deletes the draft and publishes
nothing.

The release-notes workflow (`release-notes.yml`) writes the release's notes behind a lint and a review. The
notes open with one sentence and then with the section's "who must act" lines as the changelog has them, which
the lint holds word for word (work item 418): read those lines before the release, because the release page
shows them at its top, right after that sentence. Then it tells the members: one issue titled `<v> is out — run /brigade:update`. The same step closes the announcement of
each release before, so the one open announcement is the newest release's and the repository's open issues are
the ones that need an answer. `scripts/ci/announce-release.sh plan` shows what it would open and close
([scripts/ci/README.md](../../../scripts/ci/README.md)).

## The last step

Codex CLI and Grok Build read their marketplace file from `master`, and each file names its package at one
commit: the commit of the last published release (work item 424,
[ADR 0030](../../decisions/0030-pin-the-codex-cli-and-grok-build-packages-to-the-last-release-in-their-marketplace-files.md)).
So a push changes nothing for their members, and neither do steps 1 to 5. Step 6's commit is what gives them
the release, and it comes after the publication because the package's bootstrap downloads the release's binary.

The step is its own script, `scripts/release-pin.sh`, and runs by itself as
`make release-pin version=<v> work_item=<n>`:

| What it finds | What it does |
| --- | --- |
| a version that is not the one `origin/master`'s `plugin/bin/VERSION` pins | refuses: the pin never goes back to an older release |
| `origin/master` names the tag's commit in both files | says so and ends |
| the release is a draft, or not there yet | waits for it, 15 minutes at most |
| `release.yml`'s run on the tag's commit ended in anything but success | stops at once |
| `gh` cannot ask (logged out, no network) | stops at once, with `gh`'s words |
| the release is published | writes the commit, then `make push`, unless the tree took another change meanwhile |
| the two files written and not committed, from a run whose chain failed | commits and pushes them |
| the commit made and not pushed, because origin refused it | `make pull`, then `git push` |

A stop in it, or a release whose step 6 never ran, leaves the release published and the members of the two
harnesses on the release before. `make release` ends with a line that says so, the package checks print
`behind:` and the two versions on every run without failing, the next `make release` prints the same line
when it starts, and `make release-pin` finishes it. Never write the commit by hand: the package checks refuse a
commit that is not a release tag's, and two files that name two commits.

The pointer commit is an ordinary commit: `ci.yml` runs on it, it changes no release pin, and nothing in the
release workflows waits for it. Ask the other sessions to hold their pushes until it is on `master`, as for
step 4: its chain is the same, and origin can move under it.

## The two CI checks

`scripts/release-ci-check.sh <full sha>` reads the newest run of `ci.yml` on a commit and prints it:

```text
release-ci: ci.yml on <sha>: run <id> is in_progress; waiting for it to end (<url>)
release-ci: ci.yml on <sha>: success (run <id>, <url>)
```

| The run | What the release does |
| --- | --- |
| in progress | waits for it, in every case, then reads it |
| green: every job of `ci.yml`, `windows` too | goes on |
| red: any other conclusion | stops, and prints `release_over_ci=<the run's id>` |
| none: the commit has no run | stops, and prints `release_over_ci=missing:<the commit's sha>` |
| GitHub cannot be asked (`gh` missing, logged out, no network), five times in a row | stops; the override does not apply |

`release_over_ci` is the override, on the command line of the same `make release`: it is the owner's to allow,
and its value has to name what was found, one run or one commit, so it covers neither the other of the
release's two checks nor another release. The rule is in
[gates.md](../../agent/repository/gates.md) and the decision in
[ADR 0015](../../decisions/0015-stop-a-release-over-a-red-or-missing-ci-run-with-an-override.md).

A stop at the first check changes nothing. A stop at the second leaves the release commit pushed and no tag:
when its run is green (`gh run rerun <id> --failed` for a flake), or with the override, run the same command
again. Step 1 is then a no-op, steps 2 and 3 build again, step 4 says there is nothing to commit, and the
sequence goes on from the check.

### The workflow asks again

`release.yml` and `server-release.yml` each run `scripts/ci/release-guard.sh <the tag's commit> <the tag>` before
they build or publish anything (work item 416,
[ADR 0028](../../decisions/0028-ask-the-ci-check-again-in-the-release-workflows-with-the-override-in-the-tags-message.md)):

1. The tag's commit is on `master`: `ci.yml` runs for a pull request too, so a commit that was never merged can
   have a green run of its own.
2. `ci.yml` is green on it, by `scripts/release-ci-check.sh`, waiting one minute for a run to appear and three
   for one in progress.
3. An override is read from the tag's message. `make release` and `make server-tag` write the
   `release_over_ci` they were given into the message of the tag they make, as a line of its own; the guard
   reads it from the tag on GitHub and the check takes it only where it names this commit's run or this commit.

For a release the scripts made, the run is green and has ended, and the guard costs four lookups. A tag pushed
by hand over a red or a missing run, or on a commit `master` does not hold, publishes nothing. When the guard
stops because a run had not ended in its three minutes, run the workflow's run again once it has
(`gh run rerun <id>`).

## Recovery

- If the release job fails and nothing in the tagged tree is at fault (a runner, GitHub, a run of `ci.yml` that
  had not ended), run the workflow's run again: `gh run rerun <id>`. If the tagged tree is at fault, fix it on
  `master` and release the next patch version: a release tag can be neither deleted nor moved, published or not
  (the ruleset `protect-release-tags`, read on 2026-11-23; this page said until then to delete a tag that was
  never published, which origin refuses).
- After a stop between the release commit and its tag, a rerun of `make release` has nothing to commit at step 4
  and goes on to the CI check and the tag (until work item 399 it died there, and step 5 was done by hand). Until
  the release is published keep every fix to non-Go files: a Go change makes `checksums-check`'s fresh build
  differ from the committed file.
- By hand the tag still follows the check: `scripts/release-ci-check.sh "$(git rev-parse HEAD)" && git tag -a v<v>
  -m v<v> && git push origin v<v>`. The workflow asks again, and over a red or a missing run a tag made this way
  publishes nothing: an override is the scripts' to write into the tag's message, on the owner's word.
- After a stop in step 6 the release is out: `make release-pin version=<v> work_item=<n>`, not `make release`,
  which refuses a version that is tagged ([The last step](#the-last-step)).
- `DRY_RUN=1 work_item=<n> scripts/release-prep.sh <v>` does steps 1 to 3 for real and prints 4 to 6. It asks
  the first CI check once, without waiting for a run in progress, and goes on whatever it says. On a branch
  other than `master` (`branch=<name>`) a dry run is all the script does: the workflow publishes nothing for a
  tag whose commit `master` does not hold, and the tag could not be removed.
- `make release-dry-run` is goreleaser's check and a local release without publishing.

## After

Both Claude Code personas on a developer's machine update like any member's; the `make plugin-dev` pointer is
unaffected. Comment the release's work item with the version; closing it is the owner's. When the changelog
names a migration or a gateway change, the Project Owners apply it
([Apply a release](../../project-owner/update/apply-a-release.md)).

A tree in which `make release` has not run carries the pre-release `0.0.0` and an empty checksums file, and its
plugin has nothing to download.
