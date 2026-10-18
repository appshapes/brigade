# Cut a release

```sh
make release version=0.32.0 card=110          # on a clean master, with bin/goreleaser (make setup-goreleaser)
```

`scripts/release-prep.sh` does, in order:

0. **Preconditions**: a version token, a clean tree (untracked files included: step 4 stages everything), the
   expected branch, the manifests, `bin/goreleaser`, `muse` on PATH (Muse Code on the release machine: step 4
   rewrites the Muse catalog's digest with it, owner ruling 2026-10-17; card 114 is the way without it), a
   selectable `go<go.mod line>` toolchain, a `## [<v>]` section in `CHANGELOG.md`, `make docs-check` green,
   `git pull --no-edit`.
1. **Bump the pins**: `plugin/bin/VERSION`, `plugin/.claude-plugin/plugin.json`, `muse/brigade/.muse-plugin/plugin.json`,
   `codex/brigade/.codex-plugin/plugin.json`.
2. **Reproducible build**: `make cross version=<v>` builds the four targets (darwin and linux, amd64 and arm64) with
   `GOTOOLCHAIN` forced, `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false` and a version-only `-X`, so the bytes do
   not depend on the commit.
3. **Cross-check**: goreleaser's own build of the same four targets must produce an identical `checksums.txt`.
4. **Commit**: `cp dist/checksums.txt plugin/bin/checksums.txt`, the same into `muse/brigade/bin/` and `codex/brigade/bin/`,
   `scripts/muse-digest.sh write` (the digest Muse computes over the final `muse/brigade/`, and the version, into
   `marketplace.json`), then `make push message="<card>: Release <v>"` (typecheck, pull, build, test with the
   documentation gate, commit, push).
5. **Tag**: `git tag -a v<v> && git push origin v<v>`.

`release.yml` then checks the tag is not already published, guards that the plugin pins this tag, the three
manifests and the Muse catalog agree, the three `checksums.txt` are equal, the catalog's digest has its shape (its
value is verified where Muse is installed), `make docs-check` passes and `make cross` reproduces the committed
checksums; goreleaser builds a draft; `scripts/ci/release-verify.sh` compares its checksums with the committed
file; the draft is published and the release-notes workflow dispatched. A failure deletes the draft and publishes
nothing.

## Recovery

- If the release job fails, fix the drift, delete the never-published tag (`git tag -d v<v> && git push --delete
  origin v<v>`) and rerun. A **published** tag is never rewritten: bump the patch version instead.
- If the failure was in the workflow alone, a rerun of `make release` dies at step 4 with nothing to commit: do step
  5 by hand (`git tag -a v<v> -m v<v> && git push origin v<v>`). Until the retag keep every fix to non-Go files: a
  Go change makes `checksums-check`'s fresh build differ from the committed file.
- `DRY_RUN=1 card=<n> scripts/release-prep.sh <v>` does steps 1 to 3 for real and prints 4 and 5.
- `make release-dry-run` is goreleaser's check and a local release without publishing.

## After

Both Claude Code personas on a developer's machine update like any member's; the `make plugin-dev` pointer is
unaffected. Comment the card with the version and close it when its criteria are met. When the changelog names a
migration or a gateway change, the Project Owners apply it ([Apply a release](../../project-owner/update/apply-a-release.md)).

A tree in which `make release` has not run carries the pre-release `0.0.0` and an empty checksums file, and its
plugin has nothing to download.
