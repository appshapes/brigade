# Research inputs (2026-08-30)

Committed copy of the research that produced `.context/plans/claude-code-team-messaging-implementation-plan.md`.
These files are the reference for every `U-`, `I-`, `E2E-` and `CI-` test id the plan cites (defined in
`security-threat-model.md`, section 8) and for the empirical facts the plan marks `[verified]`. They were written by
research agents on 2026-08-30 against Claude Code v2.1.251 and Supabase CLI 2.116.0; treat them as dated evidence,
not as living documentation. The plan's Appendix A is the authority where anything here disagrees with it.

| File | What it is |
| --- | --- |
| `supabase-auth-rls.md` | Anonymous sign-ins, JWT claims, RLS patterns, `join_team` design; 37 live checks on a local stack |
| `supabase-auth-rls.schema.sql` | The live-tested schema (`public` variant; the plan ports it to schema `brigade`) |
| `supabase-auth-rls.test.mjs`, `supabase-auth-rls.test3.mjs` | The two live-check scripts for the schema above |
| `supabase-realtime-delivery.md` | Broadcast-from-database vs `postgres_changes`, drain/ack design, limits |
| `supabase-local-dev-ci.md` | Local stack, `config.toml`, pgTAP, GitHub Actions |
| `claude-plugin-mcp.md` | Plugin, hooks, MCP shim and watcher mechanics; seven empirical runs |
| `claude-plugin-mcp-skeleton/` | The validated plugin skeleton (source, manifests, skills, `fake-adapter.mjs`). Its built `dist/` and `node_modules/` are not committed (the repo `.gitignore` ignores every `dist`); rebuild it as `claude-plugin-mcp.md` section 13 describes (`esbuild --bundle --platform=node --format=esm --target=node20` with a `createRequire` banner, after installing `@modelcontextprotocol/sdk@1.30.0` and `zod`), or use the full copy under `.ignored/research/` on the machine that ran the research |
| `claude-plugin-mcp-evidence/` | Raw hook inputs, `stream-json` transcripts and environment dumps from the seven runs (`*.log` renamed `*.log.txt`) |
| `node-cli-packaging-secrets.md` | Workspace, TypeScript, esbuild, tests, credential storage, process detachment |
| `claude-code-docs-gaps.md` | Verbatim quotes from the Claude Code docs pages the plan relies on |
| `security-threat-model.md` | Threat model; defines the `U-`/`I-`/`E2E-`/`CI-` test ids |

Not committed (local only, under `.ignored/research/`): the fetched documentation pages the agents saved, the three
candidate plans that were judged (`designs/`), the workflow's `verified-facts.md`, and the skeleton's built `dist/`.

## Second research round (2026-08-30 afternoon) — the all-Go, CLI-only redesign

After the first plan review the user chose: empowering-by-default (allow by default, security opt-in); no MCP server (the model drives a `brigade` CLI through the Bash tool, playwright-cli style); and all-Go (one static binary, no Node runtime for teammates). `decisions-2026-08-30.md` is the full decision brief. These four digests and their lab evidence back the plan's Go/CLI sections and Appendix A.7.

| File / dir | What it is |
| --- | --- |
| `decisions-2026-08-30.md` | The decision brief (D3, D6, D18, D20, D22, D31, D32, D33, D34, D35 and the guiding principle) — final, overrides the plan on conflict |
| `go-toolchain-layout-release.md` | Go module layout, dispatch, json/v2, cross-compile sizes, reproducible builds, goreleaser, golangci-lint, tools.mod, CI |
| `supabase-in-go.md` | Hand-rolled GoTrue + PostgREST + Phoenix client, validated against a real local stack; verbatim request/response shapes; the reason the community Go libs are not used |
| `plugin-bootstrap-cli.md` | CLI-only plugin shape: `bin/` on the Bash PATH, which CLAUDE_* vars reach the Bash tool and hooks, the POSIX-sh download-and-verify bootstrap, the skill, permission-rule matching, sandbox notes |
| `testing-conformance-in-go.md` | Go test architecture (testscript, conformance binary, integration, release CI), C-/U-/I-/E2E-/CI- id mapping |
| `supabase-in-go/gotest/` | The working Go client module (`gotrue.go`, `postgrest.go`, `phoenix.go`) and `supabase-in-go/evidence/` its ten request/response logs (`*.log` → `*.log.txt`); `config.toml` and the migration used |
| `plugin-bootstrap-cli.files/` | The probe plugin, `hooks.json`, `SKILL.md`, `plugin.json`, the Go fake `brigade`, and `plugin-bootstrap-cli.evidence/` its stream-json runs |
| `go-lab/` | Go lab sources (detach, socket, exec, flock, pidfile, size/cobra probes; `.golangci.yml`); no built binaries |
| `release-lab/`, `release-plain/`, `bootstrap-lab/`, `modfile-lab/` | goreleaser config + dry-run checksums/artefacts/metadata, plain-build checksums, the tested bootstrap script, `tools.mod` |
| `exp/probe/`, `exp/tscript/` | testing-digest example sources (probe binary, testscript examples) |

Not committed (local only, `.ignored/research/wf2/`): built binaries, the full go-lab/release-lab trees with `dist/`, fetched documentation pages, the running `sb/` stack config with keys. `dist/`, `bin/`, `logs/`, `out/` and `*.out` are ignored at any depth by the seeded Node template, so evidence is stored under non-colliding names and `docs/research/**` is force-un-ignored in `.gitignore`.

## House conventions (2026-09-04) — Phase 6 input

Read from Rjae's own repositories under `/Users/rjae/Development/thinktech/` (read-only, not committed here), not from the Brigade tree. It is the input to P6-2..P6-5, which may not invent a convention this file does not evidence.

| File | What it is |
| --- | --- |
| `house-conventions.md` | The P6-1 convention digest: Rjae's eight stated conventions confirmed / refined / contradicted against `thinktech-web`, `thinktech-app`, `thinktech-php` and `thinktech-api` (every one cited `repo/path:line`), plus the full Brigade target-and-script inventory with each script's caller and inlineability, the collisions with the constraints the execution log fixes, and the branch-vs-tag release analysis for P6-2/P5-10 |
