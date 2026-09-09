# Codex participation capability gate

Date: 2026-09-08  
Repository revision: `d7088fca2fbdd0a7216da189b0d19f62ab38a14f`  
Client: Codex CLI 0.153.4, `gpt-6-astra`  
Host: macOS 26.6 (Darwin 25.6.0, arm64 host; amd64 Go toolchain)  
Result: **P0 FAIL; hard stop after P1**

This run tested an isolated repository-local marketplace and plugin in a
temporary `CODEX_HOME`. It copied authentication only into that temporary home,
did not alter the user's global Codex installation, did not contact teammates,
and did not join a Brigade team. Raw streams and native IDs stayed under the
temporary directory and are not committed. The committed probe records only
truncated SHA-256 values, field presence, event names, and booleans.

## Decisive result

`SessionStart`, `UserPromptSubmit`, and `SessionEnd` hooks all received one
stable native `session_id`. A Bash tool child received `CODEX_SESSION_ID` and
`CODEX_THREAD_ID`; both matched that hook ID. This is a usable lookup route on
0.153.4. The variables are absent from OpenAI's stable public environment
variable list, are absent in hook processes, and are mutable by the model.

Four adversarial cases were attempted in one fresh isolated session:

| Case | Result |
| --- | --- |
| Direct Bash child | Both IDs present and equal the hook `session_id` |
| `env -u` child | All three Codex markers absent |
| Poisoned child | All three replacement values accepted |
| Nested Python child | All three Codex markers absent |

The direct route therefore cannot enforce Brigade's human-only verbs. A model
can invoke an unmarked nested `brigade` process and be classified as a human
terminal if `InSession` relies on these variables. A hook-minted binding would
resolve a sender but would not prevent the same fail-open. The plan explicitly
requires this boundary and says an identity failure without a validated
supported alternative is a hard stop after P1, so P2-P5 were not started.

## Capability table

| Capability | Verdict | Evidence | Minimum version |
| --- | --- | --- | --- |
| Repository marketplace install and cached plugin load | PASS | install JSON; fixture manifest | 0.153.4 |
| Explicit skill invocation | PASS | cached `SKILL.md` read in `stream-clean.jsonl` | 0.153.4 |
| SessionStart/UserPromptSubmit/SessionEnd hooks | PASS | hashed `events.jsonl` records | 0.153.4 |
| `${PLUGIN_ROOT}` command expansion | PASS | installed-cache hook executed | 0.153.4 |
| `PLUGIN_DATA` writable hook state | PASS | private `events.jsonl` | 0.153.4 |
| Bare plugin `bin/` command on PATH | FAIL | `BARE=absent` | 0.153.4 |
| Hook ID to direct Bash-child ID | PASS | equal 16-character hashes | 0.153.4 |
| Stable documented shell identity | FAIL | markers absent from stable public environment docs | 0.153.4 |
| Unset/spoof/nested-child trust boundary | FAIL | 4/4 cases attempted; three bypass forms succeeded | 0.153.4 |
| Synchronous model-visible delivery | PASS (unverified) | assistant preserved 40-character anchor and reconstructed split nonce in 5/5 runs | 0.153.4 |
| Default model-visible output limit | PASS (documented) | approximately 2,500 tokens with spill | current docs |
| Hook timeout default | PASS (documented) | 600 s generally; SessionEnd 1 s default, 3 s maximum | current docs |
| Resume, clear, compact | NOT RUN | identity hard stop | — |
| Two simultaneous threads | NOT RUN | identity hard stop | — |
| Subagent lifecycle | NOT RUN | identity hard stop | — |
| Plugin option mechanism | NOT RUN | identity hard stop | — |
| Desktop idle wake | UNAVAILABLE | hook completion while idle waits for next user turn; no attachment API proven | — |
| App Server worker wake | AVAILABLE, worker-only | `turn/start` can start a managed thread; excluded from desktop PASS | current docs |

“PASS (unverified)” means the local author run passed, but the required
independent Fable verifier was unavailable in this runtime. It does not open the
P2 gate.

## Delivery classifier

The five live runs each returned the first 40 characters of Brigade's delivery
anchor and reconstructed a per-run token supplied as two pieces by
`UserPromptSubmit`. The scorer reads only completed `agent_message` items from
Codex's JSONL stream. It ignores prompts, hook input, command output, and other
items. `analyze_test.py` removes the anchor, nonce, and lifecycle readiness in
turn; every mutation flips delivery to false. This satisfies the local
non-vacuity check, pending the required independent review.

## P1 baseline

| Gate | Result |
| --- | --- |
| `make typecheck` | PASS |
| `make plugin-check` | PASS |
| `make test` | FAIL before changes and on isolated rerun |

The existing race-enabled test suite exceeded the three-second fake-adapter
deadline in multiple testscript cases. Failures reported `adapter did not finish
within its deadline`; the fixture comments already describe macOS first-exec
assessment taking 3-6 seconds under load. No production timeout was changed.

## Required test matrix

| ID | Verdict | Evidence or reason |
| --- | --- | --- |
| C01 | NOT RUN | Production Codex registration gated by P0 |
| C02 | NOT RUN | Identity hard stop |
| C03 | PASS (baseline) | Typecheck and Claude plugin check pass; full suite has pre-existing timing failures |
| C04 | NOT RUN | Resume/compact gated |
| C05 | NOT RUN | Production lifecycle gated |
| C06 | FAIL (capability) | Shell identity can be removed or poisoned |
| C07 | NOT RUN | Subagent lifecycle gated |
| C08 | NOT RUN | No production registration or backend messages |
| C09 | NOT RUN | No P3 implementation |
| C10 | NOT RUN | No P3 implementation |
| C11 | NOT RUN | No P3 implementation |
| C12 | NOT RUN | No P3 implementation |
| C13 | PASS (docs/fixture) | 2,500-token spill documented; production batching gated |
| C14 | NOT RUN | Fable verifier unavailable and P0 failed |
| C15 | NOT RUN | Production binding gated |
| C16 | NOT RUN | Production presence gated |
| C17 | PASS (fixture) | Cached package loads; explicit launcher works; bare command absent |
| C18 | PASS (fixture, unverified) | 5/5 model responses contained anchor and reconstructed nonce |
| C19 | FAIL for polling wake | Official hook behavior waits for next user turn while idle |
| C20 | NOT RUN | 60-second live null arm skipped after identity hard stop |
| C21 | NOT RUN | Production recipient gated |
| C22 | PASS (fixture) | Temporary auth copy outside repo; no global installation changes |
| C23 | FAIL | Direct marker works; unset, poisoned, and nested children defeat the boundary |
| C24 | NOT RUN | Production prompt budget gated; documented timeout values recorded |

## Reproduction and retained artifacts

The fixture and scorer are in
`scripts/experiments/codex-participation/`. Raw files from this run were named
`stream.jsonl`, `stream-clean.jsonl`, `stream-shell.jsonl`,
`stream-controls.jsonl`, `events.jsonl`, and matching `last-*.txt` files in the
temporary home. They contain native thread IDs or installed-cache paths and are
intentionally excluded from version control.

Official sources rechecked for this run:

- https://developers.openai.com/plugins/build/plugins
- https://learn.chatgpt.com/docs/hooks
- https://learn.chatgpt.com/docs/app-server
- https://learn.chatgpt.com/docs/config-file/environment-variables
