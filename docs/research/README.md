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
