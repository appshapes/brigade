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

## Judging delivery

Delivery needs the split nonce (always an exact match) and a verdict that the
relayed message actually landed. `--judge exact` is the default: it requires the
literal anchor string, stays offline, and is deterministic, so a run that
paraphrases the message instead of quoting it scores a false FAIL.

`--judge typesafe` scores that verdict with a TypeSafe Noul instead, so a
paraphrase still counts. It is opt-in because it sends the run's assistant text
to TypeSafe and makes the score non-deterministic; it needs `pip install
typesafe-sdk` and `TYPESAFE_API_KEY`, and takes `--judge-threshold` (default
0.7).

Either way the report records both signals: `anchor_in_assistant_text` is always
the exact check, kept for auditing, while `judged_received` (with
`judged_probability` and `judge`) is what drives `pass`. Comparing the two shows
where the judge and the literal check disagree.
