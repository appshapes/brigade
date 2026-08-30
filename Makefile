# Makefile for this repository
# Conventions: lowercase variable names; per-target `.PHONY`; `##` doc-comments scraped by `help`.
# "TBD" indicates work to be done.

# ========== Variables (alphabetical) ==========

detach := --detach
tag := $(shell git log -1 --pretty=format:"%H")

# ========== Help ==========

.PHONY: help
help: ## Show this help message
	@echo "Make commands"
	@echo ""
	@echo "Usage: make [target] [var=value ...]"
	@echo ""
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-22s %s\n", $$1, $$2}'

# ========== Setup ==========

.PHONY: setup
setup: ## Initial project setup (dependencies install, .env scaffold)
	# TBD
	@cp -n .env.example .env || true

# ========== Build / Test / Lint ==========

.PHONY: build
build: ## Build
	# TBD

.PHONY: clean
clean: ## Remove build artifacts and local caches
	rm -rf playwright-report test-results # Maintain this list of transient build output files and folders

.PHONY: typecheck
typecheck: ## Type checks
	# TBD

.PHONY: lint
lint: ## Lint
	# TBD

.PHONY: lint-fix
lint-fix: ## Lint fix
	# TBD

.PHONY: test
test: ## Test
	# TBD

# ========== Git ==========

.PHONY: pull
pull: ## Merge origin into the current branch (plain merge — never rebase)
	git pull

.PHONY: commit
commit: typecheck pull build test ## Typecheck, pull, build, test, stage, commit (usage: make commit message="...")
	git add --verbose :/ .
	git commit -m "$(message)"

.PHONY: push
push: commit ## commit + push
	git push --verbose

# ========== Docker ==========

.PHONY: docker-build
docker-build: ## docker compose build $(service)
	docker compose build $(service)

.PHONY: docker-exec
docker-exec: ## Exec into a running container (usage: make docker-exec container=... command=sh)
	docker exec -it $(container) $(command)

.PHONY: docker-stop
docker-stop: ## docker compose stop $(service)
	docker compose stop $(service)

.PHONY: docker-up
docker-up: docker-build ## docker compose up (detached) for $(service)
	docker compose up --remove-orphans $(detach) $(service)

# Default target
.DEFAULT_GOAL := help
