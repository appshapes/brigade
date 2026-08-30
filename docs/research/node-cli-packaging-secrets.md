# Brigade: Node/TypeScript packaging, process handling, and local secrets

Date: 2026-08-30. Scope: engineering choices for a small npm-workspaces monorepo containing
`@brigade/protocol` (types + zod schemas + NDJSON helpers), `@brigade/adapter-supabase` (CLI),
`@brigade/mcp-shim` (stdio MCP server), and `@brigade/watcher` (detached daemon), targeting
Node 24 on macOS/Linux (Windows best-effort), shipped as a Claude Code plugin and optionally via npm/npx.

How to read confidence marks:

- **verified**: read from the official page cited on this date, or executed locally on this machine
  (Node v24.16.0, npm 11.13.0, macOS arm64).
- **likely**: consistent with docs read today but not directly exercised, or based on a secondary source.
- **uncertain**: my judgement or memory; validate in the first spike.

Version numbers below are `latest` dist-tags as read from `registry.npmjs.org` today.

---

## 1. Runtime target: Node 24 LTS

| Fact | Value | Confidence / source |
| --- | --- | --- |
| Node 24 "Krypton" | Active LTS since 2025-10-28; Maintenance from 2026-10-20; EOL 2028-04-30 | verified: https://github.com/nodejs/Release |
| Node 26 | Current since 2026-05-05; Active LTS from 2026-10-28; EOL 2029-04-30 | verified: same |
| Node 25 | EOL (2026-03-31) | verified: https://nodejs.org/en/about/previous-releases |
| Node 27+ | Cadence changes to one major per year with a 6-month alpha channel (27.0.0-alpha.x from Oct 2026, 27.0.0 in Apr 2027); every major becomes LTS | likely: https://nodejs.org/en/blog/announcements/evolving-the-nodejs-release-schedule (via search summary; not read in full) |
| Type stripping (`.ts` run directly) | On by default since v23.6.0/v22.18.0; **Stable** since v24.12.0 | verified: https://nodejs.org/docs/latest-v24.x/api/typescript.html and local run |
| `--experimental-transform-types` | Exists in v24 (Release candidate); needed for enums/namespaces-with-code/parameter properties/`import x = require()`; prints an ExperimentalWarning | verified: v24 docs + local run |
| `.ts` under `node_modules` | Refused (`ERR_UNSUPPORTED_NODE_MODULES_TYPE_STRIPPING`) — but realpath is used, so a **workspace symlink to `packages/*/src/*.ts` works** unless `--preserve-symlinks` is set | verified locally (see section 5) |
| `util.parseArgs` | Stable since v20.0.0; no subcommand support | verified: https://nodejs.org/api/util.html |
| `node:test` | Stable; default discovery includes `**/*.test.{ts,mts,cts}` when type stripping is enabled; coverage still experimental | verified: https://nodejs.org/api/test.html + local run |
| `--conditions`/`-C` | Stable (v22.9.0/v20.18.0) | verified: https://nodejs.org/docs/latest-v24.x/api/cli.html |
| `--env-file`, `--env-file-if-exists` | Stable since v24.10.0 | verified: same |
| `import.meta.dirname` / `import.meta.filename` | Available (string) | verified locally |

Recommendation: `"engines": { "node": ">=24" }` everywhere, CI matrix on 24 and 26. Do not use Node 26-only APIs.

Plugin-specific gotcha (verified in the plugins reference): `.mcp.json`/`hooks.json` invoke `node` by PATH. Claude Code
does not ship a Node runtime for plugins, so the user's PATH must resolve to Node >= 24. The SessionStart hook
should run `node --version`-equivalent self-check (`process.versions.node`) and print one clear line if it is too old;
never assume `process.execPath` of Claude Code is Node.

---

## 2. Workspace tooling: npm workspaces (not pnpm)

Decision: **npm workspaces with a single root `package-lock.json`**.

Why not pnpm: Claude Code's plugin dependency installer runs only when the plugin root contains `package.json`
plus a supported lockfile, in priority order `bun.lock` → `bun.lockb` → `npm-shrinkwrap.json` → `package-lock.json`;
`yarn.lock` and `pnpm-lock.yaml` are skipped on purpose ("they support resolution-time config hooks that bypass
`--ignore-scripts`"). Install is `npm ci --ignore-scripts` (or `bun install --frozen-lockfile --ignore-scripts`),
frozen, with a 60-second timeout; on timeout the plugin still loads with partial `node_modules`
(verified: https://code.claude.com/docs/en/plugins-reference). Bun is not installed on this machine (verified facts),
so npm is the only lockfile we can produce and test locally.

Even though npm ci is available, the plan is to **not need it at all**: the plugin directory ships single-file
bundles and carries no `package.json`, so Claude Code performs no install step, no timeout applies, and
`--ignore-scripts` is moot. That removes the single largest source of install-time failures.

npm workspace facts (verified: https://docs.npmjs.com/cli/v11/using-npm/workspaces and
https://docs.npmjs.com/cli/v11/commands/npm-ci):

- `workspaces: ["packages/*"]` in the root; `npm install` symlinks each package into root `node_modules/@brigade/<name>`.
- One root lockfile covers all workspaces; `npm ci` requires it to agree with every `package.json` and deletes
  `node_modules` first.
- `npm run <script> --workspaces --if-present` runs a script in each workspace (in `workspaces` array order);
  `--workspace=@brigade/protocol` targets one.
- In CI use `npm ci --ignore-scripts` as well: esbuild's `postinstall` (`node install.js`) is an optimisation only;
  the platform binary arrives via `optionalDependencies` and the JS API works without the script
  (verified: https://esbuild.github.io/getting-started/ and local install under `--ignore-scripts`).

Layout:

```text
brigade/
  package.json              private; workspaces; root devDependencies (typescript, esbuild, eslint, prettier)
  package-lock.json
  tsconfig.base.json
  tsconfig.json             "solution" file: files: [], references to each package
  eslint.config.js          JS, not TS (avoids ESLint's unstable native-TS-config flag)
  build.mjs                 esbuild driver; writes plugin/dist/*.js
  packages/
    protocol/               @brigade/protocol        (publishable library: types, zod schemas, NDJSON helpers)
    adapter-supabase/       @brigade/adapter-supabase (bin: brigade-adapter-supabase)
    mcp-shim/               @brigade/mcp-shim         (bin: brigade-mcp)
    watcher/                @brigade/watcher          (bin: brigade-watcher)
    cli/                    @brigade/cli (optional, user-facing `brigade` command for join/profile/doctor)
  plugin/                   Claude Code plugin root (marketplace entry uses "path": "plugin")
    .claude-plugin/plugin.json
    .mcp.json
    hooks/hooks.json
    skills/team-messaging/SKILL.md
    dist/                   adapter-supabase.js, mcp.js, watcher.js, hook.js (+ .map) — self-contained ESM
```

Distribution paths (verified in the plugins reference):

- Marketplace entry with a subdirectory: `{ "source": "<archive or github source>", "path": "plugin" }` — only files
  under `path` are copied to the cache; `bin/` in a plugin cannot be used when the plugin is distributed via
  claude.ai organisation settings (use `scripts/`/hooks instead).
- `github:owner/repo#vX.Y.Z` (release tag) or `npm:@brigade/plugin` (installed with `npm ci --ignore-scripts`) are
  also valid sources; `"version"` in `plugin.json` pins the version users see, so bump it on every release.
- Consequence: `plugin/dist/*.js` must exist in whatever the marketplace points at. Either commit built output
  (with a CI job that runs `make build && git diff --exit-code plugin/dist`) or point the marketplace at a GitHub
  release tag whose tree was built by CI. Committing dist is simplest for a private repo and keeps
  `claude --plugin-dir ./plugin` working from a checkout.

---

## 3. TypeScript: pin 6.0.x now; 7.x later

State of the ecosystem today (verified):

- `typescript@latest` = **7.0.2** (2026-07-08), the native Go compiler distributed as JS shim + per-platform
  `optionalDependencies` (`@typescript/typescript-darwin-arm64`, ...). It has **no stable programmatic API**; the
  announcement says a new API is expected in 7.1 (https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/).
- `typescript@6` latest = **6.0.3**, the final JS-based release and the bridge to 7 (https://www.typescriptlang.org/docs/handbook/release-notes/typescript-6-0.html).
  The `@typescript/typescript6` package (6.0.2) exists for side-by-side installs via npm alias.
- `typescript-eslint@8.68.0` peer range is `typescript >=4.8.4 <6.1.0` — it cannot load TS 7
  (verified: https://typescript-eslint.io/users/dependency-versions/; TS 7 support request closed "not planned"
  until the 7.1 API — likely, from search summaries).
- TypeScript 6.0 changed defaults that matter here: `strict: true`, `module: esnext`, `target: es2025`,
  `types: []` (you must list `"types": ["node"]`), `rootDir` defaults to the tsconfig directory, `noUncheckedSideEffectImports: true`;
  `moduleResolution node/node10`, `baseUrl`, `outFile`, `module amd/umd/system` are deprecated errors.

Decision: **`"typescript": "~6.0.3"` as the single toolchain version** for `tsc -b`, declaration emit and
typescript-eslint. Type checking four small packages does not need the 10x native speed-up. Revisit when
typescript-eslint supports 7.1; at that point either flip the pin or add `"@typescript/native": "npm:typescript@7"`
for a fast `tsc` while ESLint keeps 6 (the pattern Microsoft documents).

`tsconfig.base.json` (rationale inline):

```jsonc
{
  "compilerOptions": {
    "target": "es2024",                      // Node 24 syntax level; esbuild's target=node24 governs the bundles anyway
    "lib": ["es2024"],
    "module": "nodenext",                    // respects package.json "type": "module", enforces explicit extensions
    "moduleResolution": "nodenext",
    "types": ["node"],                       // TS 6 default is [] — must be explicit
    "strict": true,
    "exactOptionalPropertyTypes": true,
    "noUncheckedIndexedAccess": true,
    "noImplicitOverride": true,
    "noFallthroughCasesInSwitch": true,
    "verbatimModuleSyntax": true,            // forces `import type`, which Node's type stripping requires
    "erasableSyntaxOnly": true,              // no enums/namespaces/parameter properties → runs under Node without transform-types
    "isolatedModules": true,
    "rewriteRelativeImportExtensions": true, // source imports use './x.ts'; tsc emits './x.js'; also implies allowImportingTsExtensions
    "composite": true,                       // project references: tsc -b builds protocol before dependants
    "declaration": true,
    "declarationMap": true,
    "sourceMap": true,
    "skipLibCheck": true
  }
}
```

Per package (`packages/<name>/tsconfig.json`):

```jsonc
{
  "extends": "../../tsconfig.base.json",
  "compilerOptions": { "rootDir": "src", "outDir": "dist", "tsBuildInfoFile": "dist/.tsbuildinfo" },
  "include": ["src"],
  "references": [{ "path": "../protocol" }]   // omit in protocol itself
}
```

Tests (`packages/<name>/test/*.test.ts`) get their own `tsconfig.test.json` extending the base with
`"noEmit": true`, `"rootDir": ".."`, `"include": ["test", "src"]`, `"composite": false` so that
`.test.ts` files are never emitted into `dist` (which would make `node --test` pick up the compiled copies).

Root `tsconfig.json` is a solution file: `{ "files": [], "references": [ {"path":"packages/protocol"}, ... ] }`.
`tsc -b` builds in dependency order and is incremental; `tsc -b --noEmit` is not a supported combination, so
"typecheck" = `tsc -b` (it emits `dist/` for the packages, which is cheap and also what `npm publish` needs)
(verified: https://www.typescriptlang.org/docs/handbook/project-references.html;
https://www.typescriptlang.org/tsconfig/ for `allowImportingTsExtensions` defaulting to true under
`rewriteRelativeImportExtensions`).

Source-import rule (verified: Node typescript docs): write relative imports as `./file.ts` and type-only imports as
`import type` / `import { x, type T }`. That single convention lets the same source run under `node` directly,
compile with `tsc`, and bundle with esbuild.

---

## 4. Bundling: esbuild directly, one self-contained ESM file per entrypoint

Options considered (all verified today):

| Tool | Version | Status | Notes |
| --- | --- | --- | --- |
| esbuild | 0.28.2 | active; `engines >=18` | stable API; platform binary via optionalDependencies; works under `--ignore-scripts` |
| tsup | 8.5.1 | README: "This project is not actively maintained anymore. Please consider using tsdown instead." (https://github.com/egoist/tsup) | depends on esbuild ^0.27 |
| tsdown | 0.22.14 (`rc` 0.23.0-rc.1) | official Rolldown project; pre-1.0 | needs Node ^22.18 or >=24.11; has dts, shebang (`exe`), `noExternal`; migration guide from tsup |
| tsx | 4.23.13 | active | dev-time TS runner; unnecessary now that Node strips types |

Decision: a 40-line `build.mjs` calling esbuild's JS API. Reasons: four entrypoints, no plugin system needed,
esbuild is the most stable piece in this stack, and tsup's own maintainers point elsewhere. tsdown is the
reasonable upgrade path if we want `.d.ts` bundling or its CLI conveniences once it reaches 1.0.

Verified locally: `esbuild@0.28.2` installed with `--ignore-scripts` bundled a TS entry importing `zod@4.5.4` and
`node:util` into one 164 KB ESM file that runs from a directory with no `node_modules`; remaining imports were only
`node:module` and `node:util`.

```js
// build.mjs — run after `tsc -b` (typecheck) — emits plugin/dist/*.js
import { build } from 'esbuild';
import { readFileSync } from 'node:fs';

const version = JSON.parse(readFileSync('packages/protocol/package.json', 'utf8')).version;

await build({
  entryPoints: {
    'adapter-supabase': 'packages/adapter-supabase/src/main.ts',
    mcp: 'packages/mcp-shim/src/main.ts',
    watcher: 'packages/watcher/src/main.ts',
    hook: 'packages/watcher/src/hook.ts',
  },
  outdir: 'plugin/dist',
  bundle: true,
  platform: 'node',          // node builtins external, "node" export condition, main+module fields
  format: 'esm',
  target: 'node24',
  packages: 'bundle',        // default; everything except node:* goes into the file
  sourcemap: 'linked',       // .map next to the file; keep for stack traces in bug reports
  minify: false,             // reviewable output matters more than size for a security-sensitive plugin
  legalComments: 'none',
  define: { __BRIGADE_VERSION__: JSON.stringify(version) },
  banner: {
    js: [
      '#!/usr/bin/env node',
      'import { createRequire as __createRequire } from "node:module";',
      'const require = __createRequire(import.meta.url);',
    ].join('\n'),
  },
});
```

Notes:

- The `createRequire` banner is the standard fix for `Dynamic require of "x" is not supported` when a CommonJS
  dependency ends up inside ESM output (verified: https://github.com/evanw/esbuild/issues/1921 and the esbuild API
  page's banner section). Keep it even if today's deps are pure ESM.
- Native addons (`.node`) cannot be bundled; if one is ever required it must ship on disk and be `external`
  (relevant to `@napi-rs/keyring`, section 11).
- `#!/usr/bin/env node` is only meaningful if the file is executed directly; hooks/.mcp.json invoke
  `node <file>` explicitly, so the shebang is a convenience for `npx`/`bin` use.
- `chmod +x` is not preserved by esbuild; set it in the Makefile `build` target for files referenced as executables.
- The npm-published packages (`@brigade/protocol`, and the CLIs if published) use `tsc` output in `dist/` with
  `exports`/`bin`, not the bundle; the plugin uses the bundles. Both come from the same sources.

---

## 5. Tests: `node:test` on Node's own type stripping

Decision: **`node --test` with native `.ts` execution, no tsx, no vitest**, plus one export condition so tests
import workspace packages from source.

What was verified locally on Node 24.16.0:

- `node --test` with no arguments discovered and ran `sample.test.ts` (default patterns include `.ts`).
- A workspace package whose `exports` map has `"development": "./src/index.ts"` alongside `"default": "./dist/index.js"`
  is importable from another workspace with `node --conditions=development file.ts` **before any build**, because Node
  resolves the `node_modules/@brigade/protocol` symlink to its real path, which is not under `node_modules`.
  With `--preserve-symlinks` the same import fails with `ERR_UNSUPPORTED_NODE_MODULES_TYPE_STRIPPING`.
- Without the condition and without `dist/`, the import fails with `ERR_MODULE_NOT_FOUND` (expected).

So the root script is:

```json
"test": "node --conditions=development --test \"packages/*/test/**/*.test.ts\""
```

(explicit glob rather than default discovery, so `dist/` and `plugin/dist/` are never scanned).

Rationale versus vitest 4.1.11 (verified: `engines ^20 || ^22 || >=24`; `test.projects` replaces the deprecated
workspace file — https://vitest.dev/guide/projects):

- node:test: zero dev-dependencies, the runtime under test is the runtime we ship, `mock.timers`/`mock.module`
  and snapshot testing are stable, `--experimental-test-coverage` exists (experimental) — adequate for CLI/daemon
  code. Cost: `.test.ts` files must obey `erasableSyntaxOnly` (already enforced), and the `development` condition
  must be maintained in each package's `exports`.
- vitest: better watch UX, richer mocking, stable coverage via V8, Vite resolution of TS sources across packages
  without the condition trick; costs ~100 transitive dev deps and a second module loader whose behaviour can differ
  from Node's (ESM/CJS interop, `import.meta`). Choose it if the team already knows it; it does not affect the
  shipped artefacts.

Integration tests that spawn the built CLI should run against `plugin/dist/*.js` (the artefact users get), using
`spawn(process.execPath, [distFile, ...])` — this exercises the bundle, the exit codes and the NDJSON framing
end to end.

---

## 6. Lint and format

Current versions (verified): ESLint **10.9.1** (`engines ^20.19 || ^22.13 || >=24`; eslintrc removed; flat config only;
config lookup now walks up from each file — https://eslint.org/docs/latest/use/migrate-to-10.0.0),
typescript-eslint **8.68.0** (peer `eslint ^8.57 || ^9 || ^10`, `typescript <6.1`), Prettier **3.9.6**,
Biome **2.5.11** (prebuilt `@biomejs/cli-*` optional deps, no install scripts; `noFloatingPromises` exists since 2.0 but
sits in the `nursery` group and requires Biome's own type-inference scanner — https://biomejs.dev/linter/rules/no-floating-promises/).

Decision: **ESLint 10 flat config + typescript-eslint (type-aware) + Prettier**, because the rules that catch real
bugs in daemons and CLIs are type-aware (`@typescript-eslint/no-floating-promises`, `no-misused-promises`,
`switch-exhaustiveness-check`, `require-await`, `no-unnecessary-condition`) and typescript-eslint's inference uses the
real TypeScript program. Biome is a fine single-binary alternative for formatting speed, but its type-aware rules
are still nursery-grade and its inference is not tsc.

Use `eslint.config.js` (plain JS). ESLint can load `eslint.config.ts` natively on Node >= 22.13 only behind the
`unstable_native_nodejs_ts_config` flag (or via jiti >= 2.2) — not worth it (verified:
https://eslint.org/docs/latest/use/configure/configuration-files).

```js
// eslint.config.js
import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import { defineConfig, globalIgnores } from 'eslint/config';

export default defineConfig([
  globalIgnores(['**/dist/**', 'plugin/dist/**']),
  js.configs.recommended,
  ...tseslint.configs.strictTypeChecked,
  ...tseslint.configs.stylisticTypeChecked,
  {
    languageOptions: { parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname } },
    rules: {
      '@typescript-eslint/switch-exhaustiveness-check': 'error',
      'no-console': 'error',                          // all diagnostics go through the logger (stderr)
    },
  },
  { files: ['**/*.js', '**/*.mjs'], ...tseslint.configs.disableTypeChecked },
]);
```

Prettier: `.prettierrc` with `{ "singleQuote": true, "printWidth": 100 }`; run `eslint-config-prettier` last to
disable formatting rules (uncertain whether `defineConfig` needs it spelled as an object entry; check the package README).

`no-console` matters: the MCP shim and the watcher must never write to stdout except deliberate protocol/event
lines (section 12).

---

## 7. zod: v4 (4.5.4)

Verified: `zod@latest` = 4.5.4 with subpath exports `./v4`, `./v3`, `./mini`, `./v4-mini`, `./v4/core`. The top-level
`zod` import is Zod 4 in 4.x. `@modelcontextprotocol/server@2.0.0` depends on `zod ^4.2.0` directly (not a peer);
`@modelcontextprotocol/sdk@1.30.0` has peer `zod ^3.25 || ^4.0`.

Decisions:

- Application code (`adapter`, `mcp-shim`, `watcher`): `import * as z from 'zod'`; dependency `"zod": "^4.5.4"`.
- `@brigade/protocol`: also `import * as z from 'zod'` for now. If it is later published for third-party adapter
  authors, switch its imports to the `zod/v4/core` "permalink" and declare `"peerDependencies": { "zod": "^4.0.0" }`,
  per the library-author guidance (verified: https://zod.dev/library-authors). Not needed while all consumers are
  in this repo and bundled.
- Use `z.looseObject({...})` for every wire envelope so unknown fields are preserved (forward compatibility, section 14);
  use `z.strictObject` only for local config files where a typo should fail fast.
- `z.toJSONSchema(schema)` can generate the JSON Schema published with the protocol RFC from the same source of truth.
- `zod/mini` is for bundle-size-sensitive browsers; irrelevant here.

Why v4 over v3: v3 is legacy in the 4.x package (`zod/v3`), the MCP v2 server package requires v4, and v4 is what
new tooling targets. The verified probe (section 4) bundled v4 without issues.

---

## 8. CLI argument parsing: `node:util` `parseArgs`

Verified: `parseArgs` is Stable (v20+), supports `type: 'string'|'boolean'`, `short`, `multiple`, `default`,
`allowNegative`, `strict`, `tokens`, `allowPositionals`; it does **not** model subcommands. `commander@15.0.0` is
ESM-only and requires Node >= 22.12 (verified) — usable, but it adds ~30 KB per bundle and a second source of
truth for the command grammar.

Decision: **`parseArgs` with a tiny two-level dispatch table** for the adapter, watcher and hook entrypoints.
The grammar is fixed by the protocol RFC (`describe`, `session register|heartbeat|list|close`,
`message send|watch|ack`), so a table is clearer than a framework. Use commander only if a user-facing `brigade`
CLI grows interactive help, many options and prompts.

```ts
import { parseArgs } from 'node:util';

const argv = process.argv.slice(2);
const [group, verb] = argv;                    // e.g. 'message', 'send'
const rest = argv.slice(group === 'describe' ? 1 : 2);
const { values, positionals } = parseArgs({
  args: rest,
  strict: true,
  allowPositionals: false,
  options: {
    profile: { type: 'string', short: 'p' },
    to: { type: 'string' },
    'idempotency-key': { type: 'string' },
    json: { type: 'boolean', default: true },
    'log-level': { type: 'string', default: process.env.BRIGADE_LOG_LEVEL ?? 'info' },
  },
});
// parseArgs throws ERR_PARSE_ARGS_UNKNOWN_OPTION / ERR_PARSE_ARGS_INVALID_OPTION_VALUE → exit 2 (usage)
```

Rules that follow from the logical plan: never accept secrets as options (`--join-secret` is forbidden; read it
from stdin or a prompt); message bodies come from stdin JSON, not `--body`.

---

## 9. Spawning children without a shell

Facts (verified: https://nodejs.org/api/child_process.html):

- `spawn`/`execFile` default to `shell: false`; arguments are passed as an array, so no quoting or injection.
- On Windows, `.bat`/`.cmd` files cannot be executed without a shell (`execFile` fails; DEP0190 discourages
  `shell: true`). npm's Windows `bin` shims are `.cmd` files.
- `execFile` buffers stdout up to `maxBuffer` (default 1 MiB) and errors beyond it.
- `stdio` accepts `'pipe'`, `'ignore'` (attaches `/dev/null`), `'inherit'`, a stream, or a parent file descriptor
  (sharing sockets this way is unsupported on Windows).
- `timeout` + `killSignal` (default `SIGTERM`) and `signal: AbortSignal` are available on `spawn`.
- `windowsHide: true` suppresses a console window on Windows.

Decision: the harness side (MCP shim, hook, watcher) always launches the adapter as
**`spawn(process.execPath, [adapterEntry, ...args])`** when the adapter is our own bundled JS — this avoids PATH
lookups, `.cmd` shims and the Windows shell problem in one move. Third-party adapters are configured in the profile as
`{ "command": "/abs/path/or/name", "args": [...] }` and spawned with the same code path (`shell: false`); a
`.cmd` on Windows is the adapter author's problem to avoid (document: "provide a `.exe` or `node script.js`").

Request/response call (`session list`, `message send`): JSON in on stdin, JSON out on stdout, NDJSON logs on stderr.

```ts
import { spawn } from 'node:child_process';

export async function callAdapter(cmd: string, args: string[], input: unknown, opts: { timeoutMs: number; env: NodeJS.ProcessEnv }) {
  const child = spawn(cmd, args, {
    stdio: ['pipe', 'pipe', 'pipe'],
    env: opts.env,                       // allow-listed env (section 12): PATH, HOME, BRIGADE_*, XDG_*, CLAUDE_PLUGIN_DATA
    windowsHide: true,
    signal: AbortSignal.timeout(opts.timeoutMs),
    killSignal: 'SIGTERM',
  });
  const out: Buffer[] = []; const err: Buffer[] = []; let outBytes = 0;
  child.stdout.on('data', (b: Buffer) => { outBytes += b.length; if (outBytes > 4 * 1024 * 1024) child.kill(); else out.push(b); });
  child.stderr.on('data', (b: Buffer) => { err.push(b); forwardStderrLines(b); });   // re-log at debug, never to stdout
  child.stdin.on('error', () => {});     // EPIPE if the child exits early; the 'close' handler reports the real cause
  child.stdin.end(JSON.stringify(input) + '\n');
  const [code, signal] = await new Promise<[number | null, NodeJS.Signals | null]>((resolve, reject) => {
    child.once('error', reject);         // ENOENT, EACCES, AbortError
    child.once('close', (c, s) => resolve([c, s]));
  });
  return { code, signal, stdout: Buffer.concat(out).toString('utf8'), stderr: Buffer.concat(err).toString('utf8') };
}
```

Streaming events (`message watch`): NDJSON, one event per line, consumed with `readline`.

```ts
import { createInterface } from 'node:readline';

const child = spawn(cmd, ['message', 'watch', '--profile', profile], { stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true });
const rl = createInterface({ input: child.stdout, crlfDelay: Infinity });
for await (const line of rl) {           // must start iterating immediately after createInterface, or lines are lost
  if (line.length > MAX_LINE) { logger.warn({ len: line.length }, 'oversized event dropped'); continue; }
  if (line.trim() === '') continue;
  const parsed = WatchEvent.safeParse(JSON.parse(line));   // zod looseObject; unknown event kinds are ignored, not fatal
  ...
}
```

(verified: https://nodejs.org/api/readline.html — `createInterface` starts consuming immediately; "having
asynchronous operations between interface creation and asynchronous iteration may result in missed lines";
`crlfDelay: Infinity` treats `\r\n` as one break; breaking out of the loop closes the interface.)

Rules:

- Never pass message bodies, secrets or JSON blobs as argv (process listings, `ps`, shell history, argument length limits).
- Always attach `error` and `close` handlers; `exit` can fire before stdio drains.
- Distinguish "adapter produced an error result" (exit code + JSON on stdout) from "adapter could not be started"
  (`error` event, ENOENT) and from "adapter died" (signal). Map all three to distinct harness error codes.
- For long-running children (`watch`) hold an `AbortController` and abort it on shutdown; escalate SIGTERM → SIGKILL
  after a grace period.

---

## 10. Detaching the watcher daemon

Facts (verified: child_process docs):

- POSIX: `detached: true` makes the child a session and process-group leader (`setsid`), so it does not receive the
  parent's terminal `SIGHUP` and is not killed with the parent's process group.
- Windows: `detached: true` gives the child its own console; combine with `windowsHide: true`.
- "When using the `detached` option to start a long-running process, the process will not stay running in the
  background after the parent exits unless it is provided with a `stdio` configuration that is not connected to the
  parent." Use `'ignore'` or file descriptors, then `subprocess.unref()` so the parent's event loop does not wait.
- Claude Code hooks already run "in their own session without a controlling terminal" on macOS/Linux; `SessionEnd`
  hooks share a 1.5 s budget (verified: https://code.claude.com/docs/en/hooks). The SessionStart hook must therefore
  return quickly and must not keep the watcher's stdio pipes open, or Claude Code waits on them.

Launch (from `hook.js session-start`):

```ts
import { spawn } from 'node:child_process';
import { openSync } from 'node:fs';

const logFd = openSync(logPath, 'a', 0o600);
const child = spawn(process.execPath, [watcherEntry, '--session', sessionId], {
  detached: true,
  stdio: ['ignore', logFd, logFd],   // stdout is not a protocol channel for the watcher; both go to the log file
  env: watcherEnv,                   // includes CLAUDE_CODE_MESSAGING_SOCKET/TOKEN, CLAUDE_PID, CLAUDE_CONFIG_DIR, CLAUDE_PLUGIN_DATA
  windowsHide: true,
  cwd: os.homedir(),                 // never the project dir: keeps the cwd unlinkable/deletable
});
child.unref();
closeSync(logFd);                    // the child holds its own copy
```

Single instance per session and liveness:

- Pidfile `${runtimeDir}/watchers/<session_id>.json` = `{ pid, startedAt, sessionId, socketPath, watcherVersion }`,
  created with `fs.openSync(path, 'wx', 0o600)` (exclusive create). On `EEXIST`, read it and test liveness.
- Liveness = `process.kill(pid, 0)` (no signal is sent; throws `ESRCH` if absent, `EPERM` if it exists but belongs to
  another user; on Windows it tests existence too — verified: https://nodejs.org/api/process.html), **and** the
  pidfile's `startedAt` must be within a few seconds of the process start time read via `ps -o lstart= -p <pid>`
  (POSIX) to defeat PID reuse (likely; alternative below avoids the problem entirely).
- Preferred alternative: the watcher listens on its own control socket `${runtimeDir}/w-<short-session>.sock`
  (Windows: `\\.\pipe\brigade-<session>`); "alive" means "connect succeeds and `{"type":"ping"}` is answered".
  The socket doubles as the channel for `stop`, `status` and `ack` from hooks/shim. Unix socket paths are limited
  to 103 bytes on macOS and 107 on Linux, so keep the runtime dir short and use a hash, not the full session id;
  socket files persist after a crash and must be unlinked before `listen()`; named pipes are removed automatically
  (verified: https://nodejs.org/api/net.html).

Exit conditions (watcher polls every 5 s; do not rely on `fs.watch` for a file in `/tmp`):

1. `fs.stat(CLAUDE_CODE_MESSAGING_SOCKET)` fails with `ENOENT` → the Claude session is gone; exit 0 after
   `session close`.
2. `process.kill(CLAUDE_PID, 0)` throws `ESRCH` → same.
3. `$CLAUDE_CONFIG_DIR/sessions/<CLAUDE_PID>.json` disappears (best-effort, undocumented format; verified facts).
4. `SIGTERM`/`SIGINT` or a `stop` on the control socket → graceful: cancel `watch`, `session close`, unlink socket and
   pidfile, exit 0. `SIGTERM` is not deliverable on Windows (`process.kill` there terminates unconditionally), so the
   control socket is the portable stop path.
5. Repeated adapter failures beyond a backoff budget → exit non-zero and log; the next SessionStart/UserPromptSubmit
   hook relaunches it (self-healing rather than a supervisor).

Idle sessions: `CLAUDE_CODE_MESSAGING_SOCKET` posts wake an idle session (verified facts), so the watcher does not
need to poll on user turns; a `UserPromptSubmit` hook only re-checks liveness.

Log file: `${stateDir}/logs/watcher-<session>.log`, NDJSON, 0600; truncate-and-rewrite when it exceeds ~5 MB
(a `size` check in the logger); delete logs older than N days at SessionStart.

---

## 11. Secure local credential storage

What has to be stored per adapter profile (from the logical plan): the Supabase anonymous-auth refresh token
(long-lived bearer capability) and possibly the access token cache. The team join secret is used once and must not
be persisted by the adapter.

| Option | Mechanism | Works under plugin constraints? | Tradeoffs |
| --- | --- | --- | --- |
| macOS Keychain via `/usr/bin/security` | `security add-generic-password -a <account> -s <service> -U -T /usr/bin/security -w <secret>` / `find-generic-password -a … -s … -w` / `delete-generic-password` | yes (no deps) | `-w <secret>` puts the secret in argv for the lifetime of the call. Avoid by using interactive mode: spawn `security -i`, write `add-generic-password … -w "…"` on **stdin**, close (verified: man page says `-i` reads commands from stdin until EOF). Exit code 44 when not found (verified locally). Fails with `-25308 User interaction is not allowed` when the login keychain is locked or the process has no UI session (SSH, some launchd contexts) — likely; https://developer.apple.com/forums/thread/116579. The creating app (`security`) is trusted by default; `-T /usr/bin/security` makes that explicit; `-A` (any app) is documented as "insecure, not recommended". |
| Linux Secret Service via `secret-tool` (libsecret) | `printf %s "$secret" \| secret-tool store --label='Brigade <profile>' service brigade profile <name>` / `secret-tool lookup service brigade profile <name>` / `clear` | yes when present | Secret is read from stdin when stdin is not a TTY (verified: https://www.mankier.com/1/secret-tool). Needs D-Bus + a running Secret Service (gnome-keyring/KWallet) — usually absent on headless servers and in containers, where lookups fail or hang; guard with a short timeout and fall back. Package is `libsecret-tools` on Debian/Ubuntu (likely). |
| Windows DPAPI via PowerShell | `ConvertTo-SecureString -AsPlainText -Force \| ConvertFrom-SecureString` → DPAPI-encrypted string (current user); reverse with `ConvertTo-SecureString` + `ConvertFrom-SecureString -AsPlainText` (PS 7) | yes (PowerShell is on every Windows) | Verified: with no `-Key`, "the Windows Data Protection API (DPAPI) is used" (https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.security/convertfrom-securestring). Pass the plaintext to PowerShell via stdin (`-Command -` or a script reading `$input`), never via `-Command "..."`; store the ciphertext in the profile file. Bound to the Windows user + machine; Windows 5.1 lacks `-AsPlainText` on `ConvertFrom-SecureString` (use `[System.Net.NetworkCredential]::new('', $ss).Password`). |
| `@napi-rs/keyring` 1.3.0 | native binding to keyring-rs; `new Entry(service, user).setPassword/getPassword/deletePassword` | installs under `--ignore-scripts` (prebuilt via 12 platform `optionalDependencies`, no install scripts — verified on npm) but **cannot be bundled** into a single file: the `.node` binary must exist on disk, so the plugin would need `package.json` + lockfile + the npm ci step | Backends compiled in: macOS Keychain, Windows Credential Manager, Linux keyutils **and** D-Bus Secret Service (verified from the repo's Cargo.toml). Nicest API; defer until the plugin needs `node_modules` anyway. |
| File fallback | `${configDir}/credentials/<profile>.json`, dir 0700, file 0600, written atomically (`tmp` + `rename`), JSON with `{ "version": 1, "refresh_token": "…" }` | yes | Plaintext at rest, protected only by filesystem permissions (and not even that on Windows, where `mode` is ignored — likely). This is what most developer CLIs actually do; be explicit in `brigade doctor` output. |

Decision: an internal `SecretStore` interface with implementations `keychain-cli` (macOS), `secret-tool` (Linux),
`dpapi-powershell` (Windows) and `file`; selection `auto` = try the OS store, on any failure (missing binary,
locked keychain, no D-Bus, timeout) fall back to `file` and log one `warn` line saying so. Profile config records
which store holds the secret (`"secret_store": "keychain"`), so a later run does not silently create a duplicate in
another store. `BRIGADE_SECRET_STORE=file|os|auto` overrides for CI and containers. All OS-store calls go through the
section 9 spawn helper with the secret on stdin and a 10 s timeout.

Also verified in the plugins reference: `userConfig` entries with `"sensitive": true` are stored by Claude Code in the
macOS Keychain or `~/.claude/.credentials.json` (about 2 KB total) and exposed to hooks as
`CLAUDE_PLUGIN_OPTION_<KEY>` and to `.mcp.json` as `${user_config.<key>}`. That is suitable for **user-entered**
values (e.g. the Supabase project URL and publishable key, or an optional join secret entered at enable time), not for
adapter-generated refresh tokens.

Never: pass secrets as argv; print them in `describe`/`doctor`; write them to the watcher log; include
`CLAUDE_CODE_MESSAGING_TOKEN` in the environment of the adapter (only the watcher needs it).

---

## 12. Directories, environment and logging

XDG (verified: https://specifications.freedesktop.org/basedir/latest/): `XDG_CONFIG_HOME` (default `~/.config`),
`XDG_DATA_HOME` (`~/.local/share`), `XDG_STATE_HOME` (`~/.local/state`; logs, history, restart state),
`XDG_CACHE_HOME` (`~/.cache`), `XDG_RUNTIME_DIR` (sockets; must be 0700 and user-owned; may be unset — fall back
with a warning).

Layout:

```text
config   ${BRIGADE_CONFIG_DIR:-$XDG_CONFIG_HOME/brigade}/profiles/<name>.json     adapter, team_ref, endpoints, secret_store
         …/credentials/<name>.json   (file store only; 0600)
state    ${BRIGADE_STATE_DIR:-$XDG_STATE_HOME/brigade}/logs/*.log, cursors/<profile>/<session>.json
runtime  ${XDG_RUNTIME_DIR:-<os.tmpdir()>/brigade-<uid>}/  (0700)  watcher pidfiles + control sockets
```

Platform mapping: macOS uses the same XDG-style paths under `$HOME` (matching `gh`, and the user's own
`CLAUDE_CONFIG_DIR` precedent — never hardcode `~/.claude`); Windows uses `%APPDATA%\brigade` for config,
`%LOCALAPPDATA%\brigade` for state/cache, and `\\.\pipe\brigade-…` for control channels (likely).

`${CLAUDE_PLUGIN_DATA}` (`<config dir>/plugins/data/<plugin-id>/`, persists across plugin updates, deleted on
uninstall — verified in the plugins reference) is for **plugin-owned** state only: watcher logs and pidfiles when
running under the plugin, an installed-version marker, nothing a standalone `npx brigade` needs. Profiles and
credentials stay in the XDG config dir so the plugin and the npm CLI see the same team membership. The hook passes
`BRIGADE_STATE_DIR=${CLAUDE_PLUGIN_DATA}` so plugin runtime state is cleaned up on uninstall.

Environment allow-list for children: `PATH`, `HOME`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `TMPDIR`, `TEMP`,
`XDG_*`, `BRIGADE_*`, `CLAUDE_PLUGIN_DATA`, `CLAUDE_CONFIG_DIR`, `NODE_OPTIONS` stripped (a user's
`--inspect` would otherwise leak into every adapter call). Claude Code itself strips `OTEL_*` from hook subprocesses.

Logging: hand-rolled NDJSON to **stderr** only, one object per line:
`{"ts":"2026-08-30T12:00:00.000Z","level":"info","comp":"watcher","session":"…","event":"message.injected","message_id":"…"}`.
Levels `error|warn|info|debug` from `BRIGADE_LOG_LEVEL` or `--log-level`; `BRIGADE_LOG_FORMAT=pretty` for humans.
Redaction of any key matching `/token|secret|password|authorization/i`. Keep it hand-rolled: `pino@10.3.1` is fine
but its transports run in worker threads (`thread-stream`) and expect separate files on disk, which fights
single-file bundling (likely); the MCP spec requires that stdio servers write nothing but MCP messages to stdout and
allows free-form UTF-8 on stderr, which clients "may capture, forward, or ignore" (verified:
https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/stdio). Claude Code plugin monitors turn
**every stdout line** into a notification (verified facts) — the same discipline applies if a monitor is ever used.

Hook stderr goes to Claude Code's debug log only; hook stdout on SessionStart/UserPromptSubmit becomes context for
Claude (verified: hooks docs). The hook entrypoint therefore prints at most one short plain-text line on success
(e.g. `Brigade: team "payments" joined; watcher running`) and otherwise nothing.

---

## 13. Exit codes

Conventions (verified): Bash reserves 2 for builtin misuse, 126 (not executable), 127 (not found), 128+N (signal)
(https://www.gnu.org/software/bash/manual/html_node/Exit-Status.html); BSD `sysexits(3)` defines 64–78
(https://man.freebsd.org/cgi/man.cgi?query=sysexits&sektion=3); Node's own `process.exit` truncates pending stdout
writes — set `process.exitCode` and return instead (https://nodejs.org/api/process.html). Claude Code hooks treat
**exit 2 as "block"** with stderr fed to Claude, and other non-zero codes as non-blocking errors (verified).

Decision: the JSON error object on stdout is the canonical machine-readable channel
(`{"ok":false,"error":{"code":"unauthorized","message":"…","retryable":false}}`); the exit code is a coarse class for
shells and hooks. Adapter/harness table:

| Exit | `error.code` | Meaning | sysexits analogue |
| --- | --- | --- | --- |
| 0 | — | success (`ok: true`) | EX_OK |
| 1 | `internal` | unclassified failure / bug (stack in stderr at debug) | EX_SOFTWARE (70) |
| 2 | `usage` | bad flags/arguments (parseArgs error), wrong subcommand | EX_USAGE (64) |
| 3 | `invalid_input` | stdin JSON failed schema validation, body too large | EX_DATAERR (65) |
| 4 | `unauthenticated` | no/expired local credential; run `brigade join` | EX_NOPERM (77) |
| 5 | `unauthorized` | authenticated but not a member / not owner of the session | EX_NOPERM (77) |
| 6 | `not_found` | unknown recipient session / message id | — |
| 7 | `conflict` | idempotency-key reuse with different payload; session already registered by another principal | — |
| 8 | `rate_limited` | retry after `retry_after_ms` | EX_TEMPFAIL (75) |
| 9 | `unavailable` | backend unreachable / 5xx / timeout; retryable | EX_UNAVAILABLE (69) |
| 10 | `protocol_mismatch` | adapter and harness protocol majors differ | EX_PROTOCOL (76) |
| 11 | `config` | profile missing/invalid; secret store unavailable and no fallback allowed | EX_CONFIG (78) |

Rules: never exit with 126/127 or >=128 from our own code; the harness maps a child killed by a signal to
`unavailable` with `signal` in details. Plugin hook entrypoints translate everything to exit 0 (with a diagnostic
line) except a deliberate policy block, because exit 2 has a reserved meaning there and exit 1 would surface as a
"hook error" notice in the transcript. The "sysexits analogue" column is documentation only; contiguous small
numbers are easier to test and do not collide with shell semantics.

---

## 14. Versioning

- Packages: SemVer 2.0.0 (verified: https://semver.org/) — MAJOR for incompatible API changes, MINOR for additive,
  PATCH for fixes; `0.y.z` while the protocol RFC is unfrozen ("anything MAY change"). All workspace packages move
  in lockstep (one version, one tag `vX.Y.Z`, `plugin.json` `version` set to the same value so Claude Code
  detects updates).
- Protocol: a separate **integer major** carried on every envelope and in `describe`
  (`"protocol_version": "1"`), independent of package versions. Compatibility rules for the RFC:
  - harness and adapter interoperate iff protocol majors are equal; otherwise exit 10 `protocol_mismatch`;
  - additive changes within a major are advertised as capabilities in `describe`
    (`"capabilities": ["watch.realtime", "message.ack", "session.description"]`) rather than a minor number, so
    the harness feature-detects rather than version-compares;
  - all consumers parse with `looseObject` and ignore unknown fields/event kinds; producers never remove or
    re-type a field within a major;
  - `describe` also reports `adapter: { name, version }` and `harness_min_version` if an adapter needs a newer shim.
- `@brigade/protocol` MAJOR bumps only when a schema change is breaking for TypeScript consumers; adding optional
  fields is MINOR. Publish JSON Schema (`z.toJSONSchema`) alongside for non-TS adapter authors.

---

## 15. Recommended `package.json` sketches

Root:

```jsonc
{
  "name": "brigade-monorepo",
  "private": true,
  "type": "module",
  "engines": { "node": ">=24" },
  "workspaces": ["packages/*"],
  "scripts": {
    "build": "tsc -b && node build.mjs && chmod +x plugin/dist/*.js",
    "typecheck": "tsc -b",
    "test": "node --conditions=development --test \"packages/*/test/**/*.test.ts\"",
    "test:watch": "node --conditions=development --test --watch \"packages/*/test/**/*.test.ts\"",
    "lint": "eslint . && prettier --check .",
    "lint:fix": "eslint . --fix && prettier --write .",
    "clean": "tsc -b --clean && rm -rf plugin/dist packages/*/dist"
  },
  "devDependencies": {
    "@eslint/js": "^10.0.0",
    "@types/node": "^24.13.3",
    "esbuild": "^0.28.2",
    "eslint": "^10.9.1",
    "eslint-config-prettier": "^10.0.0",
    "prettier": "^3.9.6",
    "typescript": "~6.0.3",
    "typescript-eslint": "^8.68.0"
  }
}
```

(`@eslint/js` and `eslint-config-prettier` majors above are uncertain — take whatever `npm view` reports when
scaffolding. Makefile targets from the verified repo conventions map 1:1: `setup`=`npm ci --ignore-scripts`,
`build`, `typecheck`, `lint`, `lint-fix`, `test`, `clean`.)

`packages/protocol/package.json`:

```jsonc
{
  "name": "@brigade/protocol",
  "version": "0.1.0",
  "type": "module",
  "engines": { "node": ">=24" },
  "exports": {
    ".": {
      "development": "./src/index.ts",       // used only by `node --conditions=development` (tests)
      "types": "./dist/index.d.ts",
      "default": "./dist/index.js"
    },
    "./package.json": "./package.json"
  },
  "files": ["dist", "src", "README.md"],       // src ships for sourcemaps/declarationMap; harmless
  "sideEffects": false,
  "dependencies": { "zod": "^4.5.4" },
  "publishConfig": { "access": "public" }
}
```

`packages/adapter-supabase/package.json` (the shape is the same for mcp-shim and watcher):

```jsonc
{
  "name": "@brigade/adapter-supabase",
  "version": "0.1.0",
  "type": "module",
  "engines": { "node": ">=24" },
  "bin": { "brigade-adapter-supabase": "./dist/main.js" },   // first line of src/main.ts: #!/usr/bin/env node
  "exports": { ".": { "development": "./src/index.ts", "types": "./dist/index.d.ts", "default": "./dist/index.js" } },
  "files": ["dist"],
  "dependencies": {
    "@brigade/protocol": "0.1.0",
    "@supabase/supabase-js": "<verify current major before pinning>",
    "zod": "^4.5.4"
  }
}
```

(`bin` files must start with `#!/usr/bin/env node`; npm symlinks them on Unix and generates `.cmd` shims on
Windows — verified: https://docs.npmjs.com/cli/v11/configuring-npm/package-json. `npx @brigade/adapter-supabase`
works from the published tarball because `dist/` is built by `tsc -b` with `rewriteRelativeImportExtensions`.)

`plugin/.claude-plugin/plugin.json`:

```jsonc
{
  "name": "brigade",
  "version": "0.1.0",
  "description": "Team messaging between Claude Code sessions (cross-user, cross-machine) through a pluggable adapter.",
  "hooks": "./hooks/hooks.json",
  "mcpServers": "./.mcp.json",
  "userConfig": {
    "profile": { "type": "string", "title": "Brigade profile", "description": "Name of the adapter profile to use", "default": "default" }
  }
}
```

`plugin/.mcp.json`:

```json
{ "mcpServers": { "team": { "command": "node", "args": ["${CLAUDE_PLUGIN_ROOT}/dist/mcp.js"],
  "env": { "BRIGADE_PROFILE": "${user_config.profile}", "BRIGADE_STATE_DIR": "${CLAUDE_PLUGIN_DATA}" } } } }
```

Tools become `mcp__plugin_brigade_team__TeamListSessions` / `…__TeamSendMessage` (verified facts naming scheme).

`plugin/hooks/hooks.json` (exec form, no shell; `async` so the watcher launch never blocks the prompt):

```json
{
  "hooks": {
    "SessionStart": [{ "hooks": [{ "type": "command", "command": "node",
      "args": ["${CLAUDE_PLUGIN_ROOT}/dist/hook.js", "session-start"], "timeout": 20 }] }],
    "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "node",
      "args": ["${CLAUDE_PLUGIN_ROOT}/dist/hook.js", "ensure-watcher"], "async": true }] }],
    "SessionEnd": [{ "hooks": [{ "type": "command", "command": "node",
      "args": ["${CLAUDE_PLUGIN_ROOT}/dist/hook.js", "session-end"], "timeout": 5 }] }]
  }
}
```

MCP shim dependency choice: `@modelcontextprotocol/server@2.0.0` (Node >= 20, dual ESM/CJS, `./stdio` subpath,
depends on `zod ^4.2.0`; released with the 2026-07-28 spec, which replaced the `initialize` handshake with
per-request metadata and a `server/discover` probe, with a documented legacy fallback — verified:
https://github.com/modelcontextprotocol/typescript-sdk and the stdio binding page). Because Claude Code 2.1.251's
client era is unverified, keep `@modelcontextprotocol/sdk@1.30.0` (v1 line, maintained for at least six months
after v2) as the fallback and make the transport/server construction a single file so swapping is a one-file change.
Both bundle cleanly with esbuild (likely; v1 was verified in earlier projects, v2 not yet exercised).

---

## 16. Things that will bite (collected)

1. `typescript@latest` is 7.x; `npm i -D typescript` without a range breaks typescript-eslint. Pin `~6.0.3`.
2. TS 6 defaults: `types: []` and `rootDir: <tsconfig dir>` — set both explicitly or nothing type-checks.
3. Node's type stripping needs `.ts` in relative imports and `import type`; `verbatimModuleSyntax` +
   `rewriteRelativeImportExtensions` + `erasableSyntaxOnly` make the compiler enforce the same rules.
4. Never run Node with `--preserve-symlinks` in this repo; it breaks source-level workspace imports.
5. `node --test` default discovery would also run compiled `dist/*.test.js`; always pass the explicit glob and
   keep tests out of `src`.
6. Plugin `.mcp.json`/hooks resolve `node` from PATH; users on Node 22 get a confusing failure. Self-check in the
   SessionStart hook and print one line.
7. A hook that inherits its stdio into the detached watcher hangs Claude Code's hook runner; use `'ignore'`/fds.
8. Hook exit 2 = block. Never let an ordinary adapter error propagate as exit 2 from `hook.js`.
9. `security` prompts or fails (`-25308`) over SSH; `secret-tool` hangs without D-Bus — both need a timeout and
   the file fallback.
10. esbuild single-file bundles cannot include `.node` addons; choosing `@napi-rs/keyring` forces `node_modules`
    into the plugin and therefore the 60-second `npm ci --ignore-scripts` step.
11. `process.exit()` after writing JSON to stdout can truncate the output; use `process.exitCode`.
12. Unix socket path limits (103/107 bytes) — hash session ids in socket names; unlink stale sockets before listen.
13. MCP stdio servers must not write to stdout except protocol frames; `no-console` as an ESLint error is the guard.
14. Marketplace installs copy only the `path` subdirectory; anything the plugin needs must be under `plugin/`.

## 17. Open questions for the first spike

- Does Claude Code 2.1.251's MCP client interoperate with `@modelcontextprotocol/server@2.0.0` (post-2026-07-28
  spec, `server/discover` probe) or does the shim need the v1 SDK? Test both against `claude --plugin-dir ./plugin`.
- Does Claude Code kill the process group or session of a `SessionStart` hook when the hook finishes or the session
  ends? (Hooks run in their own session; the `detached` spawn should survive either way, but verify with `ps -o sess,pgid`.)
- `@supabase/supabase-js` current major and whether it bundles cleanly as pure ESM (it historically pulled in
  `ws`/`node-fetch`-style deps); Node 24 has native `fetch` and `WebSocket`, which may let those be externalised or dropped.
- Whether `tsc -b` with `rewriteRelativeImportExtensions` rewrites imports in emitted `.d.ts` files (needed for the
  published `@brigade/protocol` types) — check the emitted declarations in the first build.
- Windows: confirm `detached: true` + `windowsHide: true` + named-pipe control channel works under PowerShell-hosted
  hooks (Claude Code uses Git Bash or PowerShell for shell-form hooks; exec form requires a real `.exe`, which
  `node.exe` is).
- Node 27's annual cadence (alpha from Oct 2026): decide whether CI tracks `26` or `27-alpha` as the "next" line.
