# Cut a release

```sh
make release version=0.32.0 work_item=110     # on a clean master, with bin/goreleaser (make setup-goreleaser)
```

`work_item` is the number of the release's work item on the team's Brigade Board. `card=<n>`, its earlier name,
is still accepted with the same meaning; given both, they must agree. `scripts/release-prep.sh` does, in order:

0. **Preconditions**: a version token, a clean tree (untracked files included: step 4 stages everything), the
   expected branch, the manifests, `bin/goreleaser`, the Muse catalog `marketplace.json` current
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
   `codex/brigade/.codex-plugin/plugin.json`, `grok/brigade/.grok-plugin/plugin.json`.
2. **Reproducible build**: `make cross version=<v>` builds the six targets (darwin, linux and windows, amd64 and
   arm64) with `GOTOOLCHAIN` forced, `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false` and a version-only `-X`, so
   the bytes do not depend on the commit.
3. **Cross-check**: goreleaser's own build of the same six targets must produce an identical `checksums.txt`.
4. **Commit**: `cp dist/checksums.txt plugin/bin/checksums.txt`, the same into `muse/brigade/bin/`,
   `codex/brigade/bin/` and `grok/brigade/bin/`,
   `go run ./cmd/brigade-muse-digest write` (the digest Muse computes over the final `muse/brigade/`, and the
   version, into `marketplace.json`), then `make push message="<work item>: Release <v>"` (typecheck, pull, build, test with the
   documentation gate, commit, push).
5. **The release commit's CI, then the tag**: it waits for `ci.yml`'s run on the commit step 4 pushed, about as
   long as CI takes, and only over a green one runs `git tag -a v<v> <that commit> && git push origin v<v>`.

`release.yml` then checks the tag is not already published, guards that the plugin pins this tag, the four
manifests and the Muse catalog agree, the four `checksums.txt` are equal (`make grok-plugin-check` is what holds the
Grok package's manifest and assets), the catalog's digest is the package digest of `muse/brigade/` at the tag
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

## Recovery

- If the release job fails, fix the drift, delete the never-published tag (`git tag -d v<v> && git push --delete
  origin v<v>`) and rerun. A **published** tag is never rewritten: bump the patch version instead.
- If the failure was in the workflow alone, a rerun of `make release` has nothing to commit at step 4 and goes on
  to the CI check and the tag (until work item 399 it died there, and step 5 was done by hand). Until the retag
  keep every fix to non-Go files: a Go change makes `checksums-check`'s fresh build differ from the committed file.
- By hand the tag still follows the check: `scripts/release-ci-check.sh "$(git rev-parse HEAD)" && git tag -a v<v>
  -m v<v> && git push origin v<v>`.
- `DRY_RUN=1 work_item=<n> scripts/release-prep.sh <v>` does steps 1 to 3 for real and prints 4 and 5. It asks
  the first CI check once, without waiting for a run in progress, and goes on whatever it says.
- `make release-dry-run` is goreleaser's check and a local release without publishing.

## After

Both Claude Code personas on a developer's machine update like any member's; the `make plugin-dev` pointer is
unaffected. Comment the release's work item with the version; closing it is the owner's. When the changelog
names a migration or a gateway change, the Project Owners apply it
([Apply a release](../../project-owner/update/apply-a-release.md)).

A tree in which `make release` has not run carries the pre-release `0.0.0` and an empty checksums file, and its
plugin has nothing to download.
