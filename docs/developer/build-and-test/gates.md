# Run the gate

```sh
make typecheck lint build test vuln deps-check schema-check tidy-check docs-check
```

| Target | Holds |
| --- | --- |
| `typecheck` | `go build ./...` and `go vet ./...`, tests included |
| `lint` | golangci-lint for darwin **and** linux (config verify, run, formatters) and a plain gofmt check |
| `build` | `bin/brigade` and the dev-only binaries with the release flags, version `<pin>-dev` |
| `test` | unit, testscript, harness and conformance(fs) with `-race`, plus the drift tests in `scripts/ci/` and the documentation gate |
| `vuln` | govulncheck on the source and on the built binary |
| `deps-check` | `bin/brigade` links exactly the modules in `docs/allowed-deps.txt` |
| `schema-check` | `docs/protocol-v1.schema.json` is what `make schema` generates |
| `tidy-check` | `go.mod` and `go.sum` would not change |
| `docs-check` | the documentation tree's shape, links, orphans, harness pages, options, the make-target page, the changelog section ([Run the gate](../documentation/gate.md)) |

CI's `fast` job adds `checksums-check`, `plugin-check`, `muse-plugin-check`, `codex-plugin-check` and `functions-check`; the `macos`,
`reproducibility` and `supabase` jobs the rest ([Know the CI](ci.md)).

## What lint enforces, in one place

`.golangci.yml`'s forbidigo rules are the stdout, exit, environment and spawn discipline: no `fmt.Print` or
`os.Stdout` outside the protocol writers and `harness/commands`; `os.Exit` only in a `main`; `os.Getenv` only through
`harness/config` and `adapterkit/env`; `exec.Command` only through the spawn helpers, the conformance suite and
`internal/syncthing/engine.go`, the one place Brigade starts a long-running daemon (`syncthing
serve`, detached, argv array, allow-listed environment). depguard keeps `internal/harness` from importing the
Supabase adapter. gosec pins file modes: directories 0700, files 0600.

## Release pins

`make checksums-check` requires `plugin/bin/checksums.txt` to be reproduced by a fresh cross-compile or backed by
the published release; in the pre-release `0.0.0` state the file must be empty. Never commit a `VERSION` or
`checksums.txt` you did not produce with `make release`. A Go toolchain bump changes the bytes: bump the version
with it, or accept a red `fast` job until the next release (rule (c)).

## Shell

Every shell file is `sh -n` and `shellcheck -s sh` clean. CI's runner has shellcheck **0.9.0** and versions
disagree, so before pushing a shell file:

```sh
docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:v0.9.0 -s sh <file>
```
