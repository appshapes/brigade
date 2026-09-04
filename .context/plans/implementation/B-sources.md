# Appendix B: Sources

Record.

## Corrections recorded in the execution log

None recorded.

<!-- verbatim from the one-file plan -->
## Appendix B: Sources

All URLs were fetched on 2026-08-30 by the research digests or while writing this plan; the ones marked "re-fetched" were read again during a synthesis (the first revision) or while writing this second revision.

Claude Code

- https://code.claude.com/docs/en/plugins-reference (re-fetched for this revision: `bin/` "Executables added to the Bash tool's `PATH` and invokable as bare commands while the plugin is enabled", `CLAUDE_PLUGIN_DATA` location and deletion on uninstall, `version` pinning, plugin `settings.json` keys, environment variables exported to hooks)
- https://code.claude.com/docs/en/hooks (re-fetched for this revision: exec form and `${CLAUDE_PLUGIN_ROOT}` substitution into `command` and `args`, per-hook `timeout` defaults, the `SessionEnd` 1.5 s budget and its raise, stdout as context on `SessionStart`/`UserPromptSubmit`, stderr never shown on exit 0, exit-code-2 table)
- https://code.claude.com/docs/en/skills (bootstrap digest: frontmatter fields, `allowed-tools` grant for the invoking turn, `${CLAUDE_PLUGIN_ROOT}`/`${CLAUDE_PLUGIN_DATA}`/`${CLAUDE_SKILL_DIR}` substitutions, the 1,536-character listing budget, `plugin-name:skill-name` naming)
- https://code.claude.com/docs/en/permissions (bootstrap digest: Bash rule matching, `:*` suffix, compound commands and separators, built-in read-only commands, stripped wrappers, ask/deny precedence, redirections, hooks cannot override deny or ask rules)
- https://code.claude.com/docs/en/permission-modes (bootstrap digest: modes as baseline, deny rules in every mode, explicit ask rules still prompt in `bypassPermissions`, `dontAsk` denies ask-rule matches, unattended `-p` denies)
- https://code.claude.com/docs/en/sandboxing (bootstrap digest: Bash subprocess isolation, proxy-based domain allowlist, `strictAllowlist`, filesystem defaults, `allowUnixSockets`, environment inheritance)
- https://code.claude.com/docs/en/cross-session-messaging (re-fetched for the first revision: own-child rules, `crossSessionInbound`, `-p` hold expiry, receiver limits, 30 s rule, socket payload)
- https://code.claude.com/docs/en/mcp (re-fetched for the first revision: `_meta["anthropic/requiresUserInteraction"]`, plugin tool naming; both now historical, D34)
- https://code.claude.com/docs/en/settings-reference
- https://code.claude.com/docs/en/settings
- https://code.claude.com/docs/en/env-vars
- https://code.claude.com/docs/en/hooks
- https://code.claude.com/docs/en/plugins
- https://code.claude.com/docs/en/plugins-reference
- https://code.claude.com/docs/en/plugin-marketplaces
- https://code.claude.com/docs/en/discover-plugins
- https://code.claude.com/docs/en/permissions
- https://code.claude.com/docs/en/permission-modes
- https://code.claude.com/docs/en/skills
- https://code.claude.com/docs/en/sessions
- https://code.claude.com/docs/en/sandboxing
- https://code.claude.com/docs/en/channels and https://code.claude.com/docs/en/channels-reference
- https://code.claude.com/docs/en/agent-view
- https://code.claude.com/docs/en/headless
- https://code.claude.com/docs/en/cli-reference
- https://code.claude.com/docs/en/tools-reference
- https://code.claude.com/docs/en/errors
- https://code.claude.com/docs/en/security
- https://code.claude.com/docs/en/changelog
- https://code.claude.com/docs/en/agent-sdk/sessions, https://code.claude.com/docs/en/agent-sdk/typescript, https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode

Supabase

- https://supabase.com/docs/guides/realtime/protocol (supabase-in-go digest: `vsn` 1.0.0 and 2.0.0 formats, heartbeat every 25 s, join, `access_token`, `system`, `phx_close` events)
- https://docs.postgrest.org/en/latest/references/errors.html (supabase-in-go digest: SQLSTATE → HTTP status table, `P0*` → 500, `42501` 401/403 by role, `PTxxx` custom statuses)
- https://supabase.com/docs/guides/auth/debugging/error-codes (supabase-in-go digest: `refresh_token_already_used`, `refresh_token_not_found`, `session_expired`, `session_not_found`, `user_not_found`)
- https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html (supabase-in-go digest: per-schema default privileges are added to the global ones and cannot revoke globally granted privileges; the `REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC` example "has no effect")
- https://supabase.com/docs/guides/realtime/broadcast (re-fetched for the first revision: `realtime.send` signature, 3-day retention, `SECURITY DEFINER` trigger)
- https://supabase.com/docs/guides/realtime/authorization
- https://supabase.com/docs/guides/realtime/postgres-changes
- https://supabase.com/docs/guides/realtime/settings
- https://supabase.com/docs/guides/realtime/limits
- https://supabase.com/docs/guides/realtime/error_codes
- https://supabase.com/docs/guides/realtime/concepts and https://supabase.com/docs/guides/realtime/architecture
- https://supabase.com/docs/guides/ai-tools/ai-prompts/use-realtime
- https://supabase.com/docs/guides/auth/auth-anonymous
- https://supabase.com/docs/guides/auth/rate-limits
- https://supabase.com/docs/guides/auth/sessions
- https://supabase.com/docs/guides/auth/auth-captcha
- https://supabase.com/docs/guides/auth/auth-hooks/before-user-created-hook
- https://supabase.com/docs/guides/auth/jwt-fields and https://supabase.com/docs/guides/auth/signing-keys
- https://supabase.com/docs/guides/api/api-keys and https://supabase.com/docs/guides/getting-started/api-keys
- https://supabase.com/docs/guides/api/using-custom-schemas
- https://supabase.com/docs/guides/database/hardening-data-api
- https://supabase.com/docs/guides/database/postgres/row-level-security
- https://supabase.com/docs/guides/database/postgres/column-level-security
- https://supabase.com/docs/guides/database/functions
- https://supabase.com/docs/guides/database/database-advisors
- https://supabase.com/docs/guides/database/postgres/timeouts
- https://supabase.com/docs/guides/database/testing and https://supabase.com/docs/guides/database/extensions/pgtap
- https://supabase.com/docs/guides/local-development/cli/getting-started, https://supabase.com/docs/guides/local-development/cli/config, https://supabase.com/docs/guides/local-development/overview, https://supabase.com/docs/guides/local-development/testing/overview, https://supabase.com/docs/guides/local-development/testing/pgtap-extended, https://supabase.com/docs/guides/local-development/seeding-your-database
- https://supabase.com/docs/guides/deployment/ci/testing and https://supabase.com/docs/guides/deployment/managing-environments
- https://supabase.com/docs/guides/cron, https://supabase.com/docs/guides/cron/install, https://supabase.com/docs/guides/cron/quickstart
- https://supabase.com/docs/reference/javascript/initializing, https://supabase.com/docs/reference/javascript/auth-signinanonymously, https://supabase.com/docs/reference/javascript/auth-setsession, https://supabase.com/docs/reference/javascript/auth-refreshsession, https://supabase.com/docs/reference/javascript/auth-getclaims, https://supabase.com/docs/reference/javascript/auth-startautorefresh, https://supabase.com/docs/reference/javascript/auth-signout (re-fetched for the 2026-08-30 review revision: `scope: 'global'`)

Re-fetched for the 2026-08-30 review revision (the first revision; findings applied there and carried forward): https://code.claude.com/docs/en/cross-session-messaging (explicit `hold` vs the default hold, per-session token), https://code.claude.com/docs/en/permission-modes (auto mode default and classifier scope, `dontAsk`, `acceptEdits`), https://code.claude.com/docs/en/hooks (exit-code-2 table), https://code.claude.com/docs/en/mcp (`requiresUserInteraction` in `-p`, SDK `canUseTool`), https://code.claude.com/docs/en/headless (`-p` starts in Manual mode), https://code.claude.com/docs/en/env-vars and https://code.claude.com/docs/en/settings (settings `env` overrides the shell; applies after trust), https://code.claude.com/docs/en/settings-reference (`pluginConfigs` scope), https://code.claude.com/docs/en/plugins-reference (manifest `hooks`/`mcpServers` fields), https://supabase.com/docs/guides/api/using-custom-schemas (grants), https://supabase.com/docs/guides/local-development/cli/config (`[realtime]` keys), https://supabase.com/docs/guides/realtime/error_codes (full list), https://supabase.com/docs/guides/realtime/broadcast (public/private isolation).
- https://supabase.com/docs/reference/cli/supabase-start, -status, -db-reset, -db-push, -link, -migration-new, -gen-types, -test-db, -projects-create, -projects-api-keys, -config-push
- https://supabase.com/docs/reference/api/v1-create-a-project, https://supabase.com/docs/reference/api/v1-get-project-api-keys, https://supabase.com/docs/reference/api/v1-update-auth-service-config
- https://github.com/supabase/setup-cli, https://github.com/supabase/cli, https://github.com/supabase/auth, https://github.com/supabase/postgres, https://github.com/supabase/walrus, https://github.com/supabase/realtime
- https://github.com/supabase/cli/issues/4524, https://github.com/supabase/cli/issues/2724, https://github.com/supabase/cli/issues/1591, https://github.com/orgs/supabase/discussions/20081, https://github.com/orgs/supabase/discussions/21093, https://github.com/orgs/supabase/discussions/37869
- https://supabase.com/pricing, https://supabase.com/docs/guides/platform/manage-your-usage/realtime-messages, https://supabase.com/docs/guides/platform/manage-your-usage/realtime-peak-connections
- https://supabase.com/blog/realtime-broadcast-from-database
- Supabase client sources read as the reference specification for the hand-rolled Go client (supabase-in-go digest): https://github.com/supabase/supabase-js/tree/master/packages/core/realtime-js/src (`RealtimeClient.ts`, `RealtimeChannel.ts`, `lib/constants.ts`, `lib/serializer.ts`), `packages/core/auth-js/src/GoTrueClient.ts` (`signInAnonymously`, `_refreshAccessToken`, `_signOut`) and `lib/fetch.ts` (`handleError`), `packages/core/postgrest-js/src/PostgrestBuilder.ts` (profile headers); supabase-js v2.112.4 (2026-08-24); the archived https://github.com/supabase/realtime-js
- https://github.com/supabase-community/auth-go, https://github.com/supabase-community/postgrest-go, https://github.com/supabase-community/realtime-go, https://github.com/supabase-community/supabase-go (source and `go.mod` read; all rejected, 5.6)

Go toolchain, testing and release engineering (second round)

- https://go.dev/doc/go1.27 (`encoding/json/v2` generally available, `encoding/json` backed by v2 with `GOEXPERIMENT=nojsonv2`, `synctest.Sleep`, `go test` now running the `stdversion` vet check by default, `go mod tidy` merging require blocks), https://go.dev/doc/go1.26, https://go.dev/doc/toolchain (`go` and `toolchain` directives, `GOTOOLCHAIN`), https://go.dev/doc/articles/race_detector (cgo requirement, supported platforms), https://go.dev/blog/rebuild (reproducible builds, `-trimpath`), https://go.dev/doc/build-cover (`go build -cover`, `GOCOVERDIR`, `go tool covdata`, data lost on a fatal signal), https://pkg.go.dev/testing/synctest
- https://github.com/coder/websocket (v1.8.15; zero dependencies), https://pkg.go.dev/github.com/invopop/jsonschema (v0.14.0, draft 2020-12, `Reflector` options, tags), https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6 (v6.0.3), `go doc github.com/rogpeppe/go-internal/testscript` (v1.16.0), https://pkg.go.dev/golang.org/x/term (`ReadPassword`, `IsTerminal`)
- https://goreleaser.com/customization/builds/go/ (env merging, empty default flags, default ldflags with `.Commit`/`.Date`, `mod_timestamp`), https://goreleaser.com/customization/archive/ (`formats: [binary]`, `name_template` at upload), https://goreleaser.com/customization/checksum/, https://goreleaser.com/customization/release/ (`draft`, `prerelease: auto`, `mode`), https://goreleaser.com/ci/actions/, https://goreleaser.com/quick-start/, https://goreleaser.com/errors/dirty/ (`--skip=validate`, `--snapshot`), https://goreleaser.com/customization/snapshots/, https://goreleaser.com/cookbooks/set-a-custom-git-tag/ (`GORELEASER_CURRENT_TAG`)
- https://golangci-lint.run/docs/welcome/install/ci/ and https://golangci-lint.run/docs/welcome/install/ (pinned `install.sh`; `go install`/`go tool` "aren't guaranteed to work"; Homebrew caveat), https://golangci-lint.run/docs/configuration/file/ (v2 config keys), https://golangci-lint.run/docs/linters/, https://golangci-lint.run/docs/product/migration-guide/, https://github.com/golangci/golangci-lint/releases/latest (v2.13.2), https://github.com/golangci/golangci-lint-action (v9), https://raw.githubusercontent.com/golangci/golangci-lint/main/pkg/lint/lintersdb/builder_linter.go
- https://github.com/securego/gosec and https://github.com/securego/gosec/blob/master/RULES.md (G101/G115/G204/G301/G302/G304/G306/G402/G404), https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck (exit status rules, `-mode binary`), https://github.com/golang/govulncheck-action
- https://github.com/actions/checkout (v7), https://github.com/actions/setup-go (v7; `go-version-file`, `cache-dependency-path`), https://github.com/actions/runner-images and its `Ubuntu2404-Readme.md` (Docker 28, shellcheck, gh, jq; `macos-latest` = macOS 26 arm64), https://github.com/supabase/setup-cli (v3, `version` input), https://supabase.com/docs/reference/cli/supabase-test-db
- https://specifications.freedesktop.org/basedir/latest/ (relative `XDG_*` values are ignored)

MCP (historical; the shim was removed by D34)

- https://modelcontextprotocol.io/specification/2025-06-18/server/tools, https://modelcontextprotocol.io/specification/2025-06-18/basic/transports, https://github.com/modelcontextprotocol/typescript-sdk, https://www.npmjs.com/package/@modelcontextprotocol/sdk

Node, TypeScript and tooling (historical; the toolchain was replaced by D35, and these pages back the first-round digests that remain committed)

- https://nodejs.org/api/child_process.html, https://nodejs.org/api/process.html, https://nodejs.org/api/readline.html, https://nodejs.org/api/net.html, https://nodejs.org/api/util.html, https://nodejs.org/api/test.html, https://nodejs.org/api/globals.html
- https://nodejs.org/docs/latest-v24.x/api/typescript.html, https://nodejs.org/docs/latest-v24.x/api/cli.html
- https://github.com/nodejs/Release, https://nodejs.org/en/about/previous-releases
- https://docs.npmjs.com/cli/v11/using-npm/workspaces, https://docs.npmjs.com/cli/v11/commands/npm-ci, https://docs.npmjs.com/cli/v11/configuring-npm/package-json
- https://esbuild.github.io/getting-started/, https://github.com/evanw/esbuild/issues/1921
- https://www.typescriptlang.org/docs/handbook/release-notes/typescript-6-0.html, https://www.typescriptlang.org/docs/handbook/project-references.html, https://www.typescriptlang.org/tsconfig/, https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/
- https://typescript-eslint.io/users/dependency-versions/, https://eslint.org/docs/latest/use/migrate-to-10.0.0, https://eslint.org/docs/latest/use/configure/configuration-files
- https://zod.dev/library-authors
- https://vitest.dev/guide/projects, https://github.com/egoist/tsup
- https://specifications.freedesktop.org/basedir/latest/
- https://www.gnu.org/software/bash/manual/html_node/Exit-Status.html, https://man.freebsd.org/cgi/man.cgi?query=sysexits&sektion=3
- https://www.postgresql.org/docs/current/pgcrypto.html, https://www.postgresql.org/docs/current/functions-uuid.html
- https://docs.postgrest.org/en/latest/references/transactions.html
- https://github.com/citusdata/pg_cron
- https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.security/convertfrom-securestring, https://www.mankier.com/1/secret-tool
- https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md
- https://semver.org/
- https://genai.owasp.org/llmrisk/llm01-prompt-injection/

Local ground truth

- `.context/plans/claude-code-team-messaging-logical-plan.md` (2026-08-29)
- The workflow's `verified-facts.md` (reproduced as Appendix A.1-A.6) and the seven first-round research digests: `supabase-auth-rls`, `supabase-realtime-delivery`, `supabase-local-dev-ci`, `claude-plugin-mcp`, `node-cli-packaging-secrets`, `claude-code-docs-gaps`, `security-threat-model` (with `supabase-auth-rls.schema.sql`, the two live-check scripts, and the validated Node plugin skeleton under `research/claude-plugin-mcp-skeleton/`); committed under `docs/research/` by P0-0 in commit `6386046`.
- The decision brief of 2026-08-30 (`docs/research/decisions-2026-08-30.md` after P0-2) and the four second-round digests with their lab sources and evidence (reproduced as Appendix A.7): `go-toolchain-layout-release` (lab module, release lab, bootstrap lab, modfile lab, toolchain lab), `supabase-in-go` (the `gotest` Go module, the minimal migration and ten request/response logs), `plugin-bootstrap-cli` (the probe and bootstrap plugins, the fake `brigade`, ten `stream-json` runs), `testing-conformance-in-go` (experiments E1-E14); committed under `docs/research/` by P0-2 with this revision.
- The three candidate plans judged on 2026-08-30: `vertical-proof-first`, `protocol-portability-first`, `security-risk-first`, and the three judge rationales.
