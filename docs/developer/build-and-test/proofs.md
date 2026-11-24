# Run the proofs

A proof drives real sessions and asserts what the documentation claims. Each runs outside the current session
and strips the inherited session environment by prefix (`CLAUDE*`, `AI_AGENT`, keeping `CLAUDE_CONFIG_DIR`).

| Target | What it shows | When it is run | Needs |
| --- | --- | --- | --- |
| `make e2e` | `scripts/proof.sh`: the no-LLM proof, 221 assertions on registration, sending, delivery, policy and cleanup, with the watcher in `--sink` mode against the Supabase adapter | by CI's `e2e` job on every push; by you before pushing a change to `supabase/` or the Supabase adapter | the local stack |
| `make proof` | `make e2e`, then `scripts/proof-live.sh`: a message from another member wakes an idle interactive Claude Code session, and the session answers it with no key pressed | by you, before a release and after a change to the hooks, the watcher, delivery or the plugin's skills; no CI job can run a live session | the local stack, `make build`, a logged-in `claude`, `tmux`, `jq` |
| `make harness-smoke` | `scripts/harness-smoke.sh`: a headless smoke test with the fs adapter (`mode=bypassPermissions`) | by you, as a quick check of a headless session | a logged-in `claude` |

`scripts/proof-live.sh` runs alone too, after `make build`, and then needs no stack and no Docker: its team is on
the fs adapter, under one temporary directory that it removes. It prints one `ok:` or `FAIL:` line for each
assertion and exits with the number that failed. It shows one harness (Claude Code), one message and the fs
adapter. It does not show a second message, a crash, a resume, another backend or what a model does with a
hostile message.

## The three retired proofs

Until work item 415, `make proof` also ran three scripts that drove `claude -p` sessions against the local stack.
They were written for the store layout of before 2026-09-08, when a session named its team by a `profile` option.
Since a project's `.brigade.json` names the team, their provisioning registers no session and each stopped there,
before any session started, so `make proof` could not pass. No CI job ran them, and nobody saw when they broke.
They are retired: `make proof` no longer runs them, and each refuses to start with a line that says so.

| Script | What it measured, in September 2026 | Its record |
| --- | --- | --- |
| `scripts/proof-headless.sh` | what a headless session does with each of the 26 test messages, and a round trip between two sessions | [E4-headless](../../experiments/E4-headless.md) |
| `scripts/proof-idle-wake.sh` | that a message wakes an idle headless session, three times in one session, against a control with no message | [E4-idle-wake](../../experiments/E4-idle-wake.md) |
| `scripts/proof-crash-resume.sh` | that a session killed and resumed catches up, exactly once, on what arrived while it was down | [E4-crash-resume](../../experiments/E4-crash-resume.md) |

The scripts stay in the tree as the drivers of those reports, and the part of each that scores saved evidence
without a model (`judge`, `wake`, `catchup`) still runs in `make test`. What they measured stands as measured
then; nothing measures it on a present build. Bringing one back means giving its sessions a checkout with a team
file, as `scripts/proof.sh` does, and `BRIGADE_PROOF_RETIRED=run` lets a run get past the refusal while that is
done.

Each script's arguments and what it asserts are in [scripts/ci/README.md](../../../scripts/ci/README.md); the
exit record of the ten success criteria is [the proof results](../../../.context/plans/brigade-proof-results.md).
A proof measures attempts, not effects, and a run is one machine, one account, one model family:
[docs/security.md, section 11](../../security.md#11-what-the-proofs-proved-and-what-they-did-not) says what the
body of measurement does not cover.

Experiment drivers live in `scripts/experiments/<name>/` or `scripts/experiments/<name>.sh` beside their report
`docs/experiments/<name>.md`; a new experiment adds both and a row in the experiments index.
