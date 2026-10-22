# Set up

## If you want to

| If you want to | Go to |
| --- | --- |
| set up a fresh clone and its tools | [Set up a clone](#set-up-a-clone) |
| run the local Supabase stack for the live suites | [Run the local stack](#run-the-local-stack) |
| run the Claude Code plugin against your own build | [Run the plugin from your checkout](#run-the-plugin-from-your-checkout) |

## Set up a clone

1. Go 1.27.3, `make`, `git`, `curl`; `gh` logged in; Docker running.
2. `make setup`: module downloads, pinned golangci-lint and goreleaser into `./bin`, shellcheck, `.env`, `push.autoSetupRemote`.
3. `make test`: Docker-free, stack-free, a few minutes.

Details: [Set up a clone](clone-and-tools.md).

## Run the local stack

1. `make supabase-start supabase-env` (Docker, Node for `npx`, `jq`).
2. `make test-all` runs everything that needs it; `make supabase-clean` stops it and deletes its data.

Details: [Run the local stack](local-stack.md).

## Run the plugin from your checkout

1. `make build`, register the fs adapter in `~/.config/brigade/adapters.json`, `brigade team create --adapter fs …` once.
2. `make plugin-dev` starts Claude Code with the local plugin; `make plugin-dev-off` removes the pointer.

Details: [Run the plugin from your checkout](plugin-dev.md).
