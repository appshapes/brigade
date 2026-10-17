# Run the proofs

The Phase 4 proof scripts drive real sessions against the local stack and assert what the documentation claims.
They run outside the current session and strip the inherited session environment by prefix (`CLAUDE*`, `AI_AGENT`,
keeping `CLAUDE_CONFIG_DIR`).

| Target | What | Needs |
| --- | --- | --- |
| `make e2e` | `scripts/proof.sh`: the no-LLM proof, 221 assertions, the watcher in `--sink` mode | the local stack |
| `make proof` | `e2e` plus `scripts/proof-headless.sh`, `scripts/proof-idle-wake.sh`, `scripts/proof-crash-resume.sh`: real `claude -p` sessions | a logged-in `claude`, `jq` |
| `make harness-smoke` | `scripts/harness-smoke.sh`: a headless smoke test with the fs adapter (`mode=bypassPermissions`) | a logged-in `claude` |

CI's `supabase` job runs `make e2e` on every push; the LLM runs are a developer's, recorded in
[docs/experiments/](../../experiments/README.md). Each script's arguments and what it asserts are in
[scripts/ci/README.md](../../../scripts/ci/README.md); the exit record of the ten success criteria is
[the proof results](../../../.context/plans/brigade-proof-results.md). A proof measures attempts, not effects, and
a run is one machine, one account, one model family: [docs/security.md, section 11](../../security.md#11-what-the-proofs-proved-and-what-they-did-not)
says what the body of measurement does not cover.

Experiment drivers live in `scripts/experiments/<name>/` beside their report `docs/experiments/<name>.md`; a new
experiment adds both and a row in the experiments index.
