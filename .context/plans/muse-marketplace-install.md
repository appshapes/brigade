# Muse Code: install the package from a marketplace (card 115)

Written 2026-10-17 by Rjae's session 88-muse-rjae, after the documentation redesign (card 110) left the question
open: a Muse Code member installed Brigade from a clone of this repository where a Claude Code member types two
lines in the session. Rjae: "create the card; I accept Muse on the release machine (for now)"; then "you will
implement card 115 fully autonomously (including release)". Card 114 researches the digest without Muse.

## 1. What was verified (Muse 1.4.10-R6427.1, macOS, isolated XDG roots, 2026-10-17)

- `muse plugins marketplace add <name> <source>`: `owner/repo[#ref]` (GitHub over HTTPS), a git URL (`file://`
  included) or a local path. In a checkout Muse reads the first of `marketplace.json` (native),
  `.agents/plugins/marketplace.json` (Codex), `.claude-plugin/marketplace.json` (Claude Code).
- Before this card the repository offered Muse the Claude catalog only, whose `plugin/` entry Muse skips (hook
  `args` unsupported); from 0.32.0 the Codex catalog came first and `brigade@brigade` installed `codex/brigade`
  into Muse as a codex-compatible plugin. The native catalog fixes both: Muse reads it first.
- A native catalog needs `integrity.digest` ("must declare `/integrity/digest`"); a wrong digest fails the install
  ("failed integrity check"); the digest is the same from a local path and from a git clone; it is the SHA-256 of
  the package tree by an undocumented serialisation (ten constructions tried; card 114).
- `muse plugins install brigade@<name>` installs at user scope (`--scope project` refused); hooks wait at
  `review_needed` until `muse plugins approve brigade`; provenance `marketplace-user-added`.
- In a session the composer takes `/plugins marketplace add <name> <source>` and `/plugins install brigade@<name>`
  (the panel opens; "Installed brigade plugin."); `approve` is not a verb (verbs: list, inspect, diagnostics,
  install, enable, disable, update, remove, marketplace); review is in the panel's Runtime tab.
- Update: `muse plugins marketplace update <name>` refreshes the snapshot into `generations/`; `muse plugins update
  brigade` kept the old digest; remove, install and approve again moved it to the new one.

## 2. The design

- `marketplace.json` at the repository root: `schemaVersion` 1, one entry `brigade`, `install.source`
  `muse/brigade`, `integrity.digest`, `availability.status` `available`; `version` equals `plugin/bin/VERSION`.
- `scripts/muse-digest.sh print|write|check`: Muse installs the package into a throwaway XDG root and reports
  `package_sha256`; `write` rewrites the catalog's digest and version; `check` compares, skipping with a line when
  `muse` is not on PATH unless `MUSE_DIGEST_REQUIRE=1`. `make muse-digest` is `write`.
- `scripts/release-prep.sh`: precondition `muse` on PATH when the tree has a Muse package (owner ruling
  2026-10-17); step 4 runs `write` after the pinned assets are copied, before `make push`.
- `scripts/ci/muse-plugin-check.sh` checks 10 and 11: the catalog's shape and version; the digest through
  `muse-digest.sh check`. `release.yml`'s guard checks the catalog's version and the digest's shape.
- Pages: install, update and leave for Muse Code with their TL;DRs; the package README; the update and setup
  skills; the agent page; the release and gates pages; the CI README; the changelog's 0.33.0 section.
- Proof after the release: `appshapes/brigade` from GitHub in an isolated root, the digest equal, approve, inspect.

## 3. Limits

- Muse on the release machine until card 114 reproduces the digest; CI verifies the catalog's version and shape
  only. A change under `muse/brigade/` on a machine without Muse leaves the catalog stale until a machine with
  Muse runs `make muse-digest` (the check there fails); a stale catalog makes the install fail its integrity check,
  never install the wrong bytes.
- The first-time hook review in the panel is unobserved on a machine whose trust store does not know the hooks.
