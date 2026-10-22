# Set up a clone

```sh
git clone git@github.com:appshapes/brigade.git && cd brigade
make setup
```

`make setup` downloads the modules, installs the pinned golangci-lint and goreleaser into `./bin`, shellcheck
(Homebrew on macOS), writes `.env` from `.env.example` and sets `push.autoSetupRemote`. It stops if `gh` is missing
or logged out, or if Docker is not running. Go 1.27.3 is pinned in `go.mod` with no `toolchain` line: the release
job rebuilds from the tag with that toolchain and diffs against the committed checksums.

| For | You also need |
| --- | --- |
| `make test`, the gate, `make push` | nothing more |
| the live Supabase suites, `make e2e` | Docker, Node (`npx` runs the pinned Supabase CLI), `jq` |
| `make plugin-dev`, `make harness-smoke`, `make proof` | a logged-in `claude`, `jq` |
| `make release`, `make release-dry-run`, `make checksums-check` | `gh`, logged in; `make release` also Muse Code (`muse` on PATH) for the Muse catalog's digest |
| `make plugin-check` | shellcheck; Docker for CI's 0.9.0 ([scripts/ci/README.md](../../../scripts/ci/README.md)) |
| `make muse-plugin-check`, `make muse-digest` | the same shellcheck; with Muse installed the check verifies `marketplace.json`'s digest and `muse-digest` rewrites it, and `muse plugins validate ./muse/brigade` covers the schema half |
| `make codex-plugin-check` | nothing beyond POSIX sh and git; a local `codex plugin marketplace add <clone>` then `codex plugin add brigade@brigade` installs the package when Codex is installed |
| the real-Syncthing tests | `syncthing` on `PATH` (`brew install syncthing`); measured with 2.1.5 |
| the Trello skills | [Work a card](../contribute/cards.md) |

`make help` lists every target; a trailing ` (CI)` marks the ones a workflow step runs; the generated
[make targets](../reference/make-targets.md) page is the same list. Plans go in `.context/plans/`, ephemeral
scratch in `.ignored/` (gitignored).
