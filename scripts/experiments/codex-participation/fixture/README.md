# Codex participation P0 fixture

This local marketplace contains a probe plugin used only by
`scripts/experiments/codex-participation/`. Its manifest uses Codex's repository
marketplace location, `.agents/plugins/marketplace.json`. The probe records hashes and field presence,
not raw session IDs, prompts, paths, environment values, tokens, or credentials.

The fixture is installed only into a temporary `CODEX_HOME`. The test runner may
copy the current Codex authentication file into that temporary directory; the
copy and all raw model output stay outside the repository and are removed when
the run finishes.

The marketplace root is the `fixture/` directory. A manual isolated run uses:

```sh
CODEX_HOME="$temporary_home" codex plugin marketplace add "$PWD/scripts/experiments/codex-participation/fixture"
CODEX_HOME="$temporary_home" codex plugin add brigade-capability-probe@brigade-p0
env -u CODEX_SESSION_ID -u CODEX_THREAD_ID -u CODEX_CI \
  BRIGADE_PROBE_NONCE="$nonce" CODEX_HOME="$temporary_home" \
  codex --dangerously-bypass-hook-trust --sandbox read-only \
    --ask-for-approval never --cd "$PWD" exec --json \
    '$brigade-capability-probe Return only the requested capability probe line.'
```

The trust bypass is confined to this reviewed fixture. CLI 0.153.4 requires the
global options before `exec`, despite showing some of them in `codex exec --help`.
