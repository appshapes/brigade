# Host metadata fixtures

All values are synthetic; no conversation or real session was copied.
Schema provenance and limitations are in [the card 117 research note](../../../../../docs/research/host-session-facts.md).

- `codex-index.jsonl`: observed 0.163.0 title-index fields, repeated exact ID and another thread.
- `codex-rollout.jsonl`: observed 0.163.0 paginated-mode rollout envelope, exact header ID, latest request versus
  cumulative usage, cache and output counters, and an event without usage.
- `muse-session.jsonl`: observed 1.4.8 session envelope, name event and run-model binding; `model_completed`
  and its Usage shape are reconstructed from the installed binary's serialized schema descriptors. A child
  rollup is an adversarial fixture and must be ignored. A successful live Muse usage record was unavailable.
