# Codex participation experiment

This experiment implements the P0 gate in
`.context/plans/codex-team-participation.md`. See
`docs/experiments/codex-participation.md` for the measured result and test
matrix.

Validate the deterministic fixture and classifier with:

```sh
python3 ~/.codex/skills/.system/plugin-creator/scripts/validate_plugin.py \
  scripts/experiments/codex-participation/fixture/plugins/brigade-capability-probe
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s scripts/experiments/codex-participation -p '*_test.py'
```

Score a retained raw run outside the repository with:

```sh
python3 scripts/experiments/codex-participation/analyze.py \
  --events "$run_events" --stream "$run_stream" --nonce "$expected_nonce"
```

Exit 0 means both delivery and the human-only identity boundary passed. The
measured 0.153.4 run exits 1 because the direct marker works but unset,
poisoned, and nested children defeat the boundary.
