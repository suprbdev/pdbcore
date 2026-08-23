GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
TEST_DB ?= postgres://pdbcore:pdbcore@localhost:5434/pdbcore_test

.PHONY: help test test-e2e lint fixture release

.DEFAULT_GOAL := help

help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

test: ## Unit tests (hermetic, no database needed)
	$(GO) test -race ./...

# -p isolates the stack; port 5434 keeps it clear of pdbq's e2e database.
# The DB-backed tests (fixture drift gate, pgexec, watch) skip without
# PDBCORE_TEST_DATABASE_URL, so this target is what actually runs them.
test-e2e: ## DB-backed tests against a disposable PostGIS Postgres
	docker compose -p pdbcore-test -f compose.test.yaml up -d --wait
	PDBCORE_TEST_DATABASE_URL="$(TEST_DB)" $(GO) test -race -count=1 ./testutil/ ./pgexec/ ./watch/; \
	status=$$?; docker compose -p pdbcore-test -f compose.test.yaml down -v; exit $$status

lint: ## Run golangci-lint (falls back to go vet)
	golangci-lint run ./... || $(GO) vet ./...

fixture: ## Print the fixture SQL (products pipe this into db/init/01-schema.sql)
	$(GO) run ./testutil/cmd/fixture

# Verifies, tags, and pushes; the Release workflow (release.yaml) publishes
# the GitHub release with generated notes (library: no binaries).
release: ## Cut a new release (prompts for version, tags, pushes)
	@set -e; \
	git diff --quiet && git diff --cached --quiet || { echo "error: working tree dirty — commit or stash first"; exit 1; }; \
	current=$$(git describe --tags --abbrev=0 2>/dev/null || echo "(none)"); \
	echo "Current version: $$current"; \
	printf "New version (vX.Y.Z): "; read -r version; \
	echo "$$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$$' || { echo "error: invalid version '$$version' (expected vX.Y.Z)"; exit 1; }; \
	if git rev-parse -q --verify "refs/tags/$$version" >/dev/null; then echo "error: tag $$version already exists"; exit 1; fi; \
	$(MAKE) test lint; \
	git tag -a "$$version" -m "$$version"; \
	git push origin HEAD "$$version"; \
	echo "Pushed $$version — the Release workflow is publishing it:"; \
	echo "  https://github.com/suprbdev/pdbcore/actions/workflows/release.yaml"
