# Brigade implementation plan: Claude Code team messaging

Date: 2026-08-30
Status: Draft for discussion, second revision of the day. The first revision applied a 40-finding review; this one applies the decisions recorded after that review (D34: no MCP server, the model uses the `brigade` CLI through the Bash tool; D35: everything in Go, one multi-call static binary, a hand-rolled Supabase client, GitHub Releases plus a sh bootstrap in `plugin/bin`) and the four research digests written the same afternoon to validate them. Every environment claim below is traceable to Appendix A (A.1-A.6 from the morning probes, A.7 from the afternoon digests), to a digest under `docs/research/`, or to a URL in Appendix B.
Ticket: 15 (all commits on `master`, messages `15: <Imperative summary>`, merges only, never rebase)

## 1. Status and relationship to the logical plan

`.context/plans/claude-code-team-messaging-logical-plan.md` (2026-08-29) is the research summary and conceptual design. It fixed the architecture (a vendor-neutral CLI adapter is the portability boundary; Supabase is the default adapter, not part of the protocol; the Claude Code integration is lifecycle hooks, an inbound watcher and a model-facing surface), listed ten decisions to freeze before implementation, listed nine things not to freeze, and defined the first vertical proof with ten success criteria. The logical plan recommended an MCP shim as the model-facing surface "so Claude does not compose raw shell commands"; D34 replaces it with a CLI on the plugin's `bin/` directory, which Claude Code adds to the Bash tool's `PATH` [verified, A.7], because the quoted-heredoc form removes the quoting problem the shim was meant to avoid, the skill's `allowed-tools` grant and the documented permission rules give the same control over prompting, and one static binary is cheaper to ship, start and reason about than a Node MCP server plus a bundled adapter. The logical plan's freeze list, do-not-freeze list and ten success criteria are unchanged.

This document turns that design into an execution order. It freezes exactly the ten items (section 4.10 maps each one), leaves every item of the "do not freeze" list untouched (section 12), and defines done as the ten success criteria of the recommended first proof (section 9.7 maps each to a concrete test).

It is a synthesis of three candidate plans judged on 2026-08-30. The skeleton, sequencing and proof harness come from the vertical-proof-first candidate (the judges' winner). From the protocol-portability-first candidate it takes the normative specification style, the filesystem adapter, the conformance suite, the adapter-kit, the CI split and the `--resume` mapping. From the security-risk-first candidate it takes the inbound controls (a human gate that is never a model tool, no acknowledgement on hold or refuse, the `<brigade-message>` frame and its sanitiser, the `brigade:<code>` server error convention, the join limiter with timing parity, Security Advisor lints in CI from Phase 2). Every defect the judges found in the three candidates is fixed here; section 11.3 lists them so they are not reintroduced.

Ground truth, in order of authority:

1. Appendix A (verified environment facts). A.1-A.6 are the morning's live probing of Claude Code v2.1.251 and this machine; A.7 is what the afternoon's four digests established by experiment (plugin `bin/` on the Bash tool's `PATH`, the environment of hooks versus the Bash tool, the sandbox, GoTrue/PostgREST/Realtime request and response shapes, Go binary sizes and reproducibility, hook latency, quarantine, bootstrap timing). Where anything else disagrees with Appendix A, Appendix A wins.
2. The eleven research digests written on 2026-08-30. The seven of the first round (Supabase auth and RLS, 37 live checks; Supabase Realtime and durable delivery; Supabase local dev and CI; Claude Code plugin and MCP, seven empirical runs; Node packaging and secrets; Claude Code documentation gaps; the threat model) remain valid except where they assume TypeScript or MCP; the four of the second round (Go toolchain, layout and release engineering; Supabase in Go; the CLI-only plugin shape with the bootstrap; testing and conformance in Go) supersede them on those points. Their test identifiers (`U-`, `I-`, `E2E-`, `CI-`) are reused unchanged: U-01..U-25, I-01..I-33, E2E-01..E2E-15 and CI-01..CI-04 are defined in the threat model's section 8, while U-26..U-28 and I-34 are introduced by this plan and defined in 9.8/9.9 (U-26, U-27 and I-34 also appear in the testing digest's mapping table; U-28 exists only here). The first round is committed under `docs/research/` already (commit `6386046`), the second round joins it with the commit that records this revision (P0-2, section 13).
3. The decision brief of 2026-08-30 (committed as `docs/research/decisions-2026-08-30.md` by P0-2). Every decision in it is final and overrides earlier text; where an afternoon experiment showed that a detail of the brief cannot work as written (the bootstrap's cache directory, D35), the plan says so at the decision and keeps the decision's intent.
4. Official documentation fetched on 2026-08-30 and cited inline by URL (Appendix B). Two claims this revision adds were re-fetched while writing it: the per-hook `timeout` semantics and the `SessionEnd` budget (https://code.claude.com/docs/en/hooks), and the plugin `bin/`, `CLAUDE_PLUGIN_DATA` and `version` statements (https://code.claude.com/docs/en/plugins-reference).

Confidence marks: `[verified]` (docs plus live observation, or docs fetched today, or an experiment run today), `[likely]` (docs or source only, not exercised), `[uncertain]` (a Phase 0 experiment settles it). Anything unmarked is a design decision, not a claim about the environment.

Terms: "adapter" is the program that implements the protocol (`brigade adapter supabase`, the hidden subcommand of the shipped binary that the harness spawns as a child process; `brigade-adapter-fs`, the dev-only filesystem adapter; or any conforming third-party executable); "harness" is the Claude Code integration (the plugin files under `plugin/` plus the `hook`, `watch` and human/model command surfaces of the same `brigade` binary); "principal" is one anonymous Supabase Auth user; "Brigade session" is the adapter-issued session record, never the Claude Code native session id; "bootstrap" is the POSIX-sh script `plugin/bin/brigade` that downloads, verifies and execs the pinned release binary.

---

## Section files

| file | section | former lines in the one-file plan | status |
| --- | --- | --- | --- |
| `implementation/02-decisions.md` | 2. Decision summary | 28-85 | Decisions D1–D36 settled; 6 corrections |
| `implementation/03-architecture.md` | 3. Architecture | 86-327 | Design record, 2026-08-30; 7 corrections |
| `implementation/04-protocol.md` | 4. Protocol v1 specification outline (Brigade Adapter Protocol, BAP/1) | 328-538 | Design record, 2026-08-30; 3 corrections |
| `implementation/05-supabase-adapter.md` | 5. Supabase adapter design | 539-1322 | Design record, 2026-08-30; 10 corrections |
| `implementation/06-plugin.md` | 6. Claude Code plugin design | 1323-1755 | Design record, 2026-08-30; 29 corrections |
| `implementation/07-repository-toolchain.md` | 7. Repository layout and toolchain | 1756-2575 | Design record, 2026-08-30; 30 corrections |
| `implementation/08-phases.md` | 8. Phased implementation plan | 2576-2683 | Live; 28 corrections |
| `implementation/09-testing.md` | 9. Testing strategy | 2684-2904 | Live; 18 corrections |
| `implementation/10-security.md` | 10. Security: threat summary and mitigations by phase | 2905-2937 | Live; 2 corrections |
| `implementation/11-risks-open-questions.md` | 11. Risks and mitigations; open questions | 2938-3023 | Live; 1 correction |
| `implementation/12-out-of-scope.md` | 12. Out of scope (from the logical plan's "do not freeze" list) and future adapters | 3024-3038 | Record; 0 corrections |
| `implementation/13-commit-plan.md` | 13. Commit plan | 3039-3056 | Record; 0 corrections |
| `implementation/A-verified-facts.md` | Appendix A: Verified environment facts | 3057-3187 | Record; 4 corrections |
| `implementation/B-sources.md` | Appendix B: Sources | 3188-3309 | Record; 0 corrections |

This index carries the plan's title block and section 1 (former lines 1-27); every other line of the
one-file plan is in the section file its row names, byte for byte, under the
`<!-- verbatim from the one-file plan -->` marker.

## How to cite

`plan 6.7` means `implementation/06-plugin.md` § 6.7. Section and subsection numbers are unchanged, so
every `6.7`, `9.6`, `5.3` and `A.7` written in the execution log, the briefs and the code comments still
resolves. The former line numbers cited throughout the execution log and the briefs (`plan :2653`, `plan ~2801`,
`plan :2760`) resolve through the "former lines in the one-file plan" column above:
find the row whose range contains the number, then read that file.

Finished-phase reports and the pre-Phase-4 plan corrections are in
`.context/plans/brigade-execution-log-archive.md`; the live log is `.context/plans/brigade-execution-log.md`.
