# Makefile for this repository
# Conventions: lowercase variable names; per-target `.PHONY`; `##` doc-comments scraped by `help`.

# ========== Variables (alphabetical) ==========

bin_dir            := bin
# The conformance suite builds every adapter child's environment FROM SCRATCH (plan 4.1 / its
# launcher forwards only PATH and TMPDIR), so an instrumented bin/brigade spawned by it never sees
# GOCOVERDIR and writes nothing — measured: `go tool covdata percent` answered "no applicable files
# found in input directories" on the run that produced this line. --env is the supported way in, and
# it is the reason `make test-integration` produces coverage at all.
cover_env          := $(if $(GOCOVERDIR),--env GOCOVERDIR=$(GOCOVERDIR),)
# Coverage across the process boundary (plan 9.4). BRIGADE_COVER=1 in the environment instruments
# bin/brigade with `go build -cover`, so the adapter children the conformance suite spawns write
# coverage into GOCOVERDIR and CI's `go tool covdata percent` reports what the integration run
# actually executed. Empty by default: -cover changes the artefact under test, and an instrumented
# binary run without GOCOVERDIR warns on stderr and writes nothing. `test-integration` depends on
# `build`, so the CI step's own BRIGADE_COVER=1 is enough to rebuild instrumented.
cover_flags        := $(if $(BRIGADE_COVER),-cover,)
dist_cross         := dist-cross
env_test           := .env.test
go_flags           := -trimpath -buildvcs=false
go_test_flags      := -race -shuffle=on -count=1 -timeout 15m
go_toolchain       := go$(shell sed -n 's/^go //p' go.mod)
# Reproducible-build environment for `build` and `cross`. -trimpath, -buildvcs=false and a pinned
# GOTOOLCHAIN are NOT sufficient on their own: GOAMD64, GOARM64 and GOFLAGS are read from whatever the
# developer happens to have exported, and each of them changes the output bytes. Measured here on one
# machine, same source, same go_flags: GOAMD64=v3 moved linux/amd64 from fb4ae34d… to 7da9f338…,
# GOARM64=v9.0 moved linux/arm64 from fbfadcaf… to baf92a1e…, and GOFLAGS=-tags=foo moved linux/amd64 to
# c60d2bee…. goreleaser pins goamd64/goarm64 itself (7.7), so an unpinned `make cross` can disagree with
# the release build, with CI and with the next developer for a reason no flag in go_flags covers — which
# is exactly the cross-host claim P1-1 exists to make. Pin them at the recipe.
go_build_env       := GOTOOLCHAIN=$(go_toolchain) CGO_ENABLED=0 GOFLAGS= GOAMD64=v1 GOARM64=v8.0
golangci_lint      := $(bin_dir)/golangci-lint
golangci_version   := v2.13.2
goreleaser         := $(bin_dir)/goreleaser
goreleaser_version := v2.18.0
ld_flags            = -s -w -X github.com/appshapes/brigade/internal/buildinfo.Version=$(version)
mutant_tags        := mutant_noack mutant_teamleak mutant_trustsender mutant_caporder
ld_flags_dev        = -s -w -X github.com/appshapes/brigade/internal/buildinfo.Version=$(version)-dev
# The plugin's two identifiers, from .claude-plugin/marketplace.json and plugin/.claude-plugin/plugin.json:
# the marketplace is named `brigade` and a plugin id is `<plugin>@<marketplace>`. Both are hardcoded rather
# than read out of the manifests for the reason scripts/ci/plugin-check.sh gives for its own textual checks --
# jq is not a dependency of this repository -- and `make plugin-check` is what keeps the manifests honest.
# plugin_market_src is the marketplace SOURCE, not its name: override it to install from a fork, or from this
# tree (`plugin_market_src=.`), neither of which changes the name or the id above.
plugin_id          := brigade@brigade
plugin_json        := plugin/.claude-plugin/plugin.json
plugin_market_name := brigade
plugin_market_src  ?= appshapes/brigade
plugin_version     := $(shell cat plugin/bin/VERSION)
sha256              = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
supabase           ?= npx --yes supabase@2.116.0
supabase_exclude   := studio,postgres-meta,imgproxy,storage-api,edge-runtime,mailpit,logflare,vector,supavisor
tag                := $(shell git log -1 --pretty=format:"%H")
targets            := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
tools_mod          := tools.mod
version            ?= $(plugin_version)

# Strip the outer Claude Code session's variables from targets that run proof scripts or launch claude:
# inherited CLAUDE_PID/CLAUDE_CODE_MESSAGING_* would make the harness ignore the scripts' BRIGADE_* values,
# refuse --sink, and leak the outer session's socket into nested `claude -p` runs (9.6).
#
# By PREFIX, never by enumeration. The plan's eight-name `env -u` list is measurably short: E0-4 found
# CLAUDE_CODE_BRIDGE_SESSION_ID missing from it and E0-7 found CLAUDE_EFFORT and AI_AGENT (which is not even
# CLAUDE-prefixed) missing too — stripping by prefix removed ELEVEN variables where the list removed eight.
# `env -u` cannot express a prefix, so the -u list is computed from make's own environment at parse time.
# CLAUDE_CONFIG_DIR is the single exception and must survive: the whole test estate depends on the config dir
# staying non-default, and it is never hardcoded anywhere.
unclaude_keep      := CLAUDE_CONFIG_DIR
unclaude_vars      := $(filter-out $(unclaude_keep),$(shell env | sed -n -e 's/^\(CLAUDE[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(AI_AGENT[0-9A-Za-z_]*\)=.*/\1/p'))
unclaude           := env $(patsubst %,-u %,$(unclaude_vars))

# ========== Help ==========

# `make help` also answers "who calls this": a trailing ` (CI)` marks every target that a step of
# `.github/workflows/*.yml` invokes, and no other target. Adapted from thinktech-web/Makefile:139,143,147,157,
# which PREFIXES `## Jenkins-invoked: ` instead -- a prefix would push all nineteen descriptions right by its
# own width in this `%-25s` layout, and four of the nineteen already carried a trailing marker.
.PHONY: help
help: ## Show this help message
	@echo "Make commands"
	@echo ""
	@echo "Usage: make [target] [var=value ...]"
	@echo ""
	@echo "Available targets:"
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-25s %s\n", $$1, $$2}'

# ========== Setup ==========

.PHONY: setup
setup: ## go mod download, pinned golangci-lint and goreleaser into ./bin, dev tools, .env scaffold, docker check, push.autoSetupRemote
	go mod download
	go mod download -modfile=$(tools_mod)
	mkdir -p $(bin_dir)
	curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(bin_dir) $(golangci_version)
	$(MAKE) setup-goreleaser
	@command -v shellcheck >/dev/null || { [ "$$(uname -s)" = Darwin ] && brew install shellcheck || echo "install shellcheck for make plugin-check (CI enforces it)"; }
	@cp -n .env.example .env || true
	docker version >/dev/null
	git config push.autoSetupRemote true

.PHONY: setup-lint
setup-lint: ## Install only the pinned golangci-lint into ./bin (no Docker, no goreleaser) (CI)
	mkdir -p $(bin_dir)
	curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(bin_dir) $(golangci_version)

.PHONY: setup-goreleaser
setup-goreleaser: ## Download the pinned goreleaser release binary into ./bin (release rehearsal only)
	gh release download $(goreleaser_version) --repo goreleaser/goreleaser --pattern "goreleaser_$$(uname -s)_$$(uname -m | sed 's/aarch64/arm64/').tar.gz" --dir $(bin_dir) --clobber
	tar -xzf $(bin_dir)/goreleaser_*.tar.gz -C $(bin_dir) goreleaser && rm -f $(bin_dir)/goreleaser_*.tar.gz

# ========== Build / Test / Lint ==========

# plugin_version is $(shell cat plugin/bin/VERSION). A missing or empty file expands `version` to the empty
# string and everything downstream still SUCCEEDS: the binary reports "-dev" and `make cross` writes
# brigade__darwin_arm64, all with exit 0. Fail loudly instead -- the release path depends on this stamp.
.PHONY: version-check
version-check: ## Fail when the version stamp would be empty (missing or empty plugin/bin/VERSION)
	@test -n "$(version)" || { \
	  echo 'version is empty: plugin/bin/VERSION is missing or empty.' >&2; \
	  echo 'The binary would report "-dev" and `make cross` would emit brigade__<os>_<arch>.' >&2; \
	  echo 'Restore it (0.0.0 before the first release; `make release` writes it thereafter).' >&2; \
	  exit 1; }

.PHONY: build
build: version-check ## Build bin/brigade plus the dev-only binaries for this host with the release flags (version stamped `<version>-dev`; BRIGADE_COVER=1 adds -cover) (CI)
	$(go_build_env) go build $(go_flags) $(cover_flags) -ldflags '$(ld_flags_dev)' -o $(bin_dir)/brigade ./cmd/brigade
	$(go_build_env) go build $(go_flags) $(cover_flags) -ldflags '$(ld_flags_dev)' -o $(bin_dir)/brigade-adapter-fs ./cmd/brigade-adapter-fs
	$(go_build_env) go build $(go_flags) -ldflags '$(ld_flags_dev)' -o $(bin_dir)/brigade-fake-adapter ./cmd/brigade-fake-adapter
	$(go_build_env) go build $(go_flags) -ldflags '$(ld_flags_dev)' -o $(bin_dir)/brigade-conformance ./cmd/brigade-conformance

.PHONY: clean
clean: ## Remove build artifacts and local caches
	rm -rf $(bin_dir)/brigade $(bin_dir)/brigade-adapter-fs $(bin_dir)/brigade-fake-adapter $(bin_dir)/brigade-conformance dist $(dist_cross) cover.out $(env_test) playwright-report test-results

.PHONY: typecheck
typecheck: ## go build ./... and go vet ./... (every package, including tests) (CI)
	go build ./...
	go vet ./...

.PHONY: fmt
fmt: ## gofmt + goimports through golangci-lint
	$(golangci_lint) fmt ./...

# The gofmt step is scoped to the root module's OWN package directories, not to `.`. Plan 7.3/7.4 write it as
# `gofmt -l .`, but that is a FILESYSTEM walk while every other tool here (go build, go vet, golangci-lint) is
# MODULE-scoped. This repo commits Go under docs/research/ and scripts/experiments/, and keeps scratch under
# .ignored/ — 19 such files, every one of them inside its OWN nested go.mod and therefore not part of this
# module. `gofmt -l .` flags all 19 and turns `make lint` red over code this module does not build. Deriving
# the list from `go list` keeps the two scopes identical and stays correct as packages are added. The guards
# matter: without them a `go list` failure or an empty package set would make the check pass having read
# nothing. It lists FILES rather than directories because `gofmt` RECURSES a directory: the moment a package
# exists at the repository root `go list` emits ".", and the check silently reverts to the whole-tree walk it
# was written to eliminate (measured -- a stray root main.go brought all 8 committed nested-module files
# back). golangci-lint's own gofmt/goimports formatters cover the same ground, so this step is
# belt-and-braces rather than the only guard. There was a THIRD such path: `out="$(gofmt -l $dirs)"` word-split the directory list on spaces, so
# on a checkout whose path contains a space gofmt was handed fragments, wrote its complaints to stderr, exited
# 2 and produced no stdout — and the recipe, which looked only at stdout, passed having formatted nothing
# (measured under `.../path with spaces/`). Reading the list line by line fixes the splitting, and checking
# gofmt's exit status makes any other gofmt failure loud instead of silent.
# golangci-lint runs for BOTH supported platforms, explicitly. Build-constrained files (`_linux_test.go`,
# `_darwin_test.go`, `//go:build linux`) are invisible to a native run on the other OS, so a finding in one
# of them is a CI-only failure the developer never sees: measured -- `internal/adapterkit/pty_linux_test.go`
# carried two gosec G103 findings through three green local gates and three red CI runs (33550171587,
# 33578789887, 33581196998) before anyone looked. GOOS steers golangci-lint's package loading exactly as it
# steers `go build`, and the darwin pass on a darwin host is the native run, so nothing is linted twice
# for a different reason than "the other platform's files".
#
# The three mutant tags are applied ONE AT A TIME, on the one package that has mutants, and never in the
# config: `.golangci.yml`'s former `run.build-tags: [all three]` excluded every `//go:build !mutant_*` twin,
# so the REAL ack, list and send implementations were never linted (measured in P1-5 with an unused function
# planted in each twin: 0 issues from the config-tagged run, 1 from each of these).
.PHONY: lint
lint: ## golangci-lint for darwin AND linux (config verify + run, formatters included) and a plain gofmt check (CI)
	$(golangci_lint) config verify
	@files=$$(go list -f '{{$$d:=.Dir}}{{range .GoFiles}}{{$$d}}/{{.}}{{"\n"}}{{end}}{{range .CgoFiles}}{{$$d}}/{{.}}{{"\n"}}{{end}}{{range .TestGoFiles}}{{$$d}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$$d}}/{{.}}{{"\n"}}{{end}}' ./...) || exit 1; \
	  test -n "$$files" || { echo "gofmt check: go list produced no Go files" >&2; exit 1; }; \
	  out="$$(printf '%s\n' "$$files" | while IFS= read -r f; do gofmt -l "$$f" || exit 1; done)" \
	    || { echo "gofmt check: gofmt itself failed" >&2; exit 1; }; \
	  test -z "$$out" || { echo "$$out"; exit 1; }
	GOOS=darwin $(golangci_lint) run ./...
	GOOS=linux $(golangci_lint) run ./...
	@for t in $(mutant_tags); do \
	  echo "$(golangci_lint) run --build-tags $$t ./internal/adapters/fs/..."; \
	  $(golangci_lint) run --build-tags $$t ./internal/adapters/fs/... || exit 1; \
	done

.PHONY: lint-fix
lint-fix: ## golangci-lint --fix and fmt
	$(golangci_lint) run --fix ./...
	$(golangci_lint) fmt ./...

.PHONY: test
test: build ## Unit + testscript + harness + conformance(fs) with -race; no Docker and no local stack (what `make commit` runs) (CI)
	go test $(go_test_flags) -covermode=atomic -coverprofile=cover.out ./...
	$(bin_dir)/brigade-conformance --shared-env BRIGADE_FS_ROOT $(cover_env) --adapter $(bin_dir)/brigade-adapter-fs

.PHONY: vuln
vuln: build ## govulncheck on the source tree and on the built binary (pinned in tools.mod) (CI)
	go tool -modfile=$(tools_mod) govulncheck ./...
	go tool -modfile=$(tools_mod) govulncheck -mode binary $(bin_dir)/brigade

.PHONY: tidy-check
tidy-check: ## Fail if go.mod/go.sum would change (CI)
	go mod tidy -diff
	go mod verify

# The `!` inverts grep's exit status, so anything that makes grep exit non-zero for a reason other than
# "no disallowed module found" passes silently: a MISSING docs/allowed-deps.txt makes grep exit 2 and the
# check would report success having read nothing. `test -s` closes that (and the empty-allowlist case).
# The grep is the subset test (nothing linked outside the allow-list); the `diff` after it is the EQUALITY
# test P2-6 promised once the shipped set was actually linked: bin/deps.txt must be byte-equal to
# docs/allowed-deps.txt (five modules), so an empty deps.txt — which the subset test alone passes vacuously —
# and a module that silently stopped being linked both fail as loudly as a module that was added. The
# allow-list is kept in `LC_ALL=C sort` order for that reason.
# The `go version -m` step writes to a file instead of piping into awk for the same reason: in a pipeline the
# recipe's exit status is the LAST command's, so a failing `go version -m` (a binary the toolchain cannot
# read) left an empty deps.txt behind and the allowlist check then passed having inspected nothing —
# measured: pipeline exit 0, deps.txt 0 bytes, check exit 0. With the redirect its status is the recipe's.
.PHONY: deps-check
deps-check: build ## Fail unless bin/brigade links exactly the modules in docs/allowed-deps.txt (subset, then equality) (CI)
	test -s docs/allowed-deps.txt
	go version -m $(bin_dir)/brigade > $(bin_dir)/buildinfo.txt
	awk '$$1 == "dep" {print $$2}' $(bin_dir)/buildinfo.txt | LC_ALL=C sort > $(bin_dir)/deps.txt
	@echo "deps-check: $$(grep -c . $(bin_dir)/deps.txt || true) linked module(s) checked against docs/allowed-deps.txt"
	! grep -vxF -f docs/allowed-deps.txt $(bin_dir)/deps.txt
	diff $(bin_dir)/deps.txt docs/allowed-deps.txt

.PHONY: schema
schema: ## Regenerate docs/protocol-v1.schema.json from the Go wire types
	go run ./cmd/brigade-schema > docs/protocol-v1.schema.json

# Neither the `test -s` nor the temp file is decoration. Piping the generator straight into `diff -` makes the
# recipe's exit status diff's alone, so (a) a generator that printed nothing while the committed file was also
# empty compared empty with empty and passed — measured, exit 0 — and (b) a generator that wrote the right
# bytes and then failed was invisible, also measured. `make schema` writes the same file this diffs, so the
# two can drift into vacuity together. The redirect makes the generator's own status the recipe's.
.PHONY: schema-check
schema-check: ## Fail if docs/protocol-v1.schema.json is stale, empty or ungenerable (CI)
	test -s docs/protocol-v1.schema.json
	mkdir -p $(bin_dir)
	go run ./cmd/brigade-schema > $(bin_dir)/schema.json
	diff $(bin_dir)/schema.json docs/protocol-v1.schema.json

# The files are listed explicitly because `supabase test db` (CLI 2.116.0, pg_prove 3.36) discovers tests
# RECURSIVELY under supabase/tests, so the bare form runs supabase/tests/helpers/auth.sql — the `\ir`-included
# fixture helper of plan 9.3, which has no plan line — as a test and fails the run with "No plan found in TAP
# output" while every real assertion passes (measured in P2-4). An explicit list is not recursed.
.PHONY: test-db
test-db: ## pgTAP tests in supabase/tests against the running local stack (CI)
	$(supabase) test db supabase/tests/*.sql

# No --setup on the conformance line: with the BRIGADE_SUPABASE_* pair in the environment, `team create`/`team join`
# bind an empty profile themselves (4.1's rule for a human shell, and the suite is one), so the suite provisions its
# own teams, learns the join secret and runs C-28/C-40 instead of skipping them; scripts/ci/conformance-setup-supabase.sh
# stays as the documented out-of-band alternative for a backend whose principals are provisioned elsewhere.
.PHONY: test-integration
test-integration: build ## Adapter integration + conformance(supabase) against the local stack (reads $(env_test)) (CI)
	set -a; . ./$(env_test); set +a; BRIGADE_TEST_LIVE=1 BRIGADE_TEST_DOCKER=1 go test -count=1 -timeout 20m -run 'Integration|Supabase' ./internal/adapters/supabase/...
	set -a; . ./$(env_test); set +a; $(bin_dir)/brigade-conformance --slow --env BRIGADE_SUPABASE_URL=$$SUPABASE_URL --env BRIGADE_SUPABASE_PUBLISHABLE_KEY=$$SUPABASE_PUBLISHABLE_KEY $(cover_env) --adapter $(bin_dir)/brigade -- adapter supabase

.PHONY: test-all
test-all: test test-db advisor-lints test-integration e2e ## Everything (requires `make supabase-start supabase-env`)

.PHONY: conformance
conformance: build ## Run the conformance suite against an adapter (usage: make conformance adapter=<executable> [args="-- fixed args"])
	$(bin_dir)/brigade-conformance --adapter $(adapter) $(args)

.PHONY: e2e
e2e: build ## Phase 4 no-LLM proof in watcher sink mode against the local stack (CI)
	$(unclaude) scripts/proof.sh

.PHONY: proof
proof: e2e ## Phase 4 proof including the headless LLM run, the idle-wake run and the crash-and-resume run (needs a logged-in claude)
	$(unclaude) scripts/proof-headless.sh && $(unclaude) scripts/proof-idle-wake.sh && $(unclaude) scripts/proof-crash-resume.sh

.PHONY: harness-smoke
harness-smoke: build ## Headless claude -p smoke test with the fs adapter (needs a logged-in claude)
	$(unclaude) scripts/harness-smoke.sh

.PHONY: advisor-lints
advisor-lints: ## Security Advisor lint mirrors, run with the psql inside the local database container (no host psql needed) (CI)
	docker exec -i supabase_db_brigade psql -U postgres -d postgres -v ON_ERROR_STOP=1 < scripts/ci/advisor-lints.sql

# ========== Plugin ==========

.PHONY: plugin-check
plugin-check: ## Static checks of plugin/: exec-form hooks, no .mcp.json, VERSION == plugin.json, shellcheck, no secrets (CI)
	scripts/ci/plugin-check.sh
	scripts/ci/no-secrets.sh

.PHONY: plugin-validate
plugin-validate: ## claude plugin validate on the marketplace and the plugin root
	claude plugin validate .
	claude plugin validate ./plugin --strict

# The USER path: the two commands every member of a team runs, and their two update halves. A developer wants
# them too rather than only `plugin-dev`, because the dev-binary pointer is the FIRST branch of
# plugin/bin/brigade and skips the whole download-verify-cache path the release actually ships on -- so a tree
# driven only by the pointer never exercises what a member's machine does. Installed at the default `user`
# scope, which is the configuration directory (CLAUDE_CONFIG_DIR, else ~/.claude): every session of that
# directory gets the plugin, unlike `plugin-dev`, whose --plugin-dir loads it for one session as brigade@inline.
#
# Neither command takes `-y`, on purpose. That flag auto-accepts a marketplace-declared install command, and
# this marketplace declares none (docs/setup.md, "Installing the plugin"), so nothing here prompts today -- and
# a prompt that appears later is the one thing worth reading rather than something suppressed in advance.
#
# `brigade-install` and `brigade-update` are the two a person actually types; the four halves stay separately
# runnable. Their order is the prerequisite list, left to right, which is what `commit: typecheck pull build
# test` already relies on -- a guarantee of a serial make, and none of these is ever run under `-j`.
.PHONY: brigade-install
brigade-install: plugin-marketplace-add plugin-install ## First time: add the marketplace, then install the plugin (the user path)

.PHONY: brigade-update
brigade-update: plugin-marketplace-update plugin-update ## After a release: re-pull the marketplace, then update the plugin (restart Claude Code to apply)

.PHONY: plugin-marketplace-add
plugin-marketplace-add: ## Add the Brigade marketplace (usage: make plugin-marketplace-add [plugin_market_src=<repo|path>])
	claude plugin marketplace add $(plugin_market_src)

.PHONY: plugin-install
plugin-install: ## Install the plugin from the marketplace, for every session of this configuration directory
	claude plugin install $(plugin_id)

# Run the two in this order: `marketplace update` re-pulls the marketplace from its source, and `plugin update`
# then installs out of that local clone. AFTER A RELEASE is the intended moment for both, and the only one this
# pair is meant for: the install path is versioned (<config dir>/plugins/cache/brigade/brigade/<version>/) and
# `make release` is the only thing that moves plugin.json's version, so a release is what gives `plugin update`
# a new version to install. Between releases the version does not move and this pair is not the tool -- use
# `plugin-dev` for a plugin-tree change, and the dev-binary pointer for a binary change.
.PHONY: plugin-marketplace-update
plugin-marketplace-update: ## Re-pull the marketplace from its source (run this before plugin-update)
	claude plugin marketplace update $(plugin_market_name)

.PHONY: plugin-update
plugin-update: ## Update the installed plugin to the marketplace's version (restart Claude Code to apply)
	claude plugin update $(plugin_id)

.PHONY: plugin-dev-pointer
plugin-dev-pointer: build ## Write the dev-binary pointer (honours XDG_CONFIG_HOME) without launching anything; a prerequisite of plugin-dev and the manual step of docs/experiments/E3-interactive.md
	mkdir -p "$${XDG_CONFIG_HOME:-$$HOME/.config}/brigade"
	echo "$(CURDIR)/$(bin_dir)/brigade" > "$${XDG_CONFIG_HOME:-$$HOME/.config}/brigade/dev-binary"

# The `options` object `plugin-dev` puts in --settings, assembled from its two parameters. Either may be empty,
# and a comma appears only between two present halves.
#   adapter=fs      the D36 per-session OVERRIDE: adapter_command = the absolute bin/brigade-adapter-fs as a
#                   JSON array. The fs adapter's default root is the harness-computed BRIGADE_STATE_DIR's
#                   fs-adapter subdirectory, so neither --root nor BRIGADE_FS_ROOT is needed (and
#                   BRIGADE_FS_ROOT would not arrive anyway: the harness builds every adapter child's
#                   environment from scratch).
#   config_dir=<d>  the `config_dir` option (the two-personas-on-one-machine dev shape, P7-7: each persona
#                   is its own credential store; the profile option is gone).
comma := ,
plugin_dev_adapter_opt = $(if $(filter fs,$(adapter)),"adapter_command":"[\"$(CURDIR)/$(bin_dir)/brigade-adapter-fs\"]")
plugin_dev_profile_opt = $(if $(config_dir),"config_dir":"$(config_dir)")
plugin_dev_opts = $(plugin_dev_adapter_opt)$(if $(and $(plugin_dev_adapter_opt),$(plugin_dev_profile_opt)),$(comma))$(plugin_dev_profile_opt)
# mode=<default|acceptEdits|plan|auto|dontAsk|bypassPermissions> passes --permission-mode. An account that opted into
# Claude Code's auto-mode default offer starts a plain `claude` in `auto`, where no permission prompt or Skill dialog
# ever appears -- so the P3-8 checks that measure prompts (E3-interactive.md) launch with mode=default explicitly.
plugin_dev_mode = $(if $(mode),--permission-mode $(mode))

.PHONY: plugin-dev
plugin-dev: plugin-dev-pointer ## Start Claude Code with the local plugin (usage: make plugin-dev [adapter=fs] [config_dir=<dir>] [mode=default])
# ONCE per machine, in your own terminal (never from inside a session: `team create`/`team join` refuse there),
# before the first `make plugin-dev adapter=fs`. Register the fs adapter by NAME in adapters.json, then create
# the team IN YOUR PROJECT CHECKOUT — `team create` writes `.brigade.json`, the binding and the pin, and every
# later session in that checkout attaches by itself (`adapter=fs` stays useful as the per-session override):
#
#   make build
#   printf '{"fs": ["%s/bin/brigade-adapter-fs"]}\n' "$PWD" > ~/.config/brigade/adapters.json && chmod 600 ~/.config/brigade/adapters.json
#   bin/brigade team create --adapter fs --url http://127.0.0.1:1 --key placeholder --name ops --label dev --secret-file ~/brigade-ops.secret
#
# A SECOND persona on the same machine (`make plugin-dev config_dir=$HOME/.config/brigade-bob`) joins that team
# into ITS OWN store. The join secret goes from the 0600 file into the request document on stdin, never argv:
#
#   printf '{"fs": ["%s/bin/brigade-adapter-fs"]}\n' "$PWD" > $HOME/.config/brigade-bob/adapters.json && chmod 600 $HOME/.config/brigade-bob/adapters.json
#   { printf '{"human_label":"bob","join_secret":"'; tr -d '\n' < ~/brigade-ops.secret; printf '"}'; } | \
#     BRIGADE_CONFIG_DIR=$HOME/.config/brigade-bob bin/brigade team join --adapter fs
#
# (In the project checkout, `bin/brigade team join` at a TTY is the interactive form: the consent gate, then the
# secret read without echo.) Check what a team resolves to with `bin/brigade team status` in the checkout.
ifeq ($(plugin_dev_opts),)
	$(unclaude) claude $(plugin_dev_mode) --plugin-dir ./plugin
else
	$(unclaude) claude $(plugin_dev_mode) --plugin-dir ./plugin --settings '{"pluginConfigs":{"brigade@inline":{"options":{$(plugin_dev_opts)}}}}'
endif

.PHONY: plugin-dev-off
plugin-dev-off: ## Remove the local-build pointer so the bootstrap uses the pinned release again
	rm -f "$${XDG_CONFIG_HOME:-$$HOME/.config}/brigade/dev-binary"

# ========== Release ==========

.PHONY: print-version
print-version: ## Print the version pinned in plugin/bin/VERSION
	@echo $(plugin_version)

.PHONY: cross
cross: version-check ## Build dist-cross/brigade_$(version)_<os>_<arch> for every target with the exact release flags, plus checksums.txt (CI)
	rm -rf $(dist_cross) && mkdir -p $(dist_cross)
	for t in $(targets); do \
	  $(go_build_env) GOOS=$${t%/*} GOARCH=$${t#*/} go build $(go_flags) -ldflags '$(ld_flags)' \
	    -o $(dist_cross)/brigade_$(version)_$${t%/*}_$${t#*/} ./cmd/brigade || exit 1; \
	done
	cd $(dist_cross) && $(sha256) brigade_* > checksums.txt

.PHONY: checksums-check
checksums-check: cross ## Fail if plugin/bin/{VERSION,checksums.txt} disagree with plugin.json, a fresh build, or the published release (CI)
	scripts/ci/checksums-check.sh $(dist_cross)/checksums.txt

.PHONY: release
release: ## Pin the plugin to $(version), commit through the push chain, tag v$(version) and push the tag (usage: make release version=0.1.0 [branch=<throwaway>] — branch only for the P2-12 rehearsal)
	@test -n "$(version)" || { echo "usage: make release version=X.Y.Z [branch=<name>]"; exit 1; }
	scripts/release-prep.sh $(version) $(branch)

.PHONY: release-dry-run
release-dry-run: ## goreleaser check + a local release without publishing (needs a clean tree and a tag on HEAD)
	$(goreleaser) check
	GOTOOLCHAIN=$(go_toolchain) $(goreleaser) release --skip=publish --clean

# ========== Supabase (local stack) ==========

# E0-1: with `brigade` in config.toml's [api] schemas, `supabase start` cannot succeed until a migration has
# created that schema. PostgREST loops on `3F000 schema "brigade" does not exist`, its container never turns
# healthy, and the CLI tears the whole stack down reporting only `supabase_rest_brigade unexpected status 503`
# — which names PostgREST rather than the cause. Fail fast and legibly instead.
.PHONY: migrations-check
migrations-check: ## Fail unless supabase/migrations/ holds at least one .sql file (guards supabase-start; E0-1)
	@ls supabase/migrations/*.sql >/dev/null 2>&1 || { \
	  echo 'supabase/migrations/ holds no .sql file, so `supabase start` cannot succeed:' >&2; \
	  echo 'supabase/config.toml exposes the `brigade` schema, PostgREST loops on' >&2; \
	  echo '  3F000 schema "brigade" does not exist' >&2; \
	  echo 'its container never turns healthy, and the CLI tears the whole stack down reporting only' >&2; \
	  echo '  supabase_rest_brigade unexpected status 503' >&2; \
	  echo 'which names PostgREST rather than the cause (E0-1).' >&2; \
	  echo 'Write the schema migration first: make migration-new name=brigade_schema' >&2; \
	  exit 1; }

.PHONY: supabase-start
supabase-start: migrations-check ## Start the minimal local stack (db, auth, rest, realtime, kong); applies migrations + seed (CI)
	$(supabase) start -x $(supabase_exclude)

.PHONY: supabase-stop
supabase-stop: ## Stop the local stack, keep data
	$(supabase) stop

.PHONY: supabase-clean
supabase-clean: ## Stop the local stack and delete its data (CI)
	$(supabase) stop --no-backup

.PHONY: supabase-status
supabase-status: ## Show URLs and keys of the running stack
	$(supabase) status

# BOTH name sets are written, deliberately. Plan 7.4 renames everything to SUPABASE_* with --override-name,
# but the promoted Phase 0 regression kit under scripts/experiments/ (E0-1, E0-2, E0-6 and E0-6/verify) reads
# the CLI's OWN names -- API_URL, ANON_KEY, JWT_SECRET, PUBLISHABLE_KEY, SECRET_KEY, SERVICE_ROLE_KEY -- via
# its loadEnv(".env.test"). Since the recipe TRUNCATES the file, emitting only the renamed set silently gives
# every one of those drivers an empty string: API_URL is renamed away and ANON_KEY and JWT_SECRET disappear
# entirely. The kit fails for a reason that looks like a broken stack. So: the CLI's native output first,
# then the SUPABASE_* aliases the plan's own test-integration recipe consumes. Writing via a temp file keeps
# a failed `supabase status` from truncating a working .env.test.
.PHONY: supabase-env
supabase-env: ## Write $(env_test) from the running stack, in both name sets (never commit it) (CI)
	@$(supabase) status -o env > $(env_test).tmp
	@{ cat $(env_test).tmp; \
	   echo ''; \
	   echo '# SUPABASE_* aliases (plan 7.4/7.5; consumed by make test-integration).'; \
	   sed -n 's/^API_URL=/SUPABASE_URL=/p'                       $(env_test).tmp; \
	   sed -n 's/^PUBLISHABLE_KEY=/SUPABASE_PUBLISHABLE_KEY=/p'   $(env_test).tmp; \
	   sed -n 's/^SECRET_KEY=/SUPABASE_SECRET_KEY=/p'             $(env_test).tmp; \
	   sed -n 's/^SERVICE_ROLE_KEY=/SUPABASE_SERVICE_ROLE_KEY=/p' $(env_test).tmp; \
	   sed -n 's/^DB_URL=/SUPABASE_DB_URL=/p'                     $(env_test).tmp; \
	 } > $(env_test)
	@rm -f $(env_test).tmp
	@grep -q '^SUPABASE_URL=' $(env_test) || { echo 'supabase-env: alias generation produced no SUPABASE_URL' >&2; exit 1; }

.PHONY: supabase-reset
supabase-reset: migrations-check ## Recreate the local database from migrations + seed
	$(supabase) db reset

.PHONY: migration-new
migration-new: ## Create supabase/migrations/<timestamp>_$(name).sql (usage: make migration-new name=add_x)
	$(supabase) migration new $(name)

# ========== Supabase (hosted project; needs SUPABASE_ACCESS_TOKEN) ==========

.PHONY: supabase-link
supabase-link: ## Link a hosted project (usage: make supabase-link project=<ref>)
	$(supabase) link --project-ref $(project)

.PHONY: supabase-push-dry
supabase-push-dry: ## Show migrations that would be applied to the linked project
	$(supabase) db push --dry-run

.PHONY: supabase-push
supabase-push: ## Apply migrations to the linked project
	$(supabase) db push

# `config push` sends this config.toml WHOLE, and this config.toml describes the LOCAL stack. Measured against
# CLI 2.116.0's own config/push/SIDE_EFFECTS.md (P5-1 brief 1.4): it PATCHes ~100 auth fields and, on a gate that
# is "always processed", PUTs the Postgres settings. Two of the fields it would set are actively harmful on a
# production project: [auth.rate_limit] anonymous_users = 1000, against a hosted default of 30 per hour per IP
# -- a 33x abuse ceiling on a project whose publishable key is handed to every team member by design -- and
# site_url = http://127.0.0.1:3000. `--yes` (which an unattended run needs, and which CI's deploy-staging job
# already passes) auto-confirms every one of those diffs, and the diff-and-confirm loop is the command's only
# safety. P5-1 therefore set the three fields it needed one at a time through the Management API instead
# (scripts/backend-settings.sh). The supported vehicle if a future item wants `config push` back is a
# [remotes.<name>] block whose project_id matches the ref: @supabase/config merges only the keys that block
# declares over the base config, so the first key such a block needs is
#   [remotes.<name>.auth.rate_limit]
#   anonymous_users = 30
.PHONY: supabase-config-push
supabase-config-push: ## DANGEROUS against a production project -- read the comment above (P5-1); needs i_know=1
	@[ -n "$(i_know)" ] || { \
	  echo 'refusing: `config push` sends this config.toml WHOLE, and this config.toml is the LOCAL stack'"'"'s.' >&2; \
	  echo 'Against a hosted project it would set auth.rate_limit.anonymous_users to 1000 (hosted default 30/h/IP)' >&2; \
	  echo 'and site_url to http://127.0.0.1:3000. Use `make backend-install project=<ref>`, which sets the fields' >&2; \
	  echo 'this project actually needs one at a time and reads each one back (P5-1; see the comment above).' >&2; \
	  echo 'If you really mean it: make supabase-config-push i_know=1' >&2; \
	  exit 1; }
	$(supabase) config push

# P5-1. Requires SUPABASE_ACCESS_TOKEN in the environment (a personal access token; NEVER a file path, never
# argv, never a repository secret of the keep-alive). The database password is NOT needed: on CLI 2.116.0
# SUPABASE_DB_PASSWORD is a no-op for `link`, and `db push --linked` mints a temporary login role through the
# Management API with the access token (measured on the hosted project, P5-1: "Initialising login role...").
# Idempotent: every settings call reads before it writes and reads back after. `dry=1` stops after the dry run
# and prints the settings diffs without applying anything.
# NOT `config push` -- see the comment on supabase-config-push above for the measurement that rules it out.
# Order is not negotiable: the migrations are pushed BEFORE the schema is exposed. With `brigade` exposed and no
# schema behind it, PostgREST loops on `3F000 schema "brigade" does not exist` (E0-1, migrations-check above).
.PHONY: backend-install
backend-install: ## One-shot hosted backend setup (usage: make backend-install project=<ref> [dry=1])
	@test -n "$(project)" || { echo 'usage: make backend-install project=<ref> [dry=1]' >&2; exit 1; }
	$(supabase) link --project-ref $(project)
	$(supabase) db push --dry-run
	[ -n "$(dry)" ] || $(supabase) db push --yes
	scripts/backend-settings.sh $(project) $(if $(dry),--dry-run,)
	$(supabase) migration list --linked
	$(supabase) projects api-keys --project-ref $(project)   # no --reveal: the publishable key only

# ========== Git ==========

.PHONY: pull
pull: ## Merge origin into the current branch (plain merge — never rebase; no editor)
	git pull --no-edit

.PHONY: commit
commit: typecheck pull build test ## Typecheck, pull, build, test, stage, commit (usage: make commit message="...")
	git add --verbose :/ .
	git commit -m "$(message)"

.PHONY: push
push: commit ## commit + push
	git push --verbose

# Default target
.DEFAULT_GOAL := help
