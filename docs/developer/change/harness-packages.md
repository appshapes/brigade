# Add a harness

A harness is a program that runs a model's session and gives Brigade three things: a way to run a hook at session
start and at a prompt, an environment variable that marks a session, and a way to put text in front of the model
while it is idle. Claude Code has an inbox socket; Muse Code has a monitor tool that reads a file; Codex CLI has a
command that queues a turn into a session from outside (`codex queue`); a harness with none of these is a mailbox
until its next prompt (`poll_on_prompt`). A harness whose hooks carry no marker of their own (Codex) names itself
on the hook's argv instead: `brigade hook <event> --host <name> --plugin-root <dir>`. The research question for a new harness is always
the third: can anything outside the session wake it? Measure it first, as [the Muse live test](../../experiments/muse-participation.md)
did, and record the answer under `docs/experiments/`.

## What a package is

A directory beside `plugin/`, `muse/brigade/` and `codex/brigade/` holding the harness's native manifest, its hooks
(one wrapper per hook that execs `bin/brigade hook <event>`, or hook commands that run it), the seven skills
worded for the harness, a `README.md`, and `bin/` copies of `plugin/bin`'s bootstrap, `VERSION` and
`checksums.txt`, which the release sequence keeps equal.

## The code

- `internal/harness/config/environ.go`: the host marker (`HostOf`), the native session id, what the trusted
  environment strips.
- `internal/harness/hook`: what the session-start, prompt and session-end hooks print and cap for the host; the
  options source when the harness runs no plugin settings.
- `internal/harness/frame`: the frame variant, pinned by a golden file; one sentence may differ per host.
- `internal/harness/watch`: the detached watcher (`brigade watch`): delivery (socket, sink file, `codex queue`) and what the
  watcher owns when the hooks cannot.
- `internal/harness/sessionmap`: the by-host pointer from the native id to the by-pid map.

## The checks and the release

- A `scripts/ci/<harness>-plugin-check.sh` like `muse-plugin-check.sh` or `codex-plugin-check.sh` (allowlist,
  manifest, hooks, assets equal to `plugin/bin`'s), its Go test, a Makefile target and a step in `ci.yml`'s `fast`
  job.
- `scripts/release-prep.sh` bumps the manifest's version and copies the pins into the package's `bin/`;
  `checksums-check.sh` rule (a) and `release.yml`'s guard compare them.
- A harness that installs from a catalog carries it at the repository root (`.claude-plugin/marketplace.json`,
  `.agents/plugins/marketplace.json`, `marketplace.json` for Muse). When the harness pins a package digest, as Muse
  does, `scripts/muse-digest.sh` is the model: the harness computes it in a throwaway root, `make release` rewrites
  it, the package check verifies it where the harness is installed.

## The documentation

An entry in `docs/harnesses.json` and six pages, which `make docs-check` requires:
[Add a harness to the documentation](../documentation/add-a-harness.md). The package's `README.md` links to the
member's install page; the agent page names every skill by path.
