# Know the layout

| Path | What |
| --- | --- |
| `cmd/brigade` | the one shipped binary: the plugin harness, the bundled Supabase adapter, the Syncthing sync adapter |
| `cmd/brigade-webhook-gateway`, `internal/gateway`, `internal/gateway/webhook` | the gateway harness prefix every consumer shares, and the self-hosted webhook gateway: one session that relays to an outside service, signed |
| `cmd/brigade-adapter-fs`, `internal/adapters/fs` | the reference adapter (dev and test only) |
| `cmd/brigade-conformance`, `internal/conformance` | the conformance suite (dev and test only) |
| `cmd/brigade-schema`, `internal/protocol/schema` | the protocol's JSON Schema generator |
| `cmd/brigade-release-email`, `internal/releaseemail` | the release-notes email's layout (dev only) |
| `cmd/brigade-docs`, `internal/docscheck` | the documentation gate and the make-target generator (dev only) |
| `internal/protocol`, `internal/adapterkit` | the wire types with `Validate()`, and the shared adapter plumbing |
| `internal/harness/*` | the plugin side: config, hooks, the watcher, inbound pipeline, frame, commands, session map, folder sync |
| `internal/adapters/supabase` | the bundled adapter, a child process of the harness |
| `internal/syncthing`, `internal/syncadapters/syncthing`, `internal/harness/foldersync` | the Syncthing engine (the one daemon Brigade runs), the sync adapter's policy over it, and the harness side of the sync protocol |
| `plugin/` | the Claude Code package: manifest, hooks, nine skills, the sh bootstrap and the release pins |
| `muse/brigade/` | the Muse Code package: native manifest, one wrapper per hook, the skills, copies of the pins |
| `codex/brigade/`, `.agents/plugins/` | the Codex CLI package (native manifest, `hooks/hooks.json`, the skills, copies of the pins) and the marketplace file that names it |
| `supabase/` | migrations, pgTAP tests, Edge Functions (the mail core, two connectors, the Slack pair), local stack config |
| `scripts/` | the proof scripts, the headless smoke test, the release sequence, the gateway installers |
| `scripts/ci/` | the CI checks, their fixtures and the Go drift tests ([scripts/ci/README.md](../../../scripts/ci/README.md)) |
| `scripts/experiments/` | the experiment drivers, one directory per report |
| `docs/` | the five persona trees, the reference documents, `experiments/`, `research/`, `email/` |
| `.claude/` | the repository's skills and the agents of the agentic loop |
| `.github/workflows/` | CI, release, release notes, keep-alive, the agentic loop |
| `.context/plans/` | the plans, the execution log and its archive, the proof results, agent memory |
| `docs/allowed-deps.txt`, `tools.mod` | the shipped binary's module allowlist; the dev tools |
